// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"slices"

	"cmd/compile/internal/syntax"

	. "internal/types/errors"
)

// logosCtForward is the first instantiation through which a forwarding
// function passes one of its type parameters on.
type logosCtForward struct {
	site    *syntax.Name
	param   *TypeParam
	target  *Func
	forward bool // target is a forwarding function of the package
}

// logosCtExportedForwarders reports each exported forwarding function of a
// package that can be imported (SPEC 2.5, T-205). A forwarding function
// expands only for the callers of its own package, so a caller in another
// package cannot instantiate it. Package main cannot be imported. The
// classification is the one the lowering expands: a package-level generic
// function that is not a comptime generic forwards when it instantiates a
// comptime generic, or a forwarding function of its package, with a type
// argument that holds one of its type parameters.
func (check *Checker) logosCtExportedForwarders() {
	if !check.conf.Logos || check.pkg.name == "main" || check.Uses == nil || check.Instances == nil {
		return
	}
	fns, bodies, forwards := check.logosCtForwarders()
	for _, fn := range fns {
		if !forwards[fn] || !fn.Exported() {
			continue
		}
		f, _ := check.logosCtFirstForward(bodies[fn], forwards)
		kind := "comptime generic"
		if f.forward {
			kind = "forwarding function"
		}
		check.errorf(f.site, InvalidPkgUse, "[comptime::exported_forwarder] exported function %s forwards type parameter %s to %s %s; a caller in another package cannot instantiate it", fn.name, f.param.obj.name, kind, f.target.name)
	}
}

// logosCtForwarders returns the package-level generic functions that are
// not comptime generics in source order, their bodies, and the set of
// those that forward a type parameter to a comptime generic or to another
// forwarding function.
func (check *Checker) logosCtForwarders() ([]*Func, map[*Func]*syntax.BlockStmt, map[*Func]bool) {
	var fns []*Func
	bodies := make(map[*Func]*syntax.BlockStmt)
	for obj, di := range check.objMap {
		fn, ok := obj.(*Func)
		if !ok || di == nil || di.fdecl == nil || di.fdecl.Body == nil || di.fdecl.Recv != nil || len(di.fdecl.TParamList) == 0 || LogosUsesComptime(di.fdecl, di.file) {
			continue
		}
		fns = append(fns, fn)
		bodies[fn] = di.fdecl.Body
	}
	slices.SortFunc(fns, func(a, b *Func) int { return cmpPos(a.pos, b.pos) })
	forwards := make(map[*Func]bool)
	for found := true; found; {
		found = false
		for _, fn := range fns {
			if forwards[fn] {
				continue
			}
			if _, ok := check.logosCtFirstForward(bodies[fn], forwards); ok {
				forwards[fn] = true
				found = true
			}
		}
	}
	return fns, bodies, forwards
}

// logosCtFirstForward finds the first instantiation in body, in source order,
// whose type arguments hold a type parameter and whose callee is a comptime
// generic or one of forwards.
func (check *Checker) logosCtFirstForward(body *syntax.BlockStmt, forwards map[*Func]bool) (logosCtForward, bool) {
	var out logosCtForward
	syntax.Inspect(body, func(n syntax.Node) bool {
		if out.site != nil {
			return false
		}
		name, ok := n.(*syntax.Name)
		if !ok {
			return true
		}
		callee, _ := check.Uses[name].(*Func)
		inst, found := check.Instances[name]
		if callee == nil || !found || inst.TypeArgs == nil {
			return true
		}
		origin := callee.Origin()
		forward := forwards[origin]
		if !forward && !check.logosCtComptimeTarget(origin) {
			return true
		}
		for _, targ := range inst.TypeArgs.list() {
			if param := LogosCtForwardedParam(targ); param != nil {
				out = logosCtForward{site: name, param: param, target: origin, forward: forward}
				return false
			}
		}
		return true
	})
	return out, out.site != nil
}

// logosCtComptimeTarget reports whether fn is a comptime generic of this
// package or of an imported loGo/s package.
func (check *Checker) logosCtComptimeTarget(fn *Func) bool {
	if fn.Signature().Recv() != nil {
		return false
	}
	if di := check.objMap[fn]; di != nil && di.fdecl != nil {
		return di.fdecl.Body != nil && LogosUsesComptime(di.fdecl, di.file)
	}
	if fn.pkg == nil || fn.pkg == check.pkg {
		return false
	}
	return check.logosIsComptimeGeneric(fn)
}

// LogosCtForwardedParam returns the first type parameter that t holds,
// reading t in source order: t itself, or any element, key, field,
// parameter, result, method, embedded type or type argument of a pointer,
// slice, array, map, channel, option, const view, tuple, struct, function,
// interface, alias or instantiated named type. An instantiation of a comptime
// generic or of a forwarding function forwards when one of its type
// arguments holds a type parameter (SPEC 2.5, T-205); the checker and the
// lowering classify forwarding functions with this one rule.
func LogosCtForwardedParam(t Type) *TypeParam {
	switch t := t.(type) {
	case *TypeParam:
		return t
	case *Alias:
		return LogosCtForwardedParam(Unalias(t))
	case *Pointer:
		return LogosCtForwardedParam(t.base)
	case *Slice:
		return LogosCtForwardedParam(t.elem)
	case *Array:
		return LogosCtForwardedParam(t.elem)
	case *Chan:
		return LogosCtForwardedParam(t.elem)
	case *Option:
		return LogosCtForwardedParam(t.Elem)
	case *ConstView:
		return LogosCtForwardedParam(t.Elem)
	case *ReadonlyDescendants:
		return LogosCtForwardedParam(t.Elem)
	case *Map:
		return logosCtFirstParam(t.key, t.elem)
	case *LogosTuple:
		return logosCtFirstParam(t.types...)
	case *Struct:
		return logosCtVarsParam(t.fields)
	case *Signature:
		if t.params != nil {
			if param := logosCtVarsParam(t.params.vars); param != nil {
				return param
			}
		}
		if t.results != nil {
			return logosCtVarsParam(t.results.vars)
		}
	case *Interface:
		for _, m := range t.methods {
			if param := LogosCtForwardedParam(m.typ); param != nil {
				return param
			}
		}
		return logosCtFirstParam(t.embeddeds...)
	case *Named:
		return logosCtFirstParam(t.TypeArgs().list()...)
	}
	return nil
}

func logosCtFirstParam(types ...Type) *TypeParam {
	for _, t := range types {
		if param := LogosCtForwardedParam(t); param != nil {
			return param
		}
	}
	return nil
}

func logosCtVarsParam(vars []*Var) *TypeParam {
	for _, v := range vars {
		if param := LogosCtForwardedParam(v.typ); param != nil {
			return param
		}
	}
	return nil
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	. "internal/types/errors"
)

// logosRegionEscape prefixes every region escape diagnostic. Lint
// severity and JSON rule_id resolve from this prefix (SPEC Sec 14).
const logosRegionEscape = "[correctness::region_escape]"

// logosRegionIsRegion reports whether t denotes logos/arena.Region,
// through aliases and instantiations.
func logosRegionIsRegion(t Type) bool {
	n, _ := Unalias(t).(*Named)
	if n == nil || n.Obj() == nil || n.Obj().Pkg() == nil {
		return false
	}
	return n.Obj().Pkg().Path() == "logos/arena" && n.Obj().Name() == "Region"
}

// logosRegionCarries reports whether a value of type t holds a region
// handle: the Region type itself, or a struct or array that contains
// one. Other composites do not carry.
func logosRegionCarries(t Type) bool {
	if t == nil || !isValid(t) {
		return false
	}
	if logosRegionIsRegion(t) {
		return true
	}
	switch u := t.Underlying().(type) {
	case *Struct:
		for i, n := 0, u.NumFields(); i < n; i++ {
			if logosRegionCarries(u.Field(i).Type()) {
				return true
			}
		}
	case *Array:
		return logosRegionCarries(u.Elem())
	}
	return false
}

// logosRegionLocal returns v when it is a variable of this package
// declared inside a function, nil for the blank identifier, globals,
// and non-variables.
func (check *Checker) logosRegionLocal(v Object) *Var {
	w, _ := v.(*Var)
	if w == nil || w.pkg != check.pkg || w.Parent() == nil || w.Parent() == check.pkg.scope {
		return nil
	}
	return w
}

// logosRegionBlank reports whether lhs is the blank identifier.
func logosRegionBlank(lhs syntax.Expr) bool {
	n, _ := syntax.Unparen(lhs).(*syntax.Name)
	return n != nil && n.Value == "_"
}

// logosRegionRoot returns the local variable an lvalue denotes storage
// of, reached only through field and array selection from that local.
// It returns nil for the blank identifier and for every lvalue that
// stores into other storage: globals, dereferences, pointer fields,
// slices, maps, and interfaces.
func (check *Checker) logosRegionRoot(lhs syntax.Expr) *Var {
	switch e := syntax.Unparen(lhs).(type) {
	case *syntax.Name:
		if e.Value == "_" {
			return nil
		}
		return check.logosRegionLocal(check.lookup(e.Value))
	case *syntax.SelectorExpr:
		switch check.logosRegionUnderlying(e.X).(type) {
		case *Pointer, *Interface, *Map:
			return nil
		}
		return check.logosRegionRoot(e.X)
	case *syntax.IndexExpr:
		if _, ok := check.logosRegionUnderlying(e.X).(*Array); !ok {
			return nil
		}
		return check.logosRegionRoot(e.X)
	case *syntax.Operation:
		if e.Op == syntax.Mul && e.Y == nil {
			return nil
		}
	}
	return nil
}

// logosRegionUnderlying returns the underlying type recorded for e, or
// nil.
func (check *Checker) logosRegionUnderlying(e syntax.Expr) Type {
	t := check.Info.TypeOf(e)
	if t == nil {
		return nil
	}
	return Unalias(t).Underlying()
}

// logosRegionMarkedExpr reports whether e is a function literal that
// captures a region value, or a name holding one.
func (check *Checker) logosRegionMarkedExpr(e syntax.Expr) bool {
	switch x := syntax.Unparen(e).(type) {
	case *syntax.FuncLit:
		return check.logosRegionLit[x]
	case *syntax.Name:
		if v := check.logosRegionLocal(check.Info.Uses[x]); v != nil {
			return check.logosRegionVar[v]
		}
	}
	return false
}

// logosRegionInitMap lazily allocates the checker state.
func (check *Checker) logosRegionInitMap() {
	if check.logosRegionLit == nil {
		check.logosRegionLit = make(map[*syntax.FuncLit]bool)
	}
	if check.logosRegionVar == nil {
		check.logosRegionVar = make(map[*Var]bool)
	}
}

// logosRegionScopeInside reports whether s is of or lies below of.
func logosRegionScopeInside(s, of *Scope) bool {
	for p := s; p != nil; p = p.Parent() {
		if p == of {
			return true
		}
	}
	return false
}

// logosRegionLitMark queues the capture analysis of one literal: after
// its body checks, the literal is marked when it reads a region-carrying
// variable of an enclosing function.
func (check *Checker) logosRegionLitMark(e *syntax.FuncLit, sig *Signature) {
	if !check.conf.Logos || e.Body == nil || sig == nil {
		return
	}
	check.logosRegionInitMap()
	check.later(func() {
		syntax.Inspect(e.Body, func(n syntax.Node) bool {
			name, ok := n.(*syntax.Name)
			if !ok || name.Value == "_" {
				return true
			}
			v := check.logosRegionLocal(check.Info.Uses[name])
			if v == nil || logosRegionScopeInside(v.Parent(), sig.scope) || !logosRegionCarries(v.typ) {
				return true
			}
			check.logosRegionLit[e] = true
			return false
		})
	})
}

// logosRegionEscapedLit reports a stored, returned, sent, or run literal
// that captures a region value.
func (check *Checker) logosRegionEscapedLit(at poser, what string, e syntax.Expr) {
	check.later(func() {
		if check.logosRegionMarkedExpr(e) {
			check.errorf(at, IncompatibleAssign, "%s cannot %s %s: the literal captures a region value", logosRegionEscape, what, ExprString(e))
		}
	})
}

// logosRegionQueueLit queues the literal escape report only when e could
// carry one.
func (check *Checker) logosRegionQueueLit(at poser, what string, e syntax.Expr) {
	switch syntax.Unparen(e).(type) {
	case *syntax.FuncLit, *syntax.Name:
		check.logosRegionEscapedLit(at, what, e)
	}
}

// logosRegionAssign checks one store lhs = x. A region-carrying value
// may only land in storage of a local root reached by field or array
// selection; a literal capturing a region value follows the same rule,
// checked once its body ran, and marks a local target as holding it.
func (check *Checker) logosRegionAssign(lhs syntax.Expr, x *operand) {
	if !check.conf.Logos || x == nil || !x.isValid() {
		return
	}
	root := check.logosRegionRoot(lhs)
	blank := logosRegionBlank(lhs)
	if !blank && root == nil && logosRegionCarries(x.typ()) {
		check.errorf(x, IncompatibleAssign, "%s cannot store %s in %s: a region value must stay inside its block", logosRegionEscape, ExprString(x.expr), ExprString(lhs))
		return
	}
	if name, _ := syntax.Unparen(lhs).(*syntax.Name); name != nil {
		if lv := check.logosRegionLocal(check.lookup(name.Value)); lv != nil {
			check.logosRegionInitMap()
			check.later(func() {
				if check.logosRegionMarkedExpr(x.expr) {
					check.logosRegionVar[lv] = true
				}
			})
		}
	}
	if !blank && root == nil {
		check.logosRegionQueueLit(lhs, "store", x.expr)
	}
}

// logosRegionDecl marks a declared local that holds a literal capturing
// a region value.
func (check *Checker) logosRegionDecl(lhs *Var, x *operand) {
	if !check.conf.Logos || x == nil || !x.isValid() {
		return
	}
	local := check.logosRegionLocal(lhs)
	if local == nil {
		if lhs == nil || lhs.pkg != check.pkg || lhs.Name() == "_" || check.scope == check.pkg.scope {
			return
		}
		local = lhs
	}
	check.logosRegionInitMap()
	e := x.expr
	check.later(func() {
		if check.logosRegionMarkedExpr(e) {
			check.logosRegionVar[local] = true
		}
	})
}

// logosRegionResults checks the results of one return statement.
func (check *Checker) logosRegionResults(results []syntax.Expr) {
	if !check.conf.Logos {
		return
	}
	for _, r := range results {
		if t := check.Info.TypeOf(r); t != nil && logosRegionCarries(t) {
			check.errorf(r, IncompatibleAssign, "%s cannot return %s", logosRegionEscape, ExprString(r))
			continue
		}
		check.logosRegionQueueLit(r, "return", r)
	}
}

// logosRegionSend checks one channel send value.
func (check *Checker) logosRegionSend(val *operand) {
	if !check.conf.Logos || !val.isValid() {
		return
	}
	if logosRegionCarries(val.typ()) {
		check.errorf(val, IncompatibleAssign, "%s cannot send %s on a channel", logosRegionEscape, ExprString(val.expr))
		return
	}
	check.logosRegionQueueLit(val.expr, "send", val.expr)
}

// logosRegionSuspended checks the operands and the callee literal of a
// go statement. A defer keeps everything: its capture is a runtime
// backstop, not an escape.
func (check *Checker) logosRegionSuspended(keyword string, call syntax.Expr) {
	if !check.conf.Logos || keyword != "go" {
		return
	}
	c, ok := syntax.Unparen(call).(*syntax.CallExpr)
	if !ok {
		return
	}
	for _, arg := range c.ArgList {
		if t := check.Info.TypeOf(arg); t != nil && logosRegionCarries(t) {
			check.errorf(arg, InvalidGo, "%s cannot pass %s to a goroutine", logosRegionEscape, ExprString(arg))
		}
	}
	if lit, ok := syntax.Unparen(c.Fun).(*syntax.FuncLit); ok {
		check.logosRegionEscapedLit(lit, "run with go", lit)
		return
	}
	check.logosRegionQueueLit(c.Fun, "run with go", c.Fun)
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/logos/ctfe"
	"cmd/compile/internal/syntax"
	"go/constant"
	"go/token"
	. "internal/types/errors"
	"strconv"
)

func (check *Checker) logosConstFuncBodies() (map[string]*syntax.FuncDecl, map[*syntax.FuncDecl]bool) {
	if check.logosBodies == nil {
		check.logosBodies = map[string]*syntax.FuncDecl{}
		check.logosConstSyntax = map[*syntax.FuncDecl]bool{}
	}
	return check.logosBodies, check.logosConstSyntax
}

func (check *Checker) logosUnwrapConstFunc(decl syntax.Decl) syntax.Decl {
	if !check.conf.Logos {
		return decl
	}
	bodies, consts := check.logosConstFuncBodies()
	if cfd, ok := decl.(*syntax.ConstFuncDecl); ok {
		consts[cfd.Func] = true
		if cfd.Func.Recv == nil && cfd.Func.Name != nil {
			if _, dup := bodies[cfd.Func.Name.Value]; !dup {
				bodies[cfd.Func.Name.Value] = cfd.Func
			}
		}
		return cfd.Func
	}
	if fd, ok := decl.(*syntax.FuncDecl); ok {
		if fd.Recv == nil && fd.Name != nil {
			if _, dup := bodies[fd.Name.Value]; !dup {
				bodies[fd.Name.Value] = fd
			}
		}
	}
	return decl
}

func (check *Checker) logosConstNeedsEval(init syntax.Expr) bool {
	needed := false
	syntax.Inspect(init, func(n syntax.Node) bool {
		if needed {
			return false
		}
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.CompositeLit, *syntax.CallExpr, *syntax.SelectorExpr, *syntax.IndexExpr, *syntax.CoalesceExpr:
			needed = true
		case *syntax.Name:
			if obj, ok := check.Info.Uses[n].(*Const); ok {
				_, needed = check.logosRecorded(obj)
			}
		}
		return !needed
	})
	return needed
}

func (check *Checker) logosConstEval(obj *Const, init syntax.Expr, x *operand) bool {
	if !check.conf.Logos || init == nil || !x.isValid() {
		return false
	}
	if x.mode() == constant_ {
		return false
	}
	switch syntax.Unparen(init).(type) {
	case *syntax.Name:
		return check.logosConstAlias(obj, init)
	case *syntax.IndexExpr:
		if logosAsOption(x.typ()) != nil {
			return false
		}
	}
	if !check.logosConstNeedsEval(init) {
		return false
	}
	return check.logosConstCompute(obj, init, x)
}

func (check *Checker) logosConstUse(obj *Const, x *operand) bool {
	if !check.conf.Logos {
		return false
	}
	v, ok := check.logosRecorded(obj)
	if !ok {
		return false
	}
	return v.IsComposite()
}

func (check *Checker) logosConstAlias(obj *Const, init syntax.Expr) bool {
	name, ok := syntax.Unparen(init).(*syntax.Name)
	if !ok {
		return false
	}
	ref, ok := check.Info.Uses[name].(*Const)
	if !ok {
		return false
	}
	if ref.val != nil && ref.val.Kind() != constant.Unknown {
		return false
	}
	v, ok := check.logosRecorded(ref)
	if !ok {
		return false
	}
	check.logosRecord(obj, v)
	if obj.typ == nil {
		obj.typ = ref.typ
	}
	return true
}

func (check *Checker) logosConstCompute(obj *Const, init syntax.Expr, x *operand) bool {
	if v, ok := check.logosMemo(init); ok {
		return check.logosApplyMemo(obj, init, x, v)
	}
	if perr := check.logosPrescanImportedCalls(obj, init); perr != nil {
		check.errorf(obj, InvalidConstInit, "%s %s", logosConstEvalRule, perr)
		obj.typ = Typ[Invalid]
		return true
	}
	if t := x.typ(); t == nil {
		check.errorf(obj, InvalidConstInit, "%s const with unknown type", logosConstEvalRule)
		obj.typ = Typ[Invalid]
		return true
	}
	if mt, ok := x.typ().Underlying().(*Map); ok && !logosConstMapKeyOK(mt.Key()) {
		check.errorf(obj, InvalidConstInit, "[comptime::constmap_key_type] const map key type %s cannot be hashed: use a string, number, bool, or a struct or array of them", mt.Key())
		obj.typ = Typ[Invalid]
		return true
	}
	host := &ctfeHost{check: check}
	cfg := ctfe.Config{MaxSteps: check.conf.LogosCTFEMaxSteps, WordSize: check.logosWordSize()}
	v, _, err := ctfe.Eval(host, cfg, init)
	if err != nil {
		return check.logosCTFEFailed(obj, init, err)
	}
	check.logosMemoize(init, v)
	check.logosRecord(obj, v)
	if v.IsComposite() || !isConstType(x.typ()) {
		return check.logosRecordComposite(obj, init, x, v)
	}
	return check.logosApplyScalar(obj, init, x, v)
}

func (check *Checker) logosApplyMemo(obj *Const, init syntax.Expr, x *operand, v ctfe.Value) bool {
	if !v.IsValid() {
		obj.typ = Typ[Invalid]
		return true
	}
	check.logosRecord(obj, v)
	if v.IsComposite() || !isConstType(x.typ()) {
		return check.logosRecordComposite(obj, init, x, v)
	}
	return check.logosApplyScalar(obj, init, x, v)
}

func (check *Checker) logosApplyScalar(obj *Const, init syntax.Expr, x *operand, v ctfe.Value) bool {
	c, ok := ctfeScalarConstant(v)
	if !ok {
		check.errorf(obj, InvalidConstInit, "%s value of %s is not constant", logosConstEvalRule, x)
		obj.typ = Typ[Invalid]
		return true
	}
	x.val = c
	x.mode_ = constant_
	return false
}

func (check *Checker) logosRecordComposite(obj *Const, init syntax.Expr, x *operand, v ctfe.Value) bool {
	if obj.typ != nil {
		if ok, _ := x.assignableTo(check, obj.typ, nil); !ok {
			check.errorf(obj, InvalidConstInit, "%s cannot use %s as %s value in constant declaration", logosConstEvalRule, x, obj.typ)
			obj.typ = Typ[Invalid]
			return true
		}
	} else {
		obj.typ = x.typ()
	}
	base := obj.typ
	if cv := logosAsConstView(base); cv != nil {
		base = cv.Elem
	}
	if mt, ok := base.Underlying().(*Map); ok && v.Type().Kind == ctfe.KMap {
		if !logosConstMapKeyOK(mt.Key()) {
			check.errorf(obj, InvalidConstInit, "[comptime::constmap_key_type] const map key type %s cannot be hashed: use a string, number, bool, or a struct or array of them", mt.Key())
			obj.typ = Typ[Invalid]
			return true
		}
		obj.typ = &ConstView{Elem: base, ConstMap: true}
	}
	if _, ok := base.Underlying().(*Map); (!ok || v.Type().Kind != ctfe.KMap) && logosTypeHasMap(base) {
		check.errorf(obj, InvalidConstInit, "%s const %s holds a map and cannot live in read-only memory", logosConstEvalRule, obj.Name())
		obj.typ = Typ[Invalid]
		return true
	}
	check.logosRecord(obj, v)
	return true
}

func logosTypeHasMap(t Type) bool {
	seen := map[Type]bool{}
	var walk func(t Type) bool
	walk = func(t Type) bool {
		if t == nil || seen[t] {
			return false
		}
		seen[t] = true
		u := Unalias(t)
		if n, ok := u.(*Named); ok {
			u = n.Underlying()
		}
		switch u := u.(type) {
		case *Map:
			return true
		case *Struct:
			for i := range u.NumFields() {
				if walk(u.Field(i).Type()) {
					return true
				}
			}
		case *Slice:
			return walk(u.Elem())
		case *Array:
			return walk(u.Elem())
		}
		return false
	}
	return walk(t)
}

func (check *Checker) logosCTFEFailed(obj *Const, init syntax.Expr, err error) bool {
	check.logosMemoizeFailure(init)
	if cerr, ok := err.(*ctfe.Error); ok {
		switch cerr.Code {
		case ctfe.CodeSilent:
			obj.typ = Typ[Invalid]
			return true
		case ctfe.CodeStepLimit:
			check.errorf(obj, InvalidConstInit, "%s evaluation exceeds the step limit", logosStepLimitRule)
		case ctfe.CodeTooLarge:
			check.errorf(obj, InvalidConstInit, "%s %s", logosTooLargeRule, cerr.Detail)
		default:
			check.errorf(obj, InvalidConstInit, "%s %s", logosConstEvalRule, cerr.Detail)
		}
		obj.typ = Typ[Invalid]
		return true
	}
	check.errorf(obj, InvalidConstInit, "%s %s", logosConstEvalRule, err)
	obj.typ = Typ[Invalid]
	return true
}

func (check *Checker) logosRecord(obj *Const, v ctfe.Value) {
	if check.Info.LogosCTFE == nil {
		check.Info.LogosCTFE = map[*Const]ctfe.Value{}
	}
	check.Info.LogosCTFE[obj] = v
}

func (check *Checker) logosRecorded(obj *Const) (ctfe.Value, bool) {
	v, ok := check.Info.LogosCTFE[obj]
	return v, ok
}

func (check *Checker) logosWordSize() int64 {
	if check.conf.Sizes != nil {
		if w := check.conf.Sizes.Sizeof(Typ[Int]); w == 4 || w == 8 {
			return w
		}
	}
	return 8
}

func (check *Checker) logosMemo(init syntax.Expr) (ctfe.Value, bool) {
	if check.logosMemoized == nil {
		return ctfe.Value{}, false
	}
	m, ok := check.logosMemoized[init]
	if !ok {
		return ctfe.Value{}, false
	}
	return m.val, true
}

func (check *Checker) logosMemoize(init syntax.Expr, v ctfe.Value) {
	if check.logosMemoized == nil {
		check.logosMemoized = map[syntax.Expr]ctfeMemo{}
	}
	check.logosMemoized[init] = ctfeMemo{val: v}
}

func (check *Checker) logosMemoizeFailure(init syntax.Expr) {
	if check.logosMemoized == nil {
		check.logosMemoized = map[syntax.Expr]ctfeMemo{}
	}
	check.logosMemoized[init] = ctfeMemo{}
}

type ctfeMemo struct {
	val ctfe.Value
}

const (
	logosConstEvalRule = "[correctness::const_eval]"
	logosStepLimitRule = "[comptime::ctfe_step_limit]"
	logosTooLargeRule  = "[comptime::const_too_large]"
	logosImpureRule    = "[correctness::const_func_impure]"
)

func ctfeScalarConstant(v ctfe.Value) (constant.Value, bool) {
	t := v.Type()
	if t == nil {
		return nil, false
	}
	switch t.Kind {
	case ctfe.KBool:
		return constant.MakeBool(v.Bool()), true
	case ctfe.KString:
		return constant.MakeString(v.Str()), true
	case ctfe.KInt, ctfe.KInt8, ctfe.KInt16, ctfe.KInt32, ctfe.KInt64,
		ctfe.KUint, ctfe.KUint8, ctfe.KUint16, ctfe.KUint32, ctfe.KUint64, ctfe.KUintptr,
		ctfe.KUntypedInt, ctfe.KUntypedRune:
		return constant.MakeFromLiteral(v.BigInt().String(), token.INT, 0), true
	case ctfe.KFloat32, ctfe.KFloat64, ctfe.KUntypedFloat:
		return constant.MakeFromLiteral(strconv.FormatFloat(v.Float64(), 'g', -1, 64), token.FLOAT, 0), true
	}
	return nil, false
}

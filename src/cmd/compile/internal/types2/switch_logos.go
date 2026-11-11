// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	"go/constant"
	. "internal/types/errors"
	"strings"
)

func (check *Checker) logosSwitchExpr(x *operand, e *syntax.SwitchExpr) exprKind {
	x.invalidate()
	x.typ_ = Typ[Invalid]
	x.expr = e
	if !check.conf.Logos {
		check.error(e, InvalidSyntaxTree, "invalid use of switch expression outside logos mode")
		return statement
	}

	check.openScope(e, "switch")
	defer check.closeScope()

	check.simpleStmt(e.Init)
	check.logosValidateSwitchTry(e)
	check.logosMultipleArmDefaults(e.Arms)

	var results []*operand
	guard, _ := e.Tag.(*syntax.TypeSwitchGuard)
	if guard != nil {
		results = check.logosTypeArms(e, guard)
	} else {
		results = check.logosValueArms(e)
	}
	if len(e.Arms) == 0 {
		check.error(e, InvalidExprSwitch, "switch expression has no arms")
		return statement
	}
	if guard == nil && !logosHasDefault(e.Arms) {
		check.errorf(e, InvalidExprSwitch, "%s switch expression needs a default arm", logosNeedDefault)
	}

	typ := check.logosArmType(e, results)
	if typ == nil {
		return statement
	}
	x.mode_ = value
	x.typ_ = typ
	return expression
}

func logosHasDefault(arms []*syntax.ArmClause) bool {
	for _, arm := range arms {
		if arm.Cases == nil {
			return true
		}
	}
	return false
}

func (check *Checker) logosMultipleArmDefaults(arms []*syntax.ArmClause) {
	var first *syntax.ArmClause
	for _, arm := range arms {
		if arm.Cases != nil {
			continue
		}
		if first != nil {
			check.errorf(arm, DuplicateDefault, "multiple defaults (first at %s)", first.Pos())
		} else {
			first = arm
		}
	}
}

func (check *Checker) logosArmResult(arm *syntax.ArmClause) *operand {
	var v operand
	check.expr(nil, &v, arm.Body)
	return &v
}

func (check *Checker) logosValueArms(e *syntax.SwitchExpr) []*operand {
	var tag operand
	if e.Tag != nil {
		check.expr(nil, &tag, e.Tag)
		check.assignment(&tag, nil, "switch expression")
		if tag.isValid() && !Comparable(tag.typ()) && !hasNil(tag.typ()) {
			check.errorf(&tag, InvalidExprSwitch, "cannot switch on %s (%s is not comparable)", &tag, tag.typ())
			tag.invalidate()
		}
	} else {
		pos := e.Rbrace
		if len(e.Arms) > 0 {
			pos = e.Arms[0].Pos()
		}
		tag.mode_ = constant_
		tag.typ_ = Typ[Bool]
		tag.val = constant.MakeBool(true)
		tag.expr = syntax.NewName(pos, "true")
	}

	seen := make(valueMap)
	results := make([]*operand, 0, len(e.Arms))
	for _, arm := range e.Arms {
		check.caseValues(&tag, syntax.UnpackListExpr(arm.Cases), seen)
		check.openScope(arm, "case")
		results = append(results, check.logosArmResult(arm))
		check.closeScope()
	}
	return results
}

func (check *Checker) logosTypeArms(e *syntax.SwitchExpr, guard *syntax.TypeSwitchGuard) []*operand {
	lhs := guard.Lhs
	if lhs != nil {
		if lhs.Value == "_" {
			check.softErrorf(lhs, NoNewVar, "no new variable on left side of :=")
			lhs = nil
		} else {
			check.recordDef(lhs, nil)
		}
	}

	var sx *operand
	{
		var x operand
		check.expr(nil, &x, guard.X)
		if x.isValid() {
			if isTypeParam(x.typ()) {
				check.errorf(&x, InvalidTypeSwitch, "cannot use type switch on type parameter value %s", &x)
			} else if IsInterface(x.typ()) {
				sx = &x
			} else {
				check.errorf(&x, InvalidTypeSwitch, "%s is not an interface", &x)
			}
		}
	}

	var lhsVars []*Var
	seen := make(map[Type]syntax.Expr)
	results := make([]*operand, 0, len(e.Arms))
	for _, arm := range e.Arms {
		T := check.caseTypes(sx, syntax.UnpackListExpr(arm.Cases), seen)
		check.openScope(arm, "case")
		if lhs != nil {
			obj := newVar(LocalVar, lhs.Pos(), check.pkg, lhs.Value, T)
			check.declare(check.scope, nil, obj, arm.Arrow)
			check.recordImplicit(arm, obj)
			lhsVars = append(lhsVars, obj)
		}
		results = append(results, check.logosArmResult(arm))
		check.closeScope()
	}

	if lhs != nil {
		var used bool
		for _, v := range lhsVars {
			if check.usedVars[v] {
				used = true
			}
			check.usedVars[v] = true
		}
		if !used {
			check.softErrorf(lhs, UnusedVar, "%s declared and not used", lhs.Value)
		}
	}

	if sx != nil && len(e.Arms) > 0 && !logosHasDefault(e.Arms) {
		check.logosCoverage(e, sx, seen)
	}
	return results
}

func (check *Checker) logosCoverage(e *syntax.SwitchExpr, sx *operand, seen map[Type]syntax.Expr) {
	var misses []string
	if tset := logosClosedUnion(sx.typ()); tset != nil {
		for _, term := range tset.terms {
			if !check.logosCovers(term.typ, seen) {
				misses = append(misses, TypeString(term.typ, check.qualifier))
			}
		}
		if _, ok := seen[nil]; !ok {
			misses = append(misses, "nil")
		}
	} else {
		misses = append(misses, "default")
	}
	if len(misses) == 0 {
		return
	}
	check.errorf(e, InvalidTypeSwitch, "%s switch on %s misses: %s", logosNonExhaust, sx.typ(), strings.Join(misses, ", "))
}

func (check *Checker) logosCovers(member Type, seen map[Type]syntax.Expr) bool {
	for c := range seen {
		if c == nil {
			continue
		}
		if Identical(member, c) {
			return true
		}
		if IsInterface(c) && !isTypeParam(c) && check.implements(member, c, false, nil) {
			return true
		}
	}
	return false
}

func (check *Checker) logosArmType(e *syntax.SwitchExpr, arms []*operand) Type {
	for _, v := range arms {
		if !v.isValid() {
			return nil
		}
	}

	var typed Type
	agree := true
	for _, v := range arms {
		if v.isNil() || isUntyped(v.typ()) {
			continue
		}
		if typed == nil {
			typed = v.typ()
			continue
		}
		if !Identical(typed, v.typ()) {
			check.errorf(v, MismatchedTypes, "%s arms have types %s and %s", logosArmMismatch, typed, v.typ())
			agree = false
		}
	}
	if !agree {
		return nil
	}

	common := typed
	if common == nil {
		var untyped Type
		for _, v := range arms {
			switch {
			case v.isNil():
			case isUntyped(v.typ()):
				if untyped == nil {
					untyped = v.typ()
				} else if m := maxType(untyped, v.typ()); m != nil {
					untyped = m
				} else {
					check.errorf(v, MismatchedTypes, "%s arms have types %s and %s", logosArmMismatch, untyped, v.typ())
					agree = false
				}
			}
		}
		if !agree {
			return nil
		}
		if untyped == nil {
			check.error(e, UntypedNilUse, "use of untyped nil in switch expression")
			return nil
		}
		common = Default(untyped)
	}
	for _, v := range arms {
		if !check.logosArmAssignable(v, common) {
			agree = false
			continue
		}
		check.assignment(v, common, "switch expression arm")
		if !v.isValid() {
			agree = false
		}
	}
	if !agree {
		return nil
	}
	return common
}

func (check *Checker) logosArmAssignable(v *operand, common Type) bool {
	if ok, _ := v.assignableTo(check, common, nil); !ok {
		check.errorf(v, MismatchedTypes, "%s arms have types %s and %s", logosArmMismatch, common, v.typ())
		return false
	}
	return true
}

func (check *Checker) logosValidateSwitchTry(e *syntax.SwitchExpr) {
	if e.Init == nil || check.logosCurStmt == nil {
		return
	}
	var initTry syntax.Pos
	switch s := e.Init.(type) {
	case *syntax.AssignStmt:
		if s.Rhs == nil {
			return
		}
		if t, ok := syntax.Unparen(syntax.UnpackListExpr(s.Rhs)[0]).(*syntax.TryExpr); ok {
			initTry = t.Pos()
		} else {
			return
		}
	case *syntax.ExprStmt:
		if t, ok := syntax.Unparen(s.X).(*syntax.TryExpr); ok {
			initTry = t.Pos()
		} else {
			return
		}
	default:
		return
	}
	if check.logosSwitchTryHostOk(e) {
		return
	}
	check.error(initTry, InvalidCall, logosMisplacedTry)
}

func (check *Checker) logosSwitchTryHostOk(e *syntax.SwitchExpr) bool {
	s := check.logosCurStmt
	found := logosLocateSwitch(s, e)
	if !found.found {
		return true
	}
	if found.underCall || found.underSelect || found.underCase || found.underSend || found.underLambda || found.underTag {
		return false
	}
	switch cur := s.(type) {
	case *syntax.AssignStmt, *syntax.ExprStmt:
		return true
	case *syntax.ReturnStmt:
		return found.direct
	case *syntax.IfStmt:
		return found.direct
	case *syntax.ForStmt:
		if found.inInit && !found.direct {
			return false
		}
		return true
	case *syntax.SwitchStmt:
		return found.direct
	case *syntax.LabeledStmt:
		return check.logosSwitchTryHostOkInner(cur.Stmt, e)
	default:
		return false
	}
}

func (check *Checker) logosSwitchTryHostOkInner(s syntax.Stmt, e *syntax.SwitchExpr) bool {
	prev := check.logosCurStmt
	check.logosCurStmt = s
	ok := check.logosSwitchTryHostOk(e)
	check.logosCurStmt = prev
	return ok
}

type logosSwitchLoc struct {
	found      bool
	direct     bool
	inInit     bool
	underCall  bool
	underSelect bool
	underCase  bool
	underSend  bool
	underLambda bool
	underTag   bool
}

func logosLocateSwitch(s syntax.Stmt, e *syntax.SwitchExpr) logosSwitchLoc {
	var loc logosSwitchLoc
	var walkExpr func(n syntax.Node, underCall, underTag bool)
	walkExpr = func(n syntax.Node, underCall, underTag bool) {
		if n == nil || loc.found {
			return
		}
		if se, ok := n.(*syntax.SwitchExpr); ok && se == e {
			loc.found = true
			if underCall {
				loc.underCall = true
			}
			if underTag {
				loc.underTag = true
			}
			return
		}
		switch t := n.(type) {
		case *syntax.FuncLit:
			return
		case *syntax.LambdaExpr:
			walkExpr(t.Body, underCall, underTag)
			if loc.found {
				loc.underLambda = true
			}
			return
		case *syntax.CallExpr:
			walkExpr(t.Fun, true, underTag)
			for _, a := range t.ArgList {
				walkExpr(a, true, underTag)
			}
			return
		case *syntax.SwitchExpr:
			if t.Tag != nil {
				walkExpr(t.Tag, underCall, true)
			}
			for _, a := range t.Arms {
				for _, c := range syntax.UnpackListExpr(a.Cases) {
					walkExpr(c, underCall, underTag)
				}
				walkExpr(a.Body, underCall, underTag)
			}
			if t.Init != nil {
				walkExpr(t.Init, underCall, underTag)
			}
			return
		}
		syntax.Inspect(n, func(m syntax.Node) bool {
			if loc.found {
				return false
			}
			if m == n {
				return true
			}
			if se, ok := m.(*syntax.SwitchExpr); ok && se == e {
				loc.found = true
				if underCall {
					loc.underCall = true
				}
				if underTag {
					loc.underTag = true
				}
				return false
			}
			return true
		})
	}
	switch t := s.(type) {
	case *syntax.AssignStmt:
		for _, l := range syntax.UnpackListExpr(t.Lhs) {
			if syntax.Unparen(l) == syntax.Expr(e) {
				loc.found = true
				return loc
			}
			walkExpr(l, false, false)
		}
		for _, r := range syntax.UnpackListExpr(t.Rhs) {
			if syntax.Unparen(r) == syntax.Expr(e) {
				loc.found = true
				loc.direct = true
				return loc
			}
		}
		walkExpr(t.Rhs, false, false)
	case *syntax.ExprStmt:
		if syntax.Unparen(t.X) == syntax.Expr(e) {
			loc.found = true
			loc.direct = true
			return loc
		}
		walkExpr(t.X, false, false)
	case *syntax.ReturnStmt:
		for _, r := range syntax.UnpackListExpr(t.Results) {
			if syntax.Unparen(r) == syntax.Expr(e) {
				loc.found = true
				loc.direct = true
				return loc
			}
		}
		walkExpr(t.Results, false, false)
	case *syntax.IfStmt:
		if t.Init != nil {
			if a, ok := t.Init.(*syntax.AssignStmt); ok {
				for _, r := range syntax.UnpackListExpr(a.Rhs) {
					if syntax.Unparen(r) == syntax.Expr(e) {
						loc.found = true
						loc.direct = true
						loc.inInit = true
						return loc
					}
				}
			}
			loc.inInit = true
			walkExpr(t.Init, false, false)
			loc.inInit = loc.found
			if loc.found {
				return loc
			}
		}
		if t.Cond != nil && syntax.Unparen(t.Cond) == syntax.Expr(e) {
			loc.found = true
			loc.direct = true
			return loc
		}
		walkExpr(t.Cond, false, false)
	case *syntax.ForStmt:
		if t.Init != nil {
			if a, ok := t.Init.(*syntax.AssignStmt); ok {
				for _, r := range syntax.UnpackListExpr(a.Rhs) {
					if syntax.Unparen(r) == syntax.Expr(e) {
						loc.found = true
						loc.direct = true
						loc.inInit = true
						return loc
					}
				}
			}
			loc.inInit = true
			walkExpr(t.Init, false, false)
			if loc.found {
				return loc
			}
			loc.inInit = false
		}
		walkExpr(t.Cond, false, false)
		if loc.found {
			return loc
		}
		walkExpr(t.Post, false, false)
	case *syntax.SwitchStmt:
		if t.Init != nil {
			if a, ok := t.Init.(*syntax.AssignStmt); ok {
				for _, r := range syntax.UnpackListExpr(a.Rhs) {
					if syntax.Unparen(r) == syntax.Expr(e) {
						loc.found = true
						loc.direct = true
						loc.inInit = true
						return loc
					}
				}
			}
			loc.inInit = true
			walkExpr(t.Init, false, false)
			if loc.found {
				return loc
			}
			loc.inInit = false
		}
		if t.Tag != nil && syntax.Unparen(t.Tag) == syntax.Expr(e) {
			loc.found = true
			loc.direct = true
			return loc
		}
		walkExpr(t.Tag, false, false)
		for _, c := range t.Body {
			for _, ce := range syntax.UnpackListExpr(c.Cases) {
				walkExpr(ce, false, false)
				if loc.found {
					loc.underCase = true
					return loc
				}
			}
		}
	case *syntax.SelectStmt:
		loc.underSelect = true
		loc.found = true
		return loc
	case *syntax.SendStmt:
		loc.underSend = true
		loc.found = true
		return loc
	case *syntax.LabeledStmt:
		return logosLocateSwitch(t.Stmt, e)
	default:
		walkExpr(s, false, false)
	}
	return loc
}

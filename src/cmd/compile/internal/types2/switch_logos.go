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

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	. "internal/types/errors"
)

const (
	logosOpenUnion   = "[correctness::open_union_value]"
	logosNonMember   = "[correctness::union_nonmember]"
	logosMemberLacks = "[correctness::union_member_method]"
	logosNonExhaust  = "[correctness::nonexhaustive_switch]"
	logosArmMismatch = "[correctness::switch_arm_mismatch]"
	logosNeedDefault = "[correctness::switch_expr_needs_default]"
	logosAssertFail  = "[correctness::assertion_failed]"
)

func logosIsClosed(tset *_TypeSet) bool {
	if !tset.hasTerms() {
		return false
	}
	for _, t := range tset.terms {
		if t.tilde {
			return false
		}
	}
	return true
}

func logosClosedUnion(t Type) *_TypeSet {
	if t == nil || isTypeParam(t) {
		return nil
	}
	iface, _ := t.Underlying().(*Interface)
	if iface == nil {
		return nil
	}
	tset := iface.typeSet()
	if !logosIsClosed(tset) {
		return nil
	}
	return tset
}

func logosMemberOf(t Type, tset *_TypeSet) bool {
	for _, term := range tset.terms {
		if Identical(term.typ, t) {
			return true
		}
	}
	return false
}

func (check *Checker) logosUnionsOK() bool {
	return check.conf.Logos || check.conf.LogosUnions
}

func (check *Checker) logosUnionMethods(ityp *Interface, iface *syntax.InterfaceType) {
	if !check.logosUnionsOK() || ityp.typeSet().NumMethods() == 0 {
		return
	}
	next := 0
	for _, f := range iface.MethodList {
		if f.Name != nil {
			continue
		}
		u, _ := ityp.embeddeds[next].(*Union)
		next++
		if u == nil {
			continue
		}
		_, tlist := flattenUnion(nil, f.Type)
		for i, t := range u.terms {
			if i < len(tlist) {
				check.logosTermMethods(ityp, t, tlist[i])
			}
		}
	}
}

func (check *Checker) logosTermMethods(ityp *Interface, t *Term, at syntax.Expr) {
	if t.tilde || !isValid(t.typ) {
		return
	}
	members := []Type{t.typ}
	if nested, _ := t.typ.Underlying().(*Interface); nested != nil {
		members = members[:0]
		for _, term := range nested.typeSet().terms {
			if !term.tilde {
				members = append(members, term.typ)
			}
		}
	}
	for _, m := range members {
		var cause string
		if !check.hasAllMethods(m, ityp, true, Identical, &cause) {
			check.errorf(at, InvalidUnion, "%s union member %s does not implement the methods of the union %s", logosMemberLacks, m, cause)
		}
	}
}

func (check *Checker) logosValueType(pos syntax.Pos, typ Type, tset *_TypeSet) bool {
	if !check.logosUnionsOK() {
		return false
	}
	switch {
	case logosIsClosed(tset):
		return true
	case tset.hasTerms() || tset.comparable:
		check.softErrorf(pos, MisplacedConstraintIface, "%s %s has ~ terms or an unbounded type set", logosOpenUnion, typ)
		return true
	}
	return false
}

func (check *Checker) logosConversionUnion(T Type) bool {
	return check.logosUnionsOK() && logosClosedUnion(T) != nil
}

func (check *Checker) logosUnionDefault(x *operand, u *Interface) (Type, bool) {
	if !check.logosUnionsOK() || !logosIsClosed(u.typeSet()) {
		return nil, false
	}
	d := Default(x.typ())
	if !check.implements(d, u, false, nil) {
		return nil, false
	}
	return d, true
}

func (check *Checker) logosUnionConversion(x *operand, T Type) bool {
	if !check.logosConversionUnion(T) {
		return false
	}
	if ok, _ := x.assignableTo(check, T, nil); ok {
		return false
	}
	from := x.typ()
	if isUntyped(from) {
		from = Default(from)
	}
	check.errorf(x, InvalidConversion, "%s %s is not a member of %s", logosNonMember, from, T)
	x.invalidate()
	return true
}

func (check *Checker) logosNonMemberAssert(e syntax.Expr, x *operand, T Type) bool {
	if !check.conf.Logos {
		return false
	}
	tset := logosClosedUnion(x.typ())
	if tset == nil || isTypeParam(T) || logosMemberOf(T, tset) {
		return false
	}
	if _, ok := e.(*syntax.AssertExpr); ok {
		return true
	}
	if IsInterface(T) {
		if iface, ok := T.Underlying().(*Interface); ok {
			if ttset := iface.typeSet(); ttset != nil && ttset.hasTerms() && !ttset.subsetOf(tset) {
				check.errorf(e, ImpossibleAssert, "%s %s is not a member of %s", logosNonMember, T, x.typ())
				return true
			}
		}
		return false
	}
	check.errorf(e, ImpossibleAssert, "%s %s is not a member of %s", logosNonMember, T, x.typ())
	return true
}
func (check *Checker) logosUnionAssign(x *operand, T Type, context string) bool {
	if !check.conf.Logos {
		return false
	}
	tset := logosClosedUnion(T)
	if tset == nil {
		return false
	}
	from := x.typ()
	if from == nil {
		return false
	}
	if isUntyped(from) {
		from = Default(from)
	}
	if logosMemberOf(from, tset) {
		return false
	}
	check.errorf(x, IncompatibleAssign, "%s cannot use %s as %s value in %s: %s is not a member of %s", logosNonMember, x, T, context, from, T)
	x.invalidate()
	return true
}

func (check *Checker) logosUnionLiteralMiss(x, y *operand) (union Type, from Type) {
	if !check.conf.Logos {
		return nil, nil
	}
	for _, pair := range [2][2]*operand{{x, y}, {y, x}} {
		un, lit := pair[0], pair[1]
		tset := logosClosedUnion(un.typ())
		if tset == nil || lit.mode() != constant_ || !isUntyped(lit.typ()) {
			continue
		}
		if d := Default(lit.typ()); !logosMemberOf(d, tset) {
			return un.typ(), d
		}
	}
	return nil, nil
}

func (check *Checker) logosUnionCompareMiss(x, y *operand, op syntax.Operator) bool {
	union, from := check.logosUnionLiteralMiss(x, y)
	if union == nil {
		return false
	}
	check.errorf(y, MismatchedTypes, "%s invalid operation: %s %s %s (%s is not a member of %s)", logosNonMember, x.expr, op, y.expr, from, union)
	x.invalidate()
	return true
}

func (check *Checker) logosUnionCaseMiss(x, y *operand) bool {
	union, from := check.logosUnionLiteralMiss(x, y)
	if union == nil {
		return false
	}
	check.errorf(x, MismatchedTypes, "%s invalid case %s in switch on %s (%s is not a member of %s)", logosNonMember, x.expr, y.expr, from, union)
	x.invalidate()
	return true
}

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

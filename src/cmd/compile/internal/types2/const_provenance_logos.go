// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	. "internal/types/errors"
)

type ReadonlyDescendants struct {
	Elem Type
}

func (t *ReadonlyDescendants) Underlying() Type { return t }

func (t *ReadonlyDescendants) String() string { return TypeString(t, nil) }

func logosAsReadonlyDescendants(T Type) *ReadonlyDescendants {
	t, _ := Unalias(T).(*ReadonlyDescendants)
	return t
}

func logosWithReadonlyDescendants(T Type) Type {
	if T == nil || logosAsConstView(T) != nil || logosAsReadonlyDescendants(T) != nil || logosReferenceFree(T) {
		return T
	}
	return &ReadonlyDescendants{Elem: T}
}

func logosReadonlyBase(T Type) (base Type, descendants bool, readonly bool) {
	if t := logosAsConstView(T); t != nil {
		return t.Elem, false, true
	}
	if t := logosAsReadonlyDescendants(T); t != nil {
		return t.Elem, true, false
	}
	return T, false, false
}

func logosElementsAreCopies(container Type) bool {
	c := logosAsConstView(container)
	return c != nil && c.ConstMap
}

func logosReadonlyProject(container, projected Type, throughReference bool) Type {
	if logosElementsAreCopies(container) {
		return projected
	}
	_, descendants, readonly := logosReadonlyBase(container)
	if readonly || descendants && throughReference {
		if logosAsConstView(projected) != nil {
			return projected
		}
		if t := logosAsReadonlyDescendants(projected); t != nil {
			projected = t.Elem
		}
		return &ConstView{Elem: projected}
	}
	if descendants {
		return logosWithReadonlyDescendants(projected)
	}
	return projected
}

func logosIndexStoreProtected(container Type) bool {
	base, descendants, readonly := logosReadonlyBase(container)
	if !descendants {
		return readonly
	}
	_, inline := base.Underlying().(*Array)
	return !inline
}

func (check *Checker) logosFieldStoreProtected(container Type, name string) bool {
	base, descendants, readonly := logosReadonlyBase(container)
	if !descendants {
		return readonly
	}
	_, _, indirect := lookupFieldOrMethod(base, true, check.pkg, name, false)
	return indirect
}

func logosDerefStoreProtected(container Type) bool {
	_, descendants, readonly := logosReadonlyBase(container)
	return readonly || descendants
}

func logosContainsReadonlyDescendants(T Type) bool {
	found := false
	logosWalkType(T, func(T Type) {
		if _, ok := Unalias(T).(*ReadonlyDescendants); ok {
			found = true
		}
	}, make(map[Type]bool))
	return found
}

func (check *Checker) logosReadonlyBuiltinArg(a *operand, id builtinId) bool {
	if !logosContainsReadonlyDescendants(a.typ()) {
		return true
	}
	base, _, _ := logosReadonlyBase(a.typ())
	switch id {
	case _Len, _Cap:
		a.typ_ = base
		if p, ok := base.Underlying().(*Pointer); ok {
			if b, d, _ := logosReadonlyBase(p.base); d {
				a.typ_ = &Pointer{base: b}
			}
		}
		return true
	case _Append, _Copy, _Delete, _Clear:
		if base != a.typ() {
			a.typ_ = &ConstView{Elem: base}
			return true
		}
	}
	check.errorf(a, IncompatibleAssign, "%s cannot pass readonly descendants to builtin %s", logosConstEscape, predeclaredFuncs[id].name)
	return false
}

func (check *Checker) logosReadonlyBoundary(x *operand, T Type, context string) bool {
	if !check.conf.Logos || !x.isValid() {
		return false
	}
	src := logosContainsReadonlyDescendants(x.typ())
	if T == nil {
		return false
	}
	dst := logosContainsReadonlyDescendants(T)
	if !src && !dst {
		return false
	}
	if x.isNil() && hasNil(T) {
		x.typ_ = T
		return true
	}
	if Identical(x.typ(), T) {
		return true
	}
	base, descendants, readonly := logosReadonlyBase(T)
	if descendants || readonly {
		S, _, _ := logosReadonlyBase(x.typ())
		if p, ok := S.Underlying().(*Pointer); ok && readonly {
			if b, d, _ := logosReadonlyBase(p.base); d {
				S = &Pointer{base: b}
			}
		}
		copy := *x
		copy.typ_ = S
		if ok, _ := copy.assignableTo(check, base, nil); ok {
			x.typ_ = T
			return true
		}
	}
	check.errorf(x, IncompatibleAssign, "%s cannot erase readonly descendants of %s in %s", logosConstEscape, ExprString(x.expr), context)
	x.invalidate()
	return true
}

func (check *Checker) logosConstTypeExpr(x *operand, e *syntax.ConstType) exprKind {
	x.mode_ = typexpr
	x.typ_ = check.logosConstType(e, nil)
	x.expr = e
	return expression
}

func (check *Checker) logosZeroCopyConvert(x *operand, T Type) bool {
	if !check.conf.Logos || !x.isValid() {
		return false
	}
	cdst := logosAsConstView(T)
	if cdst == nil {
		return false
	}
	if csrc := logosAsConstView(x.typ()); csrc != nil {
		if !Identical(csrc.Elem, cdst.Elem) {
			return false
		}
		x.typ_ = T
		return true
	}
	check.conversion(x, cdst.Elem)
	if !x.isValid() {
		return true
	}
	if x.mode() == constant_ {
		x.mode_ = value
		x.val = nil
	}
	x.typ_ = T
	return true
}

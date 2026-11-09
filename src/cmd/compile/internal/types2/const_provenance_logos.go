// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import "cmd/compile/internal/syntax"

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

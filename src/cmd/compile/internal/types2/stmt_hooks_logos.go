// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	. "internal/types/errors"
)

const logosConstSwitchContinue stmtContext = inTypeSwitch << 1

func (check *Checker) logosStmtEnterFunc(name string, body *syntax.BlockStmt) {
	if name == "<function literal>" {
		check.logosEnterLit(body)
	} else {
		check.logosEnterFunc(body)
	}
}

func (check *Checker) logosStmtYield(slot *syntax.Stmt) syntax.Stmt {
	s := check.logosYieldForm(*slot)
	*slot = s
	return s
}

func (check *Checker) logosStmtReadonlyCase(e syntax.Expr, T *Type) bool {
	if logosContainsReadonlyDescendants(*T) {
		check.error(e, InvalidTypeSwitch, "cannot use readonly descendants in type switch cases")
		*T = Typ[Invalid]
		return true
	}
	return false
}

func (check *Checker) logosStmtSendValue(val *operand, value syntax.Expr) {
	if !check.logosLambdaDefer(val, value) {
		check.genericExpr(val, value, nil)
	}
}

func logosStmtSwitchContext(inner stmtContext) stmtContext {
	if inner&logosConstSwitchContinue != 0 {
		inner |= continueOk
	}
	return inner
}

func (check *Checker) logosStmtSwitchOperand(x *operand) *operand {
	if x.isValid() {
		if logosContainsReadonlyDescendants(x.typ()) {
			check.error(x, InvalidTypeSwitch, "cannot use type switch on value with readonly descendants")
		} else if isTypeParam(x.typ()) || (check.conf.Logos && logosViewedTypeParam(x.typ())) {
			check.errorf(x, InvalidTypeSwitch, "cannot use type switch on type parameter value %s", x)
		} else if IsInterface(x.typ()) {
			return x
		} else if check.conf.Logos && logosQualifiedInterface(x.typ()) {
			// A type switch reads the dynamic type without writing
			// through the view (SPEC 2.1: reads through the qualifier
			// are not mutations), so a const view of an interface is
			// a valid guard. A value carrying readonly descendants is
			// still rejected above. Each case variable keeps the view
			// through logosSwitchCaseView when its member could alias
			// protected storage.
			return x
		} else {
			check.errorf(x, InvalidTypeSwitch, "%s is not an interface", x)
		}
	}
	return nil
}

// logosSwitchCaseView returns the type of a type-switch case variable
// when the guard may carry a const view: a reference-bearing member of
// a const-view guard keeps the view so the variable cannot expose a
// writable alias to protected storage, while a reference-free member
// keeps the plain member type a reference-free view assigns to. An
// unqualified guard, an invalid type, or an already-qualified member
// keeps the member type unchanged.
func logosSwitchCaseView(sx *operand, T Type) Type {
	if sx == nil || !sx.isValid() || T == nil || !isValid(T) {
		return T
	}
	if _, qualified := logosViewBase(sx.typ()); !qualified {
		return T
	}
	if logosAsConstView(T) != nil || logosReferenceFree(T) {
		return T
	}
	return &ConstView{Elem: T}
}

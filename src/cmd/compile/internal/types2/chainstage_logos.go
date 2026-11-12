// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	. "internal/types/errors"
)

const logosNamedArgument = "[correctness::named_argument] a named argument is allowed only as limit = N in a group stage call"

func (check *Checker) logosNamedArgs(args []syntax.Expr) bool {
	found := false
	for _, arg := range args {
		if kv, ok := arg.(*syntax.KeyValueExpr); ok {
			check.error(kv, InvalidCall, logosNamedArgument)
			check.use(kv.Value)
			found = true
		}
	}
	return found
}

func (check *Checker) logosNamedArgCall(x *operand, call *syntax.CallExpr) bool {
	if !check.logosNamedArgs(call.ArgList) {
		return false
	}
	check.logosUseCallee(call.Fun)
	for _, arg := range call.ArgList {
		switch arg.(type) {
		case *syntax.KeyValueExpr, *syntax.LambdaExpr:
		default:
			check.use(arg)
		}
	}
	x.mode_ = invalid
	x.expr = call
	x.typ_ = Typ[Invalid]
	return true
}

func (check *Checker) logosUseCallee(fun syntax.Expr) {
	if sel, ok := syntax.Unparen(fun).(*syntax.SelectorExpr); ok && logosChainMethods[sel.Sel.Value] {
		check.use(sel.X)
		return
	}
	check.use(fun)
}

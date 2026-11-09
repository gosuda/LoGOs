// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	. "internal/types/errors"
)

const logosLambdaNeedsTarget = "[correctness::lambda_needs_target] lambda needs a target function type"

func (check *Checker) logosLambdaPrechecked(x *operand, e syntax.Expr) bool {
	if !check.conf.Logos {
		return false
	}
	t, ok := check.logosLambdaDone[syntax.Unparen(e)]
	if !ok {
		return false
	}
	x.mode_ = value
	x.expr = e
	x.typ_ = t
	check.record(x)
	return true
}

func (check *Checker) logosLambdaCallArgs(call *syntax.CallExpr, sig *Signature) {
	if !check.conf.Logos || sig.TypeParams().Len() > 0 {
		return
	}
	targets := check.lambdaArgTargets(call, sig)
	for i, arg := range call.ArgList {
		lam, ok := syntax.Unparen(arg).(*syntax.LambdaExpr)
		if !ok || i >= len(targets) || targets[i] == nil {
			continue
		}
		psig, ok := targets[i].(*Signature)
		if !ok || psig.TypeParams().Len() > 0 {
			continue
		}
		var op operand
		if check.logosLambdaTargeted(&op, psig, lam); op.isValid() {
			if check.logosLambdaDone == nil {
				check.logosLambdaDone = make(map[syntax.Expr]Type)
			}
			check.logosLambdaDone[arg] = op.typ()
		}
	}
}

func (check *Checker) lambdaArgTargets(call *syntax.CallExpr, sig *Signature) []Type {
	n := len(call.ArgList)
	targets := make([]Type, n)
	params := sig.params
	dots := hasDots(call)
	for i := range targets {
		targets[i] = lambdaParamTarget(params, sig.variadic, dots, n, i)
	}
	return targets
}

func lambdaParamTarget(params *Tuple, variadic, dots bool, nargs, i int) Type {
	if params == nil {
		return nil
	}
	if !variadic {
		if i < params.Len() {
			return params.At(i).Type()
		}
		return nil
	}
	if dots {
		if params.Len() == 1 {
			return params.At(0).Type()
		}
		return nil
	}
	if i < params.Len()-1 {
		return params.At(i).Type()
	}
	if params.Len() > 0 {
		if s, ok := params.At(params.Len() - 1).Type().(*Slice); ok {
			return s.Elem()
		}
	}
	return nil
}

func (check *Checker) logosLambdaTargeted(x *operand, sig *Signature, e *syntax.LambdaExpr) exprKind {
	fail := func() exprKind {
		x.mode_ = invalid
		x.expr = e
		x.typ_ = Typ[Invalid]
		return expression
	}
	if len(e.Params) != sig.params.Len() {
		check.errorf(e.Arrow, IncompatibleAssign, "cannot use %s as %s: parameter count mismatch (have %d, want %d)", ExprString(e), sig, len(e.Params), sig.params.Len())
		return fail()
	}
	scope := NewScope(check.scope, e.Pos(), endPos(e.Body), "lambda")
	scope.isFunc = true
	params := make([]*Var, len(e.Params))
	for i, name := range e.Params {
		p := NewParam(name.Pos(), check.pkg, name.Value, sig.params.At(i).Type())
		params[i] = p
		check.declare(scope, name, p, name.Pos())
	}
	lamsig := NewSignatureType(nil, nil, nil, NewTuple(params...), sig.results, sig.variadic)
	lamsig.scope = scope
	lamsig.scope.pos = e.Pos()
	lamsig.scope.end = endPos(e.Body)
	check.logosChainNestedTry(e.Body)
	saved := check.environment
	check.environment = environment{
		decl:    check.decl,
		scope:   scope,
		version: check.version,
		iota:    check.iota,
		sig:     lamsig,
	}
	defer func() { check.environment = saved }()
	switch body := e.Body.(type) {
	case *syntax.BlockStmt:
		check.logosEnterFunc(body)
		defer check.logosExitFunc()
		check.stmtList(0, body.List)
		if check.hasLabel && !check.conf.IgnoreBranchErrors {
			check.labels(body)
		}
		if sig.results.Len() > 0 && !check.isTerminating(body, "") {
			check.error(body.Rbrace, MissingReturn, "missing return")
		}
		check.usage(scope)
	default:
		expr, ok := e.Body.(syntax.Expr)
		if !ok {
			check.error(e.Body, InvalidSyntaxTree, "invalid lambda body")
			return fail()
		}
		if sig.results.Len() == 0 {
			synth := &syntax.ExprStmt{X: expr}
			synth.SetPos(expr.Pos())
			check.stmt(0, synth)
		} else {
			synth := &syntax.ReturnStmt{Results: expr}
			synth.SetPos(expr.Pos())
			check.initVars(sig.results.vars, []syntax.Expr{expr}, synth)
		}
		check.usage(scope)
	}
	check.environment = saved
	x.mode_ = value
	x.expr = e
	x.typ_ = sig
	check.record(x)
	return expression
}

func (check *Checker) logosLambda(x *operand, t *target, e *syntax.LambdaExpr) exprKind {
	fail := func() exprKind {
		x.mode_ = invalid
		x.expr = e
		x.typ_ = Typ[Invalid]
		return expression
	}
	if !check.conf.Logos {
		check.error(e, InvalidSyntaxTree, "unexpected lambda expression")
		return fail()
	}
	if t == nil || t.sig == nil {
		check.errorf(e.Arrow, IncompatibleAssign, "%s", logosLambdaNeedsTarget)
		return fail()
	}
	return check.logosLambdaTargeted(x, t.sig, e)
}

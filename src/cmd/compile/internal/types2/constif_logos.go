// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	"go/constant"
	. "internal/types/errors"
)

const logosNotConstant = "[comptime::not_constant]"

// logosDead reports whether n is a statement of a dead `const if` branch.
// Every walk that can observe a statement consults this one predicate; the
// set is filled only when a condition folded to a bool, so with no folded
// condition nothing is dead and behavior is unchanged.
func (check *Checker) logosDead(n syntax.Node) bool {
	if check.Info == nil {
		return false
	}
	s, ok := n.(syntax.Stmt)
	return ok && check.Info.LogosConstIfDead[s]
}

func (check *Checker) logosConstIf(ctxt stmtContext, s *syntax.ConstIfStmt) {
	live := check.logosConstIfLive(s)
	if live >= 0 {
		if check.Info.LogosConstIf == nil {
			check.Info.LogosConstIf = make(map[*syntax.ConstIfStmt]bool)
		}
		check.Info.LogosConstIf[s] = live == 0
		check.logosMarkConstIfDead(s, live)
	} else if check.logosCtDeclCheck {
		// Decl-time checking of a comptime generic: fact-gated arms
		// fold only in the concrete instance; check nothing here.
		check.logosNoteDeadUses(s.Then, s.Else)
		return
	}
	inner := ctxt &^ (fallthroughOk | finalSwitchCase | inTypeSwitch)
	if !check.logosDead(s.Then) {
		check.stmt(inner, s.Then)
	}
	if s.Else != nil && !check.logosDead(s.Else) {
		check.stmt(inner, s.Else)
	}
}

func (check *Checker) logosConstIfLive(s *syntax.ConstIfStmt) int {
	var x operand
	outer := check.logosPred
	check.logosPred = logosPredState{depth: 1}
	check.expr(nil, &x, s.Cond)
	deferred := check.logosPred.calls
	check.logosPred = outer
	check.logosCtTypeOfFold(s.Cond, &x)
	switch {
	case !x.isValid():
	case x.mode() != constant_ && check.logosPredOnly(s.Cond, deferred):
	case x.mode() != constant_ && check.logosCtTypeOfDefer(s.Cond):
		// A TypeOf identity condition on an unsubstituted type
		// parameter folds only in the concrete instance; both arms
		// stay live here and the instance analysis folds the branch.
	case x.mode() != constant_ && check.logosCtDeclCheck && !check.logosCondReadsVar(s.Cond):
		// Decl-time checking of a comptime generic: field-fact and
		// TypeOf conditions fold only in the concrete instance, so
		// both arms stay live here.
	case x.mode() != constant_:
		check.errorf(s.Cond, InvalidCond, "%s const if condition is not constant", logosNotConstant)
	case !allBoolean(x.typ()):
		check.error(s.Cond, InvalidCond, "non-boolean condition in const if statement")
	case x.val.Kind() != constant.Bool:
	case constant.BoolVal(x.val):
		return 0
	default:
		return 1
	}
	return -1
}

func (check *Checker) logosCondReadsVar(cond syntax.Expr) bool {
	reads := false
	syntax.Inspect(cond, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CallExpr, *syntax.FuncLit, *syntax.LambdaExpr:
			return false
		case *syntax.Name:
			if v, ok := check.Uses[n].(*Var); ok && !v.IsField() && check.logosFieldVars[v] == nil {
				reads = true
			}
		}
		return !reads
	})
	return reads
}

func (check *Checker) logosMarkConstIfDead(s *syntax.ConstIfStmt, live int) {
	if check.Info.LogosConstIfDead == nil {
		check.Info.LogosConstIfDead = make(map[syntax.Stmt]bool)
	}
	var dead syntax.Stmt
	if live == 0 {
		dead = s.Else
	} else {
		dead = s.Then
	}
	markDeadStmts(check.Info.LogosConstIfDead, dead)
	check.logosNoteDeadUses(dead)
}

func (check *Checker) logosNoteDeadUses(list ...syntax.Stmt) {
	w := logosDeadUseWalk{check: check}
	w.stmts(list)
}

type logosDeadUseWalk struct {
	check   *Checker
	parent  *logosDeadUseWalk
	checked *Scope
	names   map[string]bool
	guard   *syntax.Name
}

func (w *logosDeadUseWalk) child() *logosDeadUseWalk {
	return &logosDeadUseWalk{check: w.check, parent: w, guard: w.guard}
}

func (w *logosDeadUseWalk) bind(name *syntax.Name) {
	if name == nil || name.Value == "_" {
		return
	}
	if w.names == nil {
		w.names = make(map[string]bool)
	}
	w.names[name.Value] = true
}

func (w *logosDeadUseWalk) lookup(name *syntax.Name) (*Scope, Object) {
	for scope := w; scope != nil; scope = scope.parent {
		if scope.names[name.Value] {
			return nil, nil
		}
	}
	return w.check.lookupScope(name.Value)
}

func (w *logosDeadUseWalk) note(name *syntax.Name, qualifier bool) {
	if name.Value == "_" {
		return
	}
	scope, obj := w.lookup(name)
	switch obj := obj.(type) {
	case *PkgName:
		if qualifier {
			w.check.recordUse(name, obj)
			w.check.usedPkgNames[obj] = true
		}
	case *Var:
		if obj.pkg == w.check.pkg {
			w.check.usedVars[obj] = true
		}
	}
	if pn := w.check.dotImportMap[dotImportKey{scope, name.Value}]; pn != nil {
		w.check.usedPkgNames[pn] = true
	}
}

func (w *logosDeadUseWalk) walk(n syntax.Node) {
	if n != nil {
		syntax.Walk(n, w)
	}
}

func (w *logosDeadUseWalk) stmts(list []syntax.Stmt) {
	for _, s := range list {
		w.walk(s)
	}
}

func (w *logosDeadUseWalk) targets(lhs syntax.Expr, declare bool) {
	for _, x := range syntax.UnpackListExpr(lhs) {
		name, ok := syntax.Unparen(x).(*syntax.Name)
		if !ok {
			w.walk(x)
			continue
		}
		if !declare {
			continue
		}
		if w.checked != nil {
			if _, reused := w.checked.Lookup(name.Value).(*Var); reused {
				continue
			}
		}
		w.bind(name)
	}
}

func (w *logosDeadUseWalk) Visit(node syntax.Node) syntax.Visitor {
	switch n := node.(type) {
	case nil, *syntax.BranchStmt:
	case *syntax.Name:
		w.note(n, false)
	case *syntax.SelectorExpr:
		if name, ok := n.X.(*syntax.Name); ok {
			w.note(name, true)
		} else {
			w.walk(n.X)
		}
	case *syntax.OptSelectExpr:
		w.walk(n.X)
	case *syntax.Field:
		w.walk(n.Type)
	case *syntax.LabeledStmt:
		w.walk(n.Stmt)
	case *syntax.VarDecl:
		w.walk(n.Type)
		w.walk(n.Values)
		for _, name := range n.NameList {
			w.bind(name)
		}
	case *syntax.ConstDecl:
		w.walk(n.Type)
		w.walk(n.Values)
		for _, name := range n.NameList {
			w.bind(name)
		}
	case *syntax.TypeDecl:
		w.bind(n.Name)
		if len(n.TParamList) > 0 {
			w = w.child()
			for _, field := range n.TParamList {
				w.bind(field.Name)
			}
			for _, field := range n.TParamList {
				w.walk(field.Type)
			}
		}
		w.walk(n.Type)
	case *syntax.AssignStmt:
		if n.Rhs == nil || n.Op != 0 && n.Op != syntax.Def {
			return w
		}
		w.walk(n.Rhs)
		w.targets(n.Lhs, n.Op == syntax.Def)
	case *syntax.RangeClause:
		w.walk(n.X)
		w.targets(n.Lhs, n.Def)
	case *syntax.BlockStmt, *syntax.IfStmt, *syntax.ForStmt, *syntax.CommClause:
		return w.child()
	case *syntax.SwitchStmt:
		return w.switchScope(n.Tag)
	case *syntax.SwitchExpr:
		return w.switchScope(n.Tag)
	case *syntax.TypeSwitchGuard:
		w.walk(n.X)
	case *syntax.CaseClause:
		w.walk(n.Cases)
		branch := w.child()
		branch.bind(w.guard)
		branch.stmts(n.Body)
	case *syntax.ArmClause:
		w.walk(n.Cases)
		branch := w.child()
		branch.bind(w.guard)
		branch.walk(n.Body)
	case *syntax.FuncLit:
		w.walk(n.Type)
		body := w.child()
		for _, field := range n.Type.ParamList {
			body.bind(field.Name)
		}
		for _, field := range n.Type.ResultList {
			body.bind(field.Name)
		}
		body.walk(n.Body)
	case *syntax.LambdaExpr:
		body := w.child()
		for _, name := range n.Params {
			body.bind(name)
		}
		body.walk(n.Body)
	case *syntax.ConstForStmt:
		w.walk(n.Range)
		body := w.child()
		body.bind(n.Field)
		body.bind(n.Index)
		body.walk(n.Body)
	case *syntax.ConstSwitchStmt:
		w.walk(n.Targ)
		for _, c := range n.Cases {
			w.walk(c.Type)
			w.child().stmts(c.Body)
		}
	case *syntax.CompositeLit:
		w.walk(n.Type)
		keys := w.literalExprKeys(n.Type)
		for _, elem := range n.ElemList {
			kv, ok := elem.(*syntax.KeyValueExpr)
			if ok && !keys {
				if _, field := kv.Key.(*syntax.Name); field {
					w.walk(kv.Value)
					continue
				}
			}
			w.walk(elem)
		}
	default:
		return w
	}
	return nil
}

func (w *logosDeadUseWalk) switchScope(tag syntax.Expr) *logosDeadUseWalk {
	w = w.child()
	w.guard = nil
	if g, ok := tag.(*syntax.TypeSwitchGuard); ok {
		w.guard = g.Lhs
	}
	return w
}

func (w *logosDeadUseWalk) literalExprKeys(typ syntax.Expr) bool {
	switch typ := syntax.Unparen(typ).(type) {
	case *syntax.MapType, *syntax.ArrayType, *syntax.SliceType:
		return true
	case *syntax.Name:
		_, obj := w.lookup(typ)
		if name, ok := obj.(*TypeName); ok && name.Type() != nil {
			switch name.Type().Underlying().(type) {
			case *Map, *Array, *Slice:
				return true
			}
		}
	}
	return false
}

func (check *Checker) logosTerminatingConstIf(s *syntax.ConstIfStmt) bool {
	if live, ok := check.Info.LogosConstIf[s]; ok {
		if live {
			return check.isTerminating(s.Then, "")
		}
		return s.Else != nil && check.isTerminating(s.Else, "")
	}
	if v, ok := check.constIfValue(s.Cond); ok {
		if v {
			return check.isTerminating(s.Then, "")
		}
		return s.Else != nil && check.isTerminating(s.Else, "")
	}
	// The condition did not fold in this check, but an enclosing analysis
	// may already have marked one arm dead (a comptime instantiation):
	// the dead arm cannot supply the missing return.
	thenTerm := check.logosDead(s.Then) || check.isTerminating(s.Then, "")
	elseTerm := s.Else == nil || check.logosDead(s.Else) || check.isTerminating(s.Else, "")
	return s.Else != nil && thenTerm && elseTerm
}

func (check *Checker) logosConstIfKnown(e syntax.Expr) (bool, bool) {
	if _, val, ok := check.logosChecked(e); ok && val != nil && val.Kind() == constant.Bool {
		return constant.BoolVal(val), true
	}
	if info, ok := check.untyped[e]; ok && info.val != nil && info.val.Kind() == constant.Bool {
		return constant.BoolVal(info.val), true
	}
	return false, false
}

func (check *Checker) constIfValue(e syntax.Expr) (bool, bool) {
	if v, ok := check.logosConstIfKnown(e); ok {
		return v, true
	}
	return check.logosConstIfDerived(e)
}

func (check *Checker) logosConstIfDerived(e syntax.Expr) (bool, bool) {
	var x operand
	outerPred := check.logosPred
	check.logosPred = logosPredState{depth: 1}
	saved := check.conf.Error
	check.conf.Error = func(error) {}
	check.expr(nil, &x, e)
	check.conf.Error = saved
	check.logosPred = outerPred
	if !x.isValid() || x.val == nil || x.val.Kind() != constant.Bool {
		return false, false
	}
	return constant.BoolVal(x.val), true
}

func (check *Checker) logosHasBreakConstIf(s *syntax.ConstIfStmt, label string, implicit bool) bool {
	if check.hasBreak(s.Then, label, implicit) {
		return true
	}
	return s.Else != nil && check.hasBreak(s.Else, label, implicit)
}

func markDeadStmts(dead map[syntax.Stmt]bool, s syntax.Stmt) {
	if s == nil {
		return
	}
	dead[s] = true
	syntax.Inspect(s, func(n syntax.Node) bool {
		if st, ok := n.(syntax.Stmt); ok && st != s {
			dead[st] = true
		}
		return true
	})
}

// logosCtTypeOfFold folds an == or != whose operands are both
// comptime.TypeOf calls with recorded concrete types (instance
// analysis). The bool constant then drives the dead-branch rule.
func (check *Checker) logosCtTypeOfFold(e syntax.Expr, x *operand) {
	if x.val != nil || !x.isValid() {
		return
	}
	op, ok := syntax.Unparen(e).(*syntax.Operation)
	if !ok || (op.Op != syntax.Eql && op.Op != syntax.Neq) {
		return
	}
	tx := check.logosTypeOfOperand(op.X)
	ty := check.logosTypeOfOperand(op.Y)
	if tx == nil || ty == nil {
		return
	}
	eq := Identical(tx, ty)
	if op.Op == syntax.Neq {
		eq = !eq
	}
	x.mode_ = constant_
	x.typ_ = Typ[UntypedBool]
	x.val = constant.MakeBool(eq)
}

func (check *Checker) logosTypeOfOperand(e syntax.Expr) Type {
	call, ok := syntax.Unparen(e).(*syntax.CallExpr)
	if !ok {
		return nil
	}
	if t, ok := check.logosTypeOfFact[call]; ok && t != nil && isValid(t) && !isTypeParam(t) {
		return t
	}
	return nil
}

// logosCtTypeOfDefer reports whether e compares comptime.TypeOf calls
// whose argument still contains a type parameter: the condition folds
// only in a concrete instantiation.
func (check *Checker) logosCtTypeOfDefer(e syntax.Expr) bool {
	defer_ := false
	found := false
	syntax.Inspect(e, func(n syntax.Node) bool {
		idx, ok := n.(*syntax.IndexExpr)
		if !ok {
			return true
		}
		sel, ok := idx.X.(*syntax.SelectorExpr)
		if !ok || sel.Sel.Value != "TypeOf" {
			return true
		}
		found = true
		if check.logosTypeOfDeferred[idx] {
			defer_ = true
		}
		return true
	})
	return found && defer_
}

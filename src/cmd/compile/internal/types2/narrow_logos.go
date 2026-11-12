// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	. "internal/types/errors"
	"maps"
	"slices"
)

const logosResultBeforeErr = "[correctness::result_before_error_check]"

func (check *Checker) logosEnterFunc(body *syntax.BlockStmt) {
	check.logosEnterFuncBody(body)
	if !check.conf.Logos {
		return
	}
	check.logosLitSave = append(check.logosLitSave, nil)
}

func (check *Checker) logosEnterLit(body *syntax.BlockStmt) {
	check.logosEnterFuncBody(body)
	if !check.conf.Logos {
		return
	}
	check.logosStripTainted()
}

func (check *Checker) logosEnterFuncBody(body *syntax.BlockStmt) {
	if !check.conf.Logos {
		return
	}
	outermost := len(check.logosDisqual) == 0
	dq := &logosDisqualWalk{
		stage:     make(map[*syntax.LambdaExpr]bool),
		addr:      make(map[string]bool),
		inLoop:    make(map[string]bool),
		inClos:    make(map[string]bool),
		refInClos: make(map[string]bool),
		assignOut: make(map[string]bool),
	}
	if sig := check.sig; sig != nil {
		for _, vars := range []*Tuple{sig.params, sig.results} {
			for i := range vars.Len() {
				dq.params = append(dq.params, vars.At(i).name)
			}
		}
		if sig.recv != nil {
			dq.params = append(dq.params, sig.recv.name)
		}
	}
	if body != nil {
		syntax.Walk(body, dq)
	}
	set := make(map[string]bool, len(dq.addr)+len(dq.inLoop)+len(dq.inClos))
	for name := range dq.addr {
		set[name] = true
	}
	for name := range dq.inLoop {
		set[name] = true
	}
	for name := range dq.inClos {
		set[name] = true
	}
	check.logosDisqual = append(check.logosDisqual, set)
	if body != nil {
		if check.Info.LogosDisqual == nil {
			check.Info.LogosDisqual = make(map[*syntax.BlockStmt][]map[string]bool)
		}
		check.Info.LogosDisqual[body] = slices.Clone(check.logosDisqual)
	}
	if outermost {
		tainted := make(map[string]bool)
		for name := range dq.refInClos {
			if dq.assignOut[name] {
				tainted[name] = true
			}
		}
		check.logosTainted = tainted
	}
}

func (check *Checker) logosStripTainted() {
	var saved map[*Var]int
	for name := range check.logosTainted {
		obj := check.lookup(name)
		v, ok := obj.(*Var)
		if !ok || v.pkg != check.pkg {
			continue
		}
		if check.logosNarrow[v] > 0 {
			if saved == nil {
				saved = make(map[*Var]int)
			}
			saved[v] = check.logosNarrow[v]
			delete(check.logosNarrow, v)
		}
	}
	check.logosLitSave = append(check.logosLitSave, saved)
}

func (check *Checker) logosRestoreTainted() {
	n := len(check.logosLitSave)
	if n == 0 {
		return
	}
	saved := check.logosLitSave[n-1]
	check.logosLitSave = check.logosLitSave[:n-1]
	for v, c := range saved {
		check.logosNarrow[v] = c
	}
}

func (check *Checker) logosExitFunc() {
	if !check.conf.Logos {
		return
	}
	check.logosRestoreTainted()
	if n := len(check.logosDisqual); n > 0 {
		check.logosDisqual = check.logosDisqual[:n-1]
	}
}

type logosDisqualWalk struct {
	stack     []syntax.Node
	scopes    []map[string]bool
	params    []string
	stage     map[*syntax.LambdaExpr]bool
	loop      int
	clos      int
	lam       int
	addr      map[string]bool
	inLoop    map[string]bool
	inClos    map[string]bool
	refInClos map[string]bool
	assignOut map[string]bool
}

func (w *logosDisqualWalk) Visit(node syntax.Node) syntax.Visitor {
	if node == nil {
		w.leave()
		return w
	}
	w.declare(node)
	switch t := node.(type) {
	case *syntax.FuncLit, *syntax.GroupChild:
		w.clos++
	case *syntax.CallExpr:
		w.markStage(t)
	case *syntax.LambdaExpr:
		if w.stage[t] {
			w.lam++
		}
	case *syntax.ForStmt:
		w.loop++
	case *syntax.Operation:
		if t.Op == syntax.And && t.Y == nil {
			if id, ok := syntax.Unparen(t.X).(*syntax.Name); ok && id.Value != "_" {
				w.addr[id.Value] = true
			}
		}
	case *syntax.Name:
		if w.clos > 0 && t.Value != "_" {
			w.refInClos[t.Value] = true
		}
	case *syntax.AssignStmt:
		w.assign(t)
	case *syntax.RangeClause:
		if !t.Def {
			for _, name := range logosAssignNames(t.Lhs) {
				w.inLoop[name] = true
				w.assigned(name)
			}
		}
	}
	w.stack = append(w.stack, node)
	return w
}

func (w *logosDisqualWalk) leave() {
	top := w.stack[len(w.stack)-1]
	w.stack = w.stack[:len(w.stack)-1]
	switch top := top.(type) {
	case *syntax.FuncLit, *syntax.GroupChild:
		w.clos--
	case *syntax.LambdaExpr:
		if w.stage[top] {
			w.lam--
		}
	case *syntax.ForStmt:
		w.loop--
	case *syntax.BlockStmt, *syntax.CaseClause, *syntax.CommClause:
		w.scopes = w.scopes[:len(w.scopes)-1]
	}
}

func (w *logosDisqualWalk) declare(node syntax.Node) {
	switch t := node.(type) {
	case *syntax.FuncLit:
		for _, f := range slices.Concat(t.Type.ParamList, t.Type.ResultList) {
			if f.Name != nil {
				w.params = append(w.params, f.Name.Value)
			}
		}
	case *syntax.LambdaExpr:
		if _, ok := t.Body.(*syntax.BlockStmt); ok {
			for _, name := range t.Params {
				w.params = append(w.params, name.Value)
			}
		}
	case *syntax.BlockStmt, *syntax.CaseClause, *syntax.CommClause:
		scope := make(map[string]bool, len(w.params))
		for _, name := range w.params {
			scope[name] = true
		}
		w.params = nil
		w.scopes = append(w.scopes, scope)
	case *syntax.VarDecl:
		for _, name := range t.NameList {
			w.scopes[len(w.scopes)-1][name.Value] = true
		}
	}
}

func (w *logosDisqualWalk) assign(s *syntax.AssignStmt) {
	header := false
	switch w.stack[len(w.stack)-1].(type) {
	case *syntax.IfStmt, *syntax.ForStmt, *syntax.SwitchStmt:
		header = true
	}
	scope := w.scopes[len(w.scopes)-1]
	for _, name := range logosAssignNames(s.Lhs) {
		if s.Op == syntax.Def {
			redeclared := scope[name] && !header
			scope[name] = scope[name] || !header
			if !redeclared {
				continue
			}
		}
		if w.loop > 0 {
			w.inLoop[name] = true
		}
		w.assigned(name)
	}
}

func (w *logosDisqualWalk) assigned(name string) {
	if w.clos > 0 || w.lam > 0 {
		w.inClos[name] = true
	} else {
		w.assignOut[name] = true
	}
}

func (w *logosDisqualWalk) markStage(call *syntax.CallExpr) {
	if logosStageCalls(call) == nil {
		return
	}
	for _, arg := range call.ArgList {
		if lam, ok := syntax.Unparen(arg).(*syntax.LambdaExpr); ok {
			w.stage[lam] = true
		}
	}
}

func logosAssignNames(lhs syntax.Expr) []string {
	var names []string
	for _, e := range syntax.UnpackListExpr(lhs) {
		if id, ok := syntax.Unparen(e).(*syntax.Name); ok && id.Value != "_" {
			names = append(names, id.Value)
		}
	}
	return names
}

func (check *Checker) logosDisqualified(name string) bool {
	for _, set := range check.logosDisqual {
		if set[name] {
			return true
		}
	}
	return false
}

func (check *Checker) logosNarrowable(v *Var) bool {
	if v == nil || v.pkg != check.pkg || v.typ == nil {
		return false
	}
	switch v.Kind() {
	case LocalVar, RecvVar, ParamVar, ResultVar:
	default:
		return false
	}
	if logosAsOption(v.typ) == nil {
		return false
	}
	return !check.logosDisqualified(v.name)
}

func (check *Checker) logosNilTestOf(cond syntax.Expr) (v *Var, isNeq bool, ok bool) {
	op, ok := syntax.Unparen(cond).(*syntax.Operation)
	if !ok || (op.Op != syntax.Eql && op.Op != syntax.Neq) || op.Y == nil {
		return nil, false, false
	}
	var name *syntax.Name
	if check.isNil(op.X) {
		name, _ = syntax.Unparen(op.Y).(*syntax.Name)
	} else if check.isNil(op.Y) {
		name, _ = syntax.Unparen(op.X).(*syntax.Name)
	} else {
		return nil, false, false
	}
	if name == nil || name.Value == "_" {
		return nil, false, false
	}
	obj := check.lookup(name.Value)
	w, _ := obj.(*Var)
	if w == nil {
		return nil, false, false
	}
	return w, op.Op == syntax.Neq, true
}

func (check *Checker) logosCollectNeNil(cond syntax.Expr) []*Var {
	op, ok := syntax.Unparen(cond).(*syntax.Operation)
	if !ok || op.Y == nil {
		return nil
	}
	if op.Op == syntax.AndAnd {
		left := check.logosCollectNeNil(op.X)
		right := check.logosCollectNeNil(op.Y)
		return append(left, right...)
	}
	if op.Op != syntax.Neq {
		return nil
	}
	v, isNeq, ok := check.logosNilTestOf(cond)
	if !ok || !isNeq {
		return nil
	}
	return []*Var{v}
}

func (check *Checker) logosPushNarrow(vars []*Var) bool {
	if !check.conf.Logos {
		return false
	}
	var frame []*Var
	for _, v := range vars {
		if check.logosNarrowable(v) {
			frame = append(frame, v)
		}
	}
	if len(frame) == 0 {
		return false
	}
	if check.logosNarrow == nil {
		check.logosNarrow = make(map[*Var]int)
	}
	for _, v := range frame {
		check.logosNarrow[v]++
	}
	check.logosNarrowStack = append(check.logosNarrowStack, frame)
	return true
}

func (check *Checker) logosPopNarrow() {
	frames := check.logosNarrowStack
	frame := frames[len(frames)-1]
	check.logosNarrowStack = frames[:len(frames)-1]
	for _, v := range frame {
		if c := check.logosNarrow[v]; c <= 1 {
			delete(check.logosNarrow, v)
		} else {
			check.logosNarrow[v] = c - 1
		}
	}
}

func (check *Checker) logosNarrowBase() int {
	return len(check.logosNarrowStack)
}

func (check *Checker) logosPopToNarrow(base int) {
	for len(check.logosNarrowStack) > base {
		check.logosPopNarrow()
	}
}

func (check *Checker) logosPushIfThen(cond syntax.Expr) bool {
	return check.logosPushNarrow(check.logosCollectNeNil(cond))
}

func (check *Checker) logosPushAndCond(op syntax.Operator, lhs syntax.Expr) bool {
	if !check.conf.Logos || op != syntax.AndAnd {
		return false
	}
	return check.logosPushNarrow(check.logosCollectNeNil(lhs))
}

func (check *Checker) logosAfterStmt(s syntax.Stmt) {
	if !check.conf.Logos {
		return
	}
	ifStmt, ok := s.(*syntax.IfStmt)
	if !ok || ifStmt.Then == nil {
		return
	}
	v, isNeq, ok := check.logosNilTestOf(ifStmt.Cond)
	if !ok {
		return
	}
	if !check.logosExits(ifStmt.Then) {
		return
	}
	if ifStmt.Else != nil && !check.logosExits(ifStmt.Else) && (isNeq || check.logosElseAssigns(ifStmt.Else, v)) {
		return
	}
	if !isNeq {
		check.logosPushNarrow([]*Var{v})
	}
	check.logosDischargeErr(v)
}

func (check *Checker) logosExits(s syntax.Stmt) bool {
	switch s := s.(type) {
	case *syntax.ReturnStmt:
		return true
	case *syntax.BranchStmt:
		return s.Tok != syntax.Fallthrough
	case *syntax.BlockStmt:
		list := s.List
		for len(list) > 0 {
			if _, ok := list[len(list)-1].(*syntax.EmptyStmt); !ok {
				break
			}
			list = list[:len(list)-1]
		}
		if len(list) == 0 {
			return false
		}
		return check.logosExits(list[len(list)-1])
	case *syntax.IfStmt:
		return s.Else != nil && check.logosExits(s.Then) && check.logosExits(s.Else)
	case *syntax.LabeledStmt:
		return check.logosExits(s.Stmt)
	case *syntax.ExprStmt:
		if call, ok := syntax.Unparen(s.X).(*syntax.CallExpr); ok {
			if check.isPanic[call] {
				return true
			}
			if name, ok := syntax.Unparen(call.Fun).(*syntax.Name); ok && name.Value == "panic" {
				return true
			}
		}
		return false
	case *syntax.ForStmt:
		if s.Cond != nil {
			return false
		}
		if _, ok := s.Init.(*syntax.RangeClause); ok {
			return false
		}
		hasBreak := false
		syntax.Inspect(s.Body, func(n syntax.Node) bool {
			if b, ok := n.(*syntax.BranchStmt); ok && b.Tok == syntax.Break {
				hasBreak = true
				return false
			}
			return !hasBreak
		})
		return !hasBreak
	default:
		return false
	}
}

func (check *Checker) logosElseAssigns(els syntax.Stmt, v *Var) bool {
	assigns := false
	syntax.Inspect(els, func(n syntax.Node) bool {
		if assigns {
			return false
		}
		switch t := n.(type) {
		case *syntax.AssignStmt:
			if t.Op != syntax.Def {
				for _, name := range logosAssignNames(t.Lhs) {
					if name == v.name {
						assigns = true
						return false
					}
				}
			}
		case *syntax.RangeClause:
			if !t.Def {
				for _, name := range logosAssignNames(t.Lhs) {
					if name == v.name {
						assigns = true
						return false
					}
				}
			}
		}
		return true
	})
	return assigns
}

func (check *Checker) logosPushIfElse(cond syntax.Expr) bool {
	v, isNeq, ok := check.logosNilTestOf(cond)
	if !ok || isNeq {
		return false
	}
	return check.logosPushNarrow([]*Var{v})
}

func (check *Checker) logosNarrowIdent(x *operand, obj Object) {
	if !check.conf.Logos {
		return
	}
	v, ok := obj.(*Var)
	if !ok || v.pkg != check.pkg {
		return
	}
	if n := len(check.logosLHS); n > 0 && check.logosLHS[n-1] == v {
		return
	}
	if errV, ok := check.logosErrUncheck[v]; ok {
		if !check.logosResultUnobserved(x.expr, errV) {
			check.errorf(x.expr, IncompatibleAssign, "%s read of %s before %s is checked (read %s only after %s is checked)", logosResultBeforeErr, v.name, errV.name, v.name, errV.name)
		}
		return
	}
	if check.logosNarrow[v] > 0 {
		if opt := logosAsOption(v.typ); opt != nil {
			x.typ_ = opt.Elem
		}
	}
}

func (check *Checker) logosResultUnobserved(read syntax.Expr, errV *Var) bool {
	return check.logosErrTestedBefore(read, errV) || check.logosPassedOn(read, errV) || check.logosBlankAssigned(read)
}

func (check *Checker) logosPassedOn(read syntax.Expr, errV *Var) bool {
	ret, ok := check.logosCurStmt.(*syntax.ReturnStmt)
	if !ok {
		return false
	}
	results := syntax.UnpackListExpr(ret.Results)
	if len(results) != 2 || results[0] != read {
		return false
	}
	id, ok := results[1].(*syntax.Name)
	return ok && check.lookup(id.Value) == Object(errV)
}

func (check *Checker) logosBlankAssigned(read syntax.Expr) bool {
	as, ok := check.logosCurStmt.(*syntax.AssignStmt)
	if !ok || as.Op != 0 {
		return false
	}
	lhs, rhs := syntax.UnpackListExpr(as.Lhs), syntax.UnpackListExpr(as.Rhs)
	if len(lhs) != len(rhs) {
		return false
	}
	for i := range rhs {
		id, blank := lhs[i].(*syntax.Name)
		if blank && id.Value == "_" && syntax.Unparen(rhs[i]) == read {
			return true
		}
	}
	return false
}

func (check *Checker) logosErrTestedBefore(read syntax.Expr, errV *Var) bool {
	if check.logosCurStmt == nil {
		return false
	}
	var stack, path []syntax.Node
	syntax.Inspect(check.logosCurStmt, func(n syntax.Node) bool {
		if path != nil {
			return false
		}
		if n == nil {
			stack = stack[:len(stack)-1]
			return false
		}
		stack = append(stack, n)
		if n == syntax.Node(read) {
			path = slices.Clone(stack)
			return false
		}
		return true
	})
	for i := len(path) - 1; i > 0; i-- {
		op, ok := path[i-1].(*syntax.Operation)
		if ok && op.Y != nil && op.Y == path[i] && (op.Op == syntax.AndAnd || op.Op == syntax.OrOr) && check.logosChainHasErrTest(op.X, errV) {
			return true
		}
	}
	return false
}

func (check *Checker) logosChainHasErrTest(cond syntax.Expr, errV *Var) bool {
	cond = syntax.Unparen(cond)
	if op, ok := cond.(*syntax.Operation); ok && op.Y != nil && (op.Op == syntax.AndAnd || op.Op == syntax.OrOr) {
		return check.logosChainHasErrTest(op.X, errV) || check.logosChainHasErrTest(op.Y, errV)
	}
	v, _, ok := check.logosNilTestOf(cond)
	return ok && v == errV
}

func (check *Checker) logosStmtEnter(s syntax.Stmt) syntax.Stmt {
	prev := check.logosCurStmt
	check.logosCurStmt = s
	return prev
}

func (check *Checker) logosStmtExit(prev syntax.Stmt) {
	check.logosCurStmt = prev
}

func (check *Checker) logosLHSPush(lhs syntax.Expr) {
	if !check.conf.Logos {
		return
	}
	var v *Var
	if id, ok := syntax.Unparen(lhs).(*syntax.Name); ok && id.Value != "_" {
		v, _ = check.lookup(id.Value).(*Var)
	}
	check.logosLHS = append(check.logosLHS, v)
}

func (check *Checker) logosLHSPop() {
	if !check.conf.Logos {
		return
	}
	if n := len(check.logosLHS); n > 0 {
		check.logosLHS = check.logosLHS[:n-1]
	}
}

func (check *Checker) logosKillNarrow(lhs syntax.Expr) {
	if !check.conf.Logos {
		return
	}
	id, ok := syntax.Unparen(lhs).(*syntax.Name)
	if !ok || id.Value == "_" {
		return
	}
	obj := check.lookup(id.Value)
	v, ok := obj.(*Var)
	if !ok || v.pkg != check.pkg {
		return
	}
	check.logosKillVar(v)
}

func (check *Checker) logosShortDecl(lhs, rhs []syntax.Expr, lhsVars []*Var) {
	if !check.conf.Logos {
		return
	}
	for _, e := range lhs {
		id, ok := syntax.Unparen(e).(*syntax.Name)
		if !ok || id.Value == "_" {
			continue
		}
		if alt, _ := check.scope.Lookup(id.Value).(*Var); alt != nil {
			check.logosKillVar(alt)
		}
	}
	if len(lhs) != 2 || len(rhs) != 1 || len(lhsVars) != 2 {
		return
	}
	call, ok := syntax.Unparen(rhs[0]).(*syntax.CallExpr)
	if !ok {
		return
	}
	n, errV := lhsVars[0], lhsVars[1]
	if n == nil || errV == nil || n.name == "_" || errV.name == "_" || n == errV {
		return
	}
	check.logosTrackTuple(n, errV, call)
}

func (check *Checker) logosTrackCallAssign(lhs []syntax.Expr, rhs syntax.Expr) {
	if !check.conf.Logos || len(lhs) != 2 {
		return
	}
	call, ok := syntax.Unparen(rhs).(*syntax.CallExpr)
	if !ok {
		return
	}
	n, ok := check.logosLhsVar(lhs[0])
	if !ok {
		return
	}
	errV, ok := check.logosLhsVar(lhs[1])
	if !ok || n == errV {
		return
	}
	check.logosTrackTuple(n, errV, call)
}

func (check *Checker) logosLhsVar(e syntax.Expr) (*Var, bool) {
	id, ok := syntax.Unparen(e).(*syntax.Name)
	if !ok || id.Value == "_" {
		return nil, false
	}
	v, ok := check.lookup(id.Value).(*Var)
	if !ok || v.pkg != check.pkg {
		return nil, false
	}
	return v, true
}

func (check *Checker) logosKillVar(v *Var) {
	if v == nil || v.pkg != check.pkg {
		return
	}
	delete(check.logosNarrow, v)
	if _, ok := check.logosErrUncheck[v]; ok {
		delete(check.logosErrUncheck, v)
		check.logosNoteDead(v)
	}
	maps.DeleteFunc(check.logosErrUncheck, func(m *Var, e *Var) bool {
		if e != v {
			return false
		}
		check.logosNoteDead(m)
		return true
	})
}

func (check *Checker) logosNoteDead(v *Var) {
	if check.logosErrDead == nil {
		check.logosErrDead = make(map[*Var]bool)
	}
	check.logosErrDead[v] = true
}

func (check *Checker) logosTrackTuple(n, errV *Var, call *syntax.CallExpr) {
	if n.pkg != check.pkg || errV.pkg != check.pkg {
		return
	}
	if errV.typ == nil || !Identical(errV.typ, universeError) {
		return
	}
	if !check.logosIsLgoCall(call) {
		return
	}
	maps.DeleteFunc(check.logosErrUncheck, func(m *Var, e *Var) bool {
		if e != errV {
			return false
		}
		check.logosNoteDead(m)
		return true
	})
	delete(check.logosErrDead, n)
	if check.logosErrUncheck == nil {
		check.logosErrUncheck = make(map[*Var]*Var)
	}
	check.logosErrUncheck[n] = errV
}

func (check *Checker) logosIsLgoCall(call *syntax.CallExpr) bool {
	switch fun := syntax.Unparen(call.Fun).(type) {
	case *syntax.Name:
		if fn, ok := check.lookup(fun.Value).(*Func); ok {
			return fn.pkg == check.pkg
		}
		return true
	case *syntax.SelectorExpr:
		if xn, ok := syntax.Unparen(fun.X).(*syntax.Name); ok {
			if obj := check.lookup(xn.Value); obj != nil {
				if pn, ok := obj.(*PkgName); ok {
					return pn.imported == check.pkg
				}
			}
		}
		if sel := check.Selections[fun]; sel != nil {
			if fn, ok := sel.Obj().(*Func); ok && fn.pkg != nil {
				return fn.pkg == check.pkg
			}
		}
		return true
	default:
		return true
	}
}

func (check *Checker) logosDischargeErr(errV *Var) {
	if errV == nil {
		return
	}
	maps.DeleteFunc(check.logosErrUncheck, func(_ *Var, e *Var) bool {
		return e == errV
	})
}

func (check *Checker) logosPushChecked(cond syntax.Expr) bool {
	if !check.conf.Logos {
		return false
	}
	v, _, ok := check.logosNilTestOf(cond)
	if !ok {
		return false
	}
	var frame map[*Var]*Var
	for m, e := range check.logosErrUncheck {
		if e == v {
			if frame == nil {
				frame = make(map[*Var]*Var)
			}
			frame[m] = e
		}
	}
	if len(frame) == 0 {
		return false
	}
	for m := range frame {
		delete(check.logosErrUncheck, m)
	}
	check.logosErrStack = append(check.logosErrStack, frame)
	return true
}

func (check *Checker) logosPopChecked() {
	frames := check.logosErrStack
	frame := frames[len(frames)-1]
	check.logosErrStack = frames[:len(frames)-1]
	for m, e := range frame {
		if check.logosErrDead[m] {
			continue
		}
		if _, ok := check.logosErrUncheck[m]; !ok {
			check.logosErrUncheck[m] = e
		}
	}
}

func (check *Checker) logosErrBase() int {
	return len(check.logosErrStack)
}

func (check *Checker) logosPopToErr(base int) {
	for len(check.logosErrStack) > base {
		check.logosPopChecked()
	}
}

func (check *Checker) logosUnnarrowOperand(x *operand) {
	if !check.conf.Logos || !x.isValid() {
		return
	}
	id, ok := syntax.Unparen(x.expr).(*syntax.Name)
	if !ok {
		return
	}
	obj := check.lookup(id.Value)
	v, ok := obj.(*Var)
	if !ok || v.pkg != check.pkg || check.logosNarrow[v] == 0 {
		return
	}
	if logosAsOption(v.typ) == nil {
		return
	}
	x.typ_ = v.typ
	check.updateExprType(x.expr, v.typ, false)
}

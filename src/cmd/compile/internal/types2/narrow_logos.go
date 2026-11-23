// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"go/constant"
	"go/version"
	. "internal/types/errors"
	"maps"
	"slices"
	"strconv"
	"strings"
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
		declared:  make(map[string]bool),
		refTop:    make(map[string]bool),
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
	set := make(map[string]bool, len(dq.addr)+len(dq.inLoop)+len(dq.inClos)+len(dq.declared))
	for name := range dq.addr {
		set[name] = true
	}
	for name := range dq.inLoop {
		set[name] = true
	}
	for name := range dq.inClos {
		set[name] = true
	}
	if dq.hasGoto {
		for _, name := range dq.params {
			set[name] = true
		}
		for name := range dq.declared {
			set[name] = true
		}
		// A jump can land past a nil guard on a captured variable too:
		// every name this frame references without declaring it (a
		// captured local — package objects are never narrowable) loses
		// its proof as well.
		for name := range dq.refTop {
			if !dq.declared[name] {
				set[name] = true
			}
		}
	}
	check.logosDisqual = append(check.logosDisqual, set)
	if body != nil {
		if check.Info.LogosDisqual == nil {
			check.Info.LogosDisqual = make(map[*syntax.BlockStmt][]map[string]bool)
		}
		check.Info.LogosDisqual[body] = slices.Clone(check.logosDisqual)
	}
	if outermost {
		check.logosEFBody = body
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
		if n == 1 {
			check.logosErrorFirst(check.logosEFBody)
		}
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
	inner     int
	addr      map[string]bool
	inLoop    map[string]bool
	inClos    map[string]bool
	refInClos map[string]bool
	assignOut map[string]bool
	declared  map[string]bool
	refTop    map[string]bool
	hasGoto   bool
	// frameScopes stacks the scopes index where each enclosing closure's
	// first scope lands: names declared at or below it belong to the
	// innermost closure itself, not to a captured frame.
	frameScopes []int
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
		w.inner++
		w.frameScopes = append(w.frameScopes, len(w.scopes))
	case *syntax.CallExpr:
		w.markStage(t)
	case *syntax.LambdaExpr:
		if w.stage[t] {
			w.lam++
			w.frameScopes = append(w.frameScopes, len(w.scopes))
		}
		if _, ok := t.Body.(*syntax.BlockStmt); ok {
			w.inner++
			if !w.stage[t] {
				w.frameScopes = append(w.frameScopes, len(w.scopes))
			}
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
		} else if w.inner == 0 && t.Value != "_" {
			w.refTop[t.Value] = true
		}
	case *syntax.AssignStmt:
		w.assign(t)
	case *syntax.BranchStmt:
		if t.Tok == syntax.Goto && w.inner == 0 {
			w.hasGoto = true
		}
	case *syntax.RangeClause:
		if t.Def {
			for _, name := range logosAssignNames(t.Lhs) {
				if w.inner == 0 {
					w.declared[name] = true
				}
				// The for's scope is already on the stack.
				w.scopes[len(w.scopes)-1][name] = true
			}
		} else {
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
		w.inner--
		w.frameScopes = w.frameScopes[:len(w.frameScopes)-1]
	case *syntax.LambdaExpr:
		if w.stage[top] {
			w.lam--
			w.frameScopes = w.frameScopes[:len(w.frameScopes)-1]
		}
		if _, ok := top.Body.(*syntax.BlockStmt); ok {
			w.inner--
			if !w.stage[top] {
				w.frameScopes = w.frameScopes[:len(w.frameScopes)-1]
			}
		}
	case *syntax.ForStmt:
		w.loop--
		w.scopes = w.scopes[:len(w.scopes)-1]
	case *syntax.BlockStmt, *syntax.CaseClause, *syntax.CommClause,
		*syntax.IfStmt, *syntax.SwitchStmt, *syntax.SwitchExpr:
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
	case *syntax.BlockStmt, *syntax.CaseClause, *syntax.CommClause,
		*syntax.IfStmt, *syntax.ForStmt, *syntax.SwitchStmt, *syntax.SwitchExpr:
		// Header-bearing statements get a scope too: their initializer
		// := declares into the statement's own scope, and recording it
		// is what lets a later closure write to the name stay local
		// instead of escaping onto a same-named outer variable.
		scope := make(map[string]bool, len(w.params))
		for _, name := range w.params {
			scope[name] = true
			// Only names of this frame's own function join the goto
			// disqualification set: a jump here cannot skip a guard
			// inside a nested literal, and a jump there cannot skip one
			// here, so closure-declared names stay eligible.
			if w.inner == 0 {
				w.declared[name] = true
			}
		}
		w.params = nil
		w.scopes = append(w.scopes, scope)
	case *syntax.VarDecl:
		for _, name := range t.NameList {
			w.scopes[len(w.scopes)-1][name.Value] = true
			if w.inner == 0 {
				w.declared[name.Value] = true
			}
		}
	}
}
func (w *logosDisqualWalk) assign(s *syntax.AssignStmt) {
	header := false
	switch w.stack[len(w.stack)-1].(type) {
	case *syntax.IfStmt, *syntax.ForStmt, *syntax.SwitchStmt, *syntax.SwitchExpr:
		// A switch expression's initializer declares into the
		// expression's own scope, like the statement forms.
		header = true
	}
	scope := w.scopes[len(w.scopes)-1]
	for _, name := range logosAssignNames(s.Lhs) {
		if s.Op == syntax.Def {
			if w.inner == 0 {
				w.declared[name] = true
			}
			redeclared := scope[name] && !header
			scope[name] = true
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
	// Any closure assignment — function literal, group child, or a
	// stage lambda — can run after a guard is checked, so its captured
	// target can never narrow; only writes in the frame itself count as
	// plain outer assignments. A name the innermost closure declares — a
	// parameter or a block local — cannot outlive that closure, so a
	// write to it is not captured.
	if w.inner > 0 || w.lam > 0 {
		if n := len(w.frameScopes); n > 0 {
			for _, sc := range w.scopes[w.frameScopes[n-1]:] {
				if sc[name] {
					return
				}
			}
		}
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
	// The object the checker bound to this Name is authoritative: a
	// guard in an `if` initializer shadows an outer variable of the
	// same name, and re-resolving after that scope closes would attach
	// the proof to the wrong variable.
	obj, _ := check.Info.Uses[name]
	if obj == nil {
		obj = check.lookup(name.Value)
	}
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
}

// logosRecordVoidPanic records builtin panic calls for loGo/s narrowing in
// functions without result parameters.
func (check *Checker) logosRecordVoidPanic(call *syntax.CallExpr) {
	if !check.conf.Logos || check.sig == nil || check.sig.results.Len() != 0 {
		return
	}
	if check.isPanic == nil {
		check.isPanic = make(map[*syntax.CallExpr]bool)
	}
	check.isPanic[call] = true
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
			return check.isPanic[call]
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
	if check.logosNarrow[v] > 0 {
		if opt := logosAsOption(v.typ); opt != nil {
			x.typ_ = opt.Elem
		}
	}
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

func (check *Checker) logosShortDecl(lhs []syntax.Expr) {
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
}

func (check *Checker) logosKillVar(v *Var) {
	if v == nil || v.pkg != check.pkg {
		return
	}
	delete(check.logosNarrow, v)
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

type logosEFEntry struct {
	err    *Var
	name   string
	proven bool
	// pos is the source position of the store that created the entry, so
	// diagnostics that choose among entries stay deterministic.
	pos syntax.Pos
}

// A logosEFKey names the destination a tracked result is stored in: the
// empty path is a plain variable, a field appends ".name", and a constant
// array index appends "[i]", so h.inner.stats and a[0] spell
// {h, ".inner.stats"} and {a, "[0]"}.
type logosEFKey struct {
	root *Var
	path string
}

func (k logosEFKey) displayName() string {
	if k.path == "" {
		return k.root.name
	}
	return k.root.name + k.path
}

type logosEFState struct {
	m map[logosEFKey]logosEFEntry
	// may marks results stored by a trailing-error call whose error has
	// no tracked identity (field or discarded): the value can reach a
	// later deferred read without a check. It is killed by any overwrite
	// and merged by union across branches, and it is only consulted when
	// seeding a deferred capture, so ordinary reads (including blank
	// discards like v, _ := f(); return v) are unaffected.
	may map[logosEFKey]bool
}

func (s *logosEFState) set(k logosEFKey, e logosEFEntry) *logosEFState {
	m := maps.Clone(s.m)
	if m == nil {
		m = make(map[logosEFKey]logosEFEntry, 1)
	}
	m[k] = e
	out := &logosEFState{m: m, may: s.may}
	// A tracked store overwrites any untracked value: the earlier
	// may-reach no longer holds on this path (branch joins re-add it
	// when another path still reaches).
	if s.may != nil && s.may[k] {
		may := maps.Clone(s.may)
		delete(may, k)
		out.may = may
	}
	return out
}

func (s *logosEFState) setMay(k logosEFKey) *logosEFState {
	if s.may != nil && s.may[k] {
		return s
	}
	may := maps.Clone(s.may)
	if may == nil {
		may = make(map[logosEFKey]bool, 1)
	}
	may[k] = true
	return &logosEFState{m: s.m, may: may}
}

// setSeed adds a deferred-capture entry without killing may-reach: seeding
// is analysis, not a store, so an untracked value that reaches the literal
// stays marked for the unprove check that follows.
func (s *logosEFState) setSeed(k logosEFKey, e logosEFEntry) *logosEFState {
	m := maps.Clone(s.m)
	if m == nil {
		m = make(map[logosEFKey]logosEFEntry, 1)
	}
	m[k] = e
	return &logosEFState{m: m, may: s.may}
}

// dropWriting stops tracking a destination that a write to k can change:
// k itself, anything nested inside it, and any destination that contains k.
// Paths name direct value-struct fields only, so sibling paths never alias
// each other.
func (s *logosEFState) dropWriting(k logosEFKey) *logosEFState {
	var out map[logosEFKey]logosEFEntry
	for v := range s.m {
		if v.root != k.root {
			continue
		}
		if logosEFPathUnder(v.path, k.path) || logosEFPathUnder(k.path, v.path) {
			if out == nil {
				out = maps.Clone(s.m)
			}
			delete(out, v)
		}
	}
	var mayOut map[logosEFKey]bool
	for v := range s.may {
		if v.root != k.root {
			continue
		}
		if logosEFPathUnder(v.path, k.path) || logosEFPathUnder(k.path, v.path) {
			if mayOut == nil {
				mayOut = maps.Clone(s.may)
			}
			delete(mayOut, v)
		}
	}
	if out == nil && mayOut == nil {
		return s
	}
	m := s.m
	if out != nil {
		m = out
	}
	may := s.may
	if mayOut != nil {
		may = mayOut
	}
	return &logosEFState{m: m, may: may}
}

// logosEFPathUnder reports whether path denotes prefix or a destination
// nested inside it; the empty prefix is the whole root.
func logosEFPathUnder(path, prefix string) bool {
	if !strings.HasPrefix(path, prefix) {
		return false
	}
	if len(path) == len(prefix) {
		return true
	}
	switch path[len(prefix)] {
	case '.', '[':
		return true
	}
	return false
}

// logosEFPathPrefixes lists the proper prefixes of a destination path:
// ".a[0].b" yields ".a" and ".a[0]".
func logosEFPathPrefixes(path string) []string {
	var out []string
	for i := 0; i < len(path); {
		j := i + 1
		if path[i] == '.' {
			for j < len(path) && path[j] != '.' && path[j] != '[' {
				j++
			}
		} else {
			for j < len(path) && path[j] != ']' {
				j++
			}
			j++
		}
		i = j
		if i < len(path) {
			out = append(out, path[:i])
		}
	}
	return out
}

func (s *logosEFState) remap(f func(logosEFEntry) logosEFEntry) *logosEFState {
	var out map[logosEFKey]logosEFEntry
	for v, e := range s.m {
		ne := f(e)
		if ne == e {
			continue
		}
		if out == nil {
			out = maps.Clone(s.m)
		}
		out[v] = ne
	}
	if out == nil {
		return s
	}
	return &logosEFState{m: out, may: s.may}
}

func (s *logosEFState) prove(errV *Var) *logosEFState {
	return s.remap(func(e logosEFEntry) logosEFEntry {
		e.proven = e.proven || e.err == errV
		return e
	})
}

func (s *logosEFState) detach(errV *Var) *logosEFState {
	return s.remap(func(e logosEFEntry) logosEFEntry {
		if e.err == errV {
			e.err = nil
		}
		return e
	})
}

func (p *logosEFPass) capture(st *logosEFState) *logosEFState {
	if p.assigns == nil {
		p.collectAssigns()
	}
	var out map[logosEFKey]logosEFEntry
	for k, e := range st.m {
		if !e.proven || p.stable(k) {
			continue
		}
		if out == nil {
			out = maps.Clone(st.m)
		}
		e.proven = false
		out[k] = e
	}
	if out == nil {
		return st
	}
	return &logosEFState{m: out, may: st.may}
}

// stable reports whether a destination's memory can no longer change
// before a deferred body runs: a variable written exactly once, at its
// declaration; a field or element written exactly once, by the tracked
// call, inside a root that nothing else writes and whose address is never
// taken.
func (p *logosEFPass) stable(k logosEFKey) bool {
	if p.addr[k.root] {
		return false
	}
	if k.path == "" {
		return p.initialized[k.root] && len(p.assigns[k]) == 1
	}
	if len(p.assigns[k]) != 1 {
		return false
	}
	if n := len(p.assigns[logosEFKey{root: k.root}]); n > 0 && !(p.initialized[k.root] && n == 1) {
		return false
	}
	if p.inLoop[k] {
		return false
	}
	for _, pre := range logosEFPathPrefixes(k.path) {
		if len(p.assigns[logosEFKey{root: k.root, path: pre}]) > 0 {
			return false
		}
	}
	return true
}

func logosEFJoin(a, b *logosEFState) *logosEFState {
	switch {
	case a == nil:
		return b
	case b == nil || a == b:
		return a
	}
	m := maps.Clone(a.m)
	if m == nil {
		m = make(map[logosEFKey]logosEFEntry, len(b.m))
	}
	for v, eb := range b.m {
		ea, ok := m[v]
		if !ok {
			m[v] = eb
			continue
		}
		if ea.err != eb.err {
			ea.err = nil
		}
		ea.proven = ea.proven && eb.proven
		m[v] = ea
	}
	var may map[logosEFKey]bool
	if a.may != nil || b.may != nil {
		may = make(map[logosEFKey]bool)
		for v := range a.may {
			may[v] = true
		}
		for v := range b.may {
			may[v] = true
		}
		if len(may) == 0 {
			may = nil
		}
	}
	return &logosEFState{m: m, may: may}
}

func logosEFSame(a, b *logosEFState) bool {
	if a == nil || b == nil {
		return a == b
	}
	return maps.Equal(a.m, b.m) && maps.Equal(a.may, b.may)
}

type logosEFFrame struct {
	label    string
	loop     bool
	brk, cnt *logosEFState
}

type logosEFCtx struct {
	labels  map[string]*logosEFState
	frames  []*logosEFFrame
	exit    *logosEFState
	fall    *logosEFState
	changed bool
}

type logosEFEffects struct {
	inside   map[*Var]bool
	assigned map[logosEFKey]bool
	created  map[logosEFKey]string
}

type logosEFPass struct {
	check         *Checker
	body          *syntax.BlockStmt
	seen          map[syntax.Pos]bool
	spines        map[syntax.Pos]bool
	ctx           *logosEFCtx
	eff           *logosEFEffects
	assigns       map[logosEFKey][]syntax.Expr
	addr          map[*Var]bool
	initialized   map[*Var]bool
	tracked       map[logosEFKey]string
	storeErr      map[logosEFKey][]*Var
	sliced        map[logosEFKey]bool
	storePos      map[logosEFKey][]syntax.Pos
	inLoop        map[logosEFKey]bool
	loopFresh     map[*syntax.ForStmt]map[*Var]bool
	loopMaybe     map[*syntax.ForStmt]map[*Var]bool
	loopTuple     map[*syntax.ForStmt]map[logosEFKey]bool
	reenter       map[*syntax.ForStmt]bool
	litLoops      map[syntax.Node]map[*syntax.ForStmt]bool
	trackedByRoot map[*Var][]logosEFKey
}

func (check *Checker) logosErrorFirst(body *syntax.BlockStmt) {
	if !check.conf.Logos || body == nil || check.Uses == nil || check.Defs == nil {
		return
	}
	p := &logosEFPass{check: check, body: body, seen: make(map[syntax.Pos]bool)}
	if p.hasTuple() {
		p.run(body, &logosEFState{})
	}
}

func (p *logosEFPass) hasTuple() bool {
	found := false
	syntax.Inspect(p.body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.AssignStmt:
			if n.Rhs != nil {
				lhs := syntax.UnpackListExpr(n.Lhs)
				errV, _ := p.tuple(lhs, n.Rhs)
				found = found || errV != nil
				if errV == nil {
					if rhs := syntax.UnpackListExpr(n.Rhs); len(rhs) == 1 {
						found = found || p.errCallShape(lhs, rhs[0])
					}
				}
			}
		case *syntax.VarDecl:
			if n.Values != nil {
				lhs := logosEFNames(n)
				errV, _ := p.tuple(lhs, n.Values)
				found = found || errV != nil
			}
		}
		return !found
	})
	return found
}

func logosEFNames(d *syntax.VarDecl) []syntax.Expr {
	lhs := make([]syntax.Expr, len(d.NameList))
	for i, name := range d.NameList {
		lhs[i] = name
	}
	return lhs
}

func (p *logosEFPass) run(body *syntax.BlockStmt, in *logosEFState) *logosEFState {
	saved := p.ctx
	ctx := &logosEFCtx{labels: make(map[string]*logosEFState)}
	p.ctx = ctx
	var end *logosEFState
	for {
		ctx.frames, ctx.exit, ctx.fall, ctx.changed = nil, nil, nil, false
		end = p.stmt(body, in)
		if !ctx.changed {
			break
		}
	}
	p.ctx = saved
	return logosEFJoin(end, ctx.exit)
}

func (p *logosEFPass) varOf(e syntax.Expr) *Var {
	id, ok := syntax.Unparen(e).(*syntax.Name)
	if !ok || id.Value == "_" {
		return nil
	}
	obj := p.check.Defs[id]
	if obj == nil {
		obj = p.check.Uses[id]
	}
	v, _ := obj.(*Var)
	return v
}

func (p *logosEFPass) addressRoot(e syntax.Expr) *Var {
	switch e := syntax.Unparen(e).(type) {
	case *syntax.SelectorExpr:
		if sel := p.check.Selections[e]; sel != nil && sel.Kind() == FieldVal && !sel.Indirect() {
			return p.addressRoot(e.X)
		}
	case *syntax.IndexExpr:
		if typ, _, ok := p.check.logosChecked(e.X); ok {
			for _, u := range typeset(typ) {
				if _, ok := u.(*Array); ok {
					return p.addressRoot(e.X)
				}
			}
		}
	default:
		return p.varOf(e)
	}
	return nil
}

func (p *logosEFPass) errVar(e syntax.Expr) *Var {
	v := p.varOf(e)
	if v == nil || v.typ == nil || !Identical(v.typ, universeError) {
		return nil
	}
	return v
}

func (p *logosEFPass) isNil(e syntax.Expr) bool {
	id, ok := e.(*syntax.Name)
	if !ok {
		return false
	}
	_, isNil := p.check.Uses[id].(*Nil)
	return isNil
}

func (p *logosEFPass) nilTest(op *syntax.Operation) (errV *Var, isNeq bool) {
	if op.Y == nil || (op.Op != syntax.Eql && op.Op != syntax.Neq) {
		return nil, false
	}
	x, y := syntax.Unparen(op.X), syntax.Unparen(op.Y)
	switch {
	case p.isNil(y):
		errV = p.errVar(x)
	case p.isNil(x):
		errV = p.errVar(y)
	}
	return errV, op.Op == syntax.Neq
}

func (p *logosEFPass) tuple(lhs []syntax.Expr, rhs syntax.Expr) (errV *Var, keys []logosEFKey) {
	call, ok := syntax.Unparen(rhs).(*syntax.CallExpr)
	if !ok || len(lhs) < 2 {
		return nil, nil
	}
	typ, _, ok := p.check.logosChecked(call)
	tup, isTuple := typ.(*Tuple)
	if !ok || !isTuple || tup.Len() != len(lhs) || !Identical(tup.At(tup.Len()-1).typ, universeError) {
		return nil, nil
	}
	if !p.calleeInLgo(call.Fun) {
		return nil, nil
	}
	errV = p.errVar(lhs[len(lhs)-1])
	if errV == nil {
		return nil, nil
	}
	errKey := logosEFKey{root: errV}
	for _, e := range lhs[:len(lhs)-1] {
		if k, ok := p.destKey(e); ok && k != errKey {
			keys = append(keys, k)
		}
	}
	return errV, keys
}

// errCallShape reports whether lhs, rhs is a multi-assignment from a call
// whose result shape matches the LoGOs trailing-error convention: the
// tuple has exactly len(lhs) elements, the last one is an error type, and
// the callee is in LoGOs code. Go-contract calls and calls whose result
// does not pair a trailing error keep no nil-error store, because their
// last destination is not an error check.
func (p *logosEFPass) errCallShape(lhs []syntax.Expr, rhs syntax.Expr) bool {
	call, ok := syntax.Unparen(rhs).(*syntax.CallExpr)
	if !ok || len(lhs) < 2 || !p.calleeInLgo(call.Fun) {
		return false
	}
	typ, _, ok := p.check.logosChecked(call)
	tup, isTuple := typ.(*Tuple)
	return ok && isTuple && tup.Len() == len(lhs) && Identical(tup.At(tup.Len()-1).typ, universeError)
}

func (p *logosEFPass) staticFunc(e syntax.Expr) *Func {
	switch e := syntax.Unparen(e).(type) {
	case *syntax.Name:
		fn, _ := p.check.Uses[e].(*Func)
		return fn
	case *syntax.SelectorExpr:
		if sel := p.check.Selections[e]; sel != nil {
			fn, _ := sel.Obj().(*Func)
			return fn
		}
		fn, _ := p.check.Uses[e.Sel].(*Func)
		return fn
	case *syntax.IndexExpr:
		return p.staticFunc(e.X)
	}
	return nil
}

func (p *logosEFPass) calleeInLgo(fun syntax.Expr) bool {
	if fn := p.staticFunc(fun); fn != nil {
		return p.check.logosFuncInLgo(fn)
	}
	id, ok := syntax.Unparen(fun).(*syntax.Name)
	if !ok {
		return true
	}
	v, _ := p.check.Uses[id].(*Var)
	if origin := p.sole(v); origin != nil {
		if fn := p.staticFunc(origin); fn != nil {
			return p.check.logosFuncInLgo(fn)
		}
	}
	return true
}

func (p *logosEFPass) sole(v *Var) syntax.Expr {
	if v == nil {
		return nil
	}
	if p.assigns == nil {
		p.collectAssigns()
	}
	if exprs := p.assigns[logosEFKey{root: v}]; len(exprs) == 1 && !p.addr[v] {
		return exprs[0]
	}
	return nil
}

func (p *logosEFPass) collectAssigns() {
	p.assigns = make(map[logosEFKey][]syntax.Expr)
	p.addr = make(map[*Var]bool)
	p.initialized = make(map[*Var]bool)
	p.tracked = make(map[logosEFKey]string)
	p.storeErr = make(map[logosEFKey][]*Var)
	p.sliced = make(map[logosEFKey]bool)
	p.storePos = make(map[logosEFKey][]syntax.Pos)
	p.inLoop = make(map[logosEFKey]bool)
	p.loopFresh = make(map[*syntax.ForStmt]map[*Var]bool)
	p.loopMaybe = make(map[*syntax.ForStmt]map[*Var]bool)
	p.loopTuple = make(map[*syntax.ForStmt]map[logosEFKey]bool)
	p.reenter = make(map[*syntax.ForStmt]bool)
	mark := func(lhs []syntax.Expr, rhs syntax.Expr) {
		errV, keys := p.tuple(lhs, rhs)
		if errV == nil {
			// A LoGOs call whose trailing error is discarded still writes
			// the destination: record the store without an error identity
			// so deferred captures know the value can be overwritten.
			// Calls that do not return a trailing error, and Go-contract
			// calls, need no check and record nothing.
			if p.errCallShape(lhs, rhs) {
				errName := "its error"
				if k, ok := p.destKey(lhs[len(lhs)-1]); ok {
					errName = k.displayName()
				}
				for _, e := range lhs[:len(lhs)-1] {
					if k, ok := p.destKey(e); ok {
						if _, ok := p.tracked[k]; !ok {
							p.tracked[k] = errName
						}
						p.storePos[k] = append(p.storePos[k], e.Pos())
						p.storeErr[k] = append(p.storeErr[k], nil)
					}
				}
			}
			return
		}
		for _, k := range keys {
			if _, ok := p.tracked[k]; !ok {
				p.tracked[k] = errV.name
			}
			p.storePos[k] = append(p.storePos[k], p.logosEFDestPos(lhs, k))
			p.storeErr[k] = append(p.storeErr[k], errV)
		}
	}
	note := func(lhs []syntax.Expr, rhs []syntax.Expr, plain bool) {
		for i, l := range lhs {
			var r syntax.Expr
			if plain && len(lhs) == len(rhs) {
				r = rhs[i]
			}
			k, ok := p.destKey(l)
			if !ok {
				continue
			}
			p.assigns[k] = append(p.assigns[k], r)
			if id, isName := syntax.Unparen(l).(*syntax.Name); isName && p.check.Defs[id] == k.root {
				p.initialized[k.root] = true
			}
		}
	}
	syntax.Inspect(p.body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.AssignStmt:
			lhs := syntax.UnpackListExpr(n.Lhs)
			rhs := syntax.UnpackListExpr(n.Rhs)
			note(lhs, rhs, n.Op == 0 || n.Op == syntax.Def)
		case *syntax.VarDecl:
			if n.Values != nil {
				names := logosEFNames(n)
				note(names, syntax.UnpackListExpr(n.Values), true)
			}
		case *syntax.RangeClause:
			note(syntax.UnpackListExpr(n.Lhs), nil, false)
		case *syntax.SliceExpr:
			if k, ok := p.rawKey(n.X); ok {
				p.sliced[k] = true
			}
		case *syntax.Operation:
			if n.Op == syntax.And && n.Y == nil {
				if v := p.addressRoot(n.X); v != nil {
					p.addr[v] = true
				}
			}
		case *syntax.SelectorExpr:
			sel := p.check.Selections[n]
			if sel == nil || sel.Kind() != MethodVal || sel.Indirect() {
				break
			}
			if sel.Obj().(*Func).hasPtrRecv() {
				if v := p.addressRoot(n.X); v != nil {
					p.addr[v] = true
				}
			}
		}
		return true
	})
	// Classify tuple stores only after the assignment inventory is
	// complete: a call through a variable reassigned later in the body
	// (for example parse := strconv.Atoi then parse = parseLogos) must
	// see every assignment before its callee is resolved, or an in-loop
	// LoGOs store is mistaken for a Go call and its error pairing lost.
	syntax.Inspect(p.body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.AssignStmt:
			if (n.Op == 0 || n.Op == syntax.Def) && len(syntax.UnpackListExpr(n.Rhs)) == 1 {
				mark(syntax.UnpackListExpr(n.Lhs), syntax.UnpackListExpr(n.Rhs)[0])
			}
		case *syntax.VarDecl:
			if n.Values != nil {
				mark(logosEFNames(n), n.Values)
			}
		}
		return true
	})
	syntax.Inspect(p.body, func(n syntax.Node) bool {
		f, ok := n.(*syntax.ForStmt)
		if !ok {
			return true
		}
		fresh := p.defsIn(f.Body)
		// maybe is the possibly-fresh set. Under GOCOMPILEDEBUG=loopvarhash a
		// clause variable can stay shared even when loopDistinctVars holds,
		// so the deferred-proof unpairing assumes freshness from
		// loopDistinctVars alone while the carry checks below require the
		// certain loopVarsFresh.
		maybe := p.defsIn(f.Body)
		// Under Go 1.22 loop-var semantics a variable a := range clause or
		// a three-clause init declares gets a new instance each iteration;
		// under earlier semantics it is one shared instance, so a deferred
		// literal can observe a later iteration's store.
		distinct := p.loopDistinctVars(f)
		certain := p.loopVarsFresh(f)
		if rc, ok := f.Init.(*syntax.RangeClause); ok {
			for v := range p.defsIn(rc) {
				if distinct {
					maybe[v] = true
				}
				if certain {
					fresh[v] = true
				}
			}
		} else if f.Init != nil {
			for v := range p.defsIn(f.Init) {
				if distinct {
					maybe[v] = true
				}
				if certain {
					fresh[v] = true
				}
			}
		}
		if f.Post != nil {
			for v := range p.defsIn(f.Post) {
				fresh[v] = true
				maybe[v] = true
			}
		}
		p.loopFresh[f] = fresh
		p.loopMaybe[f] = maybe
		tuple := p.loopTuple[f]
		if tuple == nil {
			tuple = make(map[logosEFKey]bool)
			p.loopTuple[f] = tuple
		}
		note := func(l syntax.Expr) {
			k, ok := p.destKey(l)
			if !ok || fresh[k.root] {
				return
			}
			p.inLoop[k] = true
		}
		markLoop := func(m syntax.Node) bool {
			switch m := m.(type) {
			case *syntax.AssignStmt:
				lhs := syntax.UnpackListExpr(m.Lhs)
				for _, l := range lhs {
					note(l)
				}
				if (m.Op == 0 || m.Op == syntax.Def) && len(syntax.UnpackListExpr(m.Rhs)) == 1 {
					if _, keys := p.tuple(lhs, syntax.UnpackListExpr(m.Rhs)[0]); keys != nil {
						for _, k := range keys {
							tuple[k] = true
						}
					}
				}
			case *syntax.VarDecl:
				if m.Values != nil {
					names := logosEFNames(m)
					for _, l := range names {
						note(l)
					}
					if _, keys := p.tuple(names, m.Values); keys != nil {
						for _, k := range keys {
							tuple[k] = true
						}
					}
				}
			case *syntax.RangeClause:
				if m.Lhs != nil {
					for _, l := range syntax.UnpackListExpr(m.Lhs) {
						note(l)
					}
				}
			}
			return true
		}
		syntax.Inspect(f.Body, markLoop)
		if f.Post != nil {
			syntax.Inspect(f.Post, markLoop)
		}
		return true
	})
	p.trackedByRoot = make(map[*Var][]logosEFKey)
	for k := range p.tracked {
		p.trackedByRoot[k.root] = append(p.trackedByRoot[k.root], k)
	}
	// A backward goto re-enters a loop when its target sits at or before
	// the loop (the jump runs the whole loop again) or inside the loop
	// body (the jump re-runs part of the body, recreating a loop-local
	// error and rewriting a shared result). A label placed at or before a
	// loop reaches it on the way forward.
	labels := make(map[string]syntax.Stmt)
	var gotos []*syntax.BranchStmt
	syntax.Inspect(p.body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.LabeledStmt:
			labels[n.Label.Value] = n.Stmt
		case *syntax.BranchStmt:
			if n.Tok == syntax.Goto && n.Label != nil {
				gotos = append(gotos, n)
			}
		}
		return true
	})
	for _, g := range gotos {
		ls := labels[g.Label.Value]
		if ls == nil || g.Pos().Cmp(ls.Pos()) < 0 {
			continue
		}
		for f := range p.loopFresh {
			if g.Pos().Cmp(f.Pos()) < 0 {
				// A goto before the loop cannot run it again.
				continue
			}
			if ls.Pos().Cmp(f.Pos()) <= 0 || inSubtree(f, ls) {
				p.reenter[f] = true
			}
		}
	}
}

// A logosEFCand is an unproven entry eligible for one diagnostic.
type logosEFCand struct {
	key logosEFKey
	ent logosEFEntry
}

// logosEFCands collects the unproven entries rooted at v in deterministic
// order: by creation position, then by destination path, then by root name.
// Map iteration order must never choose the reported result.
func logosEFCands(st *logosEFState, v *Var) []logosEFCand {
	var cands []logosEFCand
	for key, e := range st.m {
		if key.root != v || e.proven {
			continue
		}
		cands = append(cands, logosEFCand{key, e})
	}
	slices.SortFunc(cands, func(a, b logosEFCand) int {
		if c := a.ent.pos.Cmp(b.ent.pos); c != 0 {
			return c
		}
		if a.key.path != b.key.path {
			if a.key.path < b.key.path {
				return -1
			}
			return 1
		}
		if a.key.root.name != b.key.root.name {
			if a.key.root.name < b.key.root.name {
				return -1
			}
			return 1
		}
		return 0
	})
	return cands
}

// logosEFDestPos reports the source position of the destination naming k.
func (p *logosEFPass) logosEFDestPos(lhs []syntax.Expr, k logosEFKey) syntax.Pos {
	for _, e := range lhs {
		if kk, ok := p.destKey(e); ok && kk == k {
			return e.Pos()
		}
	}
	return nopos
}

func (p *logosEFPass) read(n *syntax.Name, st *logosEFState) {
	v, _ := p.check.Uses[n].(*Var)
	if v == nil {
		return
	}
	k := logosEFKey{root: v}
	if e, ok := st.m[k]; ok {
		if e.proven || p.seen[n.Pos()] {
			return
		}
		p.seen[n.Pos()] = true
		p.check.errorf(n, IncompatibleAssign, "%s read of %s before %s is checked (read %s only after %s is checked)", logosResultBeforeErr, k.displayName(), e.name, k.displayName(), e.name)
		return
	}
	// A whole-value read loads every result stored inside the value, so it
	// fails while any nested destination is still unproven. A name that is
	// the root of a selector or index spine is not a whole-value read; the
	// spine's own check names the element.
	if p.spines[n.Pos()] {
		return
	}
	cands := logosEFCands(st, v)
	if len(cands) == 0 {
		return
	}
	if p.seen[n.Pos()] {
		return
	}
	p.seen[n.Pos()] = true
	first := cands[0]
	p.check.errorf(n, IncompatibleAssign, "%s read of %s before %s is checked (read %s only after %s is checked)", logosResultBeforeErr, first.key.displayName(), first.ent.name, first.key.displayName(), first.ent.name)
}

// rawKey decomposes a trackable destination: a plain name, or a chain of
// direct value-struct fields and constant array indices rooted at a plain
// name. Pointer dereferences, promoted fields, slices, maps, and
// non-constant indices denote memory this pass cannot follow without
// aliasing guesses, and return ok false.
func (p *logosEFPass) rawKey(e syntax.Expr) (logosEFKey, bool) {
	var rev []string
	for {
		switch x := syntax.Unparen(e).(type) {
		case *syntax.SelectorExpr:
			sel := p.check.Selections[x]
			if sel == nil || sel.Kind() != FieldVal || sel.Indirect() || len(sel.Index()) != 1 {
				return logosEFKey{}, false
			}
			rev = append(rev, "."+x.Sel.Value)
			e = x.X
		case *syntax.IndexExpr:
			n, ok := p.constArrayIndex(x)
			if !ok {
				return logosEFKey{}, false
			}
			rev = append(rev, "["+strconv.FormatInt(n, 10)+"]")
			e = x.X
		default:
			v := p.varOf(e)
			if v == nil {
				return logosEFKey{}, false
			}
			slices.Reverse(rev)
			return logosEFKey{root: v, path: strings.Join(rev, "")}, true
		}
	}
}

// constArrayIndex reports the canonical value of a constant array index:
// the checked constant, not the source spelling, so a named constant, a
// hexadecimal literal, and an octal literal name the same slot as the
// decimal form the checker validated.
func (p *logosEFPass) constArrayIndex(x *syntax.IndexExpr) (int64, bool) {
	_, val, ok := p.check.logosChecked(x.Index)
	// Only an integer constant can name an array slot. Any other index, such
	// as a string map key, a variable, or a tuple element access, is not a
	// constant the checker can place, so it stays untracked. There is no
	// integer to read in that case: handing a non-Int value to
	// constant.Int64Val panics.
	if !ok || val == nil || val.Kind() != constant.Int {
		return 0, false
	}
	n, ok := constant.Int64Val(val)
	if !ok || n < 0 {
		return 0, false
	}
	typ, _, ok := p.check.logosChecked(x.X)
	if !ok {
		return 0, false
	}
	for _, u := range typeset(typ) {
		if _, ok := u.(*Array); ok {
			return n, true
		}
	}
	return 0, false
}

func (p *logosEFPass) arrayBase(e syntax.Expr) bool {
	typ, _, ok := p.check.logosChecked(e)
	if !ok {
		return false
	}
	found := false
	for _, u := range typeset(typ) {
		if _, ok := u.(*Array); !ok {
			return false
		}
		found = true
	}
	return found
}

func (p *logosEFPass) valueSpine(e syntax.Expr) bool {
	for {
		switch x := syntax.Unparen(e).(type) {
		case *syntax.SelectorExpr:
			sel := p.check.Selections[x]
			if sel == nil || sel.Kind() != FieldVal || sel.Indirect() {
				return false
			}
			e = x.X
		case *syntax.IndexExpr:
			if _, ok := p.constArrayIndex(x); !ok {
				return false
			}
			e = x.X
		default:
			return p.varOf(e) != nil
		}
	}
}

// spineRoot names the plain variable a trackable spine is rooted at, or nil
// when the spine grows out of a call, a dereference, or another value this
// pass cannot name.
func spineRoot(x syntax.Expr) *syntax.Name {
	for {
		switch t := syntax.Unparen(x).(type) {
		case *syntax.SelectorExpr:
			x = t.X
		case *syntax.IndexExpr:
			x = t.X
		default:
			id, _ := syntax.Unparen(x).(*syntax.Name)
			return id
		}
	}
}

// trackable reports whether a destination can hold tracked state: the
// root's address is never taken in this body, and nothing that contains
// the destination is ever sliced, because writes through such a pointer
// or slice alias are invisible here and would leave a stale entry.
func (p *logosEFPass) trackable(k logosEFKey) bool {
	if p.addr == nil {
		p.collectAssigns()
	}
	if p.addr[k.root] {
		return false
	}
	for s := range p.sliced {
		if s.root == k.root && logosEFPathUnder(k.path, s.path) {
			return false
		}
	}
	return true
}

// destKey names any assignment destination: a plain variable or a rawKey
// path.
func (p *logosEFPass) destKey(e syntax.Expr) (logosEFKey, bool) {
	if v := p.varOf(e); v != nil {
		return logosEFKey{root: v}, true
	}
	return p.rawKey(e)
}

func (p *logosEFPass) markSpine(x syntax.Expr) {
	if p.spines == nil {
		p.spines = make(map[syntax.Pos]bool)
	}
	for d := x; ; {
		var nx syntax.Expr
		switch t := syntax.Unparen(d).(type) {
		case *syntax.SelectorExpr:
			nx = t.X
		case *syntax.IndexExpr:
			nx = t.X
		default:
			nx = nil
		}
		if nx == nil {
			break
		}
		if _, ok := syntax.Unparen(nx).(*syntax.SelectorExpr); ok {
			p.spines[nx.Pos()] = true
			p.spines[syntax.Unparen(nx).Pos()] = true
		} else if _, ok := syntax.Unparen(nx).(*syntax.IndexExpr); ok {
			p.spines[nx.Pos()] = true
			p.spines[syntax.Unparen(nx).Pos()] = true
		}
		d = nx
	}
	if id := spineRoot(x); id != nil {
		p.spines[id.Pos()] = true
	}
}

func (p *logosEFPass) readWholeRoot(x syntax.Expr, st *logosEFState) {
	id := spineRoot(x)
	if id == nil {
		return
	}
	v, _ := p.check.Uses[id].(*Var)
	if v == nil {
		return
	}
	e, found := st.m[logosEFKey{root: v}]
	if !found || e.proven || p.seen[id.Pos()] {
		return
	}
	p.seen[id.Pos()] = true
	p.check.errorf(id, IncompatibleAssign, "%s read of %s before %s is checked (read %s only after %s is checked)", logosResultBeforeErr, v.name, e.name, v.name, e.name)
}

func (p *logosEFPass) promotedStep(cur Type, i int) (string, Type, bool) {
	name := ""
	var next Type
	for _, u := range typeset(cur) {
		st, ok := u.(*Struct)
		if !ok || i < 0 || i >= st.NumFields() {
			return "", nil, false
		}
		f := st.Field(i)
		if name == "" {
			name = f.name
			next = f.typ
		} else if f.name != name || !Identical(f.typ, next) {
			return "", nil, false
		}
	}
	if name == "" {
		return "", nil, false
	}
	return "." + name, next, true
}

func (p *logosEFPass) promotedKey(x syntax.Expr) (logosEFKey, bool) {
	var rev []string
	multi := false
	for {
		if ix, isIx := syntax.Unparen(x).(*syntax.IndexExpr); isIx {
			n, ok := p.constArrayIndex(ix)
			if !ok {
				return logosEFKey{}, false
			}
			rev = append(rev, "["+strconv.FormatInt(n, 10)+"]")
			x = ix.X
			continue
		}
		sel, isSel := syntax.Unparen(x).(*syntax.SelectorExpr)
		if !isSel {
			break
		}
		s := p.check.Selections[sel]
		if s == nil || s.Kind() != FieldVal || s.Indirect() {
			return logosEFKey{}, false
		}
		if len(s.Index()) == 1 {
			rev = append(rev, "."+sel.Sel.Value)
			x = sel.X
			continue
		}
		bt, _, ok := p.check.logosChecked(sel.X)
		if !ok {
			return logosEFKey{}, false
		}
		base := len(rev)
		for _, i := range s.Index() {
			var seg string
			seg, bt, ok = p.promotedStep(bt, i)
			if !ok {
				return logosEFKey{}, false
			}
			rev = append(rev, seg)
		}
		slices.Reverse(rev[base:])
		multi = true
		x = sel.X
	}
	if !multi {
		return logosEFKey{}, false
	}
	v := p.varOf(x)
	if v == nil {
		return logosEFKey{}, false
	}
	slices.Reverse(rev)
	return logosEFKey{root: v, path: strings.Join(rev, "")}, true
}

func (p *logosEFPass) reportPromoted(x syntax.Expr, k logosEFKey, st *logosEFState) bool {
	at, pos := x, x.Pos()
	if sel, isSel := x.(*syntax.SelectorExpr); isSel {
		at, pos = sel.Sel, sel.Sel.Pos()
	}
	pres := logosEFPathPrefixes(k.path)
	keys := make([]logosEFKey, 0, len(pres)+2)
	keys = append(keys, k)
	for i := len(pres) - 1; i >= 0; i-- {
		keys = append(keys, logosEFKey{root: k.root, path: pres[i]})
	}
	keys = append(keys, logosEFKey{root: k.root})
	for _, ck := range keys {
		e, found := st.m[ck]
		if !found || e.proven {
			continue
		}
		if p.seen[pos] {
			return true
		}
		p.seen[pos] = true
		if id := spineRoot(x); id != nil {
			p.seen[id.Pos()] = true
		}
		p.check.errorf(at, IncompatibleAssign, "%s read of %s before %s is checked (read %s only after %s is checked)", logosResultBeforeErr, ck.displayName(), e.name, ck.displayName(), e.name)
		return true
	}
	// A promoted read loads the whole embedded value, so it also fails while
	// any tracked destination nested under the read path is unproven.
	var cands []logosEFCand
	for _, c := range logosEFCands(st, k.root) {
		if c.key == k || !logosEFPathUnder(c.key.path, k.path) {
			continue
		}
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return false
	}
	if p.seen[pos] {
		return true
	}
	p.seen[pos] = true
	if id := spineRoot(x); id != nil {
		p.seen[id.Pos()] = true
	}
	first := cands[0]
	p.check.errorf(at, IncompatibleAssign, "%s read of %s before %s is checked (read %s only after %s is checked)", logosResultBeforeErr, first.key.displayName(), first.ent.name, first.key.displayName(), first.ent.name)
	return true
}

func (p *logosEFPass) readDest(x syntax.Expr, st *logosEFState) {
	k, ok := p.rawKey(x)
	if !ok {
		// A segment below an outer spine is a path step, not a standalone
		// copy: the outer check decides what the read loads.
		if p.spines[x.Pos()] {
			return
		}
		if p.valueSpine(x) {
			p.markSpine(x)
			if k2, ok := p.promotedKey(x); !ok || !p.reportPromoted(x, k2, st) {
				p.readWholeRoot(x, st)
			}
		}
		return
	}
	if p.spines == nil {
		p.spines = make(map[syntax.Pos]bool)
	}
	p.markSpine(x)
	at := x
	pos := x.Pos()
	if sel, isSel := x.(*syntax.SelectorExpr); isSel {
		at, pos = sel.Sel, sel.Sel.Pos()
	}
	if e, found := st.m[k]; found {
		if e.proven || p.seen[pos] {
			return
		}
		p.seen[pos] = true
		p.check.errorf(at, IncompatibleAssign, "%s read of %s before %s is checked (read %s only after %s is checked)", logosResultBeforeErr, k.displayName(), e.name, k.displayName(), e.name)
		return
	}
	// A segment below an outer spine is not a standalone read: the outer
	// check names the entry, so this check stays silent for it.
	if p.spines[x.Pos()] {
		return
	}
	var cands []logosEFCand
	for _, c := range logosEFCands(st, k.root) {
		if c.key == k || !logosEFPathUnder(c.key.path, k.path) {
			continue
		}
		cands = append(cands, c)
	}
	if len(cands) == 0 {
		return
	}
	if p.seen[pos] {
		return
	}
	p.seen[pos] = true
	first := cands[0]
	p.check.errorf(at, IncompatibleAssign, "%s read of %s before %s is checked (read %s only after %s is checked)", logosResultBeforeErr, first.key.displayName(), first.ent.name, first.key.displayName(), first.ent.name)
}

// quietPath walks the destination spine of a plain assignment's left side
// and marks the trackable part as written, not read: a direct value-struct
// field or a constant array element names the memory without loading a
// value. Anything else on the spine loads a value first: a pointer the
// selector runs through, a slice or map the index runs on, a dereference,
// or a call. That remainder is checked as a read.
func (p *logosEFPass) quietPath(l syntax.Expr, st *logosEFState) *logosEFState {
	for {
		switch x := syntax.Unparen(l).(type) {
		case *syntax.SelectorExpr:
			sel := p.check.Selections[x]
			if sel == nil || sel.Kind() != FieldVal || sel.Indirect() || len(sel.Index()) != 1 {
				if !p.valueSpine(l) {
					return p.expr(l, st)
				}
			}
			p.seen[x.Sel.Pos()] = true
			l = x.X
		case *syntax.IndexExpr:
			if _, ok := p.constArrayIndex(x); !ok {
				if !p.arrayBase(x.X) {
					return p.expr(l, st)
				}
				st = p.quietPath(x.X, st)
				st = p.expr(x.Index, st)
				p.seen[x.Pos()] = true
				return st
			}
			p.seen[x.Pos()] = true
			st = p.expr(x.Index, st)
			l = x.X
		default:
			if id, ok := syntax.Unparen(l).(*syntax.Name); ok {
				p.seen[id.Pos()] = true
				return st
			}
			return p.expr(l, st)
		}
	}
}

func (p *logosEFPass) noteAssigned(k logosEFKey) {
	if p.eff != nil && !p.eff.inside[k.root] {
		p.eff.assigned[k] = true
	}
}

func (p *logosEFPass) noteCreated(k logosEFKey, errName string) {
	if p.eff != nil && !p.eff.inside[k.root] {
		p.eff.created[k] = errName
	}
}

func (p *logosEFPass) assigned(st *logosEFState, v *Var) *logosEFState {
	k := logosEFKey{root: v}
	p.noteAssigned(k)
	return st.dropWriting(k).detach(v)
}

func (p *logosEFPass) track(lhs []syntax.Expr, rhs syntax.Expr, st *logosEFState) *logosEFState {
	errV, keys := p.tuple(lhs, rhs)
	if errV == nil {
		// A trailing-error call whose error has no tracked identity
		// (field or discarded) still leaves a value that can reach a
		// deferred read: mark it may-reach. The marker is killed by any
		// overwrite through dropDest, merged by union across branches,
		// and consulted only when seeding a deferred capture, so
		// ordinary reads (including v, _ := f(); return v) stay as
		// before.
		if !p.errCallShape(lhs, rhs) {
			return st
		}
		for _, e := range lhs[:len(lhs)-1] {
			k, ok := p.destKey(e)
			if !ok {
				continue
			}
			if k.path != "" && !p.trackable(k) {
				continue
			}
			st = st.setMay(k)
		}
		return st
	}
	// Plain results track regardless of later address-taking or slicing: a
	// read before the alias still needs the check, and stable withholds the
	// proof from closures. Field and element destinations keep the alias
	// inventory because a write through the alias is invisible.
	for _, k := range keys {
		if k.path != "" && !p.trackable(k) {
			continue
		}
		st = st.set(k, logosEFEntry{err: errV, name: errV.name, pos: p.logosEFDestPos(lhs, k)})
		p.noteCreated(k, errV.name)
	}
	return st
}

func (p *logosEFPass) expr(n syntax.Node, st *logosEFState) *logosEFState {
	if n == nil || st == nil {
		return st
	}
	syntax.Inspect(n, func(x syntax.Node) bool {
		switch x := x.(type) {
		case *syntax.Name:
			p.read(x, st)
		case *syntax.SelectorExpr:
			p.readDest(x, st)
		case *syntax.IndexExpr:
			p.readDest(x, st)
		case *syntax.CallExpr:
			if _, val, ok := p.check.logosChecked(x); ok && val != nil {
				return false
			}
		case *syntax.Operation:
			if x.Y == nil || (x.Op != syntax.AndAnd && x.Op != syntax.OrOr) {
				return true
			}
			t, f := p.cond(x, st)
			st = logosEFJoin(t, f)
			return false
		case *syntax.FuncLit:
			st = p.later(x, x.Body, st)
			return false
		case *syntax.LambdaExpr:
			st = p.lambda(x, st)
			return false
		}
		return true
	})
	return st
}

func (p *logosEFPass) cond(e syntax.Expr, st *logosEFState) (t, f *logosEFState) {
	if st == nil {
		return nil, nil
	}
	e = syntax.Unparen(e)
	op, ok := e.(*syntax.Operation)
	if !ok {
		st = p.expr(e, st)
		return st, st
	}
	switch {
	case op.Op == syntax.Not && op.Y == nil:
		t, f = p.cond(op.X, st)
		return f, t
	case op.Op == syntax.AndAnd && op.Y != nil:
		xt, xf := p.cond(op.X, st)
		yt, yf := p.cond(op.Y, xt)
		return yt, logosEFJoin(xf, yf)
	case op.Op == syntax.OrOr && op.Y != nil:
		xt, xf := p.cond(op.X, st)
		yt, yf := p.cond(op.Y, xf)
		return logosEFJoin(xt, yt), yf
	}
	st = p.expr(op, st)
	errV, isNeq := p.nilTest(op)
	if errV == nil {
		return st, st
	}
	if isNeq {
		return st, st.prove(errV)
	}
	return st.prove(errV), st
}

func (p *logosEFPass) stmts(list []syntax.Stmt, st *logosEFState) *logosEFState {
	for _, s := range list {
		st = p.stmt(s, st)
	}
	return st
}

func (p *logosEFPass) stmt(s syntax.Stmt, st *logosEFState) *logosEFState {
	if s == nil {
		return st
	}
	if ls, ok := s.(*syntax.LabeledStmt); ok {
		return p.labeled(ls, st)
	}
	if st == nil {
		return nil
	}
	switch s := s.(type) {
	case *syntax.BlockStmt:
		return p.stmts(s.List, st)
	case *syntax.ExprStmt:
		return p.exprStmt(s, st)
	case *syntax.SendStmt:
		return p.expr(s.Value, p.expr(s.Chan, st))
	case *syntax.DeclStmt:
		return p.decls(s, st)
	case *syntax.AssignStmt:
		return p.assign(s, st)
	case *syntax.BranchStmt:
		return p.branch(s, st)
	case *syntax.ReturnStmt:
		return p.ret(s, st)
	case *syntax.IfStmt:
		return p.ifStmt(s, st)
	case *syntax.ForStmt:
		return p.forStmt(s, "", st)
	case *syntax.SwitchStmt:
		return p.switchStmt(s, "", st)
	case *syntax.SelectStmt:
		return p.selectStmt(s, "", st)
	}
	return p.logosStmt(s, st)
}

func (p *logosEFPass) logosStmt(s syntax.Stmt, st *logosEFState) *logosEFState {
	switch s := s.(type) {
	case *syntax.GroupStmt:
		return p.stmt(s.Body, p.expr(s.Limit, p.expr(s.Ctx, st)))
	case *syntax.GroupChild:
		return p.later(s.Body, s.Body, st)
	case *syntax.ConstIfStmt:
		st = p.expr(s.Cond, st)
		return logosEFJoin(p.stmt(s.Then, st), p.stmt(s.Else, st))
	case *syntax.ConstSwitchStmt:
		out := st
		for _, c := range s.Cases {
			out = logosEFJoin(out, p.stmts(c.Body, st))
		}
		return out
	case *syntax.ConstForStmt:
		return p.loop(s.Body, nil, nil, "", p.expr(s.Range, st), nil)
	}
	return p.expr(s, st)
}

func (p *logosEFPass) labeled(s *syntax.LabeledStmt, st *logosEFState) *logosEFState {
	st = logosEFJoin(st, p.ctx.labels[s.Label.Value])
	if st == nil {
		return nil
	}
	switch inner := s.Stmt.(type) {
	case *syntax.ForStmt:
		return p.forStmt(inner, s.Label.Value, st)
	case *syntax.SwitchStmt:
		return p.switchStmt(inner, s.Label.Value, st)
	case *syntax.SelectStmt:
		return p.selectStmt(inner, s.Label.Value, st)
	}
	return p.stmt(s.Stmt, st)
}

func (p *logosEFPass) exprStmt(s *syntax.ExprStmt, st *logosEFState) *logosEFState {
	st = p.expr(s.X, st)
	call, ok := syntax.Unparen(s.X).(*syntax.CallExpr)
	if !ok {
		return st
	}
	if fun, ok := syntax.Unparen(call.Fun).(*syntax.Name); ok {
		if b, ok := p.check.Uses[fun].(*Builtin); ok && b.Name() == "panic" {
			return nil
		}
	}
	if fn := p.staticFunc(call.Fun); fn != nil && logosNeverReturns(fn) {
		return nil
	}
	return st
}

// logosNeverReturns reports whether a call to fn cannot return to its
// caller, from facts the type checker already knows: the listed
// standard-library calls, each of which ends the process, panics, or
// ends the test goroutine through runtime.Goexit. A user-declared
// function is never inferred as never returning, even when it always
// exits, and a same-named method declared outside its listed package
// never counts.
func logosNeverReturns(fn *Func) bool {
	fn = fn.Origin()
	pkg := fn.Pkg()
	if pkg == nil {
		return false
	}
	sig, _ := fn.Type().(*Signature)
	if sig == nil {
		return false
	}
	switch pkg.Path() {
	case "os":
		return sig.Recv() == nil && fn.Name() == "Exit"
	case "runtime":
		return sig.Recv() == nil && fn.Name() == "Goexit"
	case "log":
		if sig.Recv() != nil {
			return false
		}
		switch fn.Name() {
		case "Fatal", "Fatalf", "Fatalln", "Panic", "Panicf", "Panicln":
			return true
		}
		return false
	case "testing":
		switch fn.Name() {
		case "Fatal", "Fatalf", "FailNow", "Skip", "Skipf", "SkipNow":
		default:
			return false
		}
		if sig.Recv() == nil {
			return true
		}
		base, _ := deref(sig.Recv().Type())
		if named, ok := base.(*Named); ok {
			obj := named.Obj()
			// Any receiver type declared in package testing: the Fatal,
			// FailNow and Skip families are declared on the unexported
			// *common and reach *T, *B, and *F by promotion. A same-named
			// method from any other package is rejected by this check.
			return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == "testing"
		}
		// testing.TB declares the same names as interface methods, so the
		// receiver is the interface literal and there is no named type to
		// check. The method itself is declared in package testing (checked
		// above), which only testing.TB satisfies for these names.
		_, ok := base.(*Interface)
		return ok
	}
	return false
}

func (p *logosEFPass) decls(s *syntax.DeclStmt, st *logosEFState) *logosEFState {
	for _, d := range s.DeclList {
		vd, ok := d.(*syntax.VarDecl)
		if !ok {
			continue
		}
		st = p.expr(vd.Values, st)
		names := logosEFNames(vd)
		for _, name := range names {
			if v := p.varOf(name); v != nil {
				st = p.assigned(st, v)
			}
		}
		if vd.Values != nil {
			st = p.track(names, vd.Values, st)
		}
	}
	return st
}

func (p *logosEFPass) assign(s *syntax.AssignStmt, st *logosEFState) *logosEFState {
	lhs := syntax.UnpackListExpr(s.Lhs)
	rhs := syntax.UnpackListExpr(s.Rhs)
	plain := s.Op == 0 || s.Op == syntax.Def
	for i, r := range rhs {
		if s.Op == 0 && len(lhs) == len(rhs) && logosEFBlank(lhs[i]) && p.isBlankable(r) {
			continue
		}
		st = p.expr(r, st)
	}
	for _, l := range lhs {
		if !(plain && s.Rhs != nil && logosEFIsName(l)) {
			if plain && s.Rhs != nil {
				st = p.quietPath(l, st)
			} else {
				st = p.expr(l, st)
			}
		}
	}
	for _, l := range lhs {
		st = p.dropDest(st, l)
	}
	if plain && len(rhs) == 1 {
		st = p.track(lhs, rhs[0], st)
	}
	return st
}

// isBlankable reports whether discarding r to the blank identifier drops a
// tracked destination without reading it: a plain name or a rawKey path.
// Anything larger, such as h.count+1, still reads its operands.
func (p *logosEFPass) isBlankable(r syntax.Expr) bool {
	if logosEFIsName(r) {
		return true
	}
	_, ok := p.rawKey(r)
	return ok
}

func logosEFIsName(e syntax.Expr) bool {
	_, ok := syntax.Unparen(e).(*syntax.Name)
	return ok
}

func logosEFBlank(e syntax.Expr) bool {
	id, ok := syntax.Unparen(e).(*syntax.Name)
	return ok && id.Value == "_"
}

// dropDest stops tracking a destination that a statement writes: the
// entry for that destination, everything nested inside it, and, for a
// whole-variable write, every entry rooted at that variable.
func (p *logosEFPass) dropDest(st *logosEFState, l syntax.Expr) *logosEFState {
	l = syntax.Unparen(l)
	if v := p.varOf(l); v != nil {
		return p.assigned(st, v)
	}
	if k, ok := p.rawKey(l); ok {
		p.noteAssigned(k)
		return st.dropWriting(k)
	}
	if k, ok := p.promotedKey(l); ok {
		// A promoted write names the same memory as its canonical path.
		p.noteAssigned(k)
		return st.dropWriting(k)
	}
	if v := p.derefRoot(l); v != nil {
		return p.assigned(st, v)
	}
	return st
}

func (p *logosEFPass) derefRoot(e syntax.Expr) *Var {
	op, ok := syntax.Unparen(e).(*syntax.Operation)
	if !ok || op.Op != syntax.Mul || op.Y != nil {
		return nil
	}
	id, ok := syntax.Unparen(op.X).(*syntax.Name)
	if !ok {
		return nil
	}
	v, _ := p.check.Uses[id].(*Var)
	if v == nil {
		return nil
	}
	if p.assigns == nil {
		p.collectAssigns()
	}
	if !p.initialized[v] {
		return nil
	}
	origin := p.sole(v)
	if origin == nil {
		return nil
	}
	and, ok := syntax.Unparen(origin).(*syntax.Operation)
	if !ok || and.Op != syntax.And || and.Y != nil {
		return nil
	}
	return p.varOf(and.X)
}

func (p *logosEFPass) ret(s *syntax.ReturnStmt, st *logosEFState) *logosEFState {
	results := syntax.UnpackListExpr(s.Results)
	passed := make(map[*Var]bool, len(results))
	for _, r := range results {
		if v := p.varOf(r); v != nil {
			passed[v] = true
		}
	}
	for _, r := range results {
		if k, ok := p.destKey(r); ok {
			if e, found := st.m[k]; found && e.err != nil && passed[e.err] {
				continue
			}
		}
		st = p.expr(r, st)
	}
	p.ctx.exit = logosEFJoin(p.ctx.exit, st)
	return nil
}

func (p *logosEFPass) branch(s *syntax.BranchStmt, st *logosEFState) *logosEFState {
	switch s.Tok {
	case syntax.Break:
		if f := p.frame(s.Label, false); f != nil {
			f.brk = logosEFJoin(f.brk, st)
		}
	case syntax.Continue:
		if f := p.frame(s.Label, true); f != nil {
			f.cnt = logosEFJoin(f.cnt, st)
		}
	case syntax.Goto:
		old := p.ctx.labels[s.Label.Value]
		if joined := logosEFJoin(old, st); !logosEFSame(old, joined) {
			p.ctx.labels[s.Label.Value] = joined
			p.ctx.changed = true
		}
	case syntax.Fallthrough:
		p.ctx.fall = logosEFJoin(p.ctx.fall, st)
	}
	return nil
}

func (p *logosEFPass) pushFrame(label string, loop bool) *logosEFFrame {
	f := &logosEFFrame{label: label, loop: loop}
	p.ctx.frames = append(p.ctx.frames, f)
	return f
}

func (p *logosEFPass) popFrame() {
	p.ctx.frames = p.ctx.frames[:len(p.ctx.frames)-1]
}

func (p *logosEFPass) frame(label *syntax.Name, loop bool) *logosEFFrame {
	for i := len(p.ctx.frames) - 1; i >= 0; i-- {
		f := p.ctx.frames[i]
		switch {
		case label != nil && f.label == label.Value:
			return f
		case label == nil && (f.loop || !loop):
			return f
		}
	}
	return nil
}

func (p *logosEFPass) ifStmt(s *syntax.IfStmt, st *logosEFState) *logosEFState {
	st = p.stmt(s.Init, st)
	t, f := p.cond(s.Cond, st)
	return logosEFJoin(p.stmt(s.Then, t), p.stmt(s.Else, f))
}

func (p *logosEFPass) forStmt(s *syntax.ForStmt, label string, st *logosEFState) *logosEFState {
	rc, isRange := s.Init.(*syntax.RangeClause)
	if !isRange {
		return p.loop(s.Body, s.Cond, s.Post, label, p.stmt(s.Init, st), nil)
	}
	return p.loop(s.Body, nil, nil, label, p.expr(rc.X, st), rc)
}

func (p *logosEFPass) loop(body *syntax.BlockStmt, cond syntax.Expr, post syntax.SimpleStmt, label string, st *logosEFState, rc *syntax.RangeClause) *logosEFState {
	if st == nil {
		return nil
	}
	head := st
	for {
		f := p.pushFrame(label, true)
		in, out := head, (*logosEFState)(nil)
		switch {
		case rc != nil:
			in, out = p.rangeAssign(rc, head), head
		case cond != nil:
			in, out = p.cond(cond, head)
		}
		end := p.stmt(post, logosEFJoin(p.stmt(body, in), f.cnt))
		p.popFrame()
		next := logosEFJoin(head, end)
		if logosEFSame(next, head) {
			return logosEFJoin(out, f.brk)
		}
		head = next
	}
}

func (p *logosEFPass) rangeAssign(rc *syntax.RangeClause, st *logosEFState) *logosEFState {
	if rc.Lhs == nil || rc.Def {
		return st
	}
	for _, l := range syntax.UnpackListExpr(rc.Lhs) {
		if v := p.varOf(l); v != nil {
			st = p.assigned(st, v)
		} else if _, ok := p.rawKey(l); ok {
			st = p.quietPath(l, st)
			st = p.dropDest(st, l)
		} else {
			st = p.expr(l, st)
		}
	}
	return st
}

func (p *logosEFPass) switchStmt(s *syntax.SwitchStmt, label string, st *logosEFState) *logosEFState {
	st = p.stmt(s.Init, st)
	var tagErr *Var
	switch tag := s.Tag.(type) {
	case *syntax.TypeSwitchGuard:
		tagErr = p.errVar(tag.X)
		st = p.expr(tag.X, st)
	case nil:
	default:
		tagErr = p.errVar(tag)
		st = p.expr(tag, st)
	}
	f := p.pushFrame(label, false)
	enter := make([]*logosEFState, len(s.Body))
	miss := st
	hasDefault := false
	for i, c := range s.Body {
		if c.Cases == nil {
			hasDefault = true
			continue
		}
		for _, e := range syntax.UnpackListExpr(c.Cases) {
			var hit *logosEFState
			hit, miss = p.caseCond(s.Tag, tagErr, e, miss)
			enter[i] = logosEFJoin(enter[i], hit)
		}
	}
	var out, fall *logosEFState
	for i, c := range s.Body {
		if c.Cases == nil {
			enter[i] = miss
		}
		p.ctx.fall = nil
		out = logosEFJoin(out, p.stmts(c.Body, logosEFJoin(enter[i], fall)))
		fall = p.ctx.fall
	}
	p.ctx.fall = nil
	p.popFrame()
	if !hasDefault {
		out = logosEFJoin(out, miss)
	}
	return logosEFJoin(out, f.brk)
}

func (p *logosEFPass) caseCond(tag syntax.Expr, tagErr *Var, e syntax.Expr, miss *logosEFState) (hit, rest *logosEFState) {
	switch {
	case tag == nil:
		return p.cond(e, miss)
	case tagErr != nil && p.isNil(syntax.Unparen(e)):
		return miss.prove(tagErr), miss
	}
	miss = p.expr(e, miss)
	return miss, miss
}

func (p *logosEFPass) selectStmt(s *syntax.SelectStmt, label string, st *logosEFState) *logosEFState {
	f := p.pushFrame(label, false)
	var out *logosEFState
	for _, c := range s.Body {
		out = logosEFJoin(out, p.stmts(c.Body, p.stmt(c.Comm, st)))
	}
	p.popFrame()
	return logosEFJoin(out, f.brk)
}

func (p *logosEFPass) defsIn(n syntax.Node) map[*Var]bool {
	inside := make(map[*Var]bool)
	syntax.Inspect(n, func(x syntax.Node) bool {
		if id, ok := x.(*syntax.Name); ok {
			if v, _ := p.check.Defs[id].(*Var); v != nil {
				inside[v] = true
			}
		}
		return true
	})
	return inside
}

func inSubtree(root, target syntax.Node) bool {
	found := false
	syntax.Inspect(root, func(n syntax.Node) bool {
		if n == target {
			found = true
			return false
		}
		return !found
	})
	return found
}

// loopExitsAfterDefer reports whether lit's loop cannot run its tuple
// stores again before a deferred literal executes: lit sits in a top-level
// defer statement of the loop body and every later statement of that body
// is an unlabeled break or a return, so the loop exits on every path after
// registration. It answers only about this loop's own stores; a store
// positioned after the literal still counts through storePos.
func (p *logosEFPass) loopExitsAfterDefer(lit syntax.Node, f *syntax.ForStmt) bool {
	if f.Body == nil {
		return false
	}
	i := -1
	for j, s := range f.Body.List {
		if s == lit || inSubtree(s, lit) {
			i = j
			break
		}
	}
	if i < 0 || i+1 >= len(f.Body.List) {
		return false
	}
	cs, ok := f.Body.List[i].(*syntax.CallStmt)
	if !ok || cs.Tok != syntax.Defer {
		return false
	}
	for _, s := range f.Body.List[i+1:] {
		switch s := s.(type) {
		case *syntax.BranchStmt:
			if s.Tok != syntax.Break || s.Label != nil {
				return false
			}
		case *syntax.ReturnStmt:
		default:
			return false
		}
	}
	return true
}

// storeLiveAfter reports whether the store of k at pos can write the value
// a literal registered at lit later observes: the store is positioned after
// the literal, or it sits in a loop containing the literal that can reach
// it again before the literal runs.
func (p *logosEFPass) storeLiveAfter(pos syntax.Pos, k logosEFKey, lit syntax.Node) bool {
	if pos.Cmp(lit.Pos()) >= 0 {
		return true
	}
	for f := range p.loopsFor(lit) {
		keys := p.loopTuple[f]
		if !keys[k] {
			continue
		}
		// A variable declared inside f's own body or post is fresh each
		// iteration, but a variable from an outer loop is shared by f's
		// iterations: freshness is a property of this loop, not of the
		// variable.
		if p.loopFresh[f][k.root] {
			continue
		}
		if !p.reenter[f] && p.loopExitsAfterDefer(lit, f) {
			continue
		}
		if pos.Cmp(f.Pos()) >= 0 && pos.Cmp(syntax.EndPos(f)) <= 0 {
			return true
		}
	}
	return false
}

// loopDistinctVars reports whether f gives its declared variables a new
// instance each iteration, mirroring noder's distinctVars: go1.22 file
// semantics, or the loopvar debug knob.
func (p *logosEFPass) loopDistinctVars(f *syntax.ForStmt) bool {
	lv := base.Debug.LoopVar
	v := p.check.versions[f.Pos().FileBase()]
	return (v == "" || version.Compare(v, "go1.22") >= 0) || lv > 0 && lv != 3
}

// loopVarsFresh reports whether f's iteration variables (range clause,
// for-init, for-post) provably get a new instance each iteration.
// loopDistinctVars mirrors noder's syntactic eligibility, but ForCapture
// also hash-gates each variable under GOCOMPILEDEBUG=loopvarhash, so with
// base.LoopVarHash set a clause variable can stay shared; freshness is
// then not provable and the analysis must assume sharing.
func (p *logosEFPass) loopVarsFresh(f *syntax.ForStmt) bool {
	return base.LoopVarHash == nil && p.loopDistinctVars(f)
}

// loopsFor memoizes the loops whose bodies contain lit; lateCaptures asks
// for every tracked key, and rescanning each loop body per key made the
// pass cubic on large bodies.
func (p *logosEFPass) loopsFor(lit syntax.Node) map[*syntax.ForStmt]bool {
	if p.litLoops == nil {
		p.litLoops = make(map[syntax.Node]map[*syntax.ForStmt]bool)
	}
	if m, ok := p.litLoops[lit]; ok {
		return m
	}
	m := make(map[*syntax.ForStmt]bool)
	for f := range p.loopTuple {
		if inSubtree(f.Body, lit) {
			m[f] = true
		}
	}
	p.litLoops[lit] = m
	return m
}

func (p *logosEFPass) seedAfter(k logosEFKey, lit syntax.Node) bool {
	for _, pos := range p.storePos[k] {
		if p.storeLiveAfter(pos, k, lit) {
			return true
		}
	}
	return false
}

func (p *logosEFPass) seedEntry(k logosEFKey, lit syntax.Node) (logosEFEntry, bool) {
	errName, ok := p.tracked[k]
	if !ok {
		return logosEFEntry{}, false
	}
	e := logosEFEntry{name: errName, pos: lit.Pos()}
	// The entry's proof is valid only when every store that can write the
	// captured value pairs it with the same error variable; mixed error
	// variables leave the result unproven, so a check on the first error
	// cannot pass a value a different call may have failed on.
	var errV *Var
	seen := false
	// Only stores that can reach the captured value at function exit
	// contribute: flow analysis already keeps dead stores out, so a store
	// absent from st.m at lit names a value overwritten before lit.
	consider := func(ev *Var) {
		if seen && errV != ev {
			errV = nil
			seen = false
			return
		}
		errV = ev
		seen = true
	}
	for i, ev := range p.storeErr[k] {
		if !p.storeLiveAfter(p.storePos[k][i], k, lit) {
			continue
		}
		consider(ev)
		if errV == nil && !seen {
			break
		}
	}
	if errV != nil && len(p.assigns[logosEFKey{root: errV}]) == 1 {
		e.err = errV
	}
	if e.err != nil && p.loopLocalErrShared(errV, k, lit) {
		// A loop-local error names a new instance each iteration, so a
		// check on the captured one cannot prove a value a later
		// iteration's store may have failed on.
		e.err = nil
	}
	return e, true
}

// loopLocalErrShared reports whether errV is declared inside a loop that
// also holds a store of k which can write the captured value after lit
// registers: the seed pairs one iteration's error with another's result.
func (p *logosEFPass) loopLocalErrShared(errV *Var, k logosEFKey, lit syntax.Node) bool {
	for i := range p.storeErr[k] {
		pos := p.storePos[k][i]
		if !p.storeLiveAfter(pos, k, lit) {
			continue
		}
		for f, maybe := range p.loopMaybe {
			if !maybe[errV] {
				continue
			}
			if pos.Cmp(f.Pos()) < 0 || pos.Cmp(syntax.EndPos(f)) > 0 {
				continue
			}
			// A result root that is fresh in this loop names a new
			// instance each iteration, so a store in one iteration
			// cannot overwrite the value another iteration's defer
			// captured: the pairing is same-iteration.
			if p.loopFresh[f][k.root] {
				continue
			}
			// A loop that cannot run twice has no earlier iteration
			// whose defer could observe this store: the pairing is
			// the single iteration's own.
			if p.loopRunsOnce(f) {
				continue
			}
			// A break or return after the store exits only the iteration
			// that wrote the result: a defer registered by an earlier
			// iteration still observes the field at function exit, so
			// the pairing is cross-iteration and cannot be proven.
			return true
		}
	}
	return false
}

// loopRunsOnce reports whether f cannot register a defer in one iteration
// and store the captured value in a later one: every path through the
// body exits the loop (unlabeled break or return) without reaching the
// back edge, no continue targets f, and no goto can re-enter it. A nil
// body or an unknown branch target stays multi-iteration.
func (p *logosEFPass) loopRunsOnce(f *syntax.ForStmt) bool {
	if f.Body == nil || p.reenter[f] {
		return false
	}
	hasContinue := false
	syntax.Inspect(f.Body, func(n syntax.Node) bool {
		br, ok := n.(*syntax.BranchStmt)
		if !ok || br.Tok != syntax.Continue {
			return true
		}
		if br.Target == nil || br.Target == f {
			hasContinue = true
			return false
		}
		return true
	})
	if hasContinue {
		return false
	}
	return efBlockExitsLoop(f.Body.List, f)
}

// efBlockExitsLoop reports whether a statement list cannot fall through:
// its last statement always exits the loop f.
func efBlockExitsLoop(list []syntax.Stmt, f *syntax.ForStmt) bool {
	if len(list) == 0 {
		return false
	}
	return efStmtExitsLoop(list[len(list)-1], f)
}

// efStmtExitsLoop reports whether s always exits loop f through an
// unlabeled break targeting f or a return. Switches, selects, and nested
// loops capture unlabeled breaks, so they never count; labeled branches
// and gotos stay multi-iteration.
func efStmtExitsLoop(s syntax.Stmt, f *syntax.ForStmt) bool {
	switch s := s.(type) {
	case *syntax.BranchStmt:
		if s.Tok != syntax.Break || s.Label != nil {
			return false
		}
		return s.Target == nil || s.Target == f
	case *syntax.ReturnStmt:
		return true
	case *syntax.BlockStmt:
		return efBlockExitsLoop(s.List, f)
	case *syntax.IfStmt:
		if s.Else == nil {
			return false
		}
		return efStmtExitsLoop(s.Then, f) && efStmtExitsLoop(s.Else, f)
	case *syntax.LabeledStmt:
		return efStmtExitsLoop(s.Stmt, f)
	}
	return false
}

func (p *logosEFPass) lateCaptures(st *logosEFState, lit syntax.Node, inside map[*Var]bool) *logosEFState {
	if p.assigns == nil {
		p.collectAssigns()
	}
	unprove := func(st *logosEFState, k logosEFKey) *logosEFState {
		e, ok := st.m[k]
		if !ok {
			name := "its error"
			if n, ok := p.tracked[k]; ok {
				name = n
			}
			return st.set(k, logosEFEntry{name: name, pos: lit.Pos()})
		}
		if e.err == nil {
			return st
		}
		e.err = nil
		m := maps.Clone(st.m)
		m[k] = e
		return &logosEFState{m: m, may: st.may}
	}
	syntax.Inspect(lit, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Name:
			v, _ := p.check.Uses[n].(*Var)
			if v == nil || inside[v] {
				return true
			}
			k := logosEFKey{root: v}
			if _, ok := st.m[k]; ok {
				if st.may != nil && st.may[k] {
					st = unprove(st, k)
				}
				return true
			}
			hadMay := st.may != nil && st.may[k]
			if e, ok := p.seedEntry(k, lit); ok && p.seedAfter(k, lit) {
				st = st.set(k, e)
				if hadMay {
					st = unprove(st, k)
				}
			} else if hadMay {
				st = unprove(st, k)
			}
			for _, k2 := range p.trackedByRoot[v] {
				if k2.path == "" || !p.trackable(k2) || !p.seedAfter(k2, lit) {
					if k2.path == "" || !p.trackable(k2) {
						continue
					}
					if st.may == nil || !st.may[k2] {
						continue
					}
					if _, has := st.m[k2]; !has {
						st = unprove(st, k2)
					} else if st.may[k2] {
						st = unprove(st, k2)
					}
					continue
				}
				if _, has := st.m[k2]; !has {
					hadMay2 := st.may != nil && st.may[k2]
					if e, ok := p.seedEntry(k2, lit); ok {
						st = st.set(k2, e)
						if hadMay2 {
							st = unprove(st, k2)
						}
					} else if hadMay2 {
						st = unprove(st, k2)
					}
				} else if st.may != nil && st.may[k2] {
					st = unprove(st, k2)
				}
			}
		case *syntax.SelectorExpr, *syntax.IndexExpr:
			k, ok := p.rawKey(n.(syntax.Expr))
			if !ok || inside[k.root] {
				return true
			}
			if _, has := st.m[k]; has {
				if st.may != nil && st.may[k] {
					st = unprove(st, k)
				}
				return true
			}
			hadMaySel := st.may != nil && st.may[k]
			if e, tracked := p.seedEntry(k, lit); tracked && p.trackable(k) && p.seedAfter(k, lit) {
				st = st.set(k, e)
				if hadMaySel {
					st = unprove(st, k)
				}
			} else if hadMaySel && p.trackable(k) {
				st = unprove(st, k)
			}
			for _, k2 := range p.trackedByRoot[k.root] {
				if k2 == k || !logosEFPathUnder(k2.path, k.path) {
					continue
				}
				if !p.trackable(k2) || !p.seedAfter(k2, lit) {
					if p.trackable(k2) && st.may != nil && st.may[k2] {
						if _, has := st.m[k2]; !has {
							st = unprove(st, k2)
						} else {
							st = unprove(st, k2)
						}
					}
					continue
				}
				if _, has := st.m[k2]; !has {
					hadMay2 := st.may != nil && st.may[k2]
					if e, ok := p.seedEntry(k2, lit); ok {
						st = st.set(k2, e)
						if hadMay2 {
							st = unprove(st, k2)
						}
					} else if hadMay2 {
						st = unprove(st, k2)
					}
				} else if st.may != nil && st.may[k2] {
					st = unprove(st, k2)
				}
			}
		}
		return true
	})
	return st
}

func (p *logosEFPass) later(lit syntax.Node, body *syntax.BlockStmt, st *logosEFState) *logosEFState {
	if body == nil {
		return st
	}
	inside := p.defsIn(lit)
	saved := p.eff
	eff := &logosEFEffects{inside: inside, assigned: make(map[logosEFKey]bool), created: make(map[logosEFKey]string)}
	p.eff = eff
	p.run(body, p.lateCaptures(p.capture(st), lit, inside))
	p.eff = saved
	for k := range eff.assigned {
		if k.path == "" {
			st = st.detach(k.root)
		}
		p.noteAssigned(k)
	}
	for k, errName := range eff.created {
		st = st.set(k, logosEFEntry{name: errName, pos: body.Pos()})
		p.noteCreated(k, errName)
	}
	return st
}

func (p *logosEFPass) lambda(lam *syntax.LambdaExpr, st *logosEFState) *logosEFState {
	body, isBlock := lam.Body.(*syntax.BlockStmt)
	if p.check.logosInPlace[lam] {
		return p.inPlace(lam, st)
	}
	if isBlock {
		return p.later(lam, body, st)
	}
	p.expr(lam.Body, p.lateCaptures(p.capture(st), lam, p.defsIn(lam)))
	return st
}

func (p *logosEFPass) inPlace(lam *syntax.LambdaExpr, st *logosEFState) *logosEFState {
	in := st
	for {
		var end *logosEFState
		if body, ok := lam.Body.(*syntax.BlockStmt); ok {
			end = p.run(body, in)
		} else {
			end = p.expr(lam.Body, in)
		}
		next := logosEFJoin(st, end)
		if logosEFSame(next, in) {
			return in
		}
		in = next
	}
}

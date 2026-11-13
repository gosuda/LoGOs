// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lint

import (
	"fmt"
	"go/constant"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

var unboundedChannelOp = Rule{
	ID:      "correctness::unbounded_channel_op",
	Group:   Correctness,
	Default: Deny,
	Go:      true,
	Check:   checkUnboundedChannelOp,
}

func checkUnboundedChannelOp(pass *Pass) {
	for _, f := range pass.Files {
		syntax.Inspect(f, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.FuncDecl:
				if n.Body != nil {
					newChanUnit(pass, f, n).run()
				}
				return false
			case *syntax.FuncLit:
				newChanUnit(pass, f, n).run()
				return false
			}
			return true
		})
	}
}

type chanOp struct {
	start syntax.Pos
	end   syntax.Pos
	ch    *types2.Var
	what  string
}

type chanUnit struct {
	pass   *Pass
	file   *syntax.File
	root   syntax.Node
	free   bool
	loops  int
	lits   int
	defs   map[*types2.Var]int
	caps   map[*types2.Var]int64
	sent   map[*types2.Var]int
	looped map[*types2.Var]bool
	ops    []chanOp
}

func newChanUnit(pass *Pass, f *syntax.File, root syntax.Node) *chanUnit {
	return &chanUnit{pass: pass, file: f, root: root}
}

func (u *chanUnit) run() {
	syntax.Inspect(u.root, u.visit)
	if len(u.ops) == 0 {
		return
	}
	u.defs = make(map[*types2.Var]int)
	u.caps = make(map[*types2.Var]int64)
	syntax.Inspect(u.root, u.collect)
	for _, op := range u.ops {
		if op.ch != nil && u.bounded(op.ch) {
			continue
		}
		u.pass.Report(Diagnostic{
			Pos:     op.start,
			End:     op.end,
			Message: fmt.Sprintf("%s may block forever: put it in a select with a <-ctx.Done() case, or make the channel locally with a constant capacity that covers every send", op.what),
		})
	}
}

func (u *chanUnit) bounded(ch *types2.Var) bool {
	k, ok := u.caps[ch]
	return ok && u.defs[ch] == 1 && !u.looped[ch] && int64(u.sent[ch]) <= k
}

func (u *chanUnit) collect(n syntax.Node) bool {
	switch n := n.(type) {
	case *syntax.AssignStmt:
		if lhs, rhs, ok := assignPairs(n); ok {
			u.note(lhs, rhs)
		}
	case *syntax.VarDecl:
		if lhs, rhs, ok := declPairs(n); ok {
			u.note(lhs, rhs)
		}
	}
	return true
}

func (u *chanUnit) note(lhs, rhs []syntax.Expr) {
	for i, l := range lhs {
		id, ok := syntax.Unparen(l).(*syntax.Name)
		if !ok {
			continue
		}
		v := localVar(u.pass, id)
		if v == nil {
			continue
		}
		u.defs[v]++
		if len(rhs) != len(lhs) {
			continue
		}
		if k, ok := u.capacity(rhs[i]); ok {
			u.caps[v] = k
		}
	}
}

func (u *chanUnit) capacity(x syntax.Expr) (int64, bool) {
	call := callOf(x)
	if call == nil || !callsBuiltin(u.pass, call, "make") {
		return 0, false
	}
	if _, ok := underOrNil(TypeOf(call)).(*types2.Chan); !ok {
		return 0, false
	}
	if len(call.ArgList) < 2 {
		return 0, true
	}
	val := call.ArgList[1].GetTypeInfo().Value
	if val == nil {
		return -1, true
	}
	k, exact := constant.Int64Val(constant.ToInt(val))
	if !exact {
		return -1, true
	}
	return k, true
}

func (u *chanUnit) visit(n syntax.Node) bool {
	switch n := n.(type) {
	case *syntax.ForStmt:
		u.loop(n)
		return false
	case *syntax.SelectStmt:
		u.selectStmt(n)
		return false
	case *syntax.GroupChild:
		u.child(n.Body)
		return false
	case *syntax.CallExpr:
		if stageOf(u.pass, n) != nil {
			u.segmentCall(n)
			return false
		}
		if body := callbackBody(n.Fun); body != nil {
			u.invoked(n, body)
			return false
		}
	case *syntax.FuncLit:
		u.nested(n.Body)
		return false
	case *syntax.LambdaExpr:
		u.nested(n.Body)
		return false
	case *syntax.SendStmt:
		u.send(n)
	case *syntax.Operation:
		u.operation(n)
	}
	return true
}

func (u *chanUnit) operation(n *syntax.Operation) {
	if op := recvOf(n); op != nil {
		u.recv(op)
	}
}

func (u *chanUnit) walk(n syntax.Node) {
	if n != nil {
		syntax.Inspect(n, u.visit)
	}
}

func (u *chanUnit) loop(f *syntax.ForStmt) {
	u.rangeOp(f.Init)
	u.walk(f.Init)
	u.loops++
	u.walk(f.Cond)
	u.walk(f.Post)
	u.walk(f.Body)
	u.loops--
}

func (u *chanUnit) rangeOp(st syntax.SimpleStmt) {
	rc, ok := st.(*syntax.RangeClause)
	if !ok {
		return
	}
	t := TypeOf(rc.X)
	if t == nil {
		return
	}
	if _, isChan := types2.CoreType(t).(*types2.Chan); !isChan {
		return
	}
	u.record(rc.Pos(), u.exprEnd(rc.X), u.chanVar(rc.X), "range over "+syntax.String(rc.X))
}

func (u *chanUnit) nested(body syntax.Node) {
	u.lits++
	u.once(body)
	u.lits--
}

func (u *chanUnit) invoked(call *syntax.CallExpr, body syntax.Node) {
	u.once(body)
	for _, arg := range call.ArgList {
		u.walk(arg)
	}
}

func (u *chanUnit) once(body syntax.Node) {
	outer := u.free
	u.free = false
	u.walk(body)
	u.free = outer
}

func (u *chanUnit) child(body syntax.Node) {
	outer := u.free
	u.free = true
	u.walk(body)
	u.free = outer
}

func (u *chanUnit) segmentCall(call *syntax.CallExpr) {
	u.walk(call.Fun)
	for _, arg := range call.ArgList {
		if lam, ok := syntax.Unparen(arg).(*syntax.LambdaExpr); ok {
			u.lits++
			u.child(lam.Body)
			u.lits--
			continue
		}
		u.walk(arg)
	}
}

func (u *chanUnit) selectStmt(s *syntax.SelectStmt) {
	outer := u.free
	covered := outer || selectHasDone(s)
	for _, cc := range s.Body {
		u.free = covered
		operands := u.comm(cc.Comm)
		u.free = outer
		for _, x := range operands {
			u.walk(x)
		}
		for _, st := range cc.Body {
			u.walk(st)
		}
	}
}

func (u *chanUnit) comm(st syntax.SimpleStmt) []syntax.Node {
	switch st := st.(type) {
	case *syntax.SendStmt:
		u.send(st)
		return []syntax.Node{st.Chan, st.Value}
	case *syntax.ExprStmt:
		if op := recvOf(st.X); op != nil {
			u.recv(op)
			return []syntax.Node{op.X}
		}
		return []syntax.Node{st.X}
	case *syntax.AssignStmt:
		if op := commRecv(st); op != nil {
			u.recv(op)
			return []syntax.Node{st.Lhs, op.X}
		}
		return []syntax.Node{st.Lhs, st.Rhs}
	}
	return nil
}

func (u *chanUnit) send(s *syntax.SendStmt) {
	ch := u.chanVar(s.Chan)
	if ch != nil {
		u.count(ch)
	}
	u.record(syntax.StartPos(s), u.exprEnd(s.Value), ch, "send on "+syntax.String(s.Chan))
}

func (u *chanUnit) count(ch *types2.Var) {
	if u.sent == nil {
		u.sent = make(map[*types2.Var]int)
		u.looped = make(map[*types2.Var]bool)
	}
	if u.loops > 0 || u.lits > 0 {
		u.looped[ch] = true
		return
	}
	u.sent[ch]++
}

func (u *chanUnit) recv(op *syntax.Operation) {
	if isDoneCall(op.X) {
		return
	}
	u.record(syntax.StartPos(op), u.exprEnd(op.X), u.chanVar(op.X), "receive from "+syntax.String(op.X))
}

func (u *chanUnit) record(start, end syntax.Pos, ch *types2.Var, what string) {
	if !u.free {
		u.ops = append(u.ops, chanOp{start: start, end: end, ch: ch, what: what})
	}
}

func (u *chanUnit) exprEnd(x syntax.Expr) syntax.Pos {
	mode := syntax.Mode(0)
	if u.pass.Kind == KindLgo {
		mode = syntax.LogosMode
	}
	switch x := x.(type) {
	case *syntax.CallExpr:
		return u.pass.CallEnd(u.file, x)
	case *syntax.IndexExpr, *syntax.SliceExpr:
		if end, ok := syntax.BracketEnd(u.pass.Source(u.file), x.Pos(), mode); ok {
			return end
		}
	case *syntax.ParenExpr:
		if end, ok := syntax.ParenEnd(u.pass.Source(u.file), x.Pos(), mode); ok {
			return end
		}
	case *syntax.AssertExpr:
		open := syntax.MakePos(x.Pos().Base(), x.Pos().Line(), x.Pos().Col()+1)
		if end, ok := syntax.ParenEnd(u.pass.Source(u.file), open, mode); ok {
			return end
		}
	case *syntax.Operation:
		if x.Y != nil {
			return u.exprEnd(x.Y)
		}
		return u.exprEnd(x.X)
	}
	return syntax.EndPos(x)
}

func (u *chanUnit) chanVar(x syntax.Expr) *types2.Var {
	id, ok := syntax.Unparen(x).(*syntax.Name)
	if !ok {
		return nil
	}
	return localVar(u.pass, id)
}

func recvOf(x syntax.Expr) *syntax.Operation {
	op, ok := syntax.Unparen(x).(*syntax.Operation)
	if !ok || op.Op != syntax.Recv || op.Y != nil {
		return nil
	}
	return op
}

func commRecv(a *syntax.AssignStmt) *syntax.Operation {
	if a.Rhs == nil {
		return nil
	}
	return recvOf(a.Rhs)
}

func selectHasDone(s *syntax.SelectStmt) bool {
	for _, cc := range s.Body {
		if op := commOperation(cc.Comm); op != nil && isDoneCall(op.X) {
			return true
		}
	}
	return false
}

func commOperation(st syntax.SimpleStmt) *syntax.Operation {
	switch st := st.(type) {
	case *syntax.ExprStmt:
		return recvOf(st.X)
	case *syntax.AssignStmt:
		return commRecv(st)
	}
	return nil
}

func isDoneCall(x syntax.Expr) bool {
	call, ok := syntax.Unparen(x).(*syntax.CallExpr)
	if !ok || len(call.ArgList) != 0 {
		return false
	}
	sel, ok := syntax.Unparen(call.Fun).(*syntax.SelectorExpr)
	if !ok || sel.Sel.Value != "Done" {
		return false
	}
	_, isChan := underOrNil(TypeOf(call)).(*types2.Chan)
	return isChan
}

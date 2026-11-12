// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"go/token"
)

func isRecvOperandToken(k token.Token) bool {
	switch k {
	case token.IDENT, token.INT, token.FLOAT, token.IMAG, token.CHAR, token.STRING,
		token.FUNC, token.CHAN, token.MAP, token.STRUCT, token.INTERFACE:
		return true
	}
	return false
}

func isRecvPrefixOp(k token.Token) bool {
	switch k {
	case token.MUL, token.AND, token.ADD, token.SUB, token.NOT, token.XOR, token.ARROW:
		return true
	}
	return false
}

func operandEnd(src []byte, start int) int {
	end := start
	depth := 0
	needOperand := true
	done := false
	scanSegment(src, start, len(src), func(t tok) bool {
		if done {
			return false
		}
		if t.comment {
			return true
		}
		if t.auto {
			if depth == 0 {
				done = true
				return false
			}
			return true
		}
		k := t.kind
		if depth > 0 {
			switch k {
			case token.LPAREN, token.LBRACK, token.LBRACE:
				depth++
			case token.RPAREN, token.RBRACK, token.RBRACE:
				depth--
				if depth == 0 {
					end = t.end
					needOperand = false
				}
			}
			return true
		}
		if needOperand {
			switch {
			case k == token.LPAREN:
				depth = 1
				end = t.end
			case isRecvOperandToken(k):
				end = t.end
				needOperand = false
			case isRecvPrefixOp(k):
				end = t.end
			default:
				done = true
				return false
			}
			return true
		}
		switch k {
		case token.PERIOD:
			end = t.end
			needOperand = true
		case token.LPAREN, token.LBRACK:
			depth = 1
			end = t.end
		default:
			done = true
			return false
		}
		return true
	})
	return end
}

type hoistSub struct {
	start int
	end   int
	text  string
}

func (fc *funcLower) hoistSubMark() int {
	return len(fc.hoistSubs)
}

func (fc *funcLower) hoistSubTake(mark int) []hoistSub {
	subs := fc.hoistSubs[mark:]
	fc.hoistSubs = fc.hoistSubs[:mark]
	return subs
}

func (fc *funcLower) hoistSubEmit(subs []hoistSub) {
	for _, sub := range subs {
		fc.fl.add(sub.start, sub.end, sub.text)
	}
}

func groupContainsRecv(e syntax.Expr) bool {
	found := false
	syntax.Inspect(e, func(n syntax.Node) bool {
		if _, ok := n.(*syntax.FuncLit); ok {
			return false
		}
		ex, ok := n.(syntax.Expr)
		if !ok {
			return !found
		}
		if op, ok := syntax.Unparen(ex).(*syntax.Operation); ok {
			if op.Op == syntax.Recv && op.Y == nil {
				found = true
				return false
			}
		}
		return !found
	})
	return found
}
func (fc *funcLower) groupHoistOneRecv(op *syntax.Operation, top groupFrame) (syntax.Expr, []syntax.Stmt) {
	pos := op.Pos()
	ch := op.X
	elem := groupRecvElem{recv: &syntax.Operation{Op: syntax.Recv, X: ch}, vals: 1}
	elem.recv.SetPos(pos)
	native, _, names := fc.groupRecvSelect(pos, top, &elem)
	rs := fc.fl.off(leftmostOf(op))
	fc.hoistSubs = append(fc.hoistSubs, hoistSub{start: rs, end: operandEnd(fc.fl.src, rs), text: names[0]})
	return nameOf(pos, names[0]), native
}

func (fc *funcLower) groupHasCall(e syntax.Expr) bool {
	found := false
	syntax.Inspect(e, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.CallExpr:
			if tv, ok := fc.fl.pkg.info.Types[n.Fun]; !ok || !tv.IsType() {
				found = true
			}
		}
		return !found
	})
	return found
}

func groupPrimary(e syntax.Expr) bool {
	switch e.(type) {
	case *syntax.CallExpr, *syntax.ParenExpr, *syntax.SelectorExpr, *syntax.IndexExpr, *syntax.SliceExpr, *syntax.AssertExpr, *syntax.CompositeLit:
		return true
	}
	return false
}

func (fc *funcLower) groupReject(pos syntax.Pos, where string) {
	host := "a go group child"
	if top, ok := fc.groupTop(); ok && top.segment {
		host = "a group stage lambda"
	}
	base.Errorf("%s: logos: channel receive %s cannot lower in %s", pos.String(), where, host)
}

func (fc *funcLower) groupRejectCommaRecv(pos syntax.Pos, nLhs int, rhs []syntax.Expr) bool {
	if nLhs != 2 || len(rhs) != 1 {
		return false
	}
	if _, ok := groupRecvOp(rhs[0]); !ok {
		return false
	}
	fc.groupReject(pos, "in the comma-ok form of this statement")
	return true
}

func (fc *funcLower) groupHoistPlain(pos syntax.Pos, e syntax.Expr) (syntax.Expr, []syntax.Stmt) {
	if !fc.groupHasCall(e) {
		return e, nil
	}
	typ := fc.fl.pkg.info.TypeOf(e)
	if typ == nil || !groupPrimary(e) {
		fc.groupReject(e.Pos(), "after an operand that calls a function")
		return e, nil
	}
	tmp := fc.groupTempName("t")
	es := fc.fl.off(leftmostOf(e))
	fc.hoistSubs = append(fc.hoistSubs, hoistSub{start: es, end: operandEnd(fc.fl.src, es), text: tmp})
	decl := typedVarDecl(pos, nameOf(pos, tmp), fc.groupSpellType(typ, pos))
	as := assignOf(pos, []syntax.Expr{nameOf(pos, tmp)}, []syntax.Expr{e})
	return nameOf(pos, tmp), []syntax.Stmt{decl, as}
}

func groupLaterContains(list []syntax.Expr, i int) bool {
	for _, e := range list[i+1:] {
		if groupContainsRecv(e) {
			return true
		}
	}
	return false
}

func (fc *funcLower) groupHoistList(pos syntax.Pos, list []syntax.Expr, top groupFrame) ([]syntax.Expr, []syntax.Stmt) {
	out := make([]syntax.Expr, len(list))
	var pre []syntax.Stmt
	for i, e := range list {
		if !groupContainsRecv(e) {
			if groupLaterContains(list, i) {
				ne, pp := fc.groupHoistPlain(pos, e)
				pre = append(pre, pp...)
				out[i] = ne
			} else {
				out[i] = e
			}
			continue
		}
		ne, pp := fc.groupHoistExpr(e, top)
		pre = append(pre, pp...)
		out[i] = ne
	}
	return out, pre
}
func (fc *funcLower) groupHoistExpr(e syntax.Expr, top groupFrame) (syntax.Expr, []syntax.Stmt) {
	pos := e.Pos()
	if op, ok := groupRecvOp(e); ok {
		return fc.groupHoistOneRecv(op, top)
	}
	switch e := e.(type) {
	case *syntax.ParenExpr:
		ne, pre := fc.groupHoistExpr(e.X, top)
		e.X = ne
		return e, pre
	case *syntax.Operation:
		if e.Y == nil {
			ne, pre := fc.groupHoistExpr(e.X, top)
			e.X = ne
			return e, pre
		}
		if (e.Op == syntax.AndAnd || e.Op == syntax.OrOr) && groupContainsRecv(e.Y) {
			fc.groupReject(e.Y.Pos(), "in the right operand of && or ||")
			return e, nil
		}
		nl, pre := fc.groupHoistList(pos, []syntax.Expr{e.X, e.Y}, top)
		e.X, e.Y = nl[0], nl[1]
		return e, pre
	case *syntax.CallExpr:
		if fc.groupHasCall(e.Fun) && groupLaterContains(e.ArgList, -1) {
			fc.groupReject(e.Pos(), "in the arguments of a call whose function operand calls a function")
			return e, nil
		}
		nf, preF := fc.groupHoistExpr(e.Fun, top)
		e.Fun = nf
		nl, preA := fc.groupHoistList(pos, e.ArgList, top)
		e.ArgList = nl
		return e, append(preF, preA...)
	case *syntax.CompositeLit:
		nl, pre := fc.groupHoistList(pos, e.ElemList, top)
		e.ElemList = nl
		return e, pre
	case *syntax.KeyValueExpr:
		nk, preK := fc.groupHoistExpr(e.Key, top)
		nv, preV := fc.groupHoistExpr(e.Value, top)
		e.Key, e.Value = nk, nv
		return e, append(preK, preV...)
	case *syntax.IndexExpr:
		if fc.groupHasCall(e.X) && groupContainsRecv(e.Index) {
			fc.groupReject(e.Pos(), "in an index after an operand that calls a function")
			return e, nil
		}
		nx, preX := fc.groupHoistExpr(e.X, top)
		ni, preI := fc.groupHoistExpr(e.Index, top)
		e.X, e.Index = nx, ni
		return e, append(preX, preI...)
	case *syntax.SelectorExpr:
		ne, pre := fc.groupHoistExpr(e.X, top)
		e.X = ne
		return e, pre
	default:
		return e, nil
	}
}

func groupStmtSpan(fc *funcLower, s syntax.Stmt) (int, int) {
	start := fc.fl.off(syntax.StartPos(s))
	return start, stmtEnd(fc.fl.src, start)
}

func (fc *funcLower) groupHoistAssign(s *syntax.AssignStmt, top groupFrame) []syntax.Stmt {
	scanForLiterals(fc.fl, s)
	pos := s.Pos()
	lhs := syntax.UnpackListExpr(s.Lhs)
	rhs := syntax.UnpackListExpr(s.Rhs)
	for _, l := range lhs {
		if fc.groupHasCall(l) && groupLaterContains(rhs, -1) {
			fc.groupReject(l.Pos(), "after an assignment target that calls a function")
			return []syntax.Stmt{s}
		}
	}
	mark := fc.hoistSubMark()
	nlhs, preL := fc.groupHoistList(pos, lhs, top)
	nrhs, preR := fc.groupHoistList(pos, rhs, top)
	pre := append(preL, preR...)
	if len(pre) == 0 {
		fc.hoistSubTake(mark)
		return []syntax.Stmt{s}
	}
	s.Lhs = listOf(pos, nlhs)
	s.Rhs = listOf(pos, nrhs)
	start, _ := groupStmtSpan(fc, s)
	fc.hoistSubEmit(fc.hoistSubTake(mark))
	fc.fl.addT(start, start, renderText(pre, nil))
	return append(pre, s)
}

func (fc *funcLower) groupHoistAssignInner(s *syntax.AssignStmt, top groupFrame) ([]syntax.Stmt, bool) {
	pos := s.Pos()
	lhs := syntax.UnpackListExpr(s.Lhs)
	rhs := syntax.UnpackListExpr(s.Rhs)
	if fc.groupRejectCommaRecv(s.Pos(), len(lhs), rhs) {
		return []syntax.Stmt{s}, false
	}
	nlhs, preL := fc.groupHoistList(pos, lhs, top)
	nrhs, preR := fc.groupHoistList(pos, rhs, top)
	pre := append(preL, preR...)
	if len(pre) == 0 {
		return []syntax.Stmt{s}, false
	}
	s.Lhs = listOf(pos, nlhs)
	s.Rhs = listOf(pos, nrhs)
	return append(pre, s), true
}

func (fc *funcLower) groupHoistDecl(s *syntax.DeclStmt, top groupFrame) []syntax.Stmt {
	scanForLiterals(fc.fl, s)
	if len(s.DeclList) != 1 {
		return []syntax.Stmt{s}
	}
	d, ok := s.DeclList[0].(*syntax.VarDecl)
	if !ok || d.Values == nil {
		return []syntax.Stmt{s}
	}
	pos := s.Pos()
	vals := syntax.UnpackListExpr(d.Values)
	if fc.groupRejectCommaRecv(pos, len(d.NameList), vals) {
		return []syntax.Stmt{s}
	}
	mark := fc.hoistSubMark()
	nvals, pre := fc.groupHoistList(pos, vals, top)
	if len(pre) == 0 {
		fc.hoistSubTake(mark)
		return []syntax.Stmt{s}
	}
	d.Values = listOf(pos, nvals)
	start, _ := groupStmtSpan(fc, s)
	fc.hoistSubEmit(fc.hoistSubTake(mark))
	fc.fl.addT(start, start, renderText(pre, nil))
	return append(pre, s)
}
func (fc *funcLower) groupHoistExprStmt(s *syntax.ExprStmt, top groupFrame) []syntax.Stmt {
	scanForLiterals(fc.fl, s)
	mark := fc.hoistSubMark()
	start := fc.fl.off(leftmostOf(s.X))
	ne, pre := fc.groupHoistExpr(s.X, top)
	if len(pre) == 0 {
		fc.hoistSubTake(mark)
		return []syntax.Stmt{s}
	}
	s.X = ne
	fc.hoistSubEmit(fc.hoistSubTake(mark))
	fc.fl.addT(start, start, renderText(pre, nil))
	return append(pre, s)
}

func (fc *funcLower) groupHoistReturn(s *syntax.ReturnStmt, top groupFrame) []syntax.Stmt {
	scanForLiterals(fc.fl, s)
	mark := fc.hoistSubMark()
	results, pre := fc.groupHoistList(s.Pos(), syntax.UnpackListExpr(s.Results), top)
	if len(pre) == 0 {
		fc.hoistSubTake(mark)
		return []syntax.Stmt{s}
	}
	s.Results = listOf(s.Pos(), results)
	start, _ := groupStmtSpan(fc, s)
	fc.hoistSubEmit(fc.hoistSubTake(mark))
	fc.fl.addT(start, start, renderText(pre, nil))
	return append(pre, s)
}

func (fc *funcLower) groupHoistSend(s *syntax.SendStmt, top groupFrame) []syntax.Stmt {
	scanForLiterals(fc.fl, s)
	pos := s.Pos()
	mark := fc.hoistSubMark()
	nc, preC := fc.groupHoistExpr(s.Chan, top)
	nv, preV := fc.groupHoistExpr(s.Value, top)
	s.Chan, s.Value = nc, nv
	pre := append(preC, preV...)
	send := at(&syntax.SendStmt{Chan: s.Chan, Value: s.Value}, pos)
	sel := at(&syntax.SelectStmt{Body: []*syntax.CommClause{at(&syntax.CommClause{Comm: send}, pos), fc.groupDoneClause(pos, top)}}, pos)
	out := append(pre, sel)
	fc.hoistSubTake(mark)
	start, end := groupStmtSpan(fc, s)
	fc.fl.addT(start, end, renderText(out, nil))
	return out
}
func (fc *funcLower) groupHoistIf(s *syntax.IfStmt, top groupFrame) []syntax.Stmt {
	scanForLiterals(fc.fl, s.Init)
	scanForLiterals(fc.fl, s.Cond)
	mark := fc.hoistSubMark()
	var pre []syntax.Stmt
	if as, ok := s.Init.(*syntax.AssignStmt); ok && groupHasRecv(as) && len(syntax.UnpackListExpr(as.Lhs)) == 2 {
		fc.hoistSubTake(mark)
		return fc.groupHoistCommaIf(s, as, top)
	}
	if as, ok := s.Init.(*syntax.AssignStmt); ok {
		if nr, changed := fc.groupHoistAssignInner(as, top); changed {
			pre = append(pre, nr[:len(nr)-1]...)
			if si, ok := nr[len(nr)-1].(syntax.SimpleStmt); ok {
				s.Init = si
			}
		}
	}
	if s.Cond != nil && groupContainsRecv(s.Cond) {
		if s.Init != nil {
			fc.groupReject(s.Cond.Pos(), "in a condition that follows an init statement")
		} else {
			nc, pp := fc.groupHoistExpr(s.Cond, top)
			pre = append(pre, pp...)
			s.Cond = nc
		}
	}
	s.Then.List = fc.lowerList(s.Then.List)
	fc.groupHoistElse(s, top)
	if len(pre) == 0 {
		fc.hoistSubTake(mark)
		return []syntax.Stmt{s}
	}
	headStart := fc.fl.off(s.Pos())
	fc.hoistSubEmit(fc.hoistSubTake(mark))
	fc.fl.addT(headStart, headStart, renderText(pre, nil))
	return append(pre, s)
}

func (fc *funcLower) groupHoistCommaIf(s *syntax.IfStmt, as *syntax.AssignStmt, top groupFrame) []syntax.Stmt {
	pos := s.Pos()
	mark := fc.hoistSubMark()
	op, _ := groupRecvOp(syntax.UnpackListExpr(as.Rhs)[0])
	elem := groupRecvElem{recv: op, vals: 2}
	native, _, names := fc.groupRecvSelect(pos, top, &elem)
	rs := fc.fl.off(leftmostOf(op))
	fc.fl.add(rs, operandEnd(fc.fl.src, rs), names[0]+", "+names[1])
	lhs := syntax.UnpackListExpr(as.Lhs)
	if as.Op == syntax.Def {
		s.Init = defineOf(pos, lhs, []syntax.Expr{nameOf(pos, names[0]), nameOf(pos, names[1])})
	} else {
		s.Init = assignOf(pos, lhs, []syntax.Expr{nameOf(pos, names[0]), nameOf(pos, names[1])})
	}
	s.Then.List = fc.lowerList(s.Then.List)
	fc.groupHoistElse(s, top)
	fc.hoistSubTake(mark)
	start, _ := groupStmtSpan(fc, s)
	fc.fl.addT(start, start, renderText(native, nil))
	return append(native, s)
}

func (fc *funcLower) groupHoistElse(s *syntax.IfStmt, top groupFrame) {
	if s.Else == nil {
		return
	}
	switch e := s.Else.(type) {
	case *syntax.BlockStmt:
		e.List = fc.lowerList(e.List)
	case *syntax.IfStmt:
		if !groupStmtHasRecv(e) {
			s.Else = fc.lowerIf(e)[0]
			return
		}
		start := fc.fl.off(syntax.StartPos(e))
		fc.fl.add(start, start, "{ ")
		nr := fc.groupHoistIf(e, top)
		end := fc.fl.off(syntax.EndPos(e)) + 1
		fc.fl.add(end, end, " }")
		s.Else = blockOf(s.Pos(), nr)
	}
}

func (fc *funcLower) groupHoistSwitch(s *syntax.SwitchStmt, top groupFrame) []syntax.Stmt {
	scanForLiterals(fc.fl, s.Init)
	scanForLiterals(fc.fl, s.Tag)
	mark := fc.hoistSubMark()
	var pre []syntax.Stmt
	if as, ok := s.Init.(*syntax.AssignStmt); ok {
		if nr, changed := fc.groupHoistAssignInner(as, top); changed {
			pre = append(pre, nr[:len(nr)-1]...)
			if si, ok := nr[len(nr)-1].(syntax.SimpleStmt); ok {
				s.Init = si
			}
		}
	}
	if s.Tag != nil && groupContainsRecv(s.Tag) {
		if s.Init != nil {
			fc.groupReject(s.Tag.Pos(), "in a tag that follows an init statement")
		} else {
			nt, pp := fc.groupHoistExpr(s.Tag, top)
			pre = append(pre, pp...)
			s.Tag = nt
		}
	}
	for _, c := range s.Body {
		if c.Cases != nil && groupContainsRecv(c.Cases) {
			fc.groupReject(c.Cases.Pos(), "in a switch case expression")
		}
		scanForLiterals(fc.fl, c.Cases)
		c.Body = fc.lowerList(c.Body)
	}
	if len(pre) == 0 {
		fc.hoistSubTake(mark)
		return []syntax.Stmt{s}
	}
	start, _ := groupStmtSpan(fc, s)
	fc.hoistSubEmit(fc.hoistSubTake(mark))
	fc.fl.addT(start, start, renderText(pre, nil))
	return append(pre, s)
}

func groupNodesHaveRecv(nodes ...syntax.Node) bool {
	found := false
	for _, n := range nodes {
		if n == nil || found {
			continue
		}
		syntax.Inspect(n, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.FuncLit, *syntax.SelectStmt:
				return false
			case syntax.Expr:
				if _, ok := groupRecvOp(n); ok {
					found = true
				}
			}
			return !found
		})
	}
	return found
}

func groupStmtHasRecv(s syntax.Stmt) bool {
	switch s := s.(type) {
	case *syntax.IfStmt:
		return groupNodesHaveRecv(s.Init, s.Cond)
	case *syntax.SwitchStmt:
		return groupNodesHaveRecv(s.Init, s.Tag) || groupCasesHaveRecv(s)
	case *syntax.ForStmt:
		return groupNodesHaveRecv(s.Init, s.Cond, s.Post)
	}
	return groupNodesHaveRecv(s)
}

func groupCasesHaveRecv(s *syntax.SwitchStmt) bool {
	for _, c := range s.Body {
		if c.Cases != nil && groupNodesHaveRecv(c.Cases) {
			return true
		}
	}
	return false
}

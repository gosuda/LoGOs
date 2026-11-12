// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"go/token"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

func (fc *funcLower) groupChanTop() (groupFrame, bool) {
	if top, ok := fc.groupTop(); ok && top.child {
		return top, true
	}
	return groupFrame{}, false
}

func (fc *funcLower) groupDoneResults(pos syntax.Pos, top groupFrame) []syntax.Expr {
	var res []syntax.Expr
	if top.segmentRes != nil {
		res = append(res, fc.fl.zeroValue(top.segmentRes, pos))
	}
	return append(res, callOf(pos, selOf(pos, nameOf(pos, top.gctx), "Err"), nil))
}

func (fc *funcLower) groupDoneText(top groupFrame) string {
	var res []string
	for _, r := range fc.groupDoneResults(syntax.Pos{}, top) {
		res = append(res, renderExpr(r))
	}
	return "case <-" + top.gctx + ".Done(): return " + strings.Join(res, ", ")
}

func (fc *funcLower) groupDoneClause(pos syntax.Pos, top groupFrame) *syntax.CommClause {
	done := callOf(pos, selOf(pos, nameOf(pos, top.gctx), "Done"), nil)
	recv := at(&syntax.Operation{Op: syntax.Recv, X: done}, pos)
	ret := returnOf(pos, fc.groupDoneResults(pos, top))
	return at(&syntax.CommClause{Comm: exprStmtOf(pos, recv), Body: []syntax.Stmt{ret}}, pos)
}

func groupRecvOp(e syntax.Expr) (*syntax.Operation, bool) {
	op, ok := syntax.Unparen(e).(*syntax.Operation)
	if !ok || op.Op != syntax.Recv || op.Y != nil {
		return nil, false
	}
	return op, true
}

func groupChanElem(t types2.Type) types2.Type {
	ch, ok := t.Underlying().(*types2.Chan)
	if !ok {
		base.Fatalf("logos: receive from non-channel type %s", t)
	}
	return ch.Elem()
}

func groupHasRecv(s *syntax.AssignStmt) bool {
	if s.Rhs == nil {
		return false
	}
	for _, r := range syntax.UnpackListExpr(s.Rhs) {
		if _, ok := groupRecvOp(r); ok {
			return true
		}
	}
	return false
}

func (fc *funcLower) groupSpellType(t types2.Type, pos syntax.Pos) syntax.Expr {
	text := types2.TypeString(t, fc.yieldQualifier)
	texpr, ok := reparseType(text)
	if !ok {
		base.Fatalf("logos: cannot respell channel element type %q", text)
	}
	syntax.Inspect(texpr, func(n syntax.Node) bool {
		if n != nil {
			n.SetPos(pos)
		}
		return true
	})
	return texpr
}

type groupRecvElem struct {
	recv *syntax.Operation
	vals int
	tmp  string
	otmp string
}

func (fc *funcLower) groupRecvLayout(lhsN int, rhs []syntax.Expr) ([]groupRecvElem, bool) {
	elems := make([]groupRecvElem, len(rhs))
	total, recvCount := 0, 0
	for i, r := range rhs {
		if op, ok := groupRecvOp(r); ok {
			elems[i].recv = op
			elems[i].vals = 1
			if len(rhs) == 1 && lhsN == 2 {
				elems[i].vals = 2
			}
			recvCount++
		} else if tup, ok := fc.fl.pkg.info.TypeOf(r).(*types2.Tuple); ok {
			elems[i].vals = tup.Len()
		} else {
			elems[i].vals = 1
		}
		total += elems[i].vals
	}
	return elems, recvCount > 0 && total == lhsN
}

func (fc *funcLower) groupTempName(prefix string) string {
	nm := fmt.Sprintf("_logos_%s%d", prefix, fc.tmpSeq)
	fc.tmpSeq++
	return nm
}

func (fc *funcLower) groupPlainTemps(pos syntax.Pos, r syntax.Expr, vals int) (syntax.Stmt, otext, []string) {
	fl := fc.fl
	start := fl.off(leftmostOf(r))
	elemText := fl.textOf(span{start: start, end: groupExprEnd(fl.src, start)})
	names := make([]string, vals)
	for i := range names {
		names[i] = fc.groupTempName("t")
	}
	return defineOf(pos, tempNodes(pos, names), []syntax.Expr{r}), cat(gen(strings.Join(names, ", ")+" := "), elemText), names
}

func (fc *funcLower) groupRecvSelect(pos syntax.Pos, top groupFrame, e *groupRecvElem) ([]syntax.Stmt, []otext, []string) {
	fl := fc.fl
	ch := e.recv.X
	chStart := fl.off(leftmostOf(ch))
	chText := fl.textOf(span{start: chStart, end: operandEnd(fl.src, chStart)})
	elemT := groupChanElem(fl.pkg.info.TypeOf(ch))
	e.tmp = fc.groupTempName("t")
	names := []string{e.tmp}
	native := []syntax.Stmt{typedVarDecl(pos, nameOf(pos, e.tmp), fc.groupSpellType(elemT, pos))}
	text := []otext{gen("var " + e.tmp + " " + types2.TypeString(elemT, fc.yieldQualifier))}
	caseLHS := e.tmp
	if e.vals == 2 {
		e.otmp = fc.groupTempName("o")
		names = append(names, e.otmp)
		native = append(native, typedVarDecl(pos, nameOf(pos, e.otmp), nameOf(pos, "bool")))
		text = append(text, gen("var "+e.otmp+" bool"))
		caseLHS += ", " + e.otmp
	}
	recvN := assignOf(pos, tempNodes(pos, names), []syntax.Expr{at(&syntax.Operation{Op: syntax.Recv, X: ch}, pos)})
	native = append(native, at(&syntax.SelectStmt{
		Body: []*syntax.CommClause{at(&syntax.CommClause{Comm: recvN}, pos), fc.groupDoneClause(pos, top)},
	}, pos))
	text = append(text, cat(gen("select { case "+caseLHS+" = <-"), chText, gen(": "+fc.groupDoneText(top)+" }")))
	return native, text, names
}

func (fc *funcLower) groupRecvTemps(pos syntax.Pos, top groupFrame, rhs []syntax.Expr, elems []groupRecvElem) ([]syntax.Stmt, []otext, []string) {
	var native []syntax.Stmt
	var text []otext
	var temps []string
	for i := range elems {
		if elems[i].recv == nil {
			n, t, names := fc.groupPlainTemps(pos, rhs[i], elems[i].vals)
			native, text, temps = append(native, n), append(text, t), append(temps, names...)
			continue
		}
		n, t, names := fc.groupRecvSelect(pos, top, &elems[i])
		native, text, temps = append(native, n...), append(text, t...), append(temps, names...)
	}
	return native, text, temps
}

func groupAssignFinal(s *syntax.AssignStmt, lhs, temps []syntax.Expr) (syntax.Stmt, string) {
	pos := s.Pos()
	switch s.Op {
	case syntax.Def:
		return defineOf(pos, lhs, temps), " := "
	case 0:
		return assignOf(pos, lhs, temps), " = "
	}
	return at(&syntax.AssignStmt{Op: s.Op, Lhs: listOf(pos, lhs), Rhs: listOf(pos, temps)}, pos), " " + s.Op.String() + "= "
}

func (fc *funcLower) lowerGroupRecv(s *syntax.AssignStmt, top groupFrame) []syntax.Stmt {
	fl := fc.fl
	scanForLiterals(fl, s)
	lhs := syntax.UnpackListExpr(s.Lhs)
	rhs := syntax.UnpackListExpr(s.Rhs)
	elems, ok := fc.groupRecvLayout(len(lhs), rhs)
	if !ok {
		return []syntax.Stmt{s}
	}
	pos := s.Pos()
	native, text, temps := fc.groupRecvTemps(pos, top, rhs, elems)
	finalN, sep := groupAssignFinal(s, lhs, tempNodes(pos, temps))
	start := fl.off(leftmostOf(lhs[0]))
	text = append(text, cat(fl.textOf(span{start: start, end: fl.off(pos)}), gen(sep+strings.Join(temps, ", "))))
	fl.addT(start, stmtEnd(fl.src, start), joinO(text, "\n"))
	return append(native, finalN)
}

func groupRecvDecl(s *syntax.DeclStmt) (*syntax.VarDecl, bool) {
	if len(s.DeclList) != 1 {
		return nil, false
	}
	d, ok := s.DeclList[0].(*syntax.VarDecl)
	if !ok || d.Group != nil || d.Values == nil {
		return nil, false
	}
	for _, v := range syntax.UnpackListExpr(d.Values) {
		if _, isRecv := groupRecvOp(v); isRecv {
			return d, true
		}
	}
	return nil, false
}

func groupAssignOffset(src []byte, start int) int {
	off := start
	scanSegment(src, start, len(src), func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		if t.kind == token.ASSIGN {
			off = t.start
			return false
		}
		return true
	})
	return off
}

func (fc *funcLower) lowerGroupVarRecv(s *syntax.DeclStmt, d *syntax.VarDecl, top groupFrame) []syntax.Stmt {
	fl := fc.fl
	scanForLiterals(fl, s)
	rhs := syntax.UnpackListExpr(d.Values)
	elems, ok := fc.groupRecvLayout(len(d.NameList), rhs)
	if !ok {
		return []syntax.Stmt{s}
	}
	pos := s.Pos()
	native, text, temps := fc.groupRecvTemps(pos, top, rhs, elems)
	start := fl.off(pos)
	head := fl.textOf(span{start: start, end: groupAssignOffset(fl.src, start)}).trimRight(" \t\n")
	text = append(text, cat(head, gen(" = "+strings.Join(temps, ", "))))
	fl.addT(start, stmtEnd(fl.src, start), joinO(text, "\n"))
	nd := at(&syntax.VarDecl{NameList: d.NameList, Type: d.Type, Values: listOf(pos, tempNodes(pos, temps))}, pos)
	return append(native, at(&syntax.DeclStmt{DeclList: []syntax.Decl{nd}}, pos))
}

func (fc *funcLower) groupChanStmt(s syntax.Stmt) ([]syntax.Stmt, bool) {
	top, ok := fc.groupChanTop()
	if !ok {
		return nil, false
	}
	before := base.Errors()
	out, handled := fc.groupChanDispatch(s, top)
	if handled && base.Errors() == before && groupStmtHasRecv(out[len(out)-1]) {
		fc.groupReject(out[len(out)-1].Pos(), "in an unsupported position")
	}
	return out, handled
}

func (fc *funcLower) groupChanDispatch(s syntax.Stmt, top groupFrame) ([]syntax.Stmt, bool) {
	switch s := s.(type) {
	case *syntax.AssignStmt:
		if out, handled := fc.groupChanAssign(s, top); handled {
			return out, true
		}
		if groupStmtHasRecv(s) {
			return fc.groupHoistAssign(s, top), true
		}
	case *syntax.DeclStmt:
		if out, handled := fc.groupChanDecl(s, top); handled {
			return out, true
		}
		if groupStmtHasRecv(s) {
			return fc.groupHoistDecl(s, top), true
		}
	case *syntax.ExprStmt:
		if out, handled := fc.groupChanExpr(s, top); handled {
			return out, true
		}
		if groupStmtHasRecv(s) {
			return fc.groupHoistExprStmt(s, top), true
		}
	case *syntax.ReturnStmt:
		if groupStmtHasRecv(s) {
			return fc.groupHoistReturn(s, top), true
		}
	case *syntax.SendStmt:
		if groupStmtHasRecv(s) {
			return fc.groupHoistSend(s, top), true
		}
		return fc.lowerGroupSend(s, top), true
	case *syntax.ForStmt:
		if groupStmtHasRecv(s) {
			fc.groupReject(s.Pos(), "in a for statement header")
			return nil, false
		}
		return fc.groupChanFor(s, top)
	case *syntax.CallStmt:
		if groupNodesHaveRecv(s.Call) {
			fc.groupReject(s.Pos(), "in the arguments of a defer statement")
		}
	case *syntax.IfStmt:
		if groupStmtHasRecv(s) {
			return fc.groupHoistIf(s, top), true
		}
	case *syntax.SwitchStmt:
		if groupStmtHasRecv(s) {
			return fc.groupHoistSwitch(s, top), true
		}
	}
	return nil, false
}

func (fc *funcLower) groupChanAssign(s *syntax.AssignStmt, top groupFrame) ([]syntax.Stmt, bool) {
	if !groupHasRecv(s) {
		return nil, false
	}
	return fc.lowerGroupRecv(s, top), true
}

func (fc *funcLower) groupChanDecl(s *syntax.DeclStmt, top groupFrame) ([]syntax.Stmt, bool) {
	d, ok := groupRecvDecl(s)
	if !ok {
		return nil, false
	}
	return fc.lowerGroupVarRecv(s, d, top), true
}

func (fc *funcLower) groupChanExpr(s *syntax.ExprStmt, top groupFrame) ([]syntax.Stmt, bool) {
	op, ok := groupRecvOp(s.X)
	if !ok {
		return nil, false
	}
	return fc.lowerGroupRecvExpr(s, op, top), true
}

func (fc *funcLower) groupChanFor(s *syntax.ForStmt, top groupFrame) ([]syntax.Stmt, bool) {
	rc, ok := s.Init.(*syntax.RangeClause)
	if !ok || !fc.groupRangeOverChan(rc) {
		return nil, false
	}
	return fc.lowerGroupRange(s, rc, top), true
}

func (fc *funcLower) lowerGroupRecvExpr(s *syntax.ExprStmt, op *syntax.Operation, top groupFrame) []syntax.Stmt {
	fl := fc.fl
	pos := s.Pos()
	scanForLiterals(fl, s)
	recvText := fl.textOf(span{start: fl.off(leftmostOf(op)), end: groupExprEnd(fl.src, fl.off(leftmostOf(op)))})
	native := at(&syntax.SelectStmt{
		Body: []*syntax.CommClause{
			at(&syntax.CommClause{Comm: exprStmtOf(pos, op)}, pos),
			fc.groupDoneClause(pos, top),
		},
	}, pos)
	start := fl.off(leftmostOf(op))
	end := stmtEnd(fl.src, start)
	fl.addT(start, end, cat(gen("select { case "), recvText, gen(": "+fc.groupDoneText(top)+" }")))
	return []syntax.Stmt{native}
}

func (fc *funcLower) lowerGroupSend(s *syntax.SendStmt, top groupFrame) []syntax.Stmt {
	fl := fc.fl
	pos := s.Pos()
	scanForLiterals(fl, s)
	chStart := fl.off(leftmostOf(s.Chan))
	chText := fl.textOf(span{start: chStart, end: fl.off(s.Pos())})
	vStart := fl.off(leftmostOf(s.Value))
	vText := fl.textOf(span{start: vStart, end: groupExprEnd(fl.src, vStart)})
	sendN := at(&syntax.SendStmt{Chan: s.Chan, Value: s.Value}, pos)
	native := at(&syntax.SelectStmt{
		Body: []*syntax.CommClause{
			at(&syntax.CommClause{Comm: sendN}, pos),
			fc.groupDoneClause(pos, top),
		},
	}, pos)
	start := fl.off(leftmostOf(s.Chan))
	end := groupExprEnd(fl.src, vStart)
	fl.addT(start, end, cat(gen("select { case "), chText, gen(" <- "), vText, gen(": "+fc.groupDoneText(top)+" }")))
	return []syntax.Stmt{native}
}

func (fc *funcLower) groupRangeOverChan(rc *syntax.RangeClause) bool {
	t := fc.fl.pkg.info.TypeOf(syntax.Unparen(rc.X))
	if t == nil {
		return false
	}
	_, ok := t.Underlying().(*types2.Chan)
	return ok
}

func (fc *funcLower) lowerGroupRange(s *syntax.ForStmt, rc *syntax.RangeClause, top groupFrame) []syntax.Stmt {
	fl := fc.fl
	pos := s.Pos()
	scanForLiterals(fl, rc.X)
	chStart := fl.off(leftmostOf(rc.X))
	chText := fl.srcUser(chStart, fl.off(s.Body.Pos())).trimRight(" \t\n")
	fl.needRT = true
	adapter := callOf(pos, selOf(pos, nameOf(pos, rtAlias), "Chan"), []syntax.Expr{
		nameOf(pos, top.gctx), rc.X,
	})
	rc.X = adapter
	scanForLiterals(fl, s.Cond)
	scanForLiterals(fl, s.Post)
	s.Body.List = fc.lowerList(s.Body.List)
	var lhsPart otext
	if rc.Lhs != nil {
		lhsStart := fl.off(leftmostOf(rc.Lhs))
		lhsPart = fl.srcUser(lhsStart, fl.off(rc.Pos())).trimRight(" \t\n:=")
		if rc.Def {
			lhsPart = cat(lhsPart, gen(" := "))
		} else {
			lhsPart = cat(lhsPart, gen(" = "))
		}
	}
	headStart := fl.off(pos)
	braceOff := fl.off(s.Body.Pos())
	fl.addT(headStart, braceOff, cat(gen("for "), lhsPart, gen("range "+rtAlias+".Chan("+top.gctx+", "), chText, gen(") ")))
	checkPos := s.Body.Rbrace
	checkN := ifOf(checkPos, nil,
		at(&syntax.Operation{
			Op: syntax.Neq,
			X:  callOf(checkPos, selOf(checkPos, nameOf(checkPos, top.gctx), "Err"), nil),
			Y:  nilOf(checkPos),
		}, checkPos),
		[]syntax.Stmt{
			returnOf(checkPos, fc.groupDoneResults(checkPos, top)),
		})
	end := fl.off(s.Body.Rbrace) + 1
	fl.addT(end, end, cat(gen("\n"), renderText([]syntax.Stmt{checkN}, nil)))
	return []syntax.Stmt{s, checkN}
}

func groupSelectHasDefault(s *syntax.SelectStmt) bool {
	for _, c := range s.Body {
		if c.Comm == nil {
			return true
		}
	}
	return false
}

func (fc *funcLower) groupAugmentSelect(s *syntax.SelectStmt) {
	top, ok := fc.groupChanTop()
	if !ok || groupSelectHasDefault(s) {
		return
	}
	s.Body = append(s.Body, fc.groupDoneClause(s.Rbrace, top))
	rbraceOff := fc.fl.off(s.Rbrace)
	fc.fl.add(rbraceOff, rbraceOff, "\n"+fc.groupDoneText(top)+"\n")
}

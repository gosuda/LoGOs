// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
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

func (fc *funcLower) groupTempName(prefix string) string {
	nm := fmt.Sprintf("_logos_%s%d", prefix, fc.tmpSeq)
	fc.tmpSeq++
	return nm
}

func (fc *funcLower) groupRecvSelect(pos syntax.Pos, top groupFrame, ch syntax.Expr, chText otext, elemT types2.Type, vals int) ([]syntax.Stmt, []otext, []string) {
	fl := fc.fl
	elemNative, elemSpelled, ok := fl.spellTypeBoth(elemT, pos)
	if !ok {
		fc.groupReject(pos, "from a channel whose element type cannot be written")
		return nil, nil, nil
	}
	tmp := fc.groupTempName("t")
	names := []string{tmp}
	native := []syntax.Stmt{typedVarDecl(pos, nameOf(pos, tmp), elemNative)}
	text := []otext{gen("var " + tmp + " " + elemSpelled)}
	caseLHS := tmp
	if vals == 2 {
		okName := fc.groupTempName("o")
		names = append(names, okName)
		native = append(native, typedVarDecl(pos, nameOf(pos, okName), nameOf(pos, "bool")))
		text = append(text, gen("var "+okName+" bool"))
		caseLHS += ", " + okName
	}
	recvN := assignOf(pos, tempNodes(pos, names), []syntax.Expr{at(&syntax.Operation{Op: syntax.Recv, X: ch}, pos)})
	native = append(native, at(&syntax.SelectStmt{
		Body: []*syntax.CommClause{at(&syntax.CommClause{Comm: recvN}, pos), fc.groupDoneClause(pos, top)},
	}, pos))
	text = append(text, cat(gen("select { case "+caseLHS+" = <-"), chText, gen(": "+fc.groupDoneText(top)+" }")))
	return native, text, names
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
		if groupStmtHasRecv(s) {
			return fc.groupHoistAssign(s, top), true
		}
	case *syntax.DeclStmt:
		if groupStmtHasRecv(s) {
			return fc.groupHoistDecl(s, top), true
		}
	case *syntax.ExprStmt:
		if groupStmtHasRecv(s) {
			return fc.groupHoistExprStmt(s, top), true
		}
	case *syntax.ReturnStmt:
		if groupStmtHasRecv(s) {
			return fc.groupHoistReturn(s, top), true
		}
	case *syntax.SendStmt:
		return fc.groupHoistSend(s, top), true
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

func (fc *funcLower) groupChanFor(s *syntax.ForStmt, top groupFrame) ([]syntax.Stmt, bool) {
	rc, ok := s.Init.(*syntax.RangeClause)
	if !ok || !fc.groupRangeOverChan(rc) {
		return nil, false
	}
	return fc.lowerGroupRange(s, rc, top), true
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

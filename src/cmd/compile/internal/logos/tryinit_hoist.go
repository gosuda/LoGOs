// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"slices"

	"cmd/compile/internal/syntax"
)

func (fl *fileLower) hoistTryInits() {
	for _, decl := range fl.file.DeclList {
		switch d := decl.(type) {
		case *syntax.FuncDecl:
			if d.Body != nil {
				newFuncLower(fl, d.Type, d.Body).hoistSwitchExprInits()
			}
		case *syntax.VarDecl:
			syntax.Inspect(d, func(n syntax.Node) bool {
				if lit, ok := n.(*syntax.FuncLit); ok {
					newFuncLower(fl, lit.Type, lit.Body).hoistSwitchExprInits()
					return false
				}
				return true
			})
		}
	}
}

func (fc *funcLower) hoistSwitchExprInits() {
	fc.body.List = fc.hoistInitList(fc.body.List, fc.fl.off(stmtStartPos(fc.body)))
}

func (fc *funcLower) hoistInitList(list []syntax.Stmt, hostOff int) []syntax.Stmt {
	var out []syntax.Stmt
	for _, s := range list {
		out = append(out, fc.hoistInitStmt(s, fc.fl.off(stmtStartPos(s)))...)
	}
	return out
}

func (fc *funcLower) hoistInitStmt(s syntax.Stmt, hostOff int) []syntax.Stmt {
	switch t := s.(type) {
	case *syntax.LabeledStmt:
		inner := fc.hoistInitStmt(t.Stmt, hostOff)
		if len(inner) == 1 && inner[0] == t.Stmt {
			return []syntax.Stmt{s}
		}
		return append(inner[:len(inner)-1:len(inner)-1], s)
	case *syntax.BlockStmt:
		t.List = fc.hoistInitList(t.List, hostOff)
		return []syntax.Stmt{s}
	case *syntax.IfStmt:
		t.Then.List = fc.hoistInitList(t.Then.List, hostOff)
		switch els := t.Else.(type) {
		case *syntax.BlockStmt:
			els.List = fc.hoistInitList(els.List, hostOff)
		case *syntax.IfStmt:
			fc.hoistInitStmt(els, hostOff)
		}
		return []syntax.Stmt{s}
	case *syntax.ForStmt:
		t.Body.List = fc.hoistInitList(t.Body.List, hostOff)
		return fc.hoistInitFields(s, hostOff, t.Init, t.Cond, t.Post)
	case *syntax.SwitchStmt:
		for _, c := range t.Body {
			c.Body = fc.hoistInitList(c.Body, hostOff)
		}
		return fc.hoistInitFields(s, hostOff, t.Init, t.Tag)
	case *syntax.SelectStmt:
		for _, c := range t.Body {
			c.Body = fc.hoistInitList(c.Body, hostOff)
		}
		return []syntax.Stmt{s}
	case *syntax.GroupStmt, *syntax.GroupChild:
		return []syntax.Stmt{s}
	default:
		return fc.hoistInitFields(s, hostOff)
	}
}

func (fc *funcLower) hoistInitFields(host syntax.Stmt, hostOff int, fields ...syntax.Node) []syntax.Stmt {
	var out []syntax.Stmt
	for _, e := range fields {
		out = append(out, fc.hoistSwitchExprsIn(e, hostOff)...)
	}
	if len(out) == 0 {
		out = fc.hoistSwitchExprsIn(host, hostOff)
	}
	if len(out) == 0 {
		return []syntax.Stmt{host}
	}
	return append(out, host)
}

func (fc *funcLower) hoistSwitchExprsIn(e syntax.Node, hostOff int) []syntax.Stmt {
	if e == nil {
		return nil
	}
	var out []syntax.Stmt
	syntax.Inspect(e, func(n syntax.Node) bool {
		switch t := n.(type) {
		case *syntax.FuncLit:
			newFuncLower(fc.fl, t.Type, t.Body).hoistSwitchExprInits()
			return false
		case *syntax.LambdaExpr, *syntax.GroupStmt, *syntax.GroupChild:
			return false
		case *syntax.SwitchExpr:
			out = append(out, fc.hoistSwitchExprInit(t, hostOff)...)
			return false
		}
		return true
	})
	return out
}

func (fc *funcLower) hoistSwitchExprInit(sx *syntax.SwitchExpr, hostOff int) []syntax.Stmt {
	var out []syntax.Stmt
	if sx.Init != nil {
		if native, text, spanStart, spanEnd, ok := fc.hoistTryInitCore(sx.Init); ok {
			fl := fc.fl
			scanForLiterals(fl, sx.Init)
			fl.moveRegion(span{spanStart, spanEnd}, "")
			if prefix := fc.groupJoinPrefix(sx.Pos(), hostOff); prefix != nil {
				native = append(prefix, native...)
			}
			fl.addT(hostOff, hostOff, cat(text.trimRight("\n"), gen("; ")))
			out = append(out, native...)
			sx.Init = nil
		}
	}
	out = append(out, fc.hoistSwitchExprsIn(sx.Tag, hostOff)...)
	return out
}

func (fc *funcLower) groupJoinPrefix(pos syntax.Pos, hostOff int) []syntax.Stmt {
	if top, ok := fc.groupTop(); ok && !top.child {
		return fc.groupPrefixJoin(pos, top, hostOff)
	}
	return nil
}

func (fc *funcLower) hoistTryInitCore(init syntax.SimpleStmt) (native []syntax.Stmt, text otext, spanStart, spanEnd int, ok bool) {
	switch s := init.(type) {
	case *syntax.AssignStmt:
		t, call, matched := tryAssignOf(s)
		if !matched {
			return
		}
		native, text, spanStart, spanEnd = fc.hoistTryAssignCore(s, t, call)
		return native, text, spanStart, spanEnd, true
	case *syntax.ExprStmt:
		t, call, matched := tryExprOf(s)
		if !matched {
			return
		}
		native, text, spanStart, spanEnd = fc.hoistTryExprCore(s, t, call)
		return native, text, spanStart, spanEnd, true
	}
	return nil, otext{}, 0, 0, false
}

func (fc *funcLower) hoistTryAssignCore(s *syntax.AssignStmt, t *syntax.TryExpr, call *syntax.CallExpr) ([]syntax.Stmt, otext, int, int) {
	fl := fc.fl
	src := fl.src
	tpos := t.Pos()
	stmtPos := leftmostOf(syntax.UnpackListExpr(s.Lhs)[0])
	spanStart := fl.off(stmtPos)
	opOff := fl.off(s.Pos())
	qOff := fl.off(tpos)
	spanEnd := skipCloseParens(src, qOff+1)
	lhs := syntax.UnpackListExpr(s.Lhs)
	lhsText := fl.srcUser(spanStart, opOff).trim()
	callStart := fl.off(leftmostOf(call.Fun))
	errSpelling := fc.nextErr()
	siteSpelling := fl.newSite(tpos, tpos.Line())
	exitNative, exitText, exitSubs := fc.exitPair(tpos, errSpelling, siteSpelling)
	tw := &twin{}
	tw.absorb(exitSubs)
	lhsMark := tw.mark(stmtPos, lhsText)
	callMark := tw.mark(tpos, otext{s: fl.copyRegion(span{callStart, qOff})})
	var native []syntax.Stmt
	if s.Op == syntax.Def {
		native = []syntax.Stmt{
			errDefineOf(stmtPos, lhs, call, nameOf(tpos, errSpelling)),
			errCheckOf(stmtPos, nameOf(tpos, errSpelling), exitNative),
		}
		text := renderText([]syntax.Stmt{
			errDefineOf(stmtPos, []syntax.Expr{lhsMark}, callMark, nameOf(tpos, errSpelling)),
			errCheckOf(stmtPos, nameOf(tpos, errSpelling), exitText),
		}, tw.subs)
		return native, text, spanStart, spanEnd
	}
	withErr := append(slices.Clone(lhs), nameOf(tpos, errSpelling))
	native = []syntax.Stmt{blockOf(stmtPos, []syntax.Stmt{
		errorVarDecl(stmtPos, nameOf(tpos, errSpelling)),
		assignOf(stmtPos, withErr, []syntax.Expr{call}),
		errCheckOf(stmtPos, nameOf(tpos, errSpelling), exitNative),
	})}
	withErrT := []syntax.Expr{lhsMark, nameOf(tpos, errSpelling)}
	text := renderText([]syntax.Stmt{blockOf(stmtPos, []syntax.Stmt{
		errorVarDecl(stmtPos, nameOf(tpos, errSpelling)),
		assignOf(stmtPos, withErrT, []syntax.Expr{callMark}),
		errCheckOf(stmtPos, nameOf(tpos, errSpelling), exitText),
	})}, tw.subs)
	return native, text, spanStart, spanEnd
}

func (fc *funcLower) hoistTryExprCore(s *syntax.ExprStmt, t *syntax.TryExpr, call *syntax.CallExpr) ([]syntax.Stmt, otext, int, int) {
	fl := fc.fl
	tpos := t.Pos()
	stmtPos := leftmostOf(call.Fun)
	spanStart := spanStartBack(fl.src, fl.off(stmtPos))
	qOff := fl.off(tpos)
	spanEnd := skipCloseParens(fl.src, qOff+1)
	errSpelling := fc.nextErr()
	siteSpelling := fl.newSite(tpos, tpos.Line())
	exitNative, exitText, exitSubs := fc.exitPair(tpos, errSpelling, siteSpelling)
	tw := &twin{}
	tw.absorb(exitSubs)
	callMark := tw.mark(tpos, otext{s: fl.copyRegion(span{fl.off(stmtPos), qOff})})
	n := fc.callResults(call)
	var native []syntax.Stmt
	var text otext
	if n == 0 {
		native = []syntax.Stmt{errCheckInitOf(stmtPos, nil, call, nameOf(tpos, errSpelling), exitNative)}
		text = renderText([]syntax.Stmt{errCheckInitOf(stmtPos, nil, callMark, nameOf(tpos, errSpelling), exitText)}, tw.subs)
	} else {
		native = []syntax.Stmt{
			errDefineOf(stmtPos, blankNames(stmtPos, n), call, nameOf(tpos, errSpelling)),
			errCheckOf(stmtPos, nameOf(tpos, errSpelling), exitNative),
		}
		text = renderText([]syntax.Stmt{
			errDefineOf(stmtPos, blankNames(stmtPos, n), callMark, nameOf(tpos, errSpelling)),
			errCheckOf(stmtPos, nameOf(tpos, errSpelling), exitText),
		}, tw.subs)
	}
	return native, text, spanStart, spanEnd
}

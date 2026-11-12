// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"go/token"
	"slices"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

type twin struct {
	subs []textSub
	seq  int
}

func (tw *twin) mark(pos syntax.Pos, text otext) *syntax.Name {
	return markerText(&tw.subs, &tw.seq, pos, text)
}

func (tw *twin) absorb(subs []textSub) {
	tw.subs = append(tw.subs, subs...)
	tw.seq += len(subs)
}

func tryAssignOf(s *syntax.AssignStmt) (*syntax.TryExpr, *syntax.CallExpr, bool) {
	if s.Op != syntax.Def && s.Op != 0 {
		return nil, nil, false
	}
	t, ok := syntax.Unparen(s.Rhs).(*syntax.TryExpr)
	if !ok {
		return nil, nil, false
	}
	call, ok := syntax.Unparen(t.X).(*syntax.CallExpr)
	if !ok {
		return nil, nil, false
	}
	return t, call, true
}

func tryExprOf(s *syntax.ExprStmt) (*syntax.TryExpr, *syntax.CallExpr, bool) {
	t, ok := syntax.Unparen(s.X).(*syntax.TryExpr)
	if !ok {
		return nil, nil, false
	}
	call, ok := syntax.Unparen(t.X).(*syntax.CallExpr)
	if !ok {
		return nil, nil, false
	}
	return t, call, true
}

func tryCallOf(s *syntax.CallStmt) (*syntax.TryExpr, *syntax.CallExpr, bool) {
	t, ok := syntax.Unparen(s.Call).(*syntax.TryExpr)
	if !ok {
		return nil, nil, false
	}
	call, ok := syntax.Unparen(t.X).(*syntax.CallExpr)
	if !ok {
		return nil, nil, false
	}
	return t, call, true
}

func (fc *funcLower) callResults(call *syntax.CallExpr) int {
	typ := fc.fl.pkg.info.TypeOf(call)
	if tup, ok := typ.(*types2.Tuple); ok {
		return tup.Len() - 1
	}
	return 0
}

func (fc *funcLower) nextErr() string {
	spelling := fmt.Sprintf("_logos_e%d", fc.errSeq)
	fc.errSeq++
	return spelling
}

func (fc *funcLower) nextFun() string {
	spelling := fmt.Sprintf("_logos_f%d", fc.funSeq)
	fc.funSeq++
	return spelling
}

func (fc *funcLower) nextArg() string {
	spelling := fmt.Sprintf("_logos_a%d", fc.argSeq)
	fc.argSeq++
	return spelling
}

func (fc *funcLower) zerosAt(pos syntax.Pos) []syntax.Expr {
	var out []syntax.Expr
	for i := range fc.specs {
		sp := &fc.specs[i]
		if sp.isErr {
			continue
		}
		out = append(out, fc.fl.zeroValue(sp.typ, pos))
	}
	return out
}

func (fc *funcLower) exitPair(pos syntax.Pos, errSpelling, siteSpelling string) (native, text []syntax.Stmt, subs []textSub) {
	return fc.exitPairAt(pos, pos, errSpelling, siteSpelling)
}

func (fc *funcLower) exitPairAt(checkPos, pos syntax.Pos, errSpelling, siteSpelling string) (native, text []syntax.Stmt, subs []textSub) {
	native, text, subs = fc.scopedExitPair(pos, errSpelling, siteSpelling)
	top, ok := fc.groupTop()
	if !ok || top.child {
		return native, text, subs
	}
	joinNative, joinText, joinSubs := fc.groupJoinCheck(checkPos, top)
	native = append(joinNative, native...)
	text = append(joinText, text...)
	return native, text, append(subs, joinSubs...)
}

func (fc *funcLower) scopedExitPair(pos syntax.Pos, errSpelling, siteSpelling string) (native, text []syntax.Stmt, subs []textSub) {
	native = buildExit(exitSpec{
		pos:   pos,
		err:   nameOf(pos, errSpelling),
		site:  nameOf(pos, siteSpelling),
		zeros: fc.zerosAt(pos),
	})
	text = buildExit(exitSpec{
		pos:   pos,
		err:   nameOf(pos, errSpelling),
		site:  nameOf(pos, siteSpelling),
		zeros: fc.zerosAt(pos),
	})
	if sn, st, ssubs := fc.scopeExitStanza(pos); sn != nil {
		native = append(sn, native...)
		text = append(st, text...)
		subs = ssubs
	}
	return native, text, subs
}

func (fc *funcLower) lowerList(list []syntax.Stmt) []syntax.Stmt {
	var out []syntax.Stmt
	for _, s := range list {
		out = append(out, fc.lowerStmt(s)...)
	}
	return out
}

func (fc *funcLower) lowerStmt(s syntax.Stmt) []syntax.Stmt {
	if cs, ok := s.(*syntax.CallStmt); ok {
		if out, ok := fc.fl.optDeferGoFinish(cs); ok {
			if gout, ok := fc.lowerGroupHook(out[len(out)-1]); ok {
				return append(out[:len(out)-1], gout...)
			}
			return out
		}
	}
	if out, ok := fc.lowerGroupHook(s); ok {
		return out
	}
	switch s := s.(type) {
	case *syntax.AssignStmt,
		*syntax.ExprStmt,
		*syntax.CallStmt,
		*syntax.ErrdeferStmt,
		*syntax.ReturnStmt:
		return fc.lowerSimple(s)
	default:
		return fc.lowerCompound(s)
	}
}

func (fc *funcLower) lowerSimple(s syntax.Stmt) []syntax.Stmt {
	switch s := s.(type) {
	case *syntax.AssignStmt:
		if t, call, ok := tryAssignOf(s); ok {
			return fc.lowerTryAssign(s, t, call)
		}
		scanForLiterals(fc.fl, s)
		return []syntax.Stmt{s}
	case *syntax.ExprStmt:
		if t, call, ok := tryExprOf(s); ok {
			return fc.lowerTryExpr(s, t, call)
		}
		scanForLiterals(fc.fl, s)
		return []syntax.Stmt{s}
	case *syntax.CallStmt:
		if s.Tok == syntax.Defer {
			if t, call, ok := tryCallOf(s); ok {
				return fc.lowerDeferTry(s, t, call)
			}
		}
		scanForLiterals(fc.fl, s)
		return []syntax.Stmt{s}
	case *syntax.ErrdeferStmt:
		return fc.lowerErrdefer(s)
	case *syntax.ReturnStmt:
		if fc.hasErrdefer {
			return fc.lowerReturn(s)
		}
		scanForLiterals(fc.fl, s)
		return []syntax.Stmt{s}
	default:
		return []syntax.Stmt{s}
	}
}

func (fc *funcLower) lowerCompound(s syntax.Stmt) []syntax.Stmt {
	switch s := s.(type) {
	case *syntax.BlockStmt:
		s.List = fc.lowerList(s.List)
		if fc.fl.anchorBlocks[s] {
			anchorScopes(s.List, s.Pos())
		}
		return []syntax.Stmt{s}
	case *syntax.IfStmt:
		return fc.lowerIf(s)
	case *syntax.ForStmt:
		return fc.lowerFor(s)
	case *syntax.SwitchStmt:
		return fc.lowerSwitch(s)
	case *syntax.SelectStmt:
		for _, clause := range s.Body {
			scanForLiterals(fc.fl, clause.Comm)
			clause.Body = fc.lowerList(clause.Body)
		}
		fc.groupAugmentSelect(s)
		return []syntax.Stmt{s}
	case *syntax.ConstSwitchStmt:
		return fc.lowerConstSwitchStmt(s)
	default:
		return fc.lowerLeaf(s)
	}
}

func (fc *funcLower) lowerLeaf(s syntax.Stmt) []syntax.Stmt {
	switch s := s.(type) {
	case *syntax.LabeledStmt:
		pos := s.Stmt.Pos()
		inner := fc.lowerStmt(s.Stmt)
		if len(inner) == 0 {
			empty := &syntax.EmptyStmt{}
			empty.SetPos(pos)
			s.Stmt = empty
			off := fc.fl.off(pos)
			fc.fl.add(off, off, ";")
			return []syntax.Stmt{s}
		}
		s.Stmt = inner[0]
		return append([]syntax.Stmt{s}, inner[1:]...)
	case *syntax.DeclStmt:
		scanForLiterals(fc.fl, s)
		return []syntax.Stmt{s}
	case *syntax.SendStmt:
		scanForLiterals(fc.fl, s)
		return []syntax.Stmt{s}
	case *syntax.RangeClause:
		scanForLiterals(fc.fl, s)
		return []syntax.Stmt{s}
	default:
		return []syntax.Stmt{s}
	}
}

func (fc *funcLower) lowerIf(s *syntax.IfStmt) []syntax.Stmt {
	scanForLiterals(fc.fl, s.Cond)
	if wrapped := fc.wrapInitStmt(s, s.Init, "if"); wrapped != nil {
		s.Then.List = fc.lowerList(s.Then.List)
		fc.lowerElse(s)
		return []syntax.Stmt{wrapped}
	}
	scanForLiterals(fc.fl, s.Init)
	s.Then.List = fc.lowerList(s.Then.List)
	fc.lowerElse(s)
	return []syntax.Stmt{s}
}

func (fc *funcLower) lowerElse(s *syntax.IfStmt) {
	switch els := s.Else.(type) {
	case nil:
	case *syntax.BlockStmt:
		els.List = fc.lowerList(els.List)
	case *syntax.IfStmt:
		s.Else = fc.lowerIf(els)[0]
	}
}

func (fc *funcLower) lowerFor(s *syntax.ForStmt) []syntax.Stmt {
	decl := fc.wrapForPost(s)
	switch init := s.Init.(type) {
	case nil:
	case *syntax.RangeClause:
		scanForLiterals(fc.fl, init)
	default:
		if wrapped := fc.wrapInitStmt(s, init, "for"); wrapped != nil {
			fc.finishFor(s)
			return fc.withDecl(decl, wrapped)
		}
		scanForLiterals(fc.fl, init)
	}
	fc.finishFor(s)
	return fc.withDecl(decl, s)
}

func (fc *funcLower) withDecl(decl syntax.Stmt, s syntax.Stmt) []syntax.Stmt {
	if decl == nil {
		return []syntax.Stmt{s}
	}
	return []syntax.Stmt{decl, s}
}

func (fc *funcLower) finishFor(s *syntax.ForStmt) {
	scanForLiterals(fc.fl, s.Cond)
	scanForLiterals(fc.fl, s.Post)
	s.Body.List = fc.lowerList(s.Body.List)
}

type forPost struct {
	fc        *funcLower
	s         *syntax.ForStmt
	errPos    syntax.Pos
	postPos   syntax.Pos
	postEnd   syntax.Pos
	errSpell  string
	spanStart int
	spanEnd   int
}

func (fc *funcLower) wrapForPost(s *syntax.ForStmt) syntax.Stmt {
	post := s.Post
	if post == nil {
		return nil
	}
	var t *syntax.TryExpr
	var call *syntax.CallExpr
	switch p := post.(type) {
	case *syntax.AssignStmt:
		if p.Op != 0 {
			return nil
		}
		t, call, _ = tryAssignOf(p)
	case *syntax.ExprStmt:
		t, call, _ = tryExprOf(p)
	}
	if t == nil {
		return nil
	}
	p := &forPost{
		fc:       fc,
		s:        s,
		errPos:   s.Pos(),
		postPos:  post.Pos(),
		postEnd:  syntax.EndPos(post),
		errSpell: fc.nextErr(),
	}
	p.spanEnd = skipCloseParens(fc.fl.src, fc.fl.off(t.Pos())+1)
	switch post := post.(type) {
	case *syntax.AssignStmt:
		p.buildPostAssign(post, t, call)
	case *syntax.ExprStmt:
		p.buildPostExpr(post, t, call)
	}
	return p.emit()
}

func (p *forPost) err() *syntax.Name {
	return nameOf(p.errPos, p.errSpell)
}

func (p *forPost) closureExit(t *syntax.TryExpr, innerSpell string) []syntax.Stmt {
	site := nameOf(t.Pos(), p.fc.fl.newSite(t.Pos(), t.Pos().Line()))
	return []syntax.Stmt{
		exprStmtOf(t.Pos(), callOf(t.Pos(), rtSel(t.Pos(), "TraceErr"), []syntax.Expr{
			nameOf(t.Pos(), innerSpell),
			at(&syntax.Operation{Op: syntax.And, X: site}, t.Pos()),
		})),
		assignOf(t.Pos(), []syntax.Expr{p.err()}, []syntax.Expr{nameOf(t.Pos(), innerSpell)}),
		returnOf(t.Pos(), nil),
	}
}

func (p *forPost) buildPostAssign(s *syntax.AssignStmt, t *syntax.TryExpr, call *syntax.CallExpr) {
	fl := p.fc.fl
	tpos := t.Pos()
	innerErr := p.fc.nextErr()
	exit := p.closureExit(t, innerErr)
	lhs := syntax.UnpackListExpr(s.Lhs)
	stmtPos := leftmostOf(lhs[0])
	qOff := fl.off(tpos)
	p.spanStart = fl.off(stmtPos)
	tw := &twin{}
	lhsMark := tw.mark(stmtPos, fl.srcUser(p.spanStart, fl.off(s.Pos())).trim())
	callMark := tw.mark(tpos, fl.lazyText(span{start: fl.off(leftmostOf(call.Fun)), end: qOff}))
	withErr := append(slices.Clone(lhs), nameOf(stmtPos, innerErr))
	native := []syntax.Stmt{blockOf(stmtPos, []syntax.Stmt{
		errorVarDecl(stmtPos, nameOf(stmtPos, innerErr)),
		assignOf(stmtPos, withErr, []syntax.Expr{call}),
		errCheckOf(stmtPos, nameOf(stmtPos, innerErr), exit),
	})}
	withErrT := []syntax.Expr{lhsMark, nameOf(tpos, innerErr)}
	text := renderText([]syntax.Stmt{blockOf(stmtPos, []syntax.Stmt{
		errorVarDecl(stmtPos, nameOf(stmtPos, innerErr)),
		assignOf(stmtPos, withErrT, []syntax.Expr{callMark}),
		errCheckOf(stmtPos, nameOf(tpos, innerErr), exit),
	})}, tw.subs)
	p.emitText(native, text)
}

func (p *forPost) buildPostExpr(s *syntax.ExprStmt, t *syntax.TryExpr, call *syntax.CallExpr) {
	fl := p.fc.fl
	tpos := t.Pos()
	innerErr := p.fc.nextErr()
	exit := p.closureExit(t, innerErr)
	stmtPos := leftmostOf(call.Fun)
	qOff := fl.off(tpos)
	p.spanStart = spanStartBack(fl.src, fl.off(stmtPos))
	tw := &twin{}
	callMark := tw.mark(tpos, fl.lazyText(span{start: fl.off(stmtPos), end: qOff}))
	n := p.fc.callResults(call)
	var native []syntax.Stmt
	var text otext
	if n == 0 {
		native = []syntax.Stmt{errCheckInitOf(stmtPos, nil, call, nameOf(stmtPos, innerErr), exit)}
		text = renderText([]syntax.Stmt{errCheckInitOf(stmtPos, nil, callMark, nameOf(tpos, innerErr), exit)}, tw.subs)
	} else {
		native = []syntax.Stmt{
			errDefineOf(stmtPos, blankNames(stmtPos, n), call, nameOf(stmtPos, innerErr)),
			errCheckOf(stmtPos, nameOf(stmtPos, innerErr), exit),
		}
		text = renderText([]syntax.Stmt{
			errDefineOf(stmtPos, blankNames(stmtPos, n), callMark, nameOf(tpos, innerErr)),
			errCheckOf(stmtPos, nameOf(tpos, innerErr), exit),
		}, tw.subs)
	}
	p.emitText(native, text)
}

func (p *forPost) emitText(native []syntax.Stmt, text otext) {
	fl := p.fc.fl
	lit := at(&syntax.FuncLit{
		Type: at(&syntax.FuncType{}, p.postPos),
		Body: at(&syntax.BlockStmt{List: native, Rbrace: p.postEnd}, p.postPos),
	}, p.postPos)
	p.s.Post = exprStmtOf(p.postPos, callOf(p.postPos, lit, nil))
	fl.addT(p.spanStart, p.spanEnd, cat(gen("func() { "), text, gen(" }()")))
}

func (p *forPost) emit() syntax.Stmt {
	fl := p.fc.fl
	decl := typedVarDecl(p.errPos, p.err(), nameOf(p.errPos, "error"))
	kwOff := fl.off(p.s.Pos())
	fl.addT(kwOff, kwOff, cat(renderText([]syntax.Stmt{decl}, nil).trimRight("\n"), gen("; ")))
	if p.s.Cond != nil {
		hdr := fl.forHeaderOf(p.s)
		fl.add(hdr.cond.start, hdr.cond.start, p.errSpell+" != nil || (")
		fl.add(hdr.cond.end, hdr.cond.end, ")")
		p.s.Cond = at(&syntax.Operation{
			Op: syntax.OrOr,
			X:  notNil(p.errPos, p.err()),
			Y:  at(&syntax.ParenExpr{X: p.s.Cond}, p.errPos),
		}, p.errPos)
	}
	checkPos := p.s.Body.Pos()
	checkSite := fl.newSite(checkPos, checkPos.Line())
	checkNative, checkText, _ := p.fc.exitPair(checkPos, p.errSpell, checkSite)
	check := errCheckOf(checkPos, p.err(), checkNative)
	checkTextOf := cat(gen("if "+p.errSpell+" != nil { "), renderText(checkText, nil), gen(" }"))
	open := fl.off(checkPos) + 1
	fl.addT(open, open, cat(gen(" "), checkTextOf, gen("; ")))
	p.s.Body.List = append([]syntax.Stmt{check}, p.s.Body.List...)
	return decl
}

func (fc *funcLower) lowerSwitch(s *syntax.SwitchStmt) []syntax.Stmt {
	if wrapped := fc.wrapInitStmt(s, s.Init, "switch"); wrapped != nil {
		scanForLiterals(fc.fl, s.Tag)
		fc.lowerCases(s.Body)
		return []syntax.Stmt{wrapped}
	}
	scanForLiterals(fc.fl, s.Init)
	scanForLiterals(fc.fl, s.Tag)
	fc.lowerCases(s.Body)
	return []syntax.Stmt{s}
}

func (fc *funcLower) lowerCases(body []*syntax.CaseClause) {
	for _, clause := range body {
		scanForLiterals(fc.fl, clause.Cases)
		clause.Body = fc.lowerList(clause.Body)
	}
}

type initCore func() (native []syntax.Stmt, text otext, spanStart, spanEnd int)

func (fc *funcLower) initCoreOf(init syntax.SimpleStmt) (initCore, bool) {
	if s, isAssign := init.(*syntax.AssignStmt); isAssign {
		if t, call, matched := tryAssignOf(s); matched {
			return func() ([]syntax.Stmt, otext, int, int) {
				return fc.tryAssignCore(s, t, call)
			}, true
		}
		return nil, false
	}
	if s, isExpr := init.(*syntax.ExprStmt); isExpr {
		if t, call, matched := tryExprOf(s); matched {
			return func() ([]syntax.Stmt, otext, int, int) {
				return fc.tryExprCore(s, t, call)
			}, true
		}
	}
	return nil, false
}

func (fc *funcLower) wrapInitStmt(host syntax.Stmt, init syntax.SimpleStmt, kw string) *syntax.BlockStmt {
	core, ok := fc.initCoreOf(init)
	if !ok {
		return nil
	}
	fl := fc.fl
	scanForLiterals(fl, init)
	native, text, _, spanEnd := core()
	anchorScopes(native, host.Pos())
	kwOff := fl.off(host.Pos())
	semi := skipSemi(fl.src, spanEnd)
	clearInit(host)
	end := blockEndOff(fl, host)
	fl.addT(kwOff, semi, cat(gen("{ "), text, gen(kw+" ")))
	fl.add(end, end, "} ")
	wrapper := blockOf(host.Pos(), append(native, host))
	wrapper.Rbrace = hostRbrace(host)
	return wrapper
}

func anchorScopes(list []syntax.Stmt, pos syntax.Pos) {
	for _, s := range list {
		switch s := s.(type) {
		case *syntax.BlockStmt:
			s.SetPos(pos)
			s.Rbrace = pos
			anchorScopes(s.List, pos)
		case *syntax.IfStmt:
			s.SetPos(pos)
			anchorScopes([]syntax.Stmt{s.Then}, pos)
			if s.Else != nil {
				anchorScopes([]syntax.Stmt{s.Else}, pos)
			}
		}
	}
}

func hostRbrace(host syntax.Stmt) syntax.Pos {
	switch host := host.(type) {
	case *syntax.IfStmt:
		for {
			switch els := host.Else.(type) {
			case *syntax.BlockStmt:
				return els.Rbrace
			case *syntax.IfStmt:
				host = els
			default:
				return host.Then.Rbrace
			}
		}
	case *syntax.ForStmt:
		return host.Body.Rbrace
	case *syntax.SwitchStmt:
		return host.Rbrace
	}
	return host.Pos()
}

func clearInit(host syntax.Stmt) {
	switch host := host.(type) {
	case *syntax.IfStmt:
		host.Init = nil
	case *syntax.ForStmt:
		host.Init = nil
	case *syntax.SwitchStmt:
		host.Init = nil
	}
}

func blockEndOff(fl *fileLower, host syntax.Stmt) int {
	switch host := host.(type) {
	case *syntax.IfStmt:
		return ifEndOff(fl, host)
	case *syntax.ForStmt:
		return fl.off(host.Body.Rbrace) + 1
	case *syntax.SwitchStmt:
		return fl.off(host.Rbrace) + 1
	default:
		return fl.orig
	}
}

func ifEndOff(fl *fileLower, s *syntax.IfStmt) int {
	for {
		switch els := s.Else.(type) {
		case nil:
			return fl.off(s.Then.Rbrace) + 1
		case *syntax.BlockStmt:
			return fl.off(els.Rbrace) + 1
		case *syntax.IfStmt:
			s = els
		default:
			return fl.orig
		}
	}
}

func skipSemi(src []byte, from int) int {
	end := from
	scanSegment(src, from, len(src), func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		if t.kind == token.SEMICOLON {
			end = t.end
			return false
		}
		return true
	})
	return end
}

func (fc *funcLower) lowerTryAssign(s *syntax.AssignStmt, t *syntax.TryExpr, call *syntax.CallExpr) []syntax.Stmt {
	fl := fc.fl
	native, text, spanStart, spanEnd := fc.tryAssignCore(s, t, call)
	fl.addT(spanStart, spanEnd, text)
	return native
}

func (fc *funcLower) tryAssignCore(s *syntax.AssignStmt, t *syntax.TryExpr, call *syntax.CallExpr) ([]syntax.Stmt, otext, int, int) {
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
	callText := fl.lazyText(span{start: fl.off(leftmostOf(call.Fun)), end: qOff})
	errSpelling := fc.nextErr()
	siteSpelling := fl.newSite(tpos, tpos.Line())
	exitNative, exitText, exitSubs := fc.exitPairAt(stmtPos, tpos, errSpelling, siteSpelling)
	var native []syntax.Stmt
	tw := &twin{}
	tw.absorb(exitSubs)
	lhsMark := tw.mark(stmtPos, lhsText)
	callMark := tw.mark(tpos, callText)
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
	errDecl := nameOf(tpos, errSpelling)
	errUse := nameOf(tpos, errSpelling)
	errChk := nameOf(tpos, errSpelling)
	withErr := append(slices.Clone(lhs), errUse)
	native = []syntax.Stmt{blockOf(stmtPos, []syntax.Stmt{
		errorVarDecl(stmtPos, errDecl),
		assignOf(stmtPos, withErr, []syntax.Expr{call}),
		errCheckOf(stmtPos, errChk, exitNative),
	})}
	withErrT := []syntax.Expr{lhsMark, nameOf(tpos, errSpelling)}
	text := renderText([]syntax.Stmt{blockOf(stmtPos, []syntax.Stmt{
		errorVarDecl(stmtPos, nameOf(tpos, errSpelling)),
		assignOf(stmtPos, withErrT, []syntax.Expr{callMark}),
		errCheckOf(stmtPos, nameOf(tpos, errSpelling), exitText),
	})}, tw.subs)
	return native, text, spanStart, spanEnd
}

func (fc *funcLower) lowerTryExpr(s *syntax.ExprStmt, t *syntax.TryExpr, call *syntax.CallExpr) []syntax.Stmt {
	fl := fc.fl
	native, text, spanStart, spanEnd := fc.tryExprCore(s, t, call)
	fl.addT(spanStart, spanEnd, text)
	return native
}

func (fc *funcLower) tryExprCore(s *syntax.ExprStmt, t *syntax.TryExpr, call *syntax.CallExpr) ([]syntax.Stmt, otext, int, int) {
	fl := fc.fl
	tpos := t.Pos()
	stmtPos := leftmostOf(call.Fun)
	spanStart := spanStartBack(fl.src, fl.off(stmtPos))
	qOff := fl.off(tpos)
	spanEnd := skipCloseParens(fl.src, qOff+1)
	callText := fl.lazyText(span{start: fl.off(stmtPos), end: qOff})
	errSpelling := fc.nextErr()
	siteSpelling := fl.newSite(tpos, tpos.Line())
	exitNative, exitText, exitSubs := fc.exitPairAt(stmtPos, tpos, errSpelling, siteSpelling)
	tw := &twin{}
	tw.absorb(exitSubs)
	callMark := tw.mark(tpos, callText)
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

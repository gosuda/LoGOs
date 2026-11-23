// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
	"fmt"
	"go/constant"
	"slices"
	"strconv"
	"strings"
)

type chainHoist struct {
	stmts []syntax.Stmt
	text  otext
}

type chainOp struct {
	method    string
	call      *syntax.CallExpr
	arg       syntax.Expr
	argSpan   span
	bodySpan  span
	lam       *syntax.LambdaExpr
	savedBody syntax.Node
	isTry     bool
	isBlk     bool
	forms     []resultForm
	resultT   types2.Type
}

type resultForm int

const (
	formBorrowed resultForm = iota
	formParam
	formCall
	formMade
)

func (fl *fileLower) lowerChains() {
	if fl.loweredChains == nil {
		fl.loweredChains = make(map[*syntax.CallExpr]bool)
	}
	fl.transparentWrappers()
	for _, decl := range fl.file.DeclList {
		switch decl := decl.(type) {
		case *syntax.FuncDecl:
			if decl.Body != nil {
				decl.Body.List = fl.lowerChainList(decl.Body.List, fl.resultSpecs(decl.Type, fl.cfFuncSig(decl)))
			}
		case *syntax.VarDecl:
			fl.lowerChainPkgDecl(decl)
		}
	}
	fl.flushOuterFirst()
}

func (fl *fileLower) transparentWrappers() {
	if fl.transparent == nil {
		fl.transparent = make(map[*syntax.FuncLit]bool)
	}
	for out := range fl.wrapSpan {
		call, ok := out.(*syntax.CallExpr)
		if !ok {
			continue
		}
		if lit, ok := syntax.Unparen(call.Fun).(*syntax.FuncLit); ok {
			fl.transparent[lit] = true
		}
	}
}

func (fl *fileLower) lowerChainList(list []syntax.Stmt, enc []resultSpec) []syntax.Stmt {
	var out []syntax.Stmt
	for _, s := range list {
		if repl, ok := fl.lowerParRange(s, enc); ok {
			out = append(out, repl...)
			continue
		}
		repl, ok := fl.lowerChainSpecial(s, enc)
		if !ok {
			repl = fl.lowerChainPlain(s, enc)
		}
		out = append(out, fl.scopeHoists(s, repl)...)
	}
	return out
}

func (fl *fileLower) lowerChainPlain(s syntax.Stmt, enc []resultSpec) []syntax.Stmt {
	out := fl.lowerChainStmt(s, enc)
	out = append(out, s)
	fl.lowerChainChildren(s, enc)
	return out
}

func hoistScopable(s syntax.Stmt) bool {
	switch s := s.(type) {
	case *syntax.LabeledStmt:
		_, again := s.Stmt.(*syntax.LabeledStmt)
		return !again && hoistScopable(s.Stmt)
	case *syntax.AssignStmt:
		return s.Op != syntax.Def
	case *syntax.ExprStmt, *syntax.SendStmt, *syntax.ReturnStmt, *syntax.IfStmt, *syntax.ForStmt, *syntax.SwitchStmt, *syntax.SelectStmt:
		return true
	}
	return false
}

func (fl *fileLower) hoistedStmtEnd(s syntax.Stmt) int {
	for {
		lab, ok := s.(*syntax.LabeledStmt)
		if !ok {
			break
		}
		s = lab.Stmt
	}
	switch s.(type) {
	case *syntax.IfStmt, *syntax.ForStmt, *syntax.SwitchStmt, *syntax.SelectStmt:
		return fl.off(syntax.EndPos(s)) + 1
	}
	return stmtEnd(fl.src, fl.off(syntax.StartPos(s)))
}

func (fl *fileLower) scopeHoists(s syntax.Stmt, repl []syntax.Stmt) []syntax.Stmt {
	if len(repl) < 2 || repl[len(repl)-1] != s || !hoistScopable(s) {
		return repl
	}
	if lab, ok := s.(*syntax.LabeledStmt); ok && fl.gotoTarget(lab) {
		return fl.scopeLabeledHoists(lab, repl)
	}
	fl.addFirst(fl.stmtStartOf(s), "{ ")
	end := fl.hoistedStmtEnd(s)
	fl.add(end, end, " }")
	anchorScopes(repl[:len(repl)-1], s.Pos())
	wrapper := blockOf(s.Pos(), repl)
	wrapper.Rbrace = syntax.EndPos(s)
	return []syntax.Stmt{wrapper}
}

func (fl *fileLower) scopeLabeledHoists(lab *syntax.LabeledStmt, repl []syntax.Stmt) []syntax.Stmt {
	inner := lab.Stmt
	start := fl.stmtStartOf(lab)
	innerStart := fl.off(syntax.StartPos(inner))
	fl.addFirst(start, lab.Label.Value+": { ")
	end := fl.hoistedStmtEnd(inner)
	fl.add(end, end, " }")
	body := inner
	name := "_logos_in_" + lab.Label.Value
	if fl.renameBranchLabels(inner, lab.Label.Value, name) > 0 {
		fl.add(start, innerStart, name+": ")
		body = at(&syntax.LabeledStmt{Label: nameOf(lab.Pos(), name), Stmt: inner}, lab.Pos())
	} else {
		fl.add(start, innerStart, "")
	}
	anchorScopes(repl[:len(repl)-1], inner.Pos())
	wrapper := blockOf(inner.Pos(), append(slices.Clone(repl[:len(repl)-1]), body))
	wrapper.Rbrace = syntax.EndPos(inner)
	lab.Stmt = wrapper
	return []syntax.Stmt{lab}
}

func (fl *fileLower) renameBranchLabels(s syntax.Stmt, from, to string) int {
	renamed := 0
	syntax.Inspect(s, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit, *syntax.LambdaExpr:
			return false
		case *syntax.BranchStmt:
			if n.Label != nil && n.Label.Value == from && n.Tok != syntax.Goto {
				off := fl.off(n.Label.Pos())
				fl.add(off, off+len(from), to)
				n.Label = nameOf(n.Label.Pos(), to)
				renamed++
			}
		}
		return true
	})
	return renamed
}

func (fl *fileLower) gotoTarget(lab *syntax.LabeledStmt) bool {
	if fl.gotoTargets == nil {
		fl.gotoTargets = make(map[*syntax.LabeledStmt]bool)
		for _, decl := range fl.file.DeclList {
			collectGotoTargets(decl, fl.gotoTargets)
		}
	}
	return fl.gotoTargets[lab]
}

func collectGotoTargets(root syntax.Node, out map[*syntax.LabeledStmt]bool) {
	gotos := make(map[string]bool)
	var labeled []*syntax.LabeledStmt
	syntax.Inspect(root, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			collectGotoTargets(n.Body, out)
			return false
		case *syntax.BranchStmt:
			if n.Tok == syntax.Goto && n.Label != nil {
				gotos[n.Label.Value] = true
			}
		case *syntax.LabeledStmt:
			labeled = append(labeled, n)
		}
		return true
	})
	for _, lab := range labeled {
		if gotos[lab.Label.Value] {
			out[lab] = true
		}
	}
}

func (fl *fileLower) lowerChainChildren(s syntax.Stmt, enc []resultSpec) {
	for _, child := range tupleChildLists(s) {
		*child = fl.lowerChainList(*child, enc)
	}
	fl.lowerChainElseIf(s, enc)
	fl.lowerChainConstBodies(s, enc)
	syntax.Inspect(s, func(n syntax.Node) bool {
		if lit, ok := n.(*syntax.FuncLit); ok {
			if fl.transparent[lit] {
				return true
			}
			saved := fl.chainGrp
			fl.chainGrp = chainGroup{}
			lit.Body.List = fl.lowerChainList(lit.Body.List, fl.resultSpecs(lit.Type, fl.cfLitSig(lit)))
			fl.chainGrp = saved
			return false
		}
		if _, ok := n.(*syntax.GroupStmt); ok {
			return false
		}
		if _, ok := n.(*syntax.GroupChild); ok {
			return false
		}
		return true
	})
}

func (fl *fileLower) lowerChainPkgDecl(decl *syntax.VarDecl) {
	if decl.Values == nil {
		return
	}
	for _, call := range fl.chainsInDecl(decl) {
		fl.lowerChainGuarded(call, nil, "")
	}
}

func (fl *fileLower) chainsInDecl(decl *syntax.VarDecl) []*syntax.CallExpr {
	var all []*syntax.CallExpr
	syntax.Inspect(decl, func(n syntax.Node) bool {
		if lit, ok := n.(*syntax.FuncLit); ok {
			lit.Body.List = fl.lowerChainList(lit.Body.List, fl.resultSpecs(lit.Type, fl.cfLitSig(lit)))
			return false
		}
		if call, ok := n.(*syntax.CallExpr); ok && fl.isChainCall(call) {
			all = append(all, call)
		}
		return true
	})
	return fl.outermostChains(all)
}

func (fl *fileLower) outermostChains(all []*syntax.CallExpr) []*syntax.CallExpr {
	spans := make(map[*syntax.CallExpr]span, len(all))
	for _, c := range all {
		cs, ce := fl.chainSpan(c)
		spans[c] = span{cs, ce}
	}
	var outer []*syntax.CallExpr
	for _, c := range all {
		contained := false
		for _, o := range all {
			if o != c && spans[o].start <= spans[c].start && spans[c].end <= spans[o].end &&
				(spans[o].start < spans[c].start || spans[c].end < spans[o].end) {
				contained = true
				break
			}
		}
		if !contained {
			outer = append(outer, c)
		}
	}
	slices.SortStableFunc(outer, func(a, b *syntax.CallExpr) int {
		return spans[a].start - spans[b].start
	})
	return outer
}

func (fl *fileLower) lowerChainStmt(s syntax.Stmt, enc []resultSpec) []syntax.Stmt {
	calls := fl.chainsInStmt(s)
	if len(calls) == 0 {
		return nil
	}
	stmtStart := fl.stmtStartOf(s)
	var hoists []syntax.Stmt
	for _, h := range fl.lowerChainUnit(s, calls, enc) {
		hoists = append(hoists, h.stmts...)
		fl.addT(stmtStart, stmtStart, cat(h.text, gen("; ")))
	}
	return hoists
}

func (fl *fileLower) lowerChainElseIf(s syntax.Stmt, enc []resultSpec) {
	if lab, ok := s.(*syntax.LabeledStmt); ok {
		fl.lowerChainElseIf(lab.Stmt, enc)
		return
	}
	ifs, ok := s.(*syntax.IfStmt)
	if !ok {
		return
	}
	els, ok := ifs.Else.(*syntax.IfStmt)
	if !ok {
		return
	}
	fl.lowerChainElseIfHead(ifs, els, enc)
	for _, child := range tupleChildLists(els) {
		*child = fl.lowerChainList(*child, enc)
	}
	fl.lowerChainElseIf(els, enc)
}

func (fl *fileLower) lowerChainElseIfHead(ifs *syntax.IfStmt, els *syntax.IfStmt, enc []resultSpec) {
	if els.Init != nil {
		fl.lowerChainElseIfInit(ifs, els, enc)
		return
	}
	calls := fl.chainsInStmt(els)
	var hoists []syntax.Stmt
	parts := []otext{gen("{ ")}
	for _, h := range fl.lowerChainUnit(els, calls, enc) {
		hoists = append(hoists, h.stmts...)
		parts = append(parts, h.text, gen("; "))
	}
	if len(hoists) == 0 {
		return
	}
	elsPos := fl.off(syntax.StartPos(els))
	fl.addT(elsPos, elsPos, cat(parts...))
	fl.add(fl.off(syntax.EndPos(els)), fl.off(syntax.EndPos(els)), " }")
	ifs.Else = &syntax.BlockStmt{List: append(hoists, els)}
}

func (fl *fileLower) lowerChainConstBodies(s syntax.Stmt, enc []resultSpec) {
	if lab, ok := s.(*syntax.LabeledStmt); ok {
		fl.lowerChainConstBodies(lab.Stmt, enc)
		return
	}
	switch s := s.(type) {
	case *syntax.ConstIfStmt:
		s.Then.List = fl.lowerChainList(s.Then.List, enc)
		switch els := s.Else.(type) {
		case *syntax.BlockStmt:
			els.List = fl.lowerChainList(els.List, enc)
		case *syntax.ConstIfStmt:
			fl.lowerChainConstBodies(els, enc)
		}
	case *syntax.ConstSwitchStmt:
		for _, c := range s.Cases {
			c.Body = fl.lowerChainList(c.Body, enc)
		}
	case *syntax.GroupStmt:
		fl.lowerChainGroupBody(s, enc)
	}
}

func (fl *fileLower) lowerChainElseIfInit(ifs *syntax.IfStmt, els *syntax.IfStmt, enc []resultSpec) {
	var pre []syntax.Stmt
	parts := []otext{gen("{ ")}
	for _, h := range fl.lowerChainPiece(els.Init, enc) {
		pre = append(pre, h.stmts...)
		parts = append(parts, h.text, gen("; "))
	}
	errName := chainErrName(els.Pos())
	hoists := fl.lowerChainPieceExit(els.Cond, enc, condFail(errName))
	if len(pre) == 0 && len(hoists) == 0 {
		return
	}
	if len(hoists) > 0 {
		pre = append(pre, parseChainStmtFrag(fl, "var "+errName+" error", els.Pos())...)
		parts = append(parts, gen("var "+errName+" error; "))
		cond, check := fl.rewriteIfCond(els, hoists, enc, errName)
		els.Cond = cond
		els.Then.List = append(check, els.Then.List...)
	}
	elsPos := fl.off(syntax.StartPos(els))
	fl.addT(elsPos, elsPos, cat(parts...))
	end := fl.off(syntax.EndPos(els))
	fl.add(end, end, " }")
	wrapper := blockOf(els.Pos(), append(pre, els))
	wrapper.Rbrace = hostRbrace(els)
	ifs.Else = wrapper
}

func (fl *fileLower) lowerChainGroupBody(s *syntax.GroupStmt, enc []resultSpec) {
	s.Body.List = fl.lowerChainList(s.Body.List, enc)
	for _, st := range s.Body.List {
		if gc, ok := st.(*syntax.GroupChild); ok {
			gc.Body.List = fl.lowerChainList(gc.Body.List, enc)
		}
	}
}

func (fl *fileLower) stmtStartOf(s syntax.Stmt) int {
	switch s := s.(type) {
	case *syntax.LabeledStmt:
		return fl.off(stmtStartPos(s))
	case *syntax.CallStmt:
		if s.Call != nil {
			return fl.exprStart(s.Call)
		}
	case *syntax.ExprStmt:
		if s.X != nil {
			return fl.exprStart(s.X)
		}
	}
	return fl.off(syntax.StartPos(s))
}

func (fl *fileLower) lowerChainGuarded(call *syntax.CallExpr, enc []resultSpec, guard string) []chainHoist {
	cg := &chainGen{fl: fl, pos: call.Pos(), prefix: chainPrefix(call.Pos()), enc: enc, guard: guard, exit: fl.chainExit}
	cg.run(call)
	return cg.hoists
}

func chainPrefix(pos syntax.Pos) string {
	return fmt.Sprintf("_logos_ch%d_%d", pos.Line(), pos.Col())
}

func (fl *fileLower) chainsInStmt(s syntax.Stmt) []*syntax.CallExpr {
	skip := make(map[syntax.Stmt]bool)
	nestedChainStmts(s, skip)
	var all []*syntax.CallExpr
	syntax.Inspect(s, func(n syntax.Node) bool {
		if lit, ok := n.(*syntax.FuncLit); ok && !fl.transparent[lit] {
			return false
		}
		if st, ok := n.(syntax.Stmt); ok && st != s && skip[st] {
			return false
		}
		if call, ok := n.(*syntax.CallExpr); ok && fl.isChainCall(call) {
			all = append(all, call)
		}
		return true
	})
	return fl.outermostChains(all)
}

func nestedChainStmts(s syntax.Stmt, skip map[syntax.Stmt]bool) {
	if lab, ok := s.(*syntax.LabeledStmt); ok {
		nestedChainStmts(lab.Stmt, skip)
	}
	for _, child := range tupleChildLists(s) {
		for _, st := range *child {
			skip[st] = true
		}
	}
	if ifs, ok := s.(*syntax.IfStmt); ok {
		for els, ok := ifs.Else.(*syntax.IfStmt); ok; els, ok = els.Else.(*syntax.IfStmt) {
			skip[els] = true
			nestedChainStmts(els, skip)
		}
	}
	markConstGroupLists(s, skip)
	if gc, ok := s.(*syntax.GroupChild); ok {
		for _, st := range gc.Body.List {
			skip[st] = true
		}
	}
}

func markConstGroupLists(s syntax.Stmt, skip map[syntax.Stmt]bool) {
	switch s := s.(type) {
	case *syntax.ConstIfStmt:
		for _, st := range s.Then.List {
			skip[st] = true
		}
		switch els := s.Else.(type) {
		case *syntax.BlockStmt:
			for _, st := range els.List {
				skip[st] = true
			}
		case *syntax.ConstIfStmt:
			markConstGroupLists(els, skip)
		}
	case *syntax.ConstSwitchStmt:
		for _, c := range s.Cases {
			for _, st := range c.Body {
				skip[st] = true
			}
		}
	case *syntax.GroupStmt:
		for _, st := range s.Body.List {
			skip[st] = true
		}
	}
}

func (fl *fileLower) chainSpan(call *syntax.CallExpr) (int, int) {
	return fl.exprStart(call), fl.chainCallEnd(call)
}

func (fl *fileLower) exprStart(e syntax.Expr) int {
	for {
		if ws, ok := fl.wrapSpan[e]; ok {
			return ws.start
		}
		switch t := e.(type) {
		case *syntax.SliceExpr:
			e = t.X
		case *syntax.IndexExpr:
			e = t.X
		case *syntax.SelectorExpr:
			e = t.X
		case *syntax.AssertExpr:
			e = t.X
		case *syntax.CallExpr:
			e = t.Fun
		case *syntax.Operation:
			if t.Y == nil {
				return fl.off(t.Pos())
			}
			e = t.X
		case *syntax.CompositeLit:
			if t.Type == nil {
				return fl.off(t.Pos())
			}
			e = t.Type
		default:
			return fl.off(syntax.StartPos(e))
		}
	}
}

func (fl *fileLower) receiverSpan(call *syntax.CallExpr) span {
	sel, ok := syntax.Unparen(call.Fun).(*syntax.SelectorExpr)
	if !ok {
		base.Fatalf("logos: chain call at %s has no receiver selector", call.Pos().String())
	}
	dot := fl.off(sel.Pos())
	if dot < 0 || dot >= len(fl.src) || fl.src[dot] != '.' {
		base.Fatalf("logos: chain call at %s has no source receiver", call.Pos().String())
	}
	start := fl.exprStart(sel.X)
	end := start
	if start < dot {
		end = lastTokenEnd(fl.src, start, dot)
	}
	if end <= start {
		base.Fatalf("logos: cannot locate chain receiver at %s", call.Pos().String())
	}
	return span{start: start, end: end}
}

func (fl *fileLower) chainCallEnd(e syntax.Expr) int {
	call, ok := syntax.Unparen(e).(*syntax.CallExpr)
	if !ok {
		return fl.off(syntax.EndPos(e))
	}
	start := fl.off(syntax.StartPos(e))
	funEnd := fl.off(syntax.EndPos(syntax.Unparen(call.Fun)))
	if start >= funEnd {
		return fl.off(syntax.EndPos(e))
	}
	lparen := fl.off(call.Pos())
	if lparen < 0 || lparen >= len(fl.src) || fl.src[lparen] != '(' {
		base.Fatalf("logos: call at %s has no source argument list", call.Pos().String())
	}
	split, ok := splitCall(fl.src, lparen)
	if !ok {
		base.Fatalf("logos: call at %s has no closing parenthesis", call.Pos().String())
	}
	return split.end
}

func (fl *fileLower) isChainCall(e syntax.Expr) bool {
	call, ok := syntax.Unparen(e).(*syntax.CallExpr)
	if !ok {
		return false
	}
	sel, ok := syntax.Unparen(call.Fun).(*syntax.SelectorExpr)
	if !ok || sel.Sel == nil {
		return false
	}
	if fl.pkg.info.Selections[sel] != nil {
		return false
	}
	if tv, ok := fl.pkg.info.Types[sel.X]; ok && tv.IsType() {
		return false
	}
	switch sel.Sel.Value {
	case "filter", "map", "find", "any", "all", "sortBy",
		"first", "last", "get", "take", "drop", "sum", "count":
		t := fl.pkg.info.TypeOf(sel.X)
		if t == nil {
			return false
		}
		if c := constAsView(t); c != nil {
			t = c.Elem
		}
		if obj, _, _ := types2.LookupFieldOrMethod(t, true, fl.pkg.tpkg, sel.Sel.Value); obj != nil {
			return false
		}
		if _, ok := types2.Unalias(t).(*types2.Slice); !ok {
			return false
		}
		return true
	}
	return false
}

func chainMethodOf(call *syntax.CallExpr) string {
	if sel, ok := syntax.Unparen(call.Fun).(*syntax.SelectorExpr); ok && sel.Sel != nil {
		return sel.Sel.Value
	}
	return ""
}

func isFusibleMethod(m string) bool {
	switch m {
	case "filter", "map", "take", "drop":
		return true
	}
	return false
}

func (fl *fileLower) decomposeChain(call *syntax.CallExpr) (ops []chainOp, base syntax.Expr, term chainOp, carrier *syntax.CallExpr) {
	ops, base, term, carrier = fl.chainParts(call, fl.isChainCall)
	for i := range ops {
		fl.chainOpSpans(&ops[i])
	}
	fl.chainOpSpans(&term)
	return
}

func (fl *fileLower) chainParts(call *syntax.CallExpr, isChain func(syntax.Expr) bool) (ops []chainOp, base syntax.Expr, term chainOp, carrier *syntax.CallExpr) {
	rest := syntax.Expr(call)
	carrier = call
	if m := chainMethodOf(call); !isFusibleMethod(m) {
		term = fl.chainOpAST(call, m)
		if sel, ok := syntax.Unparen(call.Fun).(*syntax.SelectorExpr); ok {
			rest = sel.X
		}
	}
	cur := rest
	for {
		c, ok := syntax.Unparen(cur).(*syntax.CallExpr)
		if !ok || !isChain(c) {
			return ops, cur, term, carrier
		}
		m := chainMethodOf(c)
		if !isFusibleMethod(m) {
			return ops, cur, term, carrier
		}
		ops = append(ops, fl.chainOpAST(c, m))
		carrier = c
		if sel, ok := syntax.Unparen(c.Fun).(*syntax.SelectorExpr); ok {
			cur = sel.X
		} else {
			return ops, cur, term, carrier
		}
	}
}

func (fl *fileLower) soleArgSpan(call *syntax.CallExpr) span {
	lparen := fl.off(call.Pos())
	if lparen < 0 || lparen >= len(fl.src) || fl.src[lparen] != '(' {
		base.Fatalf("logos: chain call at %s has no source argument list", call.Pos().String())
	}
	split, ok := splitCall(fl.src, lparen)
	if !ok || len(split.args) != 1 {
		base.Fatalf("logos: cannot locate chain argument at %s", call.Pos().String())
	}
	return trimSpan(fl.src, split.args[0])
}

func (fl *fileLower) lambdaBodySpan(lam *syntax.LambdaExpr, arg span) span {
	start := arg.end
	scanSegment(fl.src, fl.off(lam.Arrow)+len("=>"), arg.end, func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		start = t.start
		return false
	})
	return span{start: start, end: arg.end}
}

func (fl *fileLower) chainOpAST(cur *syntax.CallExpr, method string) chainOp {
	op := chainOp{method: method, call: cur}
	if len(cur.ArgList) == 1 {
		op.arg = cur.ArgList[0]
		if lam, ok := syntax.Unparen(cur.ArgList[0]).(*syntax.LambdaExpr); ok {
			op.lam = lam
			op.isTry = types2.LogosLambdaTry(lam) != nil
			_, op.isBlk = lam.Body.(*syntax.BlockStmt)
		}
	}
	if method == "map" {
		op.forms = fl.mapForms(op)
	}
	return op
}

func (fl *fileLower) chainOpSpans(op *chainOp) {
	if op.arg == nil {
		return
	}
	op.argSpan = fl.soleArgSpan(op.call)
	if op.lam != nil {
		op.bodySpan = fl.lambdaBodySpan(op.lam, op.argSpan)
	}
}

type localWrites struct {
	defined bool
	forms   []resultForm
}

func (cg *chainGen) chainBlockTryFrame(block *syntax.BlockStmt, out types2.Type) {
	pos := block.Pos()
	errType := types2.Universe.Lookup("error").Type()
	ft := at(&syntax.FuncType{
		ResultList: []*syntax.Field{
			at(&syntax.Field{Type: nameOf(pos, cg.spellType(out))}, pos),
			at(&syntax.Field{Type: nameOf(pos, "error")}, pos),
		},
	}, pos)
	sig := types2.NewSignatureType(nil, nil, nil, nil,
		types2.NewTuple(
			types2.NewParam(pos, nil, "", out),
			types2.NewParam(pos, nil, "", errType),
		), false)
	fc := newFuncLower(cg.fl, ft, sig, block)
	fc.elementFrame = true
	fc.run()
}

func (fl *fileLower) mapForms(op chainOp) []resultForm {
	if op.lam == nil {
		return []resultForm{formCall}
	}
	param := op.lam.Params[0].Value
	if body, ok := op.lam.Body.(syntax.Expr); ok {
		return []resultForm{fl.resultForm(body, param)}
	}
	locals := fl.localForms(op.lam.Body, param)
	var forms []resultForm
	syntax.Inspect(op.lam.Body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit, *syntax.LambdaExpr:
			return false
		case *syntax.ReturnStmt:
			for _, r := range syntax.UnpackListExpr(n.Results) {
				if name, ok := syntax.Unparen(r).(*syntax.Name); ok {
					if written := locals[fl.pkg.info.ObjectOf(name)]; written != nil {
						forms = append(forms, written...)
						continue
					}
				}
				forms = append(forms, fl.resultForm(r, param))
			}
		}
		return true
	})
	return forms
}

func (fl *fileLower) localForms(body syntax.Node, param string) map[types2.Object][]resultForm {
	writes := make(map[types2.Object]*localWrites)
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.AssignStmt:
			var rhs []syntax.Expr
			if n.Op == 0 || n.Op == syntax.Def {
				rhs = syntax.UnpackListExpr(n.Rhs)
			}
			fl.recordWrites(writes, syntax.UnpackListExpr(n.Lhs), rhs, param)
		case *syntax.VarDecl:
			names := make([]syntax.Expr, len(n.NameList))
			for i, name := range n.NameList {
				names[i] = name
			}
			fl.recordWrites(writes, names, syntax.UnpackListExpr(n.Values), param)
		case *syntax.RangeClause:
			fl.recordWrites(writes, syntax.UnpackListExpr(n.Lhs), nil, param)
		}
		return true
	})
	locals := make(map[types2.Object][]resultForm)
	for obj, w := range writes {
		if w.defined {
			locals[obj] = w.forms
		}
	}
	return locals
}

func (fl *fileLower) recordWrites(writes map[types2.Object]*localWrites, lhs, rhs []syntax.Expr, param string) {
	for i, l := range lhs {
		name, ok := syntax.Unparen(l).(*syntax.Name)
		if !ok || name.Value == "_" {
			continue
		}
		obj := fl.pkg.info.ObjectOf(name)
		if obj == nil {
			continue
		}
		w := writes[obj]
		if w == nil {
			w = &localWrites{}
			writes[obj] = w
		}
		w.defined = w.defined || fl.pkg.info.Defs[name] != nil
		w.forms = append(w.forms, fl.writeForm(rhs, i, len(lhs), param))
	}
}

func (fl *fileLower) writeForm(rhs []syntax.Expr, i, n int, param string) resultForm {
	switch {
	case len(rhs) == n:
		return fl.resultForm(rhs[i], param)
	case len(rhs) == 1:
		return fl.resultForm(rhs[0], param)
	}
	return formBorrowed
}

func (fl *fileLower) resultForm(e syntax.Expr, param string) resultForm {
	switch e := syntax.Unparen(e).(type) {
	case *syntax.Name:
		if e.Value == param {
			return formParam
		}
	case *syntax.CompositeLit:
		return formMade
	case *syntax.Operation:
		if _, ok := syntax.Unparen(e.X).(*syntax.CompositeLit); ok && e.Op == syntax.And && e.Y == nil {
			return formMade
		}
	case *syntax.TryExpr:
		return fl.resultForm(e.X, param)
	case *syntax.CallExpr:
		return fl.callForm(e, param)
	}
	return formBorrowed
}

func (fl *fileLower) callForm(call *syntax.CallExpr, param string) resultForm {
	builtin, isType := typeFlagsOf(fl, call.Fun)
	switch {
	case isType && len(call.ArgList) == 1:
		return fl.resultForm(call.ArgList[0], param)
	case isType:
		return formBorrowed
	case builtin:
		if name, ok := syntax.Unparen(call.Fun).(*syntax.Name); ok && name.Value == "new" {
			return formMade
		}
		return formBorrowed
	}
	return formCall
}

type chainGen struct {
	fl        *fileLower
	pos       syntax.Pos
	prefix    string
	enc       []resultSpec
	guard     string
	exit      func(err string) string
	whole     span
	kept      []span
	hoists    []chainHoist
	elemT     types2.Type
	receiverT types2.Type
	passRcv   bool
}

func (cg *chainGen) run(call *syntax.CallExpr) {
	if cg.fl.loweredChains[call] {
		return
	}
	cg.fl.loweredChains[call] = true
	cg.fl.lowerTupleNested(call)
	ops, base, term, carrier := cg.fl.decomposeChain(call)
	if len(ops) == 0 && term.method == "" {
		return
	}
	if cg.fl.isParChain(base) {
		cg.runPar(call, ops, base, term, carrier)
		return
	}
	slices.Reverse(ops)
	recv := cg.fl.receiverSpan(carrier)
	cg.whole.start, cg.whole.end = cg.fl.chainSpan(call)
	cg.kept = keptSpans(recv, ops, term)
	for i := range ops {
		if ops[i].lam != nil {
			cg.fl.preLowerCaptureBody(ops[i].lam)
			ops[i].savedBody = ops[i].lam.Body
		}
		cg.lowerArgChains(ops[i].arg)
	}
	if term.lam != nil {
		cg.fl.preLowerCaptureBody(term.lam)
		term.savedBody = term.lam.Body
	}
	cg.lowerArgChains(term.arg)
	cg.lowerArgChains(base)
	cg.fl.lowerPlainLambdas(call)
	for i := range ops {
		if ops[i].lam == nil {
			cg.fl.lowerOperandProps(ops[i].arg)
		}
	}
	if term.lam == nil {
		cg.fl.lowerOperandProps(term.arg)
	}
	cg.fl.lowerOperandProps(base)
	cg.fl.preLowerCaptureInterps(call)
	baseText := cg.fl.textOf(recv)
	elemT := cg.baseElem(base)
	resultT := cg.fl.pkg.info.TypeOf(call)
	isTry := cg.runIsTry(ops, term)
	cg.receiverT = cg.fl.pkg.info.TypeOf(base)
	cg.passRcv = !isTry && containsCall(base)
	cg.emit(call, ops, baseText, elemT, resultT, term, isTry)
}

func (fl *fileLower) lowerOperandProps(e syntax.Expr) {
	if e == nil {
		return
	}
	c := &propCollect{}
	fl.propWalk(e, c)
	fl.emitPropReads(c)
}

func (fl *fileLower) lowerPlainLambdas(root syntax.Node) {
	chainLams := make(map[*syntax.LambdaExpr]bool)
	var lams []*syntax.LambdaExpr
	syntax.Inspect(root, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CallExpr:
			if fl.isChainCall(n) {
				for _, arg := range n.ArgList {
					if lam, ok := arg.(*syntax.LambdaExpr); ok {
						chainLams[lam] = true
					}
				}
			}
		case *syntax.LambdaExpr:
			lams = append(lams, n)
		}
		return true
	})
	for i := len(lams) - 1; i >= 0; i-- {
		if _, done := fl.stagedReplacements[lams[i]]; done || chainLams[lams[i]] {
			continue
		}
		if lit := fl.lowerLambdaExpr(lams[i]); lit != nil {
			fl.stageReplacement(lams[i], lit)
		}
	}
}

func (fl *fileLower) preLowerCaptureBody(lam *syntax.LambdaExpr) {
	if lam == nil {
		return
	}
	if body, ok := lam.Body.(syntax.Expr); ok {
		if _, done := fl.optBodySpan[lam]; !done {
			bodyStart := fl.exprStart(body)
			espan := span{bodyStart, lambdaBodyEnd(fl.src, bodyStart)}
			// A wrapSpan pin covers the pinned node's own extent; when the
			// body sits inside parens the pin must still cover them, else
			// the replacement end would cut before a closing paren.
			if ws, ok := fl.wrapSpan[syntax.Unparen(body)]; ok && ws.start <= espan.start && ws.end >= espan.end {
				espan = ws
			} else if call, ok := syntax.Unparen(body).(*syntax.CallExpr); ok {
				// A generated call can carry an operand position with no
				// source '(' after it, and a span narrower than the body
				// would cut enclosing parens; keep the natural span then.
				if cs, ok := fl.callBodySpanOr(call); ok && cs.start <= espan.start && cs.end >= espan.end {
					espan = cs
				}
			}
			if fl.optBodySpan == nil {
				fl.optBodySpan = make(map[*syntax.LambdaExpr]span)
			}
			fl.optBodySpan[lam] = espan
		}
	}
	if block, ok := lam.Body.(*syntax.BlockStmt); ok {
		block.List = fl.lowerPropBlock(block.List)
		fl.lowerOptDeferGoIn(block)
	} else if body, ok := lam.Body.(syntax.Expr); ok {
		if t, ok := syntax.Unparen(body).(*syntax.TryExpr); ok {
			if call, ok := syntax.Unparen(t.X).(*syntax.CallExpr); ok {
				c := &propCollect{}
				t.X = fl.propWalk(call, c)
				fl.emitPropReads(c)
			}
		} else {
			orig := fl.pkg.info.TypeOf(body)
			c := &propCollect{}
			newBody := fl.propWalk(body, c)
			fl.emitPropReads(c)
			lam.Body = newBody
			fl.stashBodyType(orig, body, newBody)
		}
	} else {
		return
	}
	fl.preLowerCaptureInterps(lam.Body)
}

func (fl *fileLower) stashBodyType(orig types2.Type, old, new syntax.Expr) {
	if new == old || orig == nil {
		return
	}
	if fl.optBodyType == nil {
		fl.optBodyType = make(map[syntax.Expr]types2.Type)
	}
	fl.optBodyType[new] = orig
}

func (fl *fileLower) preLowerCaptureInterps(root syntax.Node) {
	if root == nil {
		return
	}
	for _, e := range interpLitsUnder(root) {
		kinds, ok := interpKinds[e]
		if !ok {
			continue
		}
		delete(interpKinds, e)
		need, _ := fl.lowerInterpExpr(e, kinds)
		fl.interpNeedConv = fl.interpNeedConv || need
	}
}

func keptSpans(recv span, ops []chainOp, term chainOp) []span {
	kept := []span{recv, term.argSpan}
	for _, op := range ops {
		kept = append(kept, op.argSpan)
	}
	return kept
}

func (cg *chainGen) baseElem(base syntax.Expr) types2.Type {
	t := cg.fl.pkg.info.TypeOf(base)
	if c := constAsView(t); c != nil {
		t = c.Elem
	}
	if s, ok := types2.Unalias(t).(*types2.Slice); ok {
		return s.Elem()
	}
	return nil
}

func (cg *chainGen) runIsTry(ops []chainOp, term chainOp) bool {
	for _, op := range ops {
		if op.isTry {
			return true
		}
	}
	return term.isTry
}

func (cg *chainGen) lowerArgChains(arg syntax.Expr) {
	if arg == nil {
		return
	}
	if call, ok := syntax.Unparen(arg).(*syntax.CallExpr); ok && cg.fl.isChainCall(call) {
		cg.lowerNested(call)
		return
	}
	for _, n := range cg.nestedChains(arg) {
		cg.lowerNested(n)
	}
}

func (cg *chainGen) lowerNested(call *syntax.CallExpr) {
	sub := &chainGen{fl: cg.fl, pos: call.Pos(), prefix: chainPrefix(call.Pos()), enc: cg.enc, guard: cg.guard, exit: cg.exit}
	sub.run(call)
	cg.hoists = append(cg.hoists, sub.hoists...)
}

func (cg *chainGen) nestedChains(e syntax.Expr) []*syntax.CallExpr {
	var all []*syntax.CallExpr
	syntax.Inspect(e, func(n syntax.Node) bool {
		if _, ok := n.(*syntax.FuncLit); ok && n != e {
			return false
		}
		if call, ok := n.(*syntax.CallExpr); ok && call != e && cg.fl.isChainCall(call) {
			all = append(all, call)
		}
		return true
	})
	return cg.fl.outermostChains(all)
}

func (cg *chainGen) returnText(err string) string {
	if zeros := cg.enclosingZeros(); zeros != "" {
		return "return " + zeros + ", " + err
	}
	return "return " + err
}

func (cg *chainGen) failText(err string) string {
	if cg.exit != nil {
		return cg.exit(err)
	}
	return cg.returnText(err)
}

func (cg *chainGen) enclosingZeros() string {
	var out []string
	for i, r := range cg.enc {
		if i == len(cg.enc)-1 {
			break
		}
		out = append(out, renderExpr(cg.fl.zeroValue(r.typ, cg.pos)))
	}
	return strings.Join(out, ", ")
}

type chainEmit struct {
	cg        *chainGen
	ops       []chainOp
	term      chainOp
	baseText  otext
	elemT     types2.Type
	resultT   types2.Type
	isTry     bool
	closures  []otext
	outElem   string
	outCap    string
	hasCap    bool
	ownBefore []bool
	lastMap   int
	owns      bool
	flight    bool
	ownedIn   bool
}

func (ce *chainEmit) prepare() {
	cg := ce.cg
	elem := cg.spellType(cg.elemT)
	for i, op := range ce.ops {
		switch op.method {
		case "take":
			ce.closures = append(ce.closures,
				cat(gen(fmt.Sprintf("%s_tn%d := ", cg.prefix, i)), ce.argText(op)),
				gen(fmt.Sprintf("%s_tc%d := 0", cg.prefix, i)),
			)
		case "drop":
			ce.closures = append(ce.closures,
				cat(gen(fmt.Sprintf("%s_d%d := ", cg.prefix, i)), ce.argText(op)),
				gen(fmt.Sprintf("%s_di%d := 0", cg.prefix, i)),
			)
		default:
			if op.lam != nil {
				ce.closures = append(ce.closures, cg.closure(cg.prefix+fmt.Sprintf("_f%d", i), op, elem, ce.opResult(op)))
			} else if op.arg != nil && takesFunc(op.method) {
				ce.closures = append(ce.closures, cat(gen(cg.prefix+fmt.Sprintf("_f%d := ", i)), cg.fl.textOf(op.argSpan)))
			}
		}
		if op.method == "map" {
			elem = ce.opResult(op)
		}
	}
	if ce.term.lam != nil {
		ce.closures = append(ce.closures, cg.closure(cg.prefix+"_ft", ce.term, elem, ce.opResult(ce.term)))
	} else if ce.term.arg != nil && takesFunc(ce.term.method) {
		ce.closures = append(ce.closures, cat(gen(cg.prefix+"_ft := "), cg.fl.textOf(ce.term.argSpan)))
	} else if ce.term.method == "get" {
		ce.closures = append(ce.closures, cat(gen(fmt.Sprintf("%s_i := ", cg.prefix)), ce.argText(ce.term)))
	}
	ce.outElem = elem
	ce.ownership()
	ce.outCap, ce.hasCap = ce.capHint()
}

func (ce *chainEmit) capHint() (string, bool) {
	limit, ok := ce.capBound()
	if !ok {
		return "", false
	}
	recv := "len(" + ce.cg.prefix + "_rcv)"
	if limit < 0 {
		return recv, true
	}
	return fmt.Sprintf("min(%d, %s)", limit, recv), true
}

func (ce *chainEmit) capBound() (int64, bool) {
	if !ce.needOut() || ce.term.method != "" || ce.cg.elemT == nil {
		return 0, false
	}
	filtered := false
	bounded := true
	limit := int64(-1)
	for _, op := range ce.ops {
		switch op.method {
		case "filter":
			filtered = true
		case "take":
			n, ok := ce.constTake(op.arg)
			if !ok {
				bounded = false
				continue
			}
			if limit < 0 || n < limit {
				limit = n
			}
		}
	}
	if !bounded || limit < 0 {
		return -1, !filtered
	}
	return limit, true
}
func (ce *chainEmit) constTake(e syntax.Expr) (int64, bool) {
	if e == nil {
		return 0, false
	}
	v, ok := typeConst(ce.cg.fl.pkg.info, syntax.Unparen(e))
	if !ok || v == nil {
		return 0, false
	}
	n, ok := constant.Int64Val(v)
	if !ok {
		return 0, false
	}
	if n < 0 {
		n = 0
	}
	return n, true
}

func (ce *chainEmit) rsuf() string {
	if ce.isTry {
		return ", nil"
	}
	return ""
}

func (ce *chainEmit) opResult(op chainOp) string {
	cg := ce.cg
	switch op.method {
	case "filter", "find", "any", "all", "count":
		return "bool"
	case "sortBy":
		t := cg.prodType(op)
		if t != nil && isUntypedBasic(t) {
			t = defaultBasicType(t)
		}
		if t == nil {
			t = cg.elemT
		}
		return cg.spellType(t)
	default:
		t := cg.prodType(op)
		if t == nil {
			t = cg.elemT
		}
		return cg.spellType(t)
	}
}

func takesFunc(method string) bool {
	switch method {
	case "filter", "map", "find", "any", "all", "sortBy", "count":
		return true
	}
	return false
}

func (ce *chainEmit) funcOf(op chainOp, closure string) string {
	if op.lam != nil || (op.arg != nil && takesFunc(op.method)) {
		return closure
	}
	return ""
}

func (cg *chainGen) funcResult(op chainOp) types2.Type {
	if op.arg == nil {
		return nil
	}
	t := cg.fl.pkg.info.TypeOf(op.arg)
	if t == nil {
		return nil
	}
	sig, ok := types2.Unalias(t).(*types2.Signature)
	if !ok || sig.Params().Len() != 1 || sig.Results().Len() != 1 {
		return nil
	}
	return sig.Results().At(0).Type()
}

func (cg *chainGen) prodType(op chainOp) types2.Type {
	if op.resultT != nil {
		return op.resultT
	}
	if op.lam == nil {
		return cg.funcResult(op)
	}
	if block, ok := op.savedBody.(*syntax.BlockStmt); ok {
		return cg.blockProdType(block)
	}
	body, ok := op.savedBody.(syntax.Expr)
	if !ok {
		return nil
	}
	if t, ok := syntax.Unparen(body).(*syntax.TryExpr); ok {
		if tv := cg.fl.pkg.info.TypeOf(t); tv != nil {
			return tv
		}
		return nil
	}
	if tv := cg.fl.pkg.info.TypeOf(body); tv != nil {
		return tv
	}
	if st, ok := cg.fl.optBodyType[body]; ok {
		return st
	}
	return nil
}

func (cg *chainGen) blockProdType(block *syntax.BlockStmt) types2.Type {
	var first types2.Type
	for _, s := range block.List {
		ret, ok := s.(*syntax.ReturnStmt)
		if !ok {
			continue
		}
		for _, res := range syntax.UnpackListExpr(ret.Results) {
			t := cg.fl.pkg.info.TypeOf(res)
			if t == nil {
				t, _ = cg.fl.optBodyType[res]
			}
			if t == nil {
				continue
			}
			if b, ok := t.(*types2.Basic); ok && b.Kind() == types2.UntypedNil {
				continue
			}
			if isUntypedBasic(t) {
				t = defaultBasicType(t)
			}
			if first == nil {
				first = t
			}
		}
	}
	return first
}

func (cg *chainGen) closure(name string, op chainOp, elem, result string) otext {
	fl := cg.fl
	pname := op.lam.Params[0].Value
	if op.isTry && !op.isBlk {
		body, ok := op.savedBody.(syntax.Expr)
		if !ok {
			return gen("")
		}
		tryBody, ok := syntax.Unparen(body).(*syntax.TryExpr)
		if !ok {
			return gen("")
		}
		inner, ok := syntax.Unparen(tryBody.X).(*syntax.CallExpr)
		if !ok {
			return gen("")
		}
		callText := fl.textOf(span{fl.exprStart(inner), fl.chainCallEnd(inner)})
		zero := cg.zeroText(cg.inferType(op, result))
		vn := cg.prefix + "_v"
		en := cg.prefix + "_e"
		return cat(gen(name+" := func("+pname+" "+elem+") ("+result+", error) { "+vn+", "+en+" := "), callText, gen("; if "+en+" != nil { return "+zero+", "+en+" }; return "+vn+", nil }"))
	}
	if block, ok := op.savedBody.(*syntax.BlockStmt); ok {
		if op.isTry {
			cg.chainBlockTryFrame(block, cg.inferType(op, result))
		}
		bt := fl.textOf(span{fl.off(syntax.StartPos(block)), fl.off(syntax.EndPos(block))})
		inner := bt
		if strings.HasPrefix(bt.s, "{") {
			inner = bt.slice(1, len(bt.s))
		}
		if op.isTry {
			return cat(gen(name+" := func("+pname+" "+elem+") ("+result+", error) {"), inner, gen(" }"))
		}
		return cat(gen(name+" := func("+pname+" "+elem+") "+result+" {"), inner, gen(" }"))
	}
	if expr, ok := op.savedBody.(syntax.Expr); ok {
		bodyText := fl.drainBodyWrapper(expr, fl.textOf(op.bodySpan), op.bodySpan)
		return cat(gen(name+" := func("+pname+" "+elem+") "+result+" { return "), bodyText, gen(" }"))
	}
	return gen("")
}

func (cg *chainGen) inferType(op chainOp, result string) types2.Type {
	if op.lam == nil {
		return cg.elemT
	}
	if result == "bool" {
		return types2.Typ[types2.Bool]
	}
	t := cg.prodType(op)
	if t == nil {
		t = cg.elemT
	}
	return t
}

func (cg *chainGen) zeroText(t types2.Type) string {
	if t == nil {
		return "nil"
	}
	return renderExpr(cg.fl.zeroValue(t, cg.pos))
}

func (cg *chainGen) spellType(t types2.Type) string {
	if t == nil {
		return "any"
	}
	if isUntypedBasic(t) {
		t = defaultBasicType(t)
	}
	if spelled, ok := cg.fl.spellLowered(t); ok {
		return spelled
	}
	if spelled, ok := cg.fl.spellType(t); ok {
		return spelled
	}
	return "any"
}

func (ce *chainEmit) outDecl() string {
	p := ce.cg.prefix
	if ce.hasCap {
		return fmt.Sprintf("%s_out := make([]%s, 0, %s)", p, ce.outElem, ce.outCap)
	}
	return "var " + p + "_out []" + ce.outElem
}

func (ce *chainEmit) loopO() (prelude []otext, body, epilogue []string) {
	cg := ce.cg
	p := cg.prefix
	prelude = append(prelude, cat(gen(p+"_rcv := "), ce.baseText))
	prelude = append(prelude, ce.closures...)
	if ce.needOut() {
		prelude = append(prelude, gen(ce.outDecl()))
	}
	if ce.flight {
		for i, t := range ce.flightTypes() {
			slot := fmt.Sprintf("%s_fl%d", p, i)
			prelude = append(prelude, gen("var "+slot+" "+cg.spellType(t)), gen(slot+"_live := false"))
		}
	}
	if ce.guarded() {
		prelude = append(prelude, gen(p+"_done := false"))
		prelude = append(prelude, gen("defer func() { if !"+p+"_done { "+ce.rollback(p)+" } }()"))
	}
	if ce.term.method == "count" {
		prelude = append(prelude, gen(p+"_c := 0"))
	}
	cur := p + "_e"
	loopHead := fmt.Sprintf("for _, %[1]s_e := range %[1]s_rcv {", p)
	if !ce.needElem() {
		loopHead = fmt.Sprintf("for range %[1]s_rcv {", p)
	}
	body = append(body, loopHead)
	for i, op := range ce.ops {
		if op.method == "take" {
			body = append(body, fmt.Sprintf("if %[1]s_tc%[2]d >= %[1]s_tn%[2]d { break }", p, i))
		}
	}
	body = append(body, ce.ownedInLines(p)...)
	for i, op := range ce.ops {
		fname := ce.funcOf(op, fmt.Sprintf("%s_f%d", p, i))
		switch op.method {
		case "filter":
			body = append(body, ce.filterLines(p, fname, cur, i)...)
		case "take":
			body = append(body, fmt.Sprintf("%s_tc%d++", p, i))
		case "drop":
			body = append(body, fmt.Sprintf("if %[1]s_di%[2]d < %[1]s_d%[2]d { %[1]s_di%[2]d++; %[3]s }", p, i, ce.dropStmt(p, cur, i)))
		case "map":
			mname := fmt.Sprintf("%s_m%d", p, i)
			if ce.flight && ce.ownBefore[i] {
				body = append(body, ce.clearFlight(p, ce.inType(i)))
			}
			body = append(body, ce.mapLines(p, fname, cur, mname, i)...)
			if ce.flight && i < ce.lastMap && ce.ownBefore[i+1] {
				body = append(body, ce.setFlight(p, mname, ce.inType(i+1)))
			}
			if ce.owns && i == ce.lastMap {
				body = append(body, fmt.Sprintf("%[1]s_out = append(%[1]s_out, %[2]s)", p, mname))
			}
			cur = mname
		}
	}
	body = append(body, ce.terminalLines(p, cur)...)
	body = append(body, "}")
	epilogue = ce.epilogue(p)
	return prelude, body, epilogue
}

func (ce *chainEmit) needOut() bool {
	switch ce.term.method {
	case "find", "any", "all", "count", "first":
		return ce.owns
	}
	return true
}

func (ce *chainEmit) ownership() {
	cg := ce.cg
	ce.ownBefore = make([]bool, len(ce.ops))
	ce.lastMap = -1
	in := cg.elemT
	owned := ce.ownedIn
	for i, op := range ce.ops {
		ce.ownBefore[i] = owned
		if op.method != "map" {
			continue
		}
		out := cg.prodType(op)
		same := in != nil && out != nil && types2.Identical(in, out)
		owned = logosMayBeCloser(out) && formsOwn(op.forms, same, owned)
		in = out
		ce.lastMap = i
	}
	for i, op := range ce.ops {
		if op.method == "filter" && ce.ownBefore[i] && i < ce.lastMap {
			ce.flight = true
		}
	}
	ce.owns = owned
}

func formsOwn(forms []resultForm, same, prev bool) bool {
	if len(forms) == 0 {
		return false
	}
	for _, f := range forms {
		switch f {
		case formMade:
		case formCall:
			if same && !prev {
				return false
			}
		case formParam:
			if !prev {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func (ce *chainEmit) needElem() bool {
	if ce.ownedIn {
		return true
	}
	if ce.term.method != "count" || ce.term.arg != nil {
		return true
	}
	for _, op := range ce.ops {
		if op.method == "filter" || op.method == "map" {
			return true
		}
	}
	return false
}

func (ce *chainEmit) ownedInLines(p string) []string {
	switch {
	case !ce.ownedIn:
		return nil
	case ce.lastMap < 0:
		return []string{fmt.Sprintf("%[1]s_out = append(%[1]s_out, %[1]s_e)", p)}
	case ce.flight:
		return []string{ce.setFlight(p, p+"_e", ce.cg.elemT)}
	}
	return nil
}

func (ce *chainEmit) argText(op chainOp) otext {
	cg := ce.cg
	if op.arg == nil {
		return gen("0")
	}
	return cg.fl.drainBodyWrapper(op.arg, cg.fl.textOf(op.argSpan), op.argSpan)
}

func (ce *chainEmit) filterLines(p, fname, cur string, i int) []string {
	drop := ce.dropStmt(p, cur, i)
	if ce.ops[i].isTry {
		ok := fmt.Sprintf("%s_ok%d", p, i)
		en := fmt.Sprintf("%s_e%d", p, i)
		return []string{fmt.Sprintf("%s, %s := %s(%s); if %s != nil { %s }; if !%s { %s }", ok, en, fname, cur, en, ce.failStage(p, en), ok, drop)}
	}
	return []string{fmt.Sprintf("if !%s(%s) { %s }", fname, cur, drop)}
}

func (ce *chainEmit) dropStmt(p, cur string, i int) string {
	switch {
	case !ce.ownBefore[i]:
		return "continue"
	case i < ce.lastMap && ce.flight:
		return ce.closeOne(p, cur, ce.inType(i)) + "; " + ce.clearFlight(p, ce.inType(i)) + "; continue"
	case i < ce.lastMap:
		return ce.closeOne(p, cur, ce.inType(i)) + "; continue"
	}
	return fmt.Sprintf("%s; %[2]s_out = %[2]s_out[:len(%[2]s_out)-1]; continue", ce.closeOne(p, cur, ce.inType(i)), p)
}

func (ce *chainEmit) mapLines(p, fname, cur, mname string, i int) []string {
	if ce.ops[i].isTry {
		en := fmt.Sprintf("%s_e%d", p, i)
		return []string{fmt.Sprintf("%s, %s := %s(%s); if %s != nil { %s }", mname, en, fname, cur, en, ce.failStage(p, en))}
	}
	return []string{fmt.Sprintf("%s := %s(%s)", mname, fname, cur)}
}

func (ce *chainEmit) guarded() bool { return ce.owns || ce.flight }

func (ce *chainEmit) failStage(p, errVar string) string {
	if ce.guarded() {
		return ce.rollback(p) + "; " + p + "_done = true; " + ce.failReturn(p, errVar)
	}
	return ce.failReturn(p, errVar)
}

func (ce *chainEmit) hasMap() bool {
	for _, op := range ce.ops {
		if op.method == "map" {
			return true
		}
	}
	return false
}

func logosMayBeCloser(t types2.Type) bool {
	if t == nil {
		return true
	}
	switch types2.Unalias(t).(type) {
	case *types2.Interface, *types2.TypeParam, *types2.Union:
		return true
	}
	obj, _, _ := types2.LookupFieldOrMethod(t, false, nil, "Close")
	fn, ok := obj.(*types2.Func)
	if !ok {
		return false
	}
	sig, ok := types2.Unalias(fn.Type()).(*types2.Signature)
	if !ok || sig.Params().Len() != 0 || sig.Results().Len() != 1 {
		return false
	}
	errIface, ok := types2.Universe.Lookup("error").Type().(*types2.Interface)
	if !ok {
		return true
	}
	return types2.Implements(sig.Results().At(0).Type(), errIface)
}

func (ce *chainEmit) inType(i int) types2.Type {
	t := ce.cg.elemT
	for _, op := range ce.ops[:i] {
		if op.method == "map" {
			t = ce.cg.prodType(op)
		}
	}
	return t
}

func (ce *chainEmit) flightTypes() []types2.Type {
	var ts []types2.Type
	for i := 0; i <= ce.lastMap; i++ {
		if !ce.ownBefore[i] {
			continue
		}
		t := ce.inType(i)
		if ce.cg.spellType(t) == "any" {
			t = nil
		}
		found := false
		for _, prev := range ts {
			if prev == t || prev != nil && t != nil && types2.Identical(prev, t) {
				found = true
				break
			}
		}
		if !found {
			ts = append(ts, t)
		}
	}
	return ts
}

func (ce *chainEmit) flightSlot(p string, t types2.Type) (string, types2.Type) {
	if ce.cg.spellType(t) == "any" {
		t = nil
	}
	for i, slotType := range ce.flightTypes() {
		if slotType == t || slotType != nil && t != nil && types2.Identical(slotType, t) {
			return fmt.Sprintf("%s_fl%d", p, i), slotType
		}
	}
	panic("logos: missing chain flight slot")
}

func (ce *chainEmit) setFlight(p, v string, t types2.Type) string {
	slot, _ := ce.flightSlot(p, t)
	return slot + " = " + v + "; " + slot + "_live = true"
}

func (ce *chainEmit) clearFlight(p string, t types2.Type) string {
	slot, slotType := ce.flightSlot(p, t)
	return slot + " = " + ce.cg.zeroText(slotType) + "; " + slot + "_live = false"
}

func (ce *chainEmit) closeOne(p, v string, t types2.Type) string {
	return chainCloseOne(p, v, t, ce.cg.spellType(t))
}

func chainCloseOne(p, v string, t types2.Type, spelled string) string {
	if t != nil && spelled != "any" && plainMethodSet(t) {
		if types2.Implements(t, closerInterface()) {
			return v + ".Close()"
		}
		return ""
	}
	return fmt.Sprintf("if %[1]s_c, %[1]s_ok := any(%[2]s).(interface{ Close() error }); %[1]s_ok { %[1]s_c.Close() }", p, v)
}

func (ce *chainEmit) closeBut(p, handed string) string {
	return fmt.Sprintf("for %[1]s_ci := len(%[1]s_out) - 1; %[1]s_ci >= 0; %[1]s_ci-- { if %[1]s_ci != %[2]s { %[3]s } }", p, handed, ce.closeOne(p, p+"_out["+p+"_ci]", ce.inType(len(ce.ops))))
}

func (ce *chainEmit) handSlice(p string) string {
	if !ce.owns {
		return ""
	}
	return p + "_done = true; "
}

func (ce *chainEmit) handNone(p string) string {
	if !ce.owns {
		return ""
	}
	return ce.closeBut(p, "-1") + "; " + p + "_done = true; "
}

func (ce *chainEmit) handLast(p string) string {
	if !ce.owns {
		return ""
	}
	return ce.closeBut(p, "len("+p+"_out)-1") + "; " + p + "_done = true; "
}

func (ce *chainEmit) handIndex(p string) string {
	if !ce.owns {
		return ""
	}
	return ce.closeBut(p, p+"_i") + "; " + p + "_done = true; "
}

func (ce *chainEmit) failReturn(p, errVar string) string {
	switch ce.term.method {
	case "", "sortBy":
		return "return nil, " + errVar
	case "find", "first", "last", "get":
		return "return " + ce.noneText(p) + ", " + errVar
	case "any", "all":
		return "return false, " + errVar
	case "count", "sum":
		return "return 0, " + errVar
	}
	return "return nil, " + errVar
}

func (ce *chainEmit) rollback(p string) string {
	var steps []string
	if ce.flight {
		for i, t := range ce.flightTypes() {
			slot := fmt.Sprintf("%s_fl%d", p, i)
			steps = append(steps, "if "+slot+"_live { "+ce.closeOne(p, slot, t)+" }")
		}
	}
	if ce.owns {
		steps = append(steps, ce.closeBut(p, "-1"))
	}
	return strings.Join(steps, "; ")
}

func (ce *chainEmit) terminalLines(p, cur string) []string {
	switch ce.term.method {
	case "find":
		return ce.findLines(p, cur)
	case "any", "all":
		return ce.anyLines(p, cur)
	case "count":
		return ce.countLines(p, cur)
	case "first":
		return []string{ce.handLast(p) + "return " + ce.someText(p, cur) + ce.rsuf()}
	}
	if ce.owns {
		return nil
	}
	return []string{fmt.Sprintf("%s_out = append(%s_out, %s)", p, p, cur)}
}

func (ce *chainEmit) findLines(p, cur string) []string {
	fname := ce.funcOf(ce.term, p+"_ft")
	if ce.term.isTry {
		en := p + "_fte"
		return []string{fmt.Sprintf("if %[1]s_fv, %[2]s := %[3]s(%[4]s); %[2]s != nil { %[5]s } else if %[1]s_fv { %[8]sreturn %[6]s%[7]s }", p, en, fname, cur, ce.failStage(p, en), ce.someText(p, cur), ce.rsuf(), ce.handLast(p))}
	}
	return []string{fmt.Sprintf("if %s(%s) { %sreturn %s%s }", fname, cur, ce.handLast(p), ce.someText(p, cur), ce.rsuf())}
}

func (ce *chainEmit) bareOption() bool {
	o := ce.cg.fl.asOptionThroughConst(ce.resultT)
	return o != nil && types2.LogosOptionIsBare(o.Elem)
}

func (ce *chainEmit) someText(p, cur string) string {
	if ce.bareOption() {
		return cur
	}
	ce.cg.fl.needRT = true
	return fmt.Sprintf("_logos_rt.Option[%s]{Value: %s, Present: true}", ce.outElem, cur)
}

func (ce *chainEmit) noneText(p string) string {
	if ce.bareOption() {
		return "nil"
	}
	ce.cg.fl.needRT = true
	return fmt.Sprintf("_logos_rt.Option[%s]{}", ce.outElem)
}

func (ce *chainEmit) anyLines(p, cur string) []string {
	fname := ce.funcOf(ce.term, p+"_ft")
	if ce.term.method == "all" {
		if ce.term.isTry {
			en := p + "_fte"
			return []string{fmt.Sprintf("if %[1]s_fv, %[2]s := %[3]s(%[4]s); %[2]s != nil { %[5]s } else if !%[1]s_fv { %[7]sreturn false%[6]s }", p, en, fname, cur, ce.failStage(p, en), ce.rsuf(), ce.handNone(p))}
		}
		return []string{fmt.Sprintf("if !%s(%s) { %sreturn false%s }", fname, cur, ce.handNone(p), ce.rsuf())}
	}
	if ce.term.isTry {
		en := p + "_fte"
		return []string{fmt.Sprintf("if %[1]s_fv, %[2]s := %[3]s(%[4]s); %[2]s != nil { %[5]s } else if %[1]s_fv { %[7]sreturn true%[6]s }", p, en, fname, cur, ce.failStage(p, en), ce.rsuf(), ce.handNone(p))}
	}
	return []string{fmt.Sprintf("if %s(%s) { %sreturn true%s }", fname, cur, ce.handNone(p), ce.rsuf())}
}

func (ce *chainEmit) countLines(p, cur string) []string {
	if ce.term.arg == nil {
		if ce.hasMap() && !ce.owns {
			return []string{"_ = " + cur, p + "_c++"}
		}
		return []string{p + "_c++"}
	}
	fname := ce.funcOf(ce.term, p+"_ft")
	if ce.term.isTry {
		en := p + "_fte"
		return []string{fmt.Sprintf("if %[1]s_fv, %[2]s := %[3]s(%[4]s); %[2]s != nil { %[5]s } else if %[1]s_fv { %[1]s_c++ }", p, en, fname, cur, ce.failStage(p, en))}
	}
	return []string{fmt.Sprintf("if %s(%s) { %s_c++ }", fname, cur, p)}
}

func (ce *chainEmit) epilogue(p string) []string {
	switch ce.term.method {
	case "find", "any", "all", "count":
		return ce.epilogueValue(p)
	case "sum":
		return ce.epilogueSum(p)
	case "first":
		return []string{ce.handNone(p) + "return " + ce.noneText(p) + ce.rsuf()}
	case "":
		return []string{"if len(" + p + "_out) == 0 { " + p + "_out = nil }", ce.handSlice(p) + "return " + p + "_out" + ce.rsuf()}
	case "last", "get":
		return ce.epilogueIndex(p)
	case "sortBy":
		return ce.epilogueSort(p)
	}
	return []string{ce.handSlice(p) + "return " + p + "_out" + ce.rsuf()}
}

func (ce *chainEmit) epilogueValue(p string) []string {
	switch ce.term.method {
	case "find":
		return []string{ce.handNone(p) + "return " + ce.noneText(p) + ce.rsuf()}
	case "any":
		return []string{ce.handNone(p) + "return false" + ce.rsuf()}
	case "all":
		return []string{ce.handNone(p) + "return true" + ce.rsuf()}
	case "count":
		return []string{ce.handNone(p) + "return " + p + "_c" + ce.rsuf()}
	}
	return []string{ce.handSlice(p) + "return " + p + "_out" + ce.rsuf()}
}

func (ce *chainEmit) epilogueSum(p string) []string {
	t := ce.outElem
	sn := p + "_sum"
	return []string{
		fmt.Sprintf("var %[1]s func([]%[2]s) %[2]s", sn, t),
		fmt.Sprintf("%[1]s = func(%[2]s_s []%[3]s) %[3]s { switch len(%[2]s_s) { case 0: return 0; case 1: return %[2]s_s[0]; default: %[2]s_m := len(%[2]s_s) / 2; return %[1]s(%[2]s_s[:%[2]s_m]) + %[1]s(%[2]s_s[%[2]s_m:]) } }", sn, p, t),
		fmt.Sprintf("%sreturn %s(%s_out)%s", ce.handSlice(p), sn, p, ce.rsuf()),
	}
}

func (ce *chainEmit) epilogueIndex(p string) []string {
	if ce.term.method == "last" {
		return []string{fmt.Sprintf("if len(%[1]s_out) == 0 { %[5]sreturn %[2]s%[3]s }; %[5]sreturn %[4]s%[3]s", p, ce.noneText(p), ce.rsuf(), ce.someText(p, p+"_out[len("+p+"_out)-1]"), ce.handLast(p))}
	}
	return []string{
		fmt.Sprintf("if %[1]s_i < 0 || %[1]s_i >= len(%[1]s_out) { %[5]sreturn %[2]s%[3]s }; %[5]sreturn %[4]s%[3]s", p, ce.noneText(p), ce.rsuf(), ce.someText(p, p+"_out["+p+"_i]"), ce.handIndex(p)),
	}
}

func (ce *chainEmit) epilogueSort(p string) []string {
	ce.cg.fl.needRT = true
	rt := rtAlias
	fname := ce.funcOf(ce.term, p+"_ft")
	e := ce.outElem
	if !ce.term.isTry {
		return []string{
			fmt.Sprintf("%[1]s.SliceStableFunc(%[2]s_out, func(%[2]s_a, %[2]s_b %[3]s) int { return %[1]s.Compare(%[4]s(%[2]s_a), %[4]s(%[2]s_b)) })", rt, p, e, fname),
			fmt.Sprintf("%sreturn %s_out%s", ce.handSlice(p), p, ce.rsuf()),
		}
	}
	k := ce.opResult(ce.term)
	keyErr := p + "_ke"
	return []string{
		fmt.Sprintf("%[1]s_keys := make([]%[2]s, len(%[1]s_out))", p, k),
		fmt.Sprintf("for %[1]s_i := range %[1]s_out { %[1]s_kv, %[1]s_ke := %[3]s(%[1]s_out[%[1]s_i]); if %[1]s_ke != nil { %[2]s }; %[1]s_keys[%[1]s_i] = %[1]s_kv }", p, ce.failStage(p, keyErr), fname),
		fmt.Sprintf("%[1]s_idx := make([]int, len(%[1]s_out))", p),
		fmt.Sprintf("for %[1]s_i := range %[1]s_idx { %[1]s_idx[%[1]s_i] = %[1]s_i }", p),
		fmt.Sprintf("%[1]s.SliceStableFunc(%[1]s_idx, func(%[1]s_a, %[1]s_b int) int { return %[2]s.Compare(%[1]s_keys[%[1]s_a], %[1]s_keys[%[1]s_b]) })", p, rt),
		fmt.Sprintf("%[1]s_sorted := make([]%[2]s, len(%[1]s_out))", p, e),
		fmt.Sprintf("for %[1]s_i, %[1]s_v := range %[1]s_idx { %[1]s_sorted[%[1]s_i] = %[1]s_out[%[1]s_v] }", p),
		fmt.Sprintf("%sreturn %s_sorted%s", ce.handSlice(p), p, ce.rsuf()),
	}
}

func (ce *chainEmit) runLoopO() (prelude []otext, body, epilogue []string) {
	prelude, body, epilogue = ce.loopO()
	f, ok := ce.sliceSumForm()
	if !ok {
		return prelude, body, epilogue
	}
	ce.cg.fl.needSimd = true
	p := ce.cg.prefix
	if f.isFloat {
		return prelude, ce.guardedBody(f, simdSliceStage(p, f), body), epilogue
	}
	vector := simdSliceAccumulate(p, f)
	if ce.cg.fl.simdGuardCond(f.feat) == "" {
		return prelude[:1], vector, nil
	}
	return prelude, ce.guardedBody(f, vector, body), epilogue
}

func (cg *chainGen) emit(call *syntax.CallExpr, ops []chainOp, baseText otext, elemT, resultT types2.Type, term chainOp, isTry bool) {
	fl := cg.fl
	cg.elemT = elemT
	ce := &chainEmit{cg: cg, ops: ops, term: term, baseText: baseText, elemT: elemT, resultT: resultT, isTry: isTry}
	ce.prepare()
	prelude, body, epilogue := ce.runLoopO()
	if cg.passRcv {
		prelude = prelude[1:]
	}
	var parts []otext
	for _, line := range prelude {
		parts = append(parts, line, gen("; "))
	}
	for _, line := range body {
		parts = append(parts, gen(line), gen("; "))
	}
	for _, line := range epilogue {
		parts = append(parts, gen(line), gen("; "))
	}
	loopText := cat(leftoverComments(fl, cg.whole, cg.kept), cat(parts...).trimRight(" "))
	if !isTry {
		text := cg.exprLoop(ce, loopText, baseText)
		native := parseChainExprFrag(fl, text.s, call.Pos())
		fl.stageReplacement(call, native)
		fl.wrapSpan[native] = cg.whole
		fl.addT(cg.whole.start, cg.whole.end, text)
		return
	}
	if cg.enc == nil {
		base.Fatalf("logos: ?-chain outside function at %s", call.Pos().String())
	}
	text := cat(gen("func() ("+ce.resultSpelling()+", error) { "), loopText, gen(" }"))
	temp := cg.prefix + "_t"
	errName := cg.prefix + "_err"
	fail := cg.failText(errName)
	hoistText := cat(gen(temp+", "+errName+" := "), text, gen("(); if "+errName+" != nil { "+fail+" }"))
	if cg.guard != "" {
		hoistText = cat(gen("var "+temp+" "+ce.resultSpelling()+"; if "+cg.guard+" { var "+errName+" error; "+temp+", "+errName+" = "), text, gen("(); if "+errName+" != nil { "+fail+" } }"))
	}
	hoistNative := parseChainStmtFrag(fl, hoistText.s, call.Pos())
	cg.hoists = append(cg.hoists, chainHoist{stmts: hoistNative, text: hoistText})
	native := nameOf(call.Pos(), temp)
	fl.stageReplacement(call, native)
	fl.wrapSpan[native] = cg.whole
	fl.add(cg.whole.start, cg.whole.end, temp)
}

func (cg *chainGen) exprLoop(ce *chainEmit, loopText, baseText otext) otext {
	if !cg.passRcv {
		return cat(gen("func() "+ce.resultSpelling()+" { "), loopText, gen(" }()"))
	}
	head := "func(" + cg.prefix + "_rcv " + cg.spellType(cg.receiverT) + ") " + ce.resultSpelling() + " { "
	return cat(gen(head), loopText, gen(" }("), baseText, gen(")"))
}

func containsCall(e syntax.Expr) bool {
	found := false
	syntax.Inspect(e, func(n syntax.Node) bool {
		_, isCall := n.(*syntax.CallExpr)
		found = found || isCall
		return !found
	})
	return found
}

func (ce *chainEmit) resultSpelling() string {
	cg := ce.cg
	t := ce.resultT
	if s, ok := types2.Unalias(t).(*types2.Slice); ok {
		_ = s
		return "[]" + ce.outElem
	}
	return cg.spellType(t)
}

func parseChainExprFrag(fl *fileLower, text string, pos syntax.Pos) syntax.Expr {
	text = fl.settleMarks(gen(text)).s
	src, marks := fl.expandConstMapFragment("package logoschain\nvar _ = " + text + "\n")
	f, err := syntax.Parse(syntax.NewFileBase("logos-chain"), strings.NewReader(src), nil, nil, 0)
	if err != nil || len(f.DeclList) != 1 {
		base.Fatalf("logos: cannot parse chain fragment %q: %v", text, err)
	}
	fl.bindConstMapFragment(f, src, marks)
	vd, ok := f.DeclList[0].(*syntax.VarDecl)
	if !ok {
		base.Fatalf("logos: cannot parse chain fragment %q", text)
	}
	vals := syntax.UnpackListExpr(vd.Values)
	if len(vals) != 1 {
		base.Fatalf("logos: cannot parse chain fragment %q", text)
	}
	e := vals[0]
	normalizeChainPos(e, pos)
	return e
}

func normalizeChainPos(root syntax.Node, pos syntax.Pos) {
	syntax.Inspect(root, func(n syntax.Node) bool {
		if n == nil {
			return true
		}
		n.SetPos(pos)
		switch t := n.(type) {
		case *syntax.BlockStmt:
			t.Rbrace = pos
		case *syntax.SwitchStmt:
			t.Rbrace = pos
		case *syntax.SelectStmt:
			t.Rbrace = pos
		case *syntax.CaseClause:
			t.Colon = pos
		case *syntax.CommClause:
			t.Colon = pos
		case *syntax.CompositeLit:
			t.Rbrace = pos
		}
		return true
	})
}

func parseChainStmtFrag(fl *fileLower, text string, pos syntax.Pos) []syntax.Stmt {
	text = fl.settleMarks(gen(text)).s
	src, marks := fl.expandConstMapFragment("package logoschain\nfunc _() {\n" + text + "\n}\n")
	f, err := syntax.Parse(syntax.NewFileBase("logos-chain"), strings.NewReader(src), nil, nil, 0)
	if err != nil || len(f.DeclList) != 1 {
		base.Fatalf("logos: cannot parse chain fragment %q: %v", text, err)
	}
	fl.bindConstMapFragment(f, src, marks)
	fd, ok := f.DeclList[0].(*syntax.FuncDecl)
	if !ok || fd.Body == nil {
		base.Fatalf("logos: cannot parse chain fragment %q", text)
	}
	out := fd.Body.List
	for _, s := range out {
		normalizeChainPos(s, pos)
	}
	return out
}

func isUntypedBasic(t types2.Type) bool {
	b, ok := t.(*types2.Basic)
	if !ok {
		return false
	}
	k := b.Kind()
	return k == types2.UntypedBool || k == types2.UntypedInt || k == types2.UntypedRune || k == types2.UntypedFloat || k == types2.UntypedComplex || k == types2.UntypedString
}

func defaultBasicType(t types2.Type) types2.Type {
	b := t.(*types2.Basic)
	switch b.Kind() {
	case types2.UntypedBool:
		return types2.Typ[types2.Bool]
	case types2.UntypedRune:
		return types2.Typ[types2.Rune]
	case types2.UntypedFloat:
		return types2.Typ[types2.Float64]
	case types2.UntypedComplex:
		return types2.Typ[types2.Complex128]
	case types2.UntypedString:
		return types2.Typ[types2.String]
	default:
		return types2.Typ[types2.Int]
	}
}

type chainSyntaxLower struct {
	expr         func(syntax.Expr) (syntax.Expr, []syntax.Stmt)
	lambda       func(*syntax.LambdaExpr, *types2.Signature) *syntax.FuncLit
	workerLambda func(*syntax.LambdaExpr, *types2.Signature) *syntax.FuncLit
	fail         func(syntax.Expr) []syntax.Stmt
	typeExpr     func(syntax.Pos, types2.Type) syntax.Expr
	capture      bool
}

func (fl *fileLower) ctChainCall(e syntax.Expr) bool {
	call, ok := syntax.Unparen(e).(*syntax.CallExpr)
	if !ok {
		return false
	}
	sel, ok := syntax.Unparen(call.Fun).(*syntax.SelectorExpr)
	if !ok || sel.Sel == nil || !types2.LogosChainMethod(sel.Sel.Value) {
		return false
	}
	info := fl.pkg.info
	if info.TypeOf(call) == nil || info.Selections[sel] != nil || info.Types[sel.X].IsType() {
		return false
	}
	recv := ctChainBaseType(parResult(info.TypeOf(sel.X)))
	if _, ok := recv.(*types2.Slice); !ok {
		return false
	}
	obj, _, _ := types2.LookupFieldOrMethod(recv, info.Types[sel.X].Addressable(), fl.pkg.tpkg, sel.Sel.Value)
	return obj == nil
}

func ctChainBaseType(t types2.Type) types2.Type {
	for {
		switch u := types2.Unalias(t).(type) {
		case *types2.ConstView:
			t = u.Elem
		case *types2.ReadonlyDescendants:
			t = u.Elem
		default:
			return u
		}
	}
}

func ctChainElem(t types2.Type) types2.Type {
	if s, ok := ctChainBaseType(parResult(t)).(*types2.Slice); ok {
		return s.Elem()
	}
	return nil
}

func (fl *fileLower) lowerCtChain(call *syntax.CallExpr, lower chainSyntaxLower) (syntax.Expr, []syntax.Stmt) {
	cg := &chainGen{fl: fl, pos: call.Pos(), prefix: fl.cswName("ctchain")}
	ops, recv, term, _ := fl.chainParts(call, fl.ctChainCall)
	slices.Reverse(ops)
	if group, ok := syntax.Unparen(recv).(*syntax.CallExpr); ok && fl.isParStage(group) {
		return cg.emitGroupChainSyntax(call, group, ops, term, lower)
	}
	if fl.ctParChain(recv) {
		return cg.emitGroupTailSyntax(call, recv, ops, term, lower)
	}
	return cg.emitChainSyntax(recv, ops, term, fl.pkg.info.TypeOf(call), lower)
}

func (fl *fileLower) ctParChain(recv syntax.Expr) bool {
	for {
		call, ok := syntax.Unparen(recv).(*syntax.CallExpr)
		if !ok {
			return false
		}
		if fl.isParStage(call) {
			return true
		}
		if !fl.ctChainCall(call) {
			return false
		}
		recv = syntax.Unparen(call.Fun).(*syntax.SelectorExpr).X
	}
}

func (cg *chainGen) emitChainSyntax(recv syntax.Expr, ops []chainOp, term chainOp, result types2.Type, lower chainSyntaxLower) (syntax.Expr, []syntax.Stmt) {
	cg.elemT = ctChainElem(cg.fl.pkg.info.TypeOf(recv))
	ce := newChainSyntax(cg, ops, term, result, lower)
	ce.isTry = cg.runIsTry(ops, term)
	recvExpr, recvPre := lower.expr(recv)
	ce.bind("_rcv", recvExpr, recvPre, recv.Pos())
	ce.bindArgs()
	bindings, outer := ce.splitBindings()
	body := append(bindings, ce.run(ce.name("_rcv"), false)...)
	call := ce.invocation(body)
	if !ce.isTry || lower.capture {
		return call, outer
	}
	return ce.propagate(call, outer)
}

type chainSyntax struct {
	chainEmit
	lower    chainSyntaxLower
	bindings []syntax.Stmt
	outer    bool
	outT     types2.Type
	inTypes  []types2.Type
	slots    []types2.Type
	loop     *syntax.ForStmt
}

func newChainSyntax(cg *chainGen, ops []chainOp, term chainOp, result types2.Type, lower chainSyntaxLower) *chainSyntax {
	ce := &chainSyntax{
		chainEmit: chainEmit{cg: cg, ops: ops, term: term, elemT: cg.elemT, resultT: result},
		lower:     lower,
		outT:      cg.elemT,
		inTypes:   make([]types2.Type, len(ops)+1),
	}
	for i := range ce.ops {
		op := &ce.ops[i]
		ce.inTypes[i] = ce.outT
		if op.method == "map" {
			op.resultT = cg.ctCallbackResult(*op)
			ce.outT = op.resultT
		}
	}
	ce.inTypes[len(ops)] = ce.outT
	ce.term.resultT = cg.ctCallbackResult(ce.term)
	return ce
}

func (cg *chainGen) ctCallbackResult(op chainOp) types2.Type {
	if !takesFunc(op.method) || op.arg == nil {
		return nil
	}
	switch op.method {
	case "filter", "find", "any", "all", "count":
		return types2.Typ[types2.Bool]
	case "map":
		t := cg.fl.pkg.info.TypeOf(op.call)
		if elem := ctChainElem(t); elem != nil {
			return elem
		}
		if sig, ok := t.(*types2.Signature); ok && sig.Params().Len() == 1 {
			if yield, ok := sig.Params().At(0).Type().(*types2.Signature); ok && yield.Params().Len() == 1 {
				return yield.Params().At(0).Type()
			}
		}
	}
	if op.lam == nil {
		return cg.funcResult(op)
	}
	info := cg.fl.pkg.info
	if body, ok := op.lam.Body.(syntax.Expr); ok {
		body = syntax.Unparen(body)
		if t, ok := body.(*syntax.TryExpr); ok {
			if res, ok := info.TypeOf(t.X).(*types2.Tuple); ok && res.Len() == 2 {
				return res.At(0).Type()
			}
		}
		t := info.TypeOf(body)
		if isUntypedBasic(t) {
			return defaultBasicType(t)
		}
		return t
	}
	var result types2.Type
	syntax.Inspect(op.lam.Body, func(n syntax.Node) bool {
		if result != nil {
			return false
		}
		switch n := n.(type) {
		case *syntax.FuncLit, *syntax.LambdaExpr, *syntax.GroupChild:
			return false
		case *syntax.ReturnStmt:
			result = info.TypeOf(n.Results)
			if isUntypedBasic(result) {
				result = defaultBasicType(result)
			}
			return false
		}
		return true
	})
	return result
}

func (ce *chainSyntax) bind(suffix string, x syntax.Expr, pre []syntax.Stmt, pos syntax.Pos) {
	ce.outer = ce.outer || len(pre) != 0
	ce.bindings = append(ce.bindings, pre...)
	ce.bindings = append(ce.bindings, defineOf(pos, []syntax.Expr{nameOf(pos, ce.cg.prefix+suffix)}, []syntax.Expr{x}))
}

func (ce *chainSyntax) splitBindings() (inner, outer []syntax.Stmt) {
	if ce.outer || ce.lower.capture {
		return nil, ce.bindings
	}
	return ce.bindings, nil
}

func (ce *chainSyntax) bindArgs() {
	for i, op := range ce.ops {
		key := strconv.Itoa(i)
		if op.method == "take" || op.method == "drop" {
			ce.bindBound("_n"+key, op.arg, 0)
			continue
		}
		ce.bindCallback("_f"+key, op, ce.inTypes[i], op.isTry)
	}
	if ce.term.method == "get" {
		ce.bindBound("_i", ce.term.arg, ^uint64(0))
	} else if ce.term.arg != nil {
		ce.bindCallback("_ft", ce.term, ce.outT, ce.term.isTry)
	}
}

func (ce *chainSyntax) bindBound(suffix string, orig syntax.Expr, negative uint64) {
	pos := orig.Pos()
	if k, ok := typeConst(ce.cg.fl.pkg.info, syntax.Unparen(orig)); ok {
		n, fits := constant.Uint64Val(k)
		if !fits {
			n = ^uint64(0)
		}
		if constant.Sign(k) < 0 {
			n = negative
		}
		ce.bind(suffix, ce.uint64(litOf(pos, strconv.FormatUint(n, 10), syntax.IntLit)), nil, pos)
		return
	}
	x, pre := ce.lower.expr(orig)
	ce.outer = ce.outer || len(pre) != 0
	ce.bindings = append(ce.bindings, pre...)
	input := ce.name(suffix + "_input")
	ce.bindings = append(ce.bindings, defineOf(pos, []syntax.Expr{input}, []syntax.Expr{x}))
	ce.bindings = append(ce.bindings, ce.declare(suffix, ce.lower.typeExpr(pos, types2.Typ[types2.Uint64])))
	ce.bindings = append(ce.bindings, ifElseOf(pos, nil, ce.op(syntax.Lss, ce.name(suffix+"_input"), ce.number(0)),
		[]syntax.Stmt{ce.assign(ce.name(suffix), litOf(pos, strconv.FormatUint(negative, 10), syntax.IntLit))},
		blockOf(pos, []syntax.Stmt{ce.assign(ce.name(suffix), ce.uint64(ce.name(suffix+"_input")))})))
}

func (ce *chainSyntax) bindCallback(suffix string, op chainOp, in types2.Type, throwing bool) {
	x, pre := ce.lowerCallback(op, in, throwing)
	ce.bind(suffix, x, pre, op.arg.Pos())
}

func (ce *chainSyntax) lowerCallback(op chainOp, in types2.Type, throwing bool) (syntax.Expr, []syntax.Stmt) {
	if op.lam == nil {
		return ce.lower.expr(op.arg)
	}
	return ce.lower.lambda(op.lam, ce.callbackSignature(op, in, throwing)), nil
}

func (ce *chainSyntax) callbackSignature(op chainOp, in types2.Type, throwing bool) *types2.Signature {
	pos := op.lam.Pos()
	pkg := ce.cg.fl.pkg.tpkg
	if obj := ce.cg.fl.pkg.info.Defs[op.lam.Params[0]]; obj != nil {
		in = obj.Type()
	}
	params := types2.NewTuple(types2.NewParam(pos, pkg, op.lam.Params[0].Value, in))
	results := []*types2.Var{types2.NewParam(pos, pkg, "", op.resultT)}
	if throwing {
		results = append(results, types2.NewParam(pos, pkg, "", types2.Universe.Lookup("error").Type()))
	}
	return types2.NewSignatureType(nil, nil, nil, params, types2.NewTuple(results...), false)
}

func (ce *chainSyntax) name(suffix string) *syntax.Name {
	return nameOf(ce.cg.pos, ce.cg.prefix+suffix)
}

func (ce *chainSyntax) builtin(name string) syntax.Expr {
	return ce.cg.fl.ctBuiltin(ce.cg.pos, name)
}

func (ce *chainSyntax) typ(t types2.Type) syntax.Expr {
	return ce.lower.typeExpr(ce.cg.pos, t)
}

func (ce *chainSyntax) number(n int) *syntax.BasicLit {
	if n < 0 {
		panic("logos: cannot form negative chain integer literal")
	}
	return litOf(ce.cg.pos, strconv.Itoa(n), syntax.IntLit)
}

func (ce *chainSyntax) op(op syntax.Operator, x, y syntax.Expr) *syntax.Operation {
	return at(&syntax.Operation{Op: op, X: x, Y: y}, ce.cg.pos)
}

func (ce *chainSyntax) call(fn syntax.Expr, args ...syntax.Expr) *syntax.CallExpr {
	return callOf(ce.cg.pos, fn, args)
}

func (ce *chainSyntax) rt(name string) syntax.Expr {
	return ce.cg.fl.ctPkgSelector(ce.cg.pos, "logos/rt", name)
}

func (ce *chainSyntax) uint64(x syntax.Expr) syntax.Expr {
	return ce.call(ce.typ(types2.Typ[types2.Uint64]), x)
}

func (ce *chainSyntax) index(x, i syntax.Expr) *syntax.IndexExpr {
	return at(&syntax.IndexExpr{X: x, Index: i}, ce.cg.pos)
}

func (ce *chainSyntax) slice(x, lo, hi syntax.Expr) *syntax.SliceExpr {
	return at(&syntax.SliceExpr{X: x, Index: [3]syntax.Expr{lo, hi}}, ce.cg.pos)
}

func (ce *chainSyntax) length(x syntax.Expr) syntax.Expr {
	return ce.call(ce.builtin("len"), x)
}

func (ce *chainSyntax) assign(lhs, rhs syntax.Expr) *syntax.AssignStmt {
	return assignOf(ce.cg.pos, []syntax.Expr{lhs}, []syntax.Expr{rhs})
}

func (ce *chainSyntax) define(suffix string, rhs syntax.Expr) *syntax.AssignStmt {
	return defineOf(ce.cg.pos, []syntax.Expr{ce.name(suffix)}, []syntax.Expr{rhs})
}

func (ce *chainSyntax) declare(suffix string, typ syntax.Expr) *syntax.DeclStmt {
	pos := ce.cg.pos
	decl := at(&syntax.VarDecl{NameList: []*syntax.Name{ce.name(suffix)}, Type: typ}, pos)
	return at(&syntax.DeclStmt{DeclList: []syntax.Decl{decl}}, pos)
}

func (ce *chainSyntax) stepCounter(suffix string, op syntax.Operator) *syntax.AssignStmt {
	return at(&syntax.AssignStmt{Lhs: ce.name(suffix), Op: op}, ce.cg.pos)
}

func (ce *chainSyntax) cond(test syntax.Expr, body ...syntax.Stmt) *syntax.IfStmt {
	return ifOf(ce.cg.pos, nil, test, body)
}

func (ce *chainSyntax) literal(params, results []*syntax.Field, body []syntax.Stmt) *syntax.FuncLit {
	pos := ce.cg.pos
	ft := at(&syntax.FuncType{ParamList: params, ResultList: results}, pos)
	return at(&syntax.FuncLit{Type: ft, Body: blockOf(pos, body)}, pos)
}

func (ce *chainSyntax) field(name *syntax.Name, t syntax.Expr) *syntax.Field {
	return at(&syntax.Field{Name: name, Type: t}, ce.cg.pos)
}

func (ce *chainSyntax) errorType() syntax.Expr {
	return ce.typ(types2.Universe.Lookup("error").Type())
}

func (ce *chainSyntax) invocation(body []syntax.Stmt) *syntax.CallExpr {
	return ce.call(ce.chainLiteral(body))
}

func (ce *chainSyntax) resultFields() []*syntax.Field {
	results := []*syntax.Field{ce.field(nil, ce.typ(ce.resultT))}
	if ce.isTry {
		results = append(results, ce.field(nil, ce.errorType()))
	}
	return results
}

func (ce *chainSyntax) chainLiteral(body []syntax.Stmt) *syntax.FuncLit {
	return ce.literal(nil, ce.resultFields(), body)
}

func (ce *chainSyntax) propagate(call syntax.Expr, pre []syntax.Stmt) (syntax.Expr, []syntax.Stmt) {
	pos := ce.cg.pos
	bind := defineOf(pos, []syntax.Expr{ce.name("_value"), ce.name("_err")}, []syntax.Expr{call})
	check := ce.cond(ce.op(syntax.Neq, ce.name("_err"), ce.builtin("nil")), ce.lower.fail(ce.name("_err"))...)
	return ce.name("_value"), append(pre, bind, check)
}

func (ce *chainSyntax) run(src syntax.Expr, grouped bool) []syntax.Stmt {
	pos := ce.cg.pos
	ce.ownership()
	if len(ce.ops) == 0 && !grouped {
		switch ce.term.method {
		case "sum":
			if !ce.integerSum() {
				return ce.sum(src)
			}
		case "count":
			if ce.term.arg == nil {
				return []syntax.Stmt{ce.success(ce.length(src))}
			}
		}
	}
	var body []syntax.Stmt
	if ce.collects() {
		body = append(body, ce.outputDecl(grouped))
	}
	body = append(body, ce.resourcePrelude()...)
	if grouped || ce.cg.runIsTry(ce.ops, ce.term) {
		body = append(body, ce.declare("_zero", ce.typ(ce.resultT)))
	}
	switch ce.term.method {
	case "count":
		body = append(body, ce.define("_count", ce.number(0)))
	case "sum":
		if ce.integerSum() {
			body = append(body, ce.declare("_total", ce.typ(ce.outT)))
		}
	case "get", "last":
		if !ce.owns {
			body = append(body, ce.declare("_selected", ce.typ(ce.resultT)))
			if ce.term.method == "get" {
				body = append(body, ce.define("_offset", ce.uint64(ce.number(0))))
			}
		}
	}
	for i, op := range ce.ops {
		if op.method == "take" || op.method == "drop" {
			body = append(body, ce.define("_c"+strconv.Itoa(i), ce.uint64(ce.number(0))))
		}
	}

	var lhs syntax.Expr
	if ce.needElem() {
		lhs = listOf(pos, []syntax.Expr{nameOf(pos, "_"), ce.name("_elem")})
	}
	if grouped {
		elem := syntax.Expr(ce.name("_elem"))
		if lhs == nil {
			elem = nameOf(pos, "_")
		}
		lhs = listOf(pos, []syntax.Expr{elem, ce.name("_sourceErr")})
	}
	ce.loop = at(&syntax.ForStmt{Init: at(&syntax.RangeClause{Lhs: lhs, Def: lhs != nil, X: src}, pos)}, pos)
	ce.loop.Body = blockOf(pos, ce.iteration(grouped))
	body = append(body, ce.loop)
	return append(body, ce.finish()...)
}

func (ce *chainSyntax) iteration(grouped bool) []syntax.Stmt {
	var body []syntax.Stmt
	for i, op := range ce.ops {
		if op.method != "take" {
			continue
		}
		key := strconv.Itoa(i)
		test := ce.op(syntax.Geq, ce.name("_c"+key), ce.name("_n"+key))
		stop := []syntax.Stmt{ce.branch(syntax.Break)}
		if grouped && ce.ownedIn {
			owned := ce.op(syntax.Eql, ce.name("_sourceErr"), ce.builtin("nil"))
			close := ce.cond(owned, ce.closeSyntax(ce.name("_elem"), ce.inTypes[0])...)
			stop = append([]syntax.Stmt{close}, stop...)
		}
		body = append(body, ce.cond(test, stop...))
	}
	if grouped {
		body = append(body, ce.callbackError("_sourceErr"))
	}
	if ce.ownedIn {
		switch {
		case ce.lastMap < 0:
			body = append(body, ce.collect(ce.name("_elem")))
		case ce.chainEmit.flight:
			body = append(body, ce.setSlot(ce.name("_elem"), ce.inTypes[0])...)
		}
	}
	cur := "_elem"
	for i, op := range ce.ops {
		key := strconv.Itoa(i)
		switch op.method {
		case "take":
			body = append(body, ce.stepCounter("_c"+key, syntax.Add))
		case "drop":
			test := ce.op(syntax.Lss, ce.name("_c"+key), ce.name("_n"+key))
			skip := append([]syntax.Stmt{ce.stepCounter("_c"+key, syntax.Add)}, ce.dropElement(cur, i)...)
			body = append(body, ce.cond(test, skip...))
		case "filter":
			value, pre := ce.callbackValue("_f"+key, "_filter"+key, cur, op.isTry)
			body = append(body, pre...)
			body = append(body, ce.cond(ce.op(syntax.Not, value, nil), ce.dropElement(cur, i)...))
		case "map":
			if ce.chainEmit.flight && ce.ownBefore[i] {
				body = append(body, ce.clearSlot(ce.inTypes[i])...)
			}
			mapped := "_map" + key
			value, pre := ce.callbackValue("_f"+key, mapped, cur, op.isTry)
			body = append(body, pre...)
			if !op.isTry {
				body = append(body, ce.define(mapped, value))
			}
			cur = mapped
			if ce.chainEmit.flight && i < ce.lastMap && ce.ownBefore[i+1] {
				body = append(body, ce.setSlot(ce.name(cur), ce.inTypes[i+1])...)
			}
			if ce.owns && i == ce.lastMap {
				body = append(body, ce.collect(ce.name(cur)))
			}
		}
	}
	return append(body, ce.terminal(cur)...)
}

func (ce *chainSyntax) branch(tok syntax.Token) *syntax.BranchStmt {
	return at(&syntax.BranchStmt{Tok: tok, Target: ce.loop}, ce.cg.pos)
}

func (ce *chainSyntax) collects() bool {
	return ce.owns || ce.term.method == "" || ce.term.method == "sortBy" || (ce.term.method == "sum" && !ce.integerSum())
}

func (ce *chainSyntax) integerSum() bool {
	b, ok := ctChainBaseType(ce.outT).(*types2.Basic)
	return ok && b.Info()&types2.IsInteger != 0
}

func (ce *chainSyntax) outputDecl(grouped bool) syntax.Stmt {
	t := types2.NewSlice(ce.outT)
	if ce.term.method != "" || grouped {
		return ce.declare("_out", ce.typ(t))
	}
	cap, ok := ce.chainEmit.capBound()
	if !ok {
		return ce.declare("_out", ce.typ(t))
	}
	hint := ce.length(ce.name("_rcv"))
	if cap >= 0 {
		n := litOf(ce.cg.pos, strconv.FormatInt(cap, 10), syntax.IntLit)
		bound := ce.call(ce.builtin("min"), n, ce.uint64(hint))
		hint = ce.call(ce.typ(types2.Typ[types2.Int]), bound)
	}
	return ce.define("_out", ce.call(ce.builtin("make"), ce.typ(t), ce.number(0), hint))
}

func (ce *chainSyntax) collect(value syntax.Expr) *syntax.AssignStmt {
	return ce.assign(ce.name("_out"), ce.call(ce.builtin("append"), ce.name("_out"), value))
}

func (ce *chainSyntax) callbackValue(fn, dest, cur string, throwing bool) (syntax.Expr, []syntax.Stmt) {
	call := ce.call(ce.name(fn), ce.name(cur))
	if !throwing {
		return call, nil
	}
	err := dest + "_err"
	bind := defineOf(ce.cg.pos, []syntax.Expr{ce.name(dest), ce.name(err)}, []syntax.Expr{call})
	return ce.name(dest), []syntax.Stmt{bind, ce.callbackError(err)}
}

func (ce *chainSyntax) callbackError(err string) *syntax.IfStmt {
	body := ce.rollbackSyntax()
	if ce.guarded() {
		body = append(body, ce.done())
	}
	body = append(body, returnOf(ce.cg.pos, []syntax.Expr{ce.name("_zero"), ce.name(err)}))
	return ce.cond(ce.op(syntax.Neq, ce.name(err), ce.builtin("nil")), body...)
}

func (ce *chainSyntax) success(value syntax.Expr) *syntax.ReturnStmt {
	values := []syntax.Expr{value}
	if ce.isTry {
		values = append(values, ce.builtin("nil"))
	}
	return returnOf(ce.cg.pos, values)
}

func (ce *chainSyntax) option(value syntax.Expr) syntax.Expr {
	if ce.bareOption() {
		if value != nil {
			return value
		}
		return ce.builtin("nil")
	}
	pos := ce.cg.pos
	lit := at(&syntax.CompositeLit{Type: ce.typ(ce.resultT), Rbrace: pos}, pos)
	if value != nil {
		lit.NKeys = 2
		lit.ElemList = []syntax.Expr{
			at(&syntax.KeyValueExpr{Key: nameOf(pos, "Value"), Value: value}, pos),
			at(&syntax.KeyValueExpr{Key: nameOf(pos, "Present"), Value: ce.builtin("true")}, pos),
		}
	}
	return lit
}

func (ce *chainSyntax) terminal(cur string) []syntax.Stmt {
	var body []syntax.Stmt
	switch ce.term.method {
	case "sum":
		if ce.integerSum() {
			total := ce.op(syntax.Add, ce.name("_total"), ce.name(cur))
			return []syntax.Stmt{ce.assign(ce.name("_total"), total)}
		}
	case "first":
		return ce.hand(ce.lastIndex(), ce.option(ce.name(cur)))
	case "last", "get":
		if ce.owns {
			return nil
		}
		use := ce.assign(ce.name("_selected"), ce.option(ce.name(cur)))
		if ce.term.method == "last" {
			return []syntax.Stmt{use}
		}
		test := ce.op(syntax.Eql, ce.name("_offset"), ce.name("_i"))
		return []syntax.Stmt{ce.cond(test, use), ce.stepCounter("_offset", syntax.Add)}
	case "find", "any", "all", "count":
		if ce.term.arg == nil {
			if ce.hasMap() && !ce.owns {
				body = append(body, ce.assign(nameOf(ce.cg.pos, "_"), ce.name(cur)))
			}
			return append(body, ce.stepCounter("_count", syntax.Add))
		}
		ok, pre := ce.callbackValue("_ft", "_tested", cur, ce.term.isTry)
		body = append(body, pre...)
		var hit []syntax.Stmt
		switch ce.term.method {
		case "find":
			hit = ce.hand(ce.lastIndex(), ce.option(ce.name(cur)))
		case "any":
			hit = ce.hand(nil, ce.builtin("true"))
		case "all":
			hit = ce.hand(nil, ce.builtin("false"))
			ok = ce.op(syntax.Not, ok, nil)
		case "count":
			hit = []syntax.Stmt{ce.stepCounter("_count", syntax.Add)}
		}
		return append(body, ce.cond(ok, hit...))
	}
	if !ce.owns {
		body = append(body, ce.collect(ce.name(cur)))
	}
	return body
}

func (ce *chainSyntax) finish() []syntax.Stmt {
	switch ce.term.method {
	case "find", "first":
		return ce.hand(nil, ce.option(nil))
	case "any":
		return ce.hand(nil, ce.builtin("false"))
	case "all":
		return ce.hand(nil, ce.builtin("true"))
	case "count":
		return ce.hand(nil, ce.name("_count"))
	case "last", "get":
		return ce.finishSelection()
	case "sum":
		if ce.integerSum() {
			return []syntax.Stmt{ce.success(ce.name("_total"))}
		}
		return ce.sum(ce.name("_out"))
	case "sortBy":
		return ce.sort()
	}
	out := ce.name("_out")
	empty := ce.cond(ce.op(syntax.Eql, ce.length(ce.name("_out")), ce.number(0)), ce.assign(out, ce.builtin("nil")))
	return append([]syntax.Stmt{empty}, ce.handSliceSyntax(ce.name("_out"))...)
}

func (ce *chainSyntax) lastIndex() syntax.Expr {
	return ce.op(syntax.Sub, ce.length(ce.name("_out")), ce.number(1))
}

func (ce *chainSyntax) finishSelection() []syntax.Stmt {
	if !ce.owns {
		return []syntax.Stmt{ce.success(ce.name("_selected"))}
	}
	var miss syntax.Expr
	index := func() syntax.Expr { return ce.lastIndex() }
	if ce.term.method == "last" {
		miss = ce.op(syntax.Eql, ce.length(ce.name("_out")), ce.number(0))
	} else {
		miss = ce.op(syntax.Geq, ce.name("_i"), ce.uint64(ce.length(ce.name("_out"))))
		index = func() syntax.Expr { return ce.call(ce.typ(types2.Typ[types2.Int]), ce.name("_i")) }
	}
	absent := ce.cond(miss, ce.hand(nil, ce.option(nil))...)
	return append([]syntax.Stmt{absent}, ce.hand(index(), ce.option(ce.index(ce.name("_out"), index())))...)
}

func (ce *chainSyntax) sum(src syntax.Expr) []syntax.Stmt {
	pos := ce.cg.pos
	sliceT := types2.NewSlice(ce.outT)
	sig := at(&syntax.FuncType{ParamList: []*syntax.Field{ce.field(nil, ce.typ(sliceT))}, ResultList: []*syntax.Field{ce.field(nil, ce.typ(ce.outT))}}, pos)
	sum := ce.name("_sum")
	xs := "_items"
	x := func() syntax.Expr { return ce.name(xs) }
	short := func(n int, value syntax.Expr) *syntax.CaseClause {
		return at(&syntax.CaseClause{Cases: ce.number(n), Body: []syntax.Stmt{returnOf(pos, []syntax.Expr{value})}, Colon: pos}, pos)
	}
	lo := ce.call(ce.name("_sum"), ce.slice(x(), nil, ce.name("_mid")))
	hi := ce.call(ce.name("_sum"), ce.slice(x(), ce.name("_mid"), nil))
	split := at(&syntax.CaseClause{Body: []syntax.Stmt{
		ce.define("_mid", ce.op(syntax.Div, ce.length(x()), ce.number(2))),
		returnOf(pos, []syntax.Expr{ce.op(syntax.Add, lo, hi)}),
	}, Colon: pos}, pos)
	sw := at(&syntax.SwitchStmt{Tag: ce.length(x()), Body: []*syntax.CaseClause{short(0, ce.number(0)), short(1, ce.index(x(), ce.number(0))), split}, Rbrace: pos}, pos)
	lit := ce.literal([]*syntax.Field{ce.field(ce.name(xs), ce.typ(sliceT))}, []*syntax.Field{ce.field(nil, ce.typ(ce.outT))}, []syntax.Stmt{sw})
	body := []syntax.Stmt{ce.declare("_sum", sig), ce.assign(sum, lit)}
	return append(body, ce.handSliceSyntax(ce.call(ce.name("_sum"), src))...)
}

func (ce *chainSyntax) sort() []syntax.Stmt {
	if !ce.term.isTry {
		compare := func(x string) syntax.Expr { return ce.call(ce.name("_ft"), ce.name(x)) }
		sort := ce.sortBy(ce.name("_out"), ce.outT, compare)
		return append([]syntax.Stmt{sort}, ce.handSliceSyntax(ce.name("_out"))...)
	}
	pos := ce.cg.pos
	length := func() syntax.Expr { return ce.length(ce.name("_out")) }
	keys := ce.call(ce.builtin("make"), ce.typ(types2.NewSlice(ce.term.resultT)), length())
	indices := ce.call(ce.builtin("make"), ce.typ(types2.NewSlice(types2.Typ[types2.Int])), length())
	fetchKey := ce.call(ce.name("_ft"), ce.index(ce.name("_out"), ce.name("_i")))
	bindKey := defineOf(pos, []syntax.Expr{ce.name("_key"), ce.name("_keyErr")}, []syntax.Expr{fetchKey})
	readKeys := ce.rangeSyntax(ce.name("_i"), ce.name("_out"), []syntax.Stmt{
		bindKey, ce.callbackError("_keyErr"), ce.assign(ce.index(ce.name("_keys"), ce.name("_i")), ce.name("_key")),
		ce.assign(ce.index(ce.name("_indices"), ce.name("_i")), ce.name("_i")),
	})
	compare := func(x string) syntax.Expr { return ce.index(ce.name("_keys"), ce.name(x)) }
	sort := ce.sortBy(ce.name("_indices"), types2.Typ[types2.Int], compare)
	sorted := ce.call(ce.builtin("make"), ce.typ(types2.NewSlice(ce.outT)), length())
	lhs := listOf(pos, []syntax.Expr{ce.name("_i"), ce.name("_from")})
	order := ce.rangeSyntax(lhs, ce.name("_indices"), []syntax.Stmt{
		ce.assign(ce.index(ce.name("_sorted"), ce.name("_i")), ce.index(ce.name("_out"), ce.name("_from"))),
	})
	body := []syntax.Stmt{ce.define("_keys", keys), ce.define("_indices", indices), readKeys, sort, ce.define("_sorted", sorted), order}
	return append(body, ce.handSliceSyntax(ce.name("_sorted"))...)
}

func (ce *chainSyntax) rangeSyntax(lhs, src syntax.Expr, body []syntax.Stmt) *syntax.ForStmt {
	pos := ce.cg.pos
	return at(&syntax.ForStmt{Init: at(&syntax.RangeClause{Lhs: lhs, Def: lhs != nil, X: src}, pos), Body: blockOf(pos, body)}, pos)
}

func (ce *chainSyntax) sortBy(src syntax.Expr, t types2.Type, key func(string) syntax.Expr) syntax.Stmt {
	pos := ce.cg.pos
	params := []*syntax.Field{ce.field(ce.name("_a"), ce.typ(t)), ce.field(ce.name("_b"), ce.typ(t))}
	result := []*syntax.Field{ce.field(nil, ce.typ(types2.Typ[types2.Int]))}
	compare := ce.call(ce.rt("Compare"), key("_a"), key("_b"))
	lit := ce.literal(params, result, []syntax.Stmt{returnOf(pos, []syntax.Expr{compare})})
	return exprStmtOf(pos, ce.call(ce.rt("SliceStableFunc"), src, lit))
}

func (cg *chainGen) emitGroupChainSyntax(call, head *syntax.CallExpr, ops []chainOp, term chainOp, lower chainSyntaxLower) (syntax.Expr, []syntax.Stmt) {
	group := cg.groupSyntax(head, ops, lower)
	cg.elemT = group.stage.resT
	result := parResult(cg.fl.pkg.info.TypeOf(call))
	ce := newChainSyntax(cg, group.stage.post, term, result, lower)
	ce.isTry, ce.ownedIn = true, group.stage.ownedOut
	ce.bindings, ce.outer = group.bindings, group.outer
	src := group.sequence()
	ce.bindArgs()
	bindings, outer := ce.splitBindings()
	body := append(bindings, ce.run(src, true)...)
	return ce.invocation(body), outer
}

func (cg *chainGen) emitGroupTailSyntax(call *syntax.CallExpr, recv syntax.Expr, ops []chainOp, term chainOp, lower chainSyntaxLower) (syntax.Expr, []syntax.Stmt) {
	pos := cg.pos
	cg.elemT = ctChainElem(cg.fl.pkg.info.TypeOf(recv))
	result := parResult(cg.fl.pkg.info.TypeOf(call))
	ce := newChainSyntax(cg, ops, term, result, lower)
	ce.isTry = true
	source, pre := lower.expr(recv)
	bind := defineOf(pos, []syntax.Expr{ce.name("_rcv"), ce.name("_inputErr")}, []syntax.Expr{source})
	ce.bindArgs()
	body := ce.run(ce.name("_rcv"), false)
	if lower.capture {
		delay := at(&syntax.FuncType{ResultList: ce.resultFields()}, pos)
		captureTail := ce.assign(ce.name("_delayed"), ce.chainLiteral(body))
		runTail := ce.cond(ce.op(syntax.Eql, ce.name("_inputErr"), ce.builtin("nil")), append(ce.bindings, captureTail)...)
		pre = append(pre, bind, ce.declare("_delayed", delay), runTail)
		bail := ce.cond(ce.op(syntax.Neq, ce.name("_inputErr"), ce.builtin("nil")),
			returnOf(pos, []syntax.Expr{ce.zero(result), ce.name("_inputErr")}))
		return ce.invocation([]syntax.Stmt{bail, returnOf(pos, []syntax.Expr{ce.call(ce.name("_delayed"))})}), pre
	}
	if !ce.outer {
		bail := ce.cond(ce.op(syntax.Neq, ce.name("_inputErr"), ce.builtin("nil")),
			returnOf(pos, []syntax.Expr{ce.zero(result), ce.name("_inputErr")}))
		prefix := append([]syntax.Stmt{bind, bail}, ce.bindings...)
		return ce.invocation(append(prefix, body...)), pre
	}
	bindResult := assignOf(pos, []syntax.Expr{ce.name("_after"), ce.name("_inputErr")},
		[]syntax.Expr{ce.invocation(body)})
	runTail := ce.cond(ce.op(syntax.Eql, ce.name("_inputErr"), ce.builtin("nil")), append(ce.bindings, bindResult)...)
	pre = append(pre, bind, ce.declare("_after", ce.typ(result)), runTail)
	ret := returnOf(pos, []syntax.Expr{ce.name("_after"), ce.name("_inputErr")})
	return ce.invocation([]syntax.Stmt{ret}), pre
}

type groupChainSyntax struct {
	*chainSyntax
	stage *parStage
}

func (cg *chainGen) groupSyntax(head *syntax.CallExpr, ops []chainOp, lower chainSyntaxLower) groupChainSyntax {
	fl := cg.fl
	group, unordered, elem, _ := fl.parHead(head)
	recv := syntax.Unparen(group.Fun).(*syntax.SelectorExpr).X
	limit := group.ArgList[1].(*syntax.KeyValueExpr).Value
	cg.elemT = elem
	n := 0
	for n < len(ops) && (ops[n].method == "map" || ops[n].method == "filter") {
		n++
	}
	ce := newChainSyntax(cg, ops[:n], chainOp{}, nil, lower)
	stage := &parStage{
		unordered: unordered, source: fl.parSourceOf(recv),
		elemT: elem, resT: ce.outT, ctx: group.ArgList[0],
		seg: ce.ops, segT: ce.inTypes[1:], post: ops[n:],
	}
	stage.ownership()
	for _, arg := range []struct {
		suffix string
		node   syntax.Expr
	}{
		{suffix: "_source", node: recv},
		{suffix: "_ctx", node: stage.ctx},
		{suffix: "_limit", node: limit},
	} {
		x, pre := lower.expr(arg.node)
		ce.bind(arg.suffix, x, pre, arg.node.Pos())
	}
	for i, op := range stage.seg {
		if op.lam == nil {
			ce.bindCallback("_worker"+strconv.Itoa(i), op, stage.inType(i), false)
		}
	}
	return groupChainSyntax{chainSyntax: ce, stage: stage}
}

func (g groupChainSyntax) sequence() syntax.Expr {
	pos := g.cg.pos
	verb := "GroupSeq"
	var src syntax.Expr = g.name("_source")
	switch g.stage.source {
	case parChan:
		verb = "GroupChan"
	case parSlice:
		src = g.sliceSequence()
	case parSeq:
		src = g.call(g.sequenceType(), src)
	}
	if g.stage.unordered {
		verb += "Unordered"
	}
	var discard string
	switch t := ctChainBaseType(g.stage.resT); {
	case !plainMethodSet(t):
		discard = "CloseResultIfCloser"
	case types2.Implements(t, closerInterface()):
		discard = "CloseResult"
	default:
		discard = "LeaveResultOpen"
	}
	discardType := g.index(g.rt(discard), g.typ(g.stage.resT))
	args := []syntax.Expr{g.typ(g.stage.elemT), g.typ(g.stage.resT), discardType}
	fun := g.index(g.rt(verb), listOf(pos, args))
	return g.call(fun, g.name("_ctx"), g.name("_limit"), src, g.worker())
}

func (g groupChainSyntax) sliceSequence() *syntax.FuncLit {
	pos := g.cg.pos
	yield := g.name("_yield")
	value := g.name("_sourceElem")
	params := g.sequenceType().ParamList
	params[0].Name = yield
	lhs := listOf(pos, []syntax.Expr{nameOf(pos, "_"), value})
	sent := g.call(g.name("_yield"), g.name("_sourceElem"))
	loop := g.rangeSyntax(lhs, g.name("_source"), []syntax.Stmt{
		g.cond(g.op(syntax.Not, sent, nil), returnOf(pos, nil)),
	})
	return g.literal(params, nil, []syntax.Stmt{loop})
}

func (g groupChainSyntax) sequenceType() *syntax.FuncType {
	pos := g.cg.pos
	yield := at(&syntax.FuncType{
		ParamList:  []*syntax.Field{g.field(nil, g.typ(g.stage.elemT))},
		ResultList: []*syntax.Field{g.field(nil, g.typ(types2.Typ[types2.Bool]))},
	}, pos)
	return at(&syntax.FuncType{ParamList: []*syntax.Field{g.field(nil, yield)}}, pos)
}

func (g groupChainSyntax) worker() *syntax.FuncLit {
	pos := g.cg.pos
	params := []*syntax.Field{
		g.field(nameOf(pos, parCtxName), g.rt("Context")),
		g.field(g.name("_item"), g.typ(g.stage.elemT)),
	}
	results := []*syntax.Field{
		g.field(nil, g.typ(g.stage.resT)),
		g.field(nil, g.typ(types2.Typ[types2.Bool])),
		g.field(nil, g.errorType()),
	}
	var body []syntax.Stmt
	if alias, ok := plainContextName(g.stage.ctx); ok {
		body = append(body, defineOf(pos, []syntax.Expr{nameOf(pos, alias)}, []syntax.Expr{nameOf(pos, parCtxName)}),
			g.assign(nameOf(pos, "_"), nameOf(pos, alias)))
	}
	cur := "_item"
	for i, op := range g.stage.seg {
		key := strconv.Itoa(i)
		fn := "_worker" + key
		if op.lam != nil {
			lit := g.lower.workerLambda(op.lam, g.callbackSignature(op, g.stage.inType(i), true))
			body = append(body, g.define(fn, lit))
		}
		next := "_step" + key
		value, stmts := g.workerCall(fn, next, cur, i)
		body = append(body, stmts...)
		if op.method == "filter" {
			body = append(body, g.cond(g.op(syntax.Not, value, nil), g.workerDrop(i, cur)...))
			continue
		}
		if op.lam == nil {
			body = append(body, g.define(next, value))
		}
		cur = next
	}
	err := g.builtin("nil")
	if logosMayBeCloser(g.stage.resT) && !g.stage.ownedOut {
		err = g.rt("ErrNotOwned")
	}
	body = append(body, returnOf(pos, []syntax.Expr{g.name(cur), g.builtin("true"), err}))
	return g.literal(params, results, body)
}

func (g groupChainSyntax) workerCall(fn, next, cur string, i int) (syntax.Expr, []syntax.Stmt) {
	call := g.call(g.name(fn), g.name(cur))
	op := g.stage.seg[i]
	if op.lam == nil {
		return call, nil
	}
	pos := g.cg.pos
	err := next + "_err"
	bind := defineOf(pos, []syntax.Expr{g.name(next), g.name(err)}, []syntax.Expr{call})
	var fail []syntax.Stmt
	in := g.stage.inType(i)
	if op.method == "filter" && g.stage.owned[i] && logosMayBeCloser(in) {
		fail = g.closeSyntax(g.name(cur), in)
	}
	fail = append(fail, returnOf(pos, []syntax.Expr{g.zero(g.stage.resT), g.builtin("false"), g.name(err)}))
	check := g.cond(g.op(syntax.Neq, g.name(err), g.builtin("nil")), fail...)
	return g.name(next), []syntax.Stmt{bind, check}
}

func (g groupChainSyntax) workerDrop(i int, cur string) []syntax.Stmt {
	pos := g.cg.pos
	st := g.stage
	in := st.inType(i)
	laterMap := parMapAfter(st.seg, i)
	closes := st.owned[i] && logosMayBeCloser(in)
	noErr := g.builtin("nil")
	reject := func(value, err syntax.Expr) *syntax.ReturnStmt {
		return returnOf(pos, []syntax.Expr{value, g.builtin("false"), err})
	}
	switch {
	case closes && !laterMap:
		return []syntax.Stmt{reject(g.name(cur), noErr)}
	case closes:
		return append(g.closeSyntax(g.name(cur), in), reject(g.zero(st.resT), g.rt("ErrNotOwned")))
	case logosMayBeCloser(st.resT):
		return []syntax.Stmt{reject(g.zero(st.resT), g.rt("ErrNotOwned"))}
	case laterMap:
		return []syntax.Stmt{reject(g.zero(st.resT), noErr)}
	default:
		return []syntax.Stmt{reject(g.name(cur), noErr)}
	}
}

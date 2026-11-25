// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"go/token"
	"slices"
	"strconv"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

type chainGroup struct {
	inGroup bool
	inChild bool
}

func chainChildResults() []resultSpec {
	return []resultSpec{{typ: types2.Universe.Lookup("error").Type()}}
}

func (fl *fileLower) lowerChainSpecial(s syntax.Stmt, enc []resultSpec) ([]syntax.Stmt, bool) {
	inner := s
	for {
		lab, ok := inner.(*syntax.LabeledStmt)
		if !ok {
			break
		}
		inner = lab.Stmt
	}
	switch inner := inner.(type) {
	case *syntax.GroupStmt, *syntax.GroupChild:
		return fl.lowerChainGroup(s, inner, enc), true
	case *syntax.ForStmt:
		return fl.lowerChainFor(s, inner, enc), true
	case *syntax.IfStmt:
		if inner.Init != nil {
			return fl.lowerChainIf(s, inner, enc), true
		}
	case *syntax.SwitchStmt:
		return fl.lowerChainSwitch(s, inner, enc), true
	case *syntax.SelectStmt:
		return fl.lowerChainSelect(s, inner, enc), true
	}
	return nil, false
}

func recvAssign(c *syntax.CommClause) *syntax.AssignStmt {
	as, ok := c.Comm.(*syntax.AssignStmt)
	if !ok || as.Op != 0 {
		return nil
	}
	if op, ok := syntax.Unparen(as.Rhs).(*syntax.Operation); ok && op.Op == syntax.Recv && op.Y == nil {
		return as
	}
	return nil
}

func (fl *fileLower) lowerChainSelect(s syntax.Stmt, ss *syntax.SelectStmt, enc []resultSpec) []syntax.Stmt {
	targets := make([][]chainHoist, len(ss.Body))
	for i, c := range ss.Body {
		if as := recvAssign(c); as != nil {
			targets[i] = fl.lowerChainPiece(as.Lhs, enc)
		}
	}
	out := fl.lowerChainPlain(s, enc)
	for i, c := range ss.Body {
		if len(targets[i]) > 0 {
			fl.splitRecvTarget(c, recvAssign(c), targets[i])
		}
	}
	return out
}

func (fl *fileLower) splitRecvTarget(c *syntax.CommClause, as *syntax.AssignStmt, hoists []chainHoist) {
	pos := c.Pos()
	from := fl.off(pos) + len("case")
	to := fl.off(c.Colon)
	eq, eqEnd, depth := -1, -1, 0
	scanSegment(fl.src, from, to, func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		switch t.kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		case token.ASSIGN:
			if depth == 0 && eq < 0 {
				eq, eqEnd = t.start, t.end
			}
		}
		return true
	})
	if eq < 0 {
		base.Fatalf("logos: cannot locate the receive assignment at %s", pos.String())
	}
	lhs, rhs := fl.tokenSpan(from, eq), fl.tokenSpan(eqEnd, to)
	pfx := fmt.Sprintf("_logos_rv_%d_%d", pos.Line(), pos.Col())
	var names []string
	var temps, vals []syntax.Expr
	for i := range syntax.UnpackListExpr(as.Lhs) {
		name := fmt.Sprintf("%s_%d", pfx, i)
		names = append(names, name)
		temps = append(temps, nameOf(pos, name))
		vals = append(vals, nameOf(pos, name))
	}
	list := strings.Join(names, ", ")
	region := span{start: fl.off(pos), end: to + 1}
	parts := []otext{leftoverComments(fl, region, []span{lhs, rhs}), gen("case " + list + " := " + fl.copyRegion(rhs) + ": ")}
	var prefix []syntax.Stmt
	for _, h := range hoists {
		parts = append(parts, h.text, gen("; "))
		prefix = append(prefix, h.stmts...)
	}
	anchorScopes(prefix, pos)
	parts = append(parts, gen(fl.copyRegion(lhs)+" = "+list+"; "))
	fl.addT(region.start, region.end, cat(parts...))
	prefix = append(prefix, assignOf(pos, syntax.UnpackListExpr(as.Lhs), vals))
	c.Comm = defineOf(pos, temps, []syntax.Expr{as.Rhs})
	c.Body = append(prefix, c.Body...)
}

func (fl *fileLower) lowerChainGroup(s syntax.Stmt, inner syntax.Stmt, enc []resultSpec) []syntax.Stmt {
	saved := fl.chainGrp
	encBody := enc
	bodyGrp := chainGroup{inGroup: true}
	if _, isChild := inner.(*syntax.GroupChild); isChild || saved.inGroup || saved.inChild {
		encBody = chainChildResults()
		bodyGrp = chainGroup{inGroup: true, inChild: true}
	}
	var out []syntax.Stmt
	var body *syntax.BlockStmt
	if gs, ok := inner.(*syntax.GroupStmt); ok {
		body = gs.Body
		for _, e := range []syntax.Expr{gs.Ctx, gs.Limit} {
			if e == nil {
				continue
			}
			for _, h := range fl.lowerChainPiece(e, enc) {
				out = append(out, h.stmts...)
				fl.addT(fl.stmtStartOf(s), fl.stmtStartOf(s), cat(h.text, gen("; ")))
			}
		}
	} else {
		body = inner.(*syntax.GroupChild).Body
	}
	fl.chainGrp = bodyGrp
	body.List = fl.lowerChainList(body.List, encBody)
	fl.chainGrp = saved
	return append(out, s)
}

func (fl *fileLower) lowerChainHeadLits(n syntax.Node) {
	saved := fl.chainGrp
	syntax.Inspect(n, func(x syntax.Node) bool {
		if lit, ok := x.(*syntax.FuncLit); ok {
			if fl.transparent[lit] {
				return true
			}
			savedExit := fl.chainExit
			fl.chainGrp = chainGroup{}
			fl.chainExit = nil
			lit.Body.List = fl.lowerChainList(lit.Body.List, fl.resultSpecs(lit.Type, fl.cfLitSig(lit)))
			fl.chainGrp = saved
			fl.chainExit = savedExit
			return false
		}
		return true
	})
}

func (fl *fileLower) chainsInNode(n syntax.Node) []*syntax.CallExpr {
	var all []*syntax.CallExpr
	syntax.Inspect(n, func(x syntax.Node) bool {
		if lit, ok := x.(*syntax.FuncLit); ok && !fl.transparent[lit] {
			return false
		}
		if call, ok := x.(*syntax.CallExpr); ok && fl.isChainCall(call) {
			all = append(all, call)
		}
		return true
	})
	return fl.outermostChains(all)
}

func (fl *fileLower) lowerChainPiece(n syntax.Node, enc []resultSpec) []chainHoist {
	fl.lowerChainHeadLits(n)
	return fl.lowerChainUnit(n, fl.chainsInNode(n), enc)
}

func (fl *fileLower) lowerChainPieceExit(n syntax.Node, enc []resultSpec, exit func(err string) string) []chainHoist {
	fl.lowerChainHeadLits(n)
	saved := fl.chainExit
	fl.chainExit = exit
	out := fl.lowerChainUnit(n, fl.chainsInNode(n), enc)
	fl.chainExit = saved
	return out
}

func stmtStartPos(s syntax.Stmt) syntax.Pos {
	if lab, ok := s.(*syntax.LabeledStmt); ok {
		return lab.Label.Pos()
	}
	return syntax.StartPos(s)
}

func stmtLabels(s syntax.Stmt) []string {
	var out []string
	for {
		lab, ok := s.(*syntax.LabeledStmt)
		if !ok || lab.Label == nil {
			return out
		}
		out = append(out, lab.Label.Value)
		s = lab.Stmt
	}
}

func (fl *fileLower) topSemis(from, to int) []int {
	var out []int
	depth := 0
	scanSegment(fl.src, from, to, func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		switch t.kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		case token.SEMICOLON:
			if depth == 0 {
				out = append(out, t.start)
			}
		}
		return true
	})
	return out
}

func (fl *fileLower) tokenSpan(from, to int) span {
	out := span{start: from, end: from}
	seen := false
	scanSegment(fl.src, from, to, func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		if !seen {
			out.start = t.start
			seen = true
		}
		out.end = t.end
		return true
	})
	return out
}

type forHeader struct {
	cond, post span
}

func (fl *fileLower) forHeaderOf(fs *syntax.ForStmt) forHeader {
	from := fl.off(fs.Pos()) + len("for")
	to := fl.off(fs.Body.Pos())
	semis := fl.topSemis(from, to)
	var h forHeader
	switch len(semis) {
	case 0:
		h.cond = fl.tokenSpan(from, to)
		h.post = span{start: to, end: to}
	case 2:
		h.cond = fl.tokenSpan(semis[0]+1, semis[1])
		h.post = fl.tokenSpan(semis[1]+1, to)
	default:
		base.Fatalf("logos: cannot locate the for header at %s", fs.Pos().String())
	}
	return h
}

func chainErrName(pos syntax.Pos) string {
	return fmt.Sprintf("_logos_e_%d_%d", pos.Line(), pos.Col())
}

func condFail(errVar string) func(err string) string {
	return func(err string) string { return errVar + " = " + err + "; return true" }
}

func postFail(errVar string) func(err string) string {
	return func(err string) string { return errVar + " = " + err + "; return" }
}

func (fl *fileLower) declareChainErr(s syntax.Stmt, errName string) []syntax.Stmt {
	start := fl.stmtStartOf(s)
	fl.add(start, start, "var "+errName+" error; ")
	return parseChainStmtFrag(fl, "var "+errName+" error", stmtStartPos(s))
}

func (fl *fileLower) chainErrCheck(enc []resultSpec, pos syntax.Pos, errName string) ([]syntax.Stmt, string) {
	ret := (&chainGen{fl: fl, pos: pos, enc: enc}).returnText(errName)
	text := "if " + errName + " != nil { " + ret + " }"
	return parseChainStmtFrag(fl, text, pos), text
}

type condRewrite struct {
	call *syntax.CallExpr
	head otext
	tail string
}

func (fl *fileLower) condClosure(cond syntax.Expr, hoists []chainHoist, anchor syntax.Pos) condRewrite {
	call, ok := parseChainExprFrag(fl, "func() bool { return _logos_cond }()", anchor).(*syntax.CallExpr)
	if !ok {
		base.Fatalf("logos: cannot build the condition closure at %s", anchor.String())
	}
	lit := call.Fun.(*syntax.FuncLit)
	ret := lit.Body.List[0].(*syntax.ReturnStmt)
	ret.Results = cond
	var body []syntax.Stmt
	parts := []otext{gen("func() bool { ")}
	for _, h := range hoists {
		anchorScopes(h.stmts, anchor)
		body = append(body, h.stmts...)
		parts = append(parts, h.text, gen("; "))
	}
	parts = append(parts, gen("return "))
	lit.Body.List = append(body, ret)
	return condRewrite{call: call, head: cat(parts...), tail: " }()"}
}

func (fl *fileLower) postClosure(post syntax.SimpleStmt, hoists []chainHoist, sp span, anchor syntax.Pos) syntax.SimpleStmt {
	call, ok := parseChainExprFrag(fl, "func() { }()", anchor).(*syntax.CallExpr)
	if !ok {
		base.Fatalf("logos: cannot build the post closure at %s", anchor.String())
	}
	lit := call.Fun.(*syntax.FuncLit)
	var body []syntax.Stmt
	parts := []otext{gen("func() { ")}
	for _, h := range hoists {
		anchorScopes(h.stmts, anchor)
		body = append(body, h.stmts...)
		parts = append(parts, h.text, gen("; "))
	}
	lit.Body.List = append(body, post)
	fl.addT(sp.start, sp.start, cat(parts...))
	fl.add(sp.end, sp.end, " }()")
	return exprStmtOf(anchor, call)
}

func (fl *fileLower) lowerChainFor(s syntax.Stmt, fs *syntax.ForStmt, enc []resultSpec) []syntax.Stmt {
	var out []syntax.Stmt
	if fs.Init != nil {
		for _, h := range fl.lowerChainPiece(fs.Init, enc) {
			out = append(out, h.stmts...)
			fl.addT(fl.stmtStartOf(s), fl.stmtStartOf(s), cat(h.text, gen("; ")))
		}
	}
	errName := chainErrName(fs.Pos())
	var condHoists, postHoists []chainHoist
	if fs.Cond != nil {
		condHoists = fl.lowerChainPieceExit(fs.Cond, enc, condFail(errName))
	}
	if fs.Post != nil {
		postHoists = fl.lowerChainPieceExit(fs.Post, enc, postFail(errName))
	}
	if len(condHoists) == 0 && len(postHoists) == 0 {
		fl.lowerChainChildren(s, enc)
		return append(out, s)
	}
	hdr := fl.forHeaderOf(fs)
	anchor := fs.Body.Pos()
	out = append(out, fl.declareChainErr(s, errName)...)
	cond, post := fs.Cond, fs.Post
	var head otext
	var tail string
	if len(condHoists) > 0 {
		cc := fl.condClosure(fs.Cond, condHoists, anchor)
		cond, head, tail = cc.call, cc.head, cc.tail
	}
	if len(postHoists) > 0 {
		post = fl.postClosure(fs.Post, postHoists, hdr.post, anchor)
		if fs.Cond != nil {
			head = cat(gen(errName+" != nil || ("), head)
			tail += ")"
			cond = at(&syntax.Operation{Op: syntax.OrOr, X: notNil(anchor, nameOf(anchor, errName)), Y: at(&syntax.ParenExpr{X: cond}, anchor)}, anchor)
		}
	}
	if head.s != "" {
		fl.addT(hdr.cond.start, hdr.cond.start, head)
		fl.add(hdr.cond.end, hdr.cond.end, tail)
	}
	check, checkText := fl.chainErrCheck(enc, anchor, errName)
	open := fl.off(anchor) + 1
	fl.add(open, open, " "+checkText+"; ")
	fl.lowerChainChildren(s, enc)
	fs.Cond, fs.Post = cond, post
	fs.Body.List = append(check, fs.Body.List...)
	return append(out, s)
}

func (fl *fileLower) ifCondSpan(ifs *syntax.IfStmt) span {
	from := fl.off(ifs.Pos()) + len("if")
	to := fl.off(ifs.Then.Pos())
	semis := fl.topSemis(from, to)
	if len(semis) != 1 {
		base.Fatalf("logos: cannot locate the if header at %s", ifs.Pos().String())
	}
	return fl.tokenSpan(semis[0]+1, to)
}

func (fl *fileLower) lowerChainIf(s syntax.Stmt, ifs *syntax.IfStmt, enc []resultSpec) []syntax.Stmt {
	var out []syntax.Stmt
	start := fl.stmtStartOf(s)
	for _, h := range fl.lowerChainPiece(ifs.Init, enc) {
		out = append(out, h.stmts...)
		fl.addT(start, start, cat(h.text, gen("; ")))
	}
	errName := chainErrName(ifs.Pos())
	hoists := fl.lowerChainPieceExit(ifs.Cond, enc, condFail(errName))
	if len(hoists) == 0 {
		fl.lowerChainChildren(s, enc)
		return append(out, s)
	}
	out = append(out, fl.declareChainErr(s, errName)...)
	cond, check := fl.rewriteIfCond(ifs, hoists, enc, errName)
	fl.lowerChainChildren(s, enc)
	ifs.Cond = cond
	ifs.Then.List = append(check, ifs.Then.List...)
	return append(out, s)
}

func (fl *fileLower) rewriteIfCond(ifs *syntax.IfStmt, hoists []chainHoist, enc []resultSpec, errName string) (*syntax.CallExpr, []syntax.Stmt) {
	anchor := ifs.Then.Pos()
	sp := fl.ifCondSpan(ifs)
	cc := fl.condClosure(ifs.Cond, hoists, anchor)
	fl.addT(sp.start, sp.start, cc.head)
	fl.add(sp.end, sp.end, cc.tail)
	check, checkText := fl.chainErrCheck(enc, anchor, errName)
	open := fl.off(anchor) + 1
	fl.add(open, open, " "+checkText+"; ")
	return cc.call, check
}

type chainPaths struct {
	ops   map[*syntax.CallExpr][]*syntax.Operation
	spans map[*syntax.Operation]span
	types map[*syntax.Operation]string
}

func (fl *fileLower) shortCircuitOffset(op *syntax.Operation) (int, bool) {
	if op.Y == nil || (op.Op != syntax.AndAnd && op.Op != syntax.OrOr) || !op.Pos().IsKnown() {
		return 0, false
	}
	if line := int(op.Pos().Line()); line > len(fl.start) {
		return 0, false
	}
	off := fl.off(op.Pos())
	want := "&&"
	if op.Op == syntax.OrOr {
		want = "||"
	}
	if off < 0 || off+len(want) > len(fl.src) || string(fl.src[off:off+len(want)]) != want {
		return 0, false
	}
	return off, true
}

func (fl *fileLower) operandSpan(op *syntax.Operation, opOff int) span {
	x := op.X
	for {
		inner, ok := x.(*syntax.Operation)
		if !ok || inner.Y == nil {
			break
		}
		x = inner.X
	}
	start := fl.exprStart(x)
	return span{start: start, end: lastTokenEnd(fl.src, start, opOff)}
}

func (fl *fileLower) shortOperandType(x syntax.Expr) string {
	t := fl.pkg.info.TypeOf(x)
	if t == nil || isUntypedBasic(t) {
		return "bool"
	}
	if b, ok := types2.Unalias(t).(*types2.Basic); ok && b.Kind() == types2.Bool {
		return "bool"
	}
	if spelled, ok := fl.spellType(t); ok {
		return spelled
	}
	return "bool"
}

func (fl *fileLower) chainPathsOf(root syntax.Node, calls []*syntax.CallExpr) chainPaths {
	want := make(map[*syntax.CallExpr]bool, len(calls))
	for _, c := range calls {
		want[c] = true
	}
	rootStmt, _ := root.(syntax.Stmt)
	skip := make(map[syntax.Stmt]bool)
	if rootStmt != nil {
		nestedChainStmts(rootStmt, skip)
	}
	out := chainPaths{
		ops:   make(map[*syntax.CallExpr][]*syntax.Operation),
		spans: make(map[*syntax.Operation]span),
		types: make(map[*syntax.Operation]string),
	}
	var walk func(n syntax.Node, path []*syntax.Operation)
	walk = func(n syntax.Node, path []*syntax.Operation) {
		syntax.Inspect(n, func(x syntax.Node) bool {
			switch x := x.(type) {
			case *syntax.FuncLit:
				return fl.transparent[x]
			case *syntax.CallExpr:
				if want[x] {
					out.ops[x] = path
					for _, op := range path {
						if _, seen := out.spans[op]; !seen {
							off, _ := fl.shortCircuitOffset(op)
							out.spans[op] = fl.operandSpan(op, off)
							out.types[op] = fl.shortOperandType(op.X)
						}
					}
					return false
				}
			case *syntax.Operation:
				if _, ok := fl.shortCircuitOffset(x); ok {
					walk(x.X, path)
					walk(x.Y, append(slices.Clone(path), x))
					return false
				}
			case syntax.Stmt:
				if x != rootStmt && skip[x] {
					return false
				}
			}
			return true
		})
	}
	walk(root, nil)
	return out
}

func (fl *fileLower) chainTries(call *syntax.CallExpr) bool {
	found := false
	syntax.Inspect(call, func(n syntax.Node) bool {
		if found {
			return false
		}
		if lit, ok := n.(*syntax.FuncLit); ok && !fl.transparent[lit] {
			return false
		}
		c, ok := n.(*syntax.CallExpr)
		if !ok || !fl.isChainCall(c) {
			return true
		}
		for _, arg := range c.ArgList {
			if lam, ok := syntax.Unparen(arg).(*syntax.LambdaExpr); ok && types2.LogosLambdaTry(lam) != nil {
				found = true
			}
		}
		return true
	})
	return found
}

func (fl *fileLower) lowerChainUnit(root syntax.Node, calls []*syntax.CallExpr, enc []resultSpec) []chainHoist {
	if len(calls) == 0 {
		return nil
	}
	paths := fl.chainPathsOf(root, calls)
	var hoisting []*syntax.CallExpr
	guarded := false
	for _, call := range calls {
		if fl.chainTries(call) {
			hoisting = append(hoisting, call)
			guarded = guarded || len(paths.ops[call]) > 0
		}
	}
	if !guarded {
		var out []chainHoist
		for _, call := range calls {
			out = append(out, fl.lowerChainGuarded(call, enc, "")...)
		}
		return out
	}
	for _, call := range calls {
		if !fl.chainTries(call) && len(fl.lowerChainGuarded(call, enc, "")) > 0 {
			base.Fatalf("logos: chain at %s hoists without a `?` lambda", call.Pos().String())
		}
	}
	return fl.lowerChainRegion(hoisting, paths, 0, "", enc)
}

type chainItem struct {
	key   int
	call  *syntax.CallExpr
	op    *syntax.Operation
	calls []*syntax.CallExpr
}

func (fl *fileLower) lowerChainRegion(calls []*syntax.CallExpr, paths chainPaths, depth int, guard string, enc []resultSpec) []chainHoist {
	var items []chainItem
	byOp := make(map[*syntax.Operation]int)
	for _, call := range calls {
		path := paths.ops[call]
		if len(path) == depth {
			items = append(items, chainItem{key: fl.exprStart(call), call: call})
			continue
		}
		op := path[depth]
		i, seen := byOp[op]
		if !seen {
			off, _ := fl.shortCircuitOffset(op)
			i = len(items)
			byOp[op] = i
			items = append(items, chainItem{key: off, op: op})
		}
		items[i].calls = append(items[i].calls, call)
	}
	slices.SortStableFunc(items, func(a, b chainItem) int { return a.key - b.key })
	var out []chainHoist
	for _, it := range items {
		if it.op == nil {
			out = append(out, fl.lowerChainGuarded(it.call, enc, guard)...)
			continue
		}
		name := fmt.Sprintf("_logos_sc_%d_%d", it.op.Pos().Line(), it.op.Pos().Col())
		out = append(out, fl.moveShortOperand(it.op, paths.spans[it.op], paths.types[it.op], name, guard))
		cond := name
		if it.op.Op == syntax.OrOr {
			cond = "!" + name
		}
		if guard != "" {
			cond = guard + " && " + cond
		}
		out = append(out, fl.lowerChainRegion(it.calls, paths, depth+1, cond, enc)...)
	}
	return out
}

func (fl *fileLower) moveShortOperand(op *syntax.Operation, sp span, typ, name, guard string) chainHoist {
	x := op.X
	pos := op.Pos()
	fl.lowerChainHeadLits(x)
	mark := fl.moveRegion(sp, name)
	op.X = nameOf(leftmostOf(x), name)
	if guard == "" {
		return chainHoist{
			stmts: []syntax.Stmt{defineOf(pos, []syntax.Expr{nameOf(pos, name)}, []syntax.Expr{x})},
			text:  gen(name + " := " + mark),
		}
	}
	take := ifOf(pos, nil, parseChainExprFrag(fl, guard, pos), []syntax.Stmt{
		assignOf(pos, []syntax.Expr{nameOf(pos, name)}, []syntax.Expr{x}),
	})
	return chainHoist{
		stmts: append(parseChainStmtFrag(fl, "var "+name+" "+typ, pos), take),
		text:  gen("var " + name + " " + typ + "; if " + guard + " { " + name + " = " + mark + " }"),
	}
}

type swGuard struct {
	sp     span
	hoists []chainHoist
}

type switchHeader struct {
	region    span
	init, tag span
}

func isTypeSwitchTag(tag syntax.Expr) bool {
	_, ok := syntax.Unparen(tag).(*syntax.TypeSwitchGuard)
	return ok
}

func (fl *fileLower) switchHeaderOf(s syntax.Stmt, ss *syntax.SwitchStmt) switchHeader {
	from := fl.off(ss.Pos()) + len("switch")
	first := fl.off(ss.Body[0].Pos())
	brace := -1
	scanSegment(fl.src, from, first, func(t tok) bool {
		if !t.comment && !t.auto {
			brace = t.start
		}
		return true
	})
	if brace < 0 || fl.src[brace] != '{' {
		base.Fatalf("logos: cannot locate the switch body at %s", ss.Pos().String())
	}
	h := switchHeader{region: span{start: fl.off(stmtStartPos(s)), end: brace + 1}}
	semis := fl.topSemis(from, brace)
	switch len(semis) {
	case 0:
		h.tag = fl.tokenSpan(from, brace)
	case 1:
		h.init = fl.tokenSpan(from, semis[0])
		h.tag = fl.tokenSpan(semis[0]+1, brace)
	default:
		base.Fatalf("logos: cannot locate the switch header at %s", ss.Pos().String())
	}
	return h
}

func (fl *fileLower) guardSpans(c *syntax.CaseClause) []span {
	from := fl.off(c.Pos()) + len("case")
	to := fl.off(c.Colon)
	var out []span
	depth := 0
	start := from
	scanSegment(fl.src, from, to, func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		switch t.kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
		case token.COMMA:
			if depth == 0 {
				out = append(out, fl.tokenSpan(start, t.start))
				start = t.end
			}
		}
		return true
	})
	return append(out, fl.tokenSpan(start, to))
}

func clauseGuards(c *syntax.CaseClause) []syntax.Expr {
	return syntax.UnpackListExpr(c.Cases)
}

func (fl *fileLower) lowerChainSwitch(s syntax.Stmt, ss *syntax.SwitchStmt, enc []resultSpec) []syntax.Stmt {
	if len(ss.Body) == 0 || isTypeSwitchTag(ss.Tag) {
		return fl.lowerChainPlain(s, enc)
	}
	start := fl.stmtStartOf(s)
	var pre []syntax.Stmt
	if ss.Init != nil {
		for _, h := range fl.lowerChainPiece(ss.Init, enc) {
			pre = append(pre, h.stmts...)
			fl.addT(start, start, cat(h.text, gen("; ")))
		}
	}
	var tagHoists []chainHoist
	if ss.Tag != nil {
		tagHoists = fl.lowerChainPiece(ss.Tag, enc)
	}
	runs := make([][]swGuard, len(ss.Body))
	guardHoists := 0
	for i, c := range ss.Body {
		if c.Cases == nil {
			continue
		}
		for _, g := range clauseGuards(c) {
			run := swGuard{hoists: fl.lowerChainPiece(g, enc)}
			guardHoists += len(run.hoists)
			runs[i] = append(runs[i], run)
		}
	}
	if guardHoists == 0 && (ss.Init == nil || len(tagHoists) == 0) {
		out := pre
		for _, h := range tagHoists {
			out = append(out, h.stmts...)
			fl.addT(start, start, cat(h.text, gen("; ")))
		}
		out = append(out, s)
		fl.lowerChainChildren(s, enc)
		return out
	}
	return append(pre, fl.emitChainSwitch(s, ss, tagHoists, runs, enc))
}

func (fl *fileLower) emitChainSwitch(s syntax.Stmt, ss *syntax.SwitchStmt, tagHoists []chainHoist, runs [][]swGuard, enc []resultSpec) *syntax.BlockStmt {
	hdr := fl.switchHeaderOf(s, ss)
	p0 := stmtStartPos(s)
	pfx := fmt.Sprintf("_logos_sw_%d_%d", ss.Pos().Line(), ss.Pos().Col())
	tagN, selN, matchN := pfx+"_tag", pfx+"_sel", pfx+"_m"
	hasTag := ss.Tag != nil
	var wrapper []syntax.Stmt
	parts := []otext{gen("{ "), leftoverComments(fl, hdr.region, []span{hdr.init, hdr.tag})}
	if ss.Init != nil {
		parts = append(parts, gen(fl.copyRegion(hdr.init)+"; "))
		wrapper = append(wrapper, ss.Init)
	}
	for _, h := range tagHoists {
		parts = append(parts, h.text, gen("; "))
		anchorScopes(h.stmts, p0)
		wrapper = append(wrapper, h.stmts...)
	}
	if hasTag {
		parts = append(parts, gen(tagN+" := "+fl.copyRegion(hdr.tag)+"; "))
		wrapper = append(wrapper, defineOf(p0, []syntax.Expr{nameOf(p0, tagN)}, []syntax.Expr{ss.Tag}))
	}
	parts = append(parts, gen(selN+" := 0; "))
	wrapper = append(wrapper, defineOf(p0, []syntax.Expr{nameOf(p0, selN)}, []syntax.Expr{litOf(p0, "0", syntax.IntLit)}))
	for i, c := range ss.Body {
		if c.Cases == nil {
			continue
		}
		k := fmt.Sprint(i + 1)
		guards := clauseGuards(c)
		spans := fl.guardSpans(c)
		if len(spans) != len(guards) {
			base.Fatalf("logos: cannot locate the case guards at %s", c.Pos().String())
		}
		for j := range runs[i] {
			runs[i][j].sp = spans[j]
		}
		var kept []span
		for j, g := range runs[i] {
			kept = append(kept, g.sp)
			var hoists []otext
			var inner []syntax.Stmt
			for _, h := range g.hoists {
				hoists = append(hoists, h.text, gen("; "))
				anchorScopes(h.stmts, p0)
				inner = append(inner, h.stmts...)
			}
			var rhs syntax.Expr = at(&syntax.ParenExpr{X: guards[j]}, p0)
			rhsText := "(" + fl.copyRegion(g.sp) + ")"
			if hasTag {
				rhs = at(&syntax.Operation{Op: syntax.Eql, X: nameOf(p0, tagN), Y: rhs}, p0)
				rhsText = tagN + " == " + rhsText
			}
			take := ifOf(p0, nil, nameOf(p0, matchN), []syntax.Stmt{
				assignOf(p0, []syntax.Expr{nameOf(p0, selN)}, []syntax.Expr{litOf(p0, k, syntax.IntLit)}),
			})
			inner = append(inner, defineOf(p0, []syntax.Expr{nameOf(p0, matchN)}, []syntax.Expr{rhs}), take)
			unmatched := at(&syntax.Operation{Op: syntax.Eql, X: nameOf(p0, selN), Y: litOf(p0, "0", syntax.IntLit)}, p0)
			wrapper = append(wrapper, ifOf(p0, nil, unmatched, inner))
			parts = append(parts, cat(gen("if "+selN+" == 0 { "), cat(hoists...), gen(matchN+" := "+rhsText+"; if "+matchN+" { "+selN+" = "+k+" } }; ")))
		}
		clause := span{start: fl.off(c.Pos()), end: fl.off(c.Colon) + 1}
		fl.addT(clause.start, clause.end, cat(leftoverComments(fl, clause, kept), gen("case "+k+":")))
		c.Cases = litOf(c.Pos(), k, syntax.IntLit)
	}
	for _, l := range stmtLabels(s) {
		parts = append(parts, gen(l+": "))
	}
	parts = append(parts, gen("switch "+selN+" {"))
	fl.addT(hdr.region.start, hdr.region.end, cat(parts...))
	closeOff := fl.off(ss.Rbrace) + 1
	fl.add(closeOff, closeOff, " }")
	ss.Init = nil
	ss.Tag = nameOf(ss.Pos(), selN)
	block := blockOf(p0, append(wrapper, s))
	block.Rbrace = ss.Rbrace
	fl.lowerChainChildren(s, enc)
	return block
}

func (ce *chainSyntax) resourcePrelude() []syntax.Stmt {
	if !ce.guarded() {
		return nil
	}
	var pre []syntax.Stmt
	if ce.chainEmit.flight {
		for i := range ce.lastMap + 1 {
			if !ce.ownBefore[i] {
				continue
			}
			t := ce.inTypes[i]
			if slices.ContainsFunc(ce.slots, func(slot types2.Type) bool { return types2.Identical(slot, t) }) {
				continue
			}
			suffix := "_flight" + strconv.Itoa(len(ce.slots))
			ce.slots = append(ce.slots, t)
			pre = append(pre, ce.declare(suffix, ce.typ(t)), ce.declare(suffix+"_live", ce.typ(types2.Typ[types2.Bool])))
		}
	}
	pre = append(pre, ce.declare("_done", ce.typ(types2.Typ[types2.Bool])))
	body := ce.cond(ce.op(syntax.Not, ce.name("_done"), nil), ce.rollbackSyntax()...)
	cleanup := ce.literal(nil, nil, []syntax.Stmt{body})
	return append(pre, deferOf(ce.cg.pos, ce.call(cleanup)))
}

func (ce *chainSyntax) slot(t types2.Type) string {
	for i, slot := range ce.slots {
		if types2.Identical(slot, t) {
			return "_flight" + strconv.Itoa(i)
		}
	}
	panic("logos: missing native chain resource slot")
}

func (ce *chainSyntax) setSlot(value syntax.Expr, t types2.Type) []syntax.Stmt {
	slot := ce.slot(t)
	return []syntax.Stmt{ce.assign(ce.name(slot), value), ce.assign(ce.name(slot+"_live"), ce.builtin("true"))}
}

func (ce *chainSyntax) clearSlot(t types2.Type) []syntax.Stmt {
	slot := ce.slot(t)
	return []syntax.Stmt{ce.assign(ce.name(slot), ce.zero(t)), ce.assign(ce.name(slot+"_live"), ce.builtin("false"))}
}

func (ce *chainSyntax) zero(t types2.Type) syntax.Expr {
	return ce.op(syntax.Mul, ce.call(ce.builtin("new"), ce.typ(t)), nil)
}

func (ce *chainSyntax) closeSyntax(value syntax.Expr, t types2.Type) []syntax.Stmt {
	pos := ce.cg.pos
	t = ctChainBaseType(t)
	closer := closerInterface()
	if plainMethodSet(t) {
		if !types2.Implements(t, closer) {
			return nil
		}
		return []syntax.Stmt{exprStmtOf(pos, ce.call(selOf(pos, value, "Close")))}
	}
	boxed := ce.call(ce.builtin("any"), value)
	assert := at(&syntax.AssertExpr{X: boxed, Type: ce.typ(closer)}, pos)
	decl := defineOf(pos, []syntax.Expr{ce.name("_closer"), ce.name("_isCloser")}, []syntax.Expr{assert})
	close := exprStmtOf(pos, ce.call(selOf(pos, ce.name("_closer"), "Close")))
	return []syntax.Stmt{ifOf(pos, decl, ce.name("_isCloser"), []syntax.Stmt{close})}
}

func (ce *chainSyntax) dropElement(cur string, i int) []syntax.Stmt {
	var body []syntax.Stmt
	if ce.ownBefore[i] {
		t := ce.inTypes[i]
		body = ce.closeSyntax(ce.name(cur), t)
		switch {
		case i >= ce.lastMap:
			body = append(body, ce.assign(ce.name("_out"), ce.slice(ce.name("_out"), nil, ce.lastIndex())))
		case ce.chainEmit.flight:
			body = append(body, ce.clearSlot(t)...)
		}
	}
	return append(body, ce.branch(syntax.Continue))
}

func (ce *chainSyntax) closeOther(handed syntax.Expr) *syntax.ForStmt {
	pos := ce.cg.pos
	index := ce.name("_closeIndex")
	close := ce.closeSyntax(ce.index(ce.name("_out"), index), ce.outT)
	if handed != nil {
		skip := ce.op(syntax.Neq, ce.name("_closeIndex"), handed)
		close = []syntax.Stmt{ce.cond(skip, close...)}
	}
	return at(&syntax.ForStmt{
		Init: ce.define("_closeIndex", ce.lastIndex()),
		Cond: ce.op(syntax.Geq, ce.name("_closeIndex"), ce.number(0)),
		Post: ce.stepCounter("_closeIndex", syntax.Sub),
		Body: blockOf(pos, close),
	}, pos)
}

func (ce *chainSyntax) done() syntax.Stmt {
	return ce.assign(ce.name("_done"), ce.builtin("true"))
}

func (ce *chainSyntax) rollbackSyntax() []syntax.Stmt {
	var body []syntax.Stmt
	for i, t := range ce.slots {
		slot := "_flight" + strconv.Itoa(i)
		body = append(body, ce.cond(ce.name(slot+"_live"), ce.closeSyntax(ce.name(slot), t)...))
	}
	if ce.owns {
		body = append(body, ce.closeOther(nil))
	}
	return body
}

func (ce *chainSyntax) hand(handed, value syntax.Expr) []syntax.Stmt {
	if !ce.owns {
		return []syntax.Stmt{ce.success(value)}
	}
	return []syntax.Stmt{ce.closeOther(handed), ce.done(), ce.success(value)}
}

func (ce *chainSyntax) handSliceSyntax(value syntax.Expr) []syntax.Stmt {
	if !ce.owns {
		return []syntax.Stmt{ce.success(value)}
	}
	return []syntax.Stmt{ce.done(), ce.success(value)}
}

func (fl *fileLower) lowerCtChainRange(loop *syntax.ForStmt, lower chainSyntaxLower) ([]syntax.Stmt, bool) {
	rc, ok := loop.Init.(*syntax.RangeClause)
	if !ok {
		return nil, false
	}
	call, ok := syntax.Unparen(rc.X).(*syntax.CallExpr)
	if !ok || !fl.ctChainCall(call) {
		return nil, false
	}
	ops, recv, term, _ := fl.chainParts(call, fl.ctChainCall)
	head, ok := syntax.Unparen(recv).(*syntax.CallExpr)
	if !ok || term.method != "" || !fl.isParStage(head) {
		return nil, false
	}
	slices.Reverse(ops)
	cg := &chainGen{fl: fl, pos: call.Pos(), prefix: fl.cswName("ctchain")}
	group := cg.groupSyntax(head, ops, lower)
	pos := rc.Pos()
	pre := group.bindings
	names := []syntax.Expr{nameOf(pos, "_")}
	if rc.Lhs == nil {
		rc.Def = true
	} else {
		names = syntax.UnpackListExpr(rc.Lhs)
	}
	if !rc.Def {
		pre = append(pre, group.declare("_rangeErr", group.errorType()))
	}
	rc.Lhs = listOf(pos, append(names, group.name("_rangeErr")))
	rc.X = group.sequence()
	err := group.name("_rangeErr")
	check := group.cond(group.op(syntax.Neq, err, group.builtin("nil")), lower.fail(group.name("_rangeErr"))...)
	loop.Body.List = append([]syntax.Stmt{check}, loop.Body.List...)
	return pre, true
}

func (w ctWalker) ctChainPost(loop *syntax.ForStmt, pre []syntax.Stmt) {
	pos := loop.Post.Pos()
	post := loop.Post
	loop.Post = nil
	if lowered, ok := w.trySimpleSyntax(post); ok {
		pre = append(pre, lowered...)
	} else {
		pre = append(pre, post)
	}
	tail := blockOf(pos, pre)
	jump := at(&syntax.LabeledStmt{Stmt: tail, Label: nameOf(pos, w.fl.cswName("chainpost"))}, pos)
	loopLabel := ""
	syntax.Inspect(w.body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit, *syntax.LambdaExpr:
			return false
		case *syntax.LabeledStmt:
			if n.Stmt == loop {
				loopLabel = n.Label.Value
				return false
			}
		}
		return loopLabel == ""
	})
	changed := false
	var rewrite func(syntax.Node, *syntax.ForStmt)
	rewrite = func(root syntax.Node, nearest *syntax.ForStmt) {
		syntax.Inspect(root, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.FuncLit, *syntax.LambdaExpr:
				return false
			case *syntax.ForStmt:
				if n != root {
					rewrite(n, n)
					return false
				}
			case *syntax.BranchStmt:
				if n.Tok != syntax.Continue {
					return false
				}
				if (n.Label == nil && nearest == loop) || (n.Label != nil && n.Label.Value == loopLabel) {
					n.Tok, n.Target, n.Label = syntax.Goto, jump, nameOf(n.Pos(), jump.Label.Value)
					changed = true
				}
			}
			return true
		})
	}
	rewrite(loop.Body, loop)
	var next syntax.Stmt = tail
	if changed {
		next = jump
	}
	original := blockEndingAt(loop.Body.Pos(), loop.Body.Rbrace, loop.Body.List)
	loop.Body.List = []syntax.Stmt{original, next}
}

func (w ctWalker) ctChainCases(s *syntax.SwitchStmt, tagPre []syntax.Stmt) []syntax.Stmt {
	type guard struct {
		expr syntax.Expr
		pre  []syntax.Stmt
	}
	guards := make([][]guard, len(s.Body))
	lazy := false
	for i, clause := range s.Body {
		exprs := syntax.UnpackListExpr(clause.Cases)
		for j, expr := range exprs {
			e, pre := w.expr(expr)
			exprs[j] = e
			guards[i] = append(guards[i], guard{expr: e, pre: pre})
			lazy = lazy || len(pre) != 0
		}
		if len(exprs) != 0 {
			clause.Cases = listOf(clause.Pos(), exprs)
		}
	}
	if len(tagPre) == 0 && !lazy {
		return nil
	}
	var pre []syntax.Stmt
	if s.Init != nil {
		pre = append(pre, s.Init)
		s.Init = nil
	}
	pre = append(pre, tagPre...)
	if !lazy {
		return pre
	}
	pos := s.Pos()
	tag, picked := w.fl.cswName("chaintag"), w.fl.cswName("chaincase")
	if s.Tag != nil {
		pre = append(pre, defineOf(pos, []syntax.Expr{nameOf(pos, tag)}, []syntax.Expr{s.Tag}))
	}
	zero := func() syntax.Expr { return litOf(pos, "0", syntax.IntLit) }
	pre = append(pre, defineOf(pos, []syntax.Expr{nameOf(pos, picked)}, []syntax.Expr{zero()}))
	for i, clause := range s.Body {
		if len(guards[i]) == 0 {
			continue
		}
		key := strconv.Itoa(i + 1)
		clause.Cases = litOf(clause.Pos(), key, syntax.IntLit)
		for _, g := range guards[i] {
			atPos := g.expr.Pos()
			match := assignOf(atPos, []syntax.Expr{nameOf(atPos, picked)}, []syntax.Expr{litOf(atPos, key, syntax.IntLit)})
			var test syntax.Stmt
			if s.Tag == nil {
				test = ifOf(atPos, nil, g.expr, []syntax.Stmt{match})
			} else {
				one := at(&syntax.CaseClause{Cases: g.expr, Body: []syntax.Stmt{match}, Colon: atPos}, atPos)
				test = at(&syntax.SwitchStmt{Tag: nameOf(atPos, tag), Body: []*syntax.CaseClause{one}, Rbrace: atPos}, atPos)
			}
			unmatched := at(&syntax.Operation{Op: syntax.Eql, X: nameOf(atPos, picked), Y: zero()}, atPos)
			pre = append(pre, ifOf(atPos, nil, unmatched, append(g.pre, test)))
		}
	}
	s.Tag = nameOf(pos, picked)
	return pre
}

func (w ctWalker) ctChainWorkerLambda(lam *syntax.LambdaExpr, sig *types2.Signature) *syntax.FuncLit {
	lit := w.fl.chainLambdaSyntax(lam, sig)
	frame := newFuncLower(w.fl, lit.Type, sig, lit.Body)
	frame.groupStack = parGroupStack(sig.Results().At(0).Type())
	worker := ctWalker{funcLower: frame}
	worker.functionType(lit.Type)
	worker.prepareCleanupSyntax()
	lit.Body.List = worker.scopeSyntaxList(lit.Body, lit.Body.List, worker.stmts)
	worker.finishCleanupSyntax()
	return lit
}

func (w ctWalker) ctChainSelect(s *syntax.SelectStmt) []syntax.Stmt {
	type entry struct {
		slot *syntax.Expr
		send *syntax.SendStmt
	}
	var entries []entry
	for _, clause := range s.Body {
		switch comm := clause.Comm.(type) {
		case *syntax.SendStmt:
			entries = append(entries, entry{slot: &comm.Chan}, entry{slot: &comm.Value, send: comm})
		case *syntax.ExprStmt:
			if op, ok := groupRecvOp(comm.X); ok {
				entries = append(entries, entry{slot: &op.X})
			}
		case *syntax.AssignStmt:
			if op, ok := groupRecvOp(comm.Rhs); ok {
				entries = append(entries, entry{slot: &op.X})
			}
		}
	}
	pre := w.walkExprs(len(entries), func(i int) *syntax.Expr {
		return entries[i].slot
	}, func(i int, old syntax.Expr) types2.Type {
		if send := entries[i].send; send != nil {
			return w.ctOptionTarget(send, old, 0)
		}
		return nil
	})
	for _, clause := range s.Body {
		clause.Body = w.scopeSyntaxList(clause, clause.Body, func(list []syntax.Stmt) []syntax.Stmt {
			clause.Body = nil
			if w.ctPropertySelect(clause) {
				clause.Body = append(clause.Body, w.stmts(list)...)
				return clause.Body
			}
			clause.Body = list
			if w.ctTupleSelect(clause) {
				clause.Body = w.stmts(clause.Body)
			} else {
				clause.Body = append(w.ctChainReceiveTargets(clause), w.stmts(list)...)
			}
			return clause.Body
		})
	}
	return pre
}

func (w ctWalker) ctChainReceiveTargets(clause *syntax.CommClause) []syntax.Stmt {
	as := recvAssign(clause)
	if as == nil {
		return nil
	}
	targets := syntax.UnpackListExpr(as.Lhs)
	var slots []*syntax.Expr
	var keys map[*syntax.Expr]types2.Type
	for _, target := range targets {
		slots = append(slots, w.groupLvalueSlots(target)...)
		keys = w.optionKeys(keys, target)
	}
	lhs := w.walkExprs(len(slots), func(i int) *syntax.Expr {
		return slots[i]
	}, func(i int, _ syntax.Expr) types2.Type {
		return keys[slots[i]]
	})
	if len(lhs) == 0 {
		return nil
	}
	pos := as.Pos()
	temps, values := make([]syntax.Expr, len(targets)), make([]syntax.Expr, len(targets))
	for i := range temps {
		tmp := w.nextArg()
		temps[i], values[i] = nameOf(pos, tmp), nameOf(pos, tmp)
	}
	clause.Comm = defineOf(pos, temps, []syntax.Expr{as.Rhs})
	return append(lhs, assignOf(pos, targets, values))
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"slices"
	"strconv"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

type scopeItem struct {
	plan    *callPlan
	armed   string
	pos     syntax.Pos
	hoisted bool
	join    *groupFrame
	source  *syntax.ScopeDeferStmt
}

type scopeDeferCtx struct {
	owner    syntax.Node
	startOff int
	endOff   int
	items    []scopeItem
	depth    int
	join     *groupFrame
	repeated bool
	guards   []syntax.Stmt
}

type scopeFrame struct {
	label string
	loop  bool
}

func ownScopeItems(ctx *scopeDeferCtx) []scopeItem {
	items := ctx.items
	out := make([]scopeItem, 0, len(items)+1)
	if ctx.join != nil {
		out = append(out, scopeItem{join: ctx.join})
	}
	for i := len(items) - 1; i >= 0; i-- {
		out = append(out, items[i])
	}
	return out
}

func (ctx *scopeDeferCtx) guardBody(pos syntax.Pos, armed string, call syntax.Stmt) []syntax.Stmt {
	body := []syntax.Stmt{assignOf(pos, []syntax.Expr{nameOf(pos, armed)}, []syntax.Expr{nameOf(pos, "false")})}
	if ctx.join != nil {
		body = append(body, groupWaitOnce(pos, ctx.join.g, ctx.join.j))
	}
	return append(body, call)
}

func (fc *funcLower) nextScope() string {
	spelling := "_logos_sd" + strconv.Itoa(fc.scopeSeq)
	fc.scopeSeq++
	return spelling
}

func (fc *funcLower) lowerScopeDefers() {
	if !fc.hasScopeDefer {
		return
	}
	fc.collectScopeInfo()
	fl := fc.fl
	fc.body.List = fc.scopeBlockList(fc.body.List, fc.body, fl.off(fc.body.Rbrace), nil, nil)
	if len(fc.scopeHoists) != 0 {
		base.Fatalf("logos: scope defer guard left inside loop")
	}
}

func (fc *funcLower) collectScopeInfo() {
	fc.scopeParents = make(map[syntax.Node]syntax.Node)
	fc.scopeLabels = make(map[string]*syntax.LabeledStmt)
	var rec func(n syntax.Node, parent syntax.Node)
	rec = func(n, parent syntax.Node) {
		if n == nil {
			return
		}
		if _, ok := n.(*syntax.FuncLit); ok {
			return
		}
		fc.scopeParents[n] = parent
		if l, ok := n.(*syntax.LabeledStmt); ok {
			fc.scopeLabels[l.Label.Value] = l
		}
		switch n := n.(type) {
		case *syntax.BlockStmt:
			for _, s := range n.List {
				rec(s, n)
			}
		case *syntax.IfStmt:
			rec(n.Then, n)
			if n.Else != nil {
				rec(n.Else, n)
			}
		case *syntax.ForStmt:
			if n.Init != nil {
				rec(n.Init, n)
			}
			rec(n.Body, n)
		case *syntax.SwitchStmt:
			for _, c := range n.Body {
				rec(c, n)
			}
		case *syntax.SelectStmt:
			for _, c := range n.Body {
				rec(c, n)
			}
		case *syntax.CaseClause:
			for _, s := range n.Body {
				rec(s, n)
			}
		case *syntax.CommClause:
			if n.Comm != nil {
				rec(n.Comm, n)
			}
			for _, s := range n.Body {
				rec(s, n)
			}
		case *syntax.LabeledStmt:
			rec(n.Stmt, n)
		}
	}
	rec(fc.body, nil)
}

func (fc *funcLower) scopeContains(block, target syntax.Node) bool {
	for n := target; n != nil; n = fc.scopeParents[n] {
		if n == block {
			return true
		}
	}
	return false
}

func (fc *funcLower) scopeBlockList(list []syntax.Stmt, owner syntax.Node, endOff int, active []*scopeDeferCtx, stack []scopeFrame) []syntax.Stmt {
	out := make([]syntax.Stmt, 0, len(list))
	cur := active
	ctx := fc.groupCtxs[owner]
	if ctx != nil {
		cur = append(slices.Clone(active), ctx)
	}
	for _, s := range list {
		target, labels := unwrapScopeLabels(s)
		if sd, ok := target.(*syntax.ScopeDeferStmt); ok {
			if ctx == nil {
				ctx = &scopeDeferCtx{owner: owner, startOff: fc.fl.off(sd.Pos()), endOff: endOff, depth: len(stack)}
				fc.scopeCtxs = append(fc.scopeCtxs, ctx)
				next := make([]*scopeDeferCtx, len(cur)+1)
				copy(next, cur)
				next[len(cur)] = ctx
				cur = next
			}
			if len(labels) > 0 {
				if fc.scopeDeferLabels == nil {
					fc.scopeDeferLabels = make(map[string]int)
				}
				for _, lab := range labels {
					fc.scopeDeferLabels[lab.Value] = len(stack)
				}
			}
			if scopeInLoop(stack) {
				out = append(out, fc.scopeLoopPrelude(sd, labels, ctx)...)
				continue
			}
			out = append(out, fc.scopeLabeledPrelude(sd, labels, ctx)...)
			continue
		}
		if br, ok := s.(*syntax.BranchStmt); ok && br.Tok == syntax.Fallthrough && br.Label == nil {
			out = append(out, fc.scopeFallthrough(br, ctx)...)
			continue
		}
		out = append(out, fc.scopeStmt(s, cur, stack)...)
	}
	if ctx != nil && !scopeEndsExiting(out) {
		native, text, tw := fc.scopeStanza(syntax.EndPos(list[len(list)-1]), ownScopeItems(ctx))
		out = append(out, native...)
		fl := fc.fl
		gapStart := max(stmtEnd(fl.src, fl.off(syntax.StartPos(list[len(list)-1]))), scopeStmtEnd(fl, list[len(list)-1]))
		prefix := leftoverComments(fl, span{start: gapStart, end: endOff}, nil)
		gap := cat(prefix, renderText(text, tw.subs))
		if prefix.s == "" {
			gap = cat(gen("\n"), gap)
		}
		fl.addT(gapStart, endOff, gap)
	}
	return out
}

func scopeStmtEnd(fl *fileLower, s syntax.Stmt) int {
	switch s.(type) {
	case *syntax.ForStmt, *syntax.SwitchStmt, *syntax.SelectStmt, *syntax.IfStmt, *syntax.BlockStmt, *syntax.LabeledStmt, *syntax.GroupStmt, *syntax.GroupChild:
		return fl.off(syntax.EndPos(s)) + 1
	}
	return fl.off(syntax.EndPos(s))
}

func (fc *funcLower) scopeFallthrough(br *syntax.BranchStmt, ctx *scopeDeferCtx) []syntax.Stmt {
	if ctx == nil {
		return []syntax.Stmt{br}
	}
	native, text, tw := fc.scopeStanza(br.Pos(), ownScopeItems(ctx))
	fc.fl.addT(fc.fl.off(br.Pos()), fc.fl.off(br.Pos()), renderText(text, tw.subs))
	return append(native, br)
}

func scopeEndsExiting(list []syntax.Stmt) bool {
	if len(list) == 0 {
		return false
	}
	return scopeStmtExits(list[len(list)-1])
}

func scopeStmtExits(s syntax.Stmt) bool {
	switch s := s.(type) {
	case *syntax.ReturnStmt, *syntax.BranchStmt:
		return true
	case *syntax.BlockStmt:
		return scopeEndsExiting(s.List)
	case *syntax.LabeledStmt:
		return scopeStmtExits(s.Stmt)
	case *syntax.IfStmt:
		if s.Else == nil {
			return false
		}
		return scopeBlockExits(s.Then) && scopeElseExits(s.Else)
	}
	return false
}

func scopeBlockExits(b *syntax.BlockStmt) bool {
	if b == nil {
		return false
	}
	return scopeEndsExiting(b.List)
}

func scopeElseExits(e syntax.Stmt) bool {
	switch e := e.(type) {
	case *syntax.BlockStmt:
		return scopeEndsExiting(e.List)
	case *syntax.IfStmt:
		if e.Else == nil {
			return false
		}
		return scopeBlockExits(e.Then) && scopeElseExits(e.Else)
	}
	return scopeStmtExits(e)
}

func unwrapScopeLabels(s syntax.Stmt) (syntax.Stmt, []*syntax.Name) {
	var labels []*syntax.Name
	for {
		lab, ok := s.(*syntax.LabeledStmt)
		if !ok {
			return s, labels
		}
		labels = append(labels, lab.Label)
		s = lab.Stmt
	}
}
func (fc *funcLower) scopeLabeledPrelude(sd *syntax.ScopeDeferStmt, labels []*syntax.Name, ctx *scopeDeferCtx) []syntax.Stmt {
	stmtOff, prefix := fc.fl.off(sd.Pos()), gen("")
	if len(labels) > 0 {
		stmtOff = fc.fl.off(labels[0].Pos())
		prefix = fc.fl.srcUser(stmtOff, fc.fl.off(sd.Pos()))
	}
	native := fc.scopePrelude(sd, ctx, stmtOff, prefix)
	for i := len(labels) - 1; i >= 0; i-- {
		inner := native[0]
		lab := &syntax.LabeledStmt{Label: labels[i], Stmt: inner}
		lab.SetPos(labels[i].Pos())
		native[0] = lab
	}
	return native
}

func (fc *funcLower) scopePrelude(sd *syntax.ScopeDeferStmt, ctx *scopeDeferCtx, stmtOff int, prefix otext) []syntax.Stmt {
	fl := fc.fl
	pos := sd.Pos()
	call, ok := syntax.Unparen(sd.Call).(*syntax.CallExpr)
	if !ok {
		return []syntax.Stmt{sd}
	}
	recv := fc.groupScopeArgs(sd, call)
	plan, planned := fc.planScopeCall(call)
	if !planned {
		return []syntax.Stmt{sd}
	}
	item := scopeItem{plan: plan, armed: fc.nextScope(), pos: pos}
	ctx.items = append(ctx.items, item)
	tw := &twin{}
	funN, argN, capNamesN, capValsN := fc.planUses(plan, nil)
	funT, argT, capNamesT, capValsT := fc.planUses(plan, tw)
	callN := callOf(pos, funN, argN)
	callN.HasDots = plan.dots
	callT := callOf(pos, funT, argT)
	callT.HasDots = plan.dots
	armedN := nameOf(pos, item.armed)
	armedT := nameOf(pos, item.armed)
	guardN := ifOf(pos, nil, armedN, ctx.guardBody(pos, item.armed, exprStmtOf(pos, callN)))
	guardT := ifOf(pos, nil, armedT, ctx.guardBody(pos, item.armed, exprStmtOf(pos, callT)))
	var native []syntax.Stmt
	var text []syntax.Stmt
	if recv != nil {
		native = append(native, recv.native...)
		prefix = cat(prefix, recv.render())
	}
	native = append(native, planTypeDecls(plan, nil)...)
	text = append(text, planTypeDecls(plan, tw)...)
	if len(capNamesN) > 0 {
		native = append(native, fc.bindCaptures(pos, plan, capNamesN, capValsN))
		text = append(text, fc.bindCaptures(pos, plan, capNamesT, capValsT))
	}
	fc.hoistTemp(item.armed, nameOf(pos, "bool"), "bool")
	native = append(native,
		assignOf(pos, []syntax.Expr{nameOf(pos, item.armed)}, []syntax.Expr{nameOf(pos, "true")}),
		guardDefer(pos, capNamesN, guardN),
	)
	text = append(text,
		assignOf(pos, []syntax.Expr{nameOf(pos, item.armed)}, []syntax.Expr{nameOf(pos, "true")}),
		guardDefer(pos, capNamesT, guardT),
	)
	spanEnd := skipCloseParens(fl.src, plan.end)
	kept := leftoverComments(fl, span{start: stmtOff, end: spanEnd}, planSpans(plan))
	fl.addT(stmtOff, spanEnd, cat(prefix, kept, renderText(text, tw.subs)))
	return native
}

func guardDefer(pos syntax.Pos, caps []syntax.Expr, guard syntax.Stmt) syntax.Stmt {
	deferStmt := deferOf(pos, callOf(pos, deferClosure(pos, []syntax.Stmt{guard}), nil))
	if len(caps) == 0 {
		return deferStmt
	}
	return blockOf(pos, []syntax.Stmt{defineOf(pos, nameExprs(pos, caps), nameExprs(pos, caps)), deferStmt})
}

func nameExprs(pos syntax.Pos, names []syntax.Expr) []syntax.Expr {
	out := make([]syntax.Expr, len(names))
	for i, name := range names {
		out[i] = nameOf(pos, name.(*syntax.Name).Value)
	}
	return out
}

func (fc *funcLower) scopeLoopPrelude(sd *syntax.ScopeDeferStmt, labels []*syntax.Name, ctx *scopeDeferCtx) []syntax.Stmt {
	stmtOff, prefix := fc.fl.off(sd.Pos()), gen("")
	if len(labels) > 0 {
		stmtOff = fc.fl.off(labels[0].Pos())
		prefix = fc.fl.srcUser(stmtOff, fc.fl.off(sd.Pos()))
	}
	native := fc.scopeHoistedPrelude(sd, ctx, stmtOff, prefix)
	for i := len(labels) - 1; i >= 0; i-- {
		inner := native[0]
		lab := &syntax.LabeledStmt{Label: labels[i], Stmt: inner}
		lab.SetPos(labels[i].Pos())
		native[0] = lab
	}
	return native
}

func (fc *funcLower) scopeHoistedPrelude(sd *syntax.ScopeDeferStmt, ctx *scopeDeferCtx, stmtOff int, prefix otext) []syntax.Stmt {
	fl := fc.fl
	pos := sd.Pos()
	call, ok := syntax.Unparen(sd.Call).(*syntax.CallExpr)
	if !ok {
		return []syntax.Stmt{sd}
	}
	recv := fc.groupScopeArgs(sd, call)
	plan, planned := fc.planScopeCall(call)
	if !planned {
		return []syntax.Stmt{sd}
	}
	item := scopeItem{plan: plan, armed: fc.nextScope(), pos: pos, hoisted: true}
	ctx.items = append(ctx.items, item)
	fc.scopeHoists = append(fc.scopeHoists, item.armed)
	tw := &twin{}
	funN, argN, capNamesN, capValsN := fc.planUses(plan, nil)
	funT, argT, capNamesT, capValsT := fc.planUses(plan, tw)
	callN := callOf(pos, funN, argN)
	callN.HasDots = plan.dots
	callT := callOf(pos, funT, argT)
	callT.HasDots = plan.dots
	var native []syntax.Stmt
	var text []syntax.Stmt
	if recv != nil {
		native = append(native, recv.native...)
		prefix = cat(prefix, recv.render())
	}
	native = append(native, planTypeDecls(plan, nil)...)
	text = append(text, planTypeDecls(plan, tw)...)
	if len(capNamesN) > 0 {
		native = append(native, fc.bindCaptures(pos, plan, capNamesN, capValsN))
		text = append(text, fc.bindCaptures(pos, plan, capNamesT, capValsT))
	}
	native = append(native, assignOf(pos, []syntax.Expr{nameOf(pos, item.armed)}, []syntax.Expr{deferClosure(pos, []syntax.Stmt{exprStmtOf(pos, callN)})}))
	text = append(text, assignOf(pos, []syntax.Expr{nameOf(pos, item.armed)}, []syntax.Expr{deferClosure(pos, []syntax.Stmt{exprStmtOf(pos, callT)})}))
	spanEnd := skipCloseParens(fl.src, plan.end)
	kept := leftoverComments(fl, span{start: stmtOff, end: spanEnd}, planSpans(plan))
	fl.addT(stmtOff, spanEnd, cat(prefix, kept, renderText(text, tw.subs)))
	return native
}

type hoistSlot struct {
	name string
	typ  types2.Type
}

func (fc *funcLower) bindCaptures(pos syntax.Pos, plan *callPlan, names, vals []syntax.Expr) syntax.Stmt {
	if fc.hoistPlan(pos, plan) {
		return assignOf(pos, names, vals)
	}
	return defineOf(pos, names, vals)
}

func (fc *funcLower) hoistPlan(pos syntax.Pos, plan *callPlan) bool {
	var vars []hoistedVar
	for _, slot := range fc.planSlots(plan) {
		if slot.typ == nil || !hoistableType(slot.typ, fc.fl.pkg.tpkg) {
			return false
		}
		native, eject, ok := fc.fl.spellTypeBoth(slot.typ, pos)
		if !ok {
			return false
		}
		vars = append(vars, hoistedVar{name: slot.name, native: native, eject: eject})
	}
	for _, v := range vars {
		fc.hoistTemp(v.name, v.native, v.eject)
	}
	return true
}

func (fc *funcLower) planSlots(plan *callPlan) []hoistSlot {
	pieces := append([]capturePiece{plan.fun}, plan.args...)
	if plan.isMethod {
		pieces[0] = plan.recv
	}
	var slots []hoistSlot
	for _, piece := range pieces {
		slots = append(slots, fc.pieceSlots(piece)...)
	}
	return slots
}

func (fc *funcLower) pieceSlots(piece capturePiece) []hoistSlot {
	if !piece.capture {
		return nil
	}
	typ := fc.fl.pkg.info.TypeOf(piece.expr)
	if len(piece.multi) > 0 {
		tup, _ := typ.(*types2.Tuple)
		slots := make([]hoistSlot, len(piece.multi))
		for i, name := range piece.multi {
			slots[i] = hoistSlot{name: name, typ: tup.At(i).Type()}
		}
		return slots
	}
	return []hoistSlot{{name: piece.single, typ: capturedType(piece, typ)}}
}

func capturedType(piece capturePiece, typ types2.Type) types2.Type {
	if typ == nil {
		return nil
	}
	if piece.takeAddr {
		return types2.NewPointer(typ)
	}
	if ptr, ok := types2.Unalias(typ).(*types2.Pointer); ok && piece.takeDeref {
		return ptr.Elem()
	}
	return typ
}

func scopeInLoop(stack []scopeFrame) bool {
	for _, fr := range stack {
		if fr.loop {
			return true
		}
	}
	return false
}

func scopeGuardDefer(pos syntax.Pos, armed, temp string) *syntax.CallStmt {
	body := []syntax.Stmt{
		defineOf(pos, []syntax.Expr{nameOf(pos, temp)}, []syntax.Expr{nameOf(pos, armed)}),
		assignOf(pos, []syntax.Expr{nameOf(pos, armed)}, []syntax.Expr{nilOf(pos)}),
		exprStmtOf(pos, callOf(pos, nameOf(pos, temp), nil)),
	}
	return deferOf(pos, callOf(pos, deferClosure(pos, []syntax.Stmt{ifOf(pos, nil, notNil(pos, nameOf(pos, armed)), body)}), nil))
}

func scopeHolderStanza(pos syntax.Pos, armed, temp string) *syntax.IfStmt {
	body := []syntax.Stmt{
		defineOf(pos, []syntax.Expr{nameOf(pos, temp)}, []syntax.Expr{nameOf(pos, armed)}),
		assignOf(pos, []syntax.Expr{nameOf(pos, armed)}, []syntax.Expr{nilOf(pos)}),
		exprStmtOf(pos, callOf(pos, nameOf(pos, temp), nil)),
	}
	return ifOf(pos, nil, notNil(pos, nameOf(pos, armed)), body)
}

func (fc *funcLower) drainScopeHoists(pos syntax.Pos) []syntax.Stmt {
	if len(fc.scopeHoists) == 0 {
		return nil
	}
	var native []syntax.Stmt
	var text []syntax.Stmt
	for _, armed := range fc.scopeHoists {
		temp := fc.nextScope()
		fc.hoistTemp(armed, at(&syntax.FuncType{ParamList: []*syntax.Field{}}, pos), "func()")
		native = append(native, scopeGuardDefer(pos, armed, temp))
		text = append(text, scopeGuardDefer(pos, armed, temp))
	}
	fc.scopeHoists = nil
	fc.fl.addT(fc.fl.off(pos), fc.fl.off(pos), renderText(text, nil))
	return native
}

func (fc *funcLower) scopeStanza(pos syntax.Pos, items []scopeItem) (native, text []syntax.Stmt, tw *twin) {
	tw = &twin{}
	for _, it := range items {
		if it.join != nil {
			joinNative, joinText := fc.groupJoinWait(pos, *it.join)
			native = append(native, joinNative...)
			text = append(text, joinText...)
			continue
		}
		if it.hoisted {
			temp := fc.nextScope()
			native = append(native, scopeHolderStanza(pos, it.armed, temp))
			text = append(text, scopeHolderStanza(pos, it.armed, temp))
			continue
		}
		funN, argN, _, _ := fc.planUses(it.plan, nil)
		funT, argT, _, _ := fc.planUses(it.plan, tw)
		callN := callOf(pos, funN, argN)
		callN.HasDots = it.plan.dots
		callT := callOf(pos, funT, argT)
		callT.HasDots = it.plan.dots
		native = append(native, ifOf(pos, nil, nameOf(pos, it.armed), []syntax.Stmt{
			assignOf(pos, []syntax.Expr{nameOf(pos, it.armed)}, []syntax.Expr{nameOf(pos, "false")}),
			exprStmtOf(pos, callN),
		}))
		text = append(text, ifOf(pos, nil, nameOf(pos, it.armed), []syntax.Stmt{
			assignOf(pos, []syntax.Expr{nameOf(pos, it.armed)}, []syntax.Expr{nameOf(pos, "false")}),
			exprStmtOf(pos, callT),
		}))
	}
	return native, text, tw
}

func (fc *funcLower) scopeStmt(s syntax.Stmt, active []*scopeDeferCtx, stack []scopeFrame) []syntax.Stmt {
	switch s := s.(type) {
	case *syntax.ReturnStmt:
		return fc.scopeReturn(s, active)
	case *syntax.BranchStmt:
		return fc.scopeBranch(s, active, stack)
	case *syntax.BlockStmt:
		s.List = fc.scopeBlockList(s.List, s, fc.fl.off(s.Rbrace), active, stack)
		return []syntax.Stmt{s}
	case *syntax.IfStmt:
		s.Then.List = fc.scopeBlockList(s.Then.List, s.Then, fc.fl.off(s.Then.Rbrace), active, stack)
		switch els := s.Else.(type) {
		case *syntax.BlockStmt:
			els.List = fc.scopeBlockList(els.List, els, fc.fl.off(els.Rbrace), active, stack)
		case *syntax.IfStmt:
			res := fc.scopeStmt(els, active, stack)
			s.Else = res[0]
		}
		return []syntax.Stmt{s}
	case *syntax.ForStmt:
		outer := !scopeInLoop(stack)
		stack = append(stack, scopeFrame{loop: true})
		s.Body.List = fc.scopeBlockList(s.Body.List, s.Body, fc.fl.off(s.Body.Rbrace), active, stack)
		if outer {
			if pre := fc.drainScopeHoists(s.Pos()); pre != nil {
				return append(pre, s)
			}
		}
		return []syntax.Stmt{s}
	case *syntax.SwitchStmt:
		stack = append(stack, scopeFrame{})
		for i, c := range s.Body {
			c.Body = fc.scopeBlockList(c.Body, c, fc.caseEndOff(s, i), active, stack)
		}
		return []syntax.Stmt{s}
	case *syntax.SelectStmt:
		stack = append(stack, scopeFrame{})
		for i, c := range s.Body {
			c.Body = fc.scopeBlockList(c.Body, c, fc.commEndOff(s, i), active, stack)
		}
		return []syntax.Stmt{s}
	case *syntax.LabeledStmt:
		loop := false
		if _, ok := s.Stmt.(*syntax.ForStmt); ok {
			loop = true
		}
		outer := !scopeInLoop(stack)
		stack = append(stack, scopeFrame{label: s.Label.Value, loop: loop})
		res := fc.scopeStmt(s.Stmt, active, stack)
		if loop && outer {
			if pre := fc.drainScopeHoists(s.Label.Pos()); pre != nil {
				if len(res) == 1 {
					s.Stmt = res[0]
					return append(pre, s)
				}
				return append(pre, blockOf(s.Pos(), res))
			}
		}
		if len(res) == 1 {
			s.Stmt = res[0]
			return []syntax.Stmt{s}
		}
		s.Stmt = blockOf(s.Pos(), res)
		return []syntax.Stmt{s}
	case *syntax.GroupStmt:
		return fc.scopeGroupStmt(s, active, stack)
	default:
		return []syntax.Stmt{s}
	}
}

func (fc *funcLower) caseEndOff(s *syntax.SwitchStmt, i int) int {
	if i+1 < len(s.Body) {
		return fc.fl.off(s.Body[i+1].Pos())
	}
	return fc.fl.off(s.Rbrace)
}

func (fc *funcLower) commEndOff(s *syntax.SelectStmt, i int) int {
	if i+1 < len(s.Body) {
		return fc.fl.off(s.Body[i+1].Pos())
	}
	return fc.fl.off(s.Rbrace)
}

func scopeItemsFor(active []*scopeDeferCtx, d int) []scopeItem {
	var out []scopeItem
	for i := len(active) - 1; i >= 0; i-- {
		if active[i].depth <= d {
			continue
		}
		out = append(out, ownScopeItems(active[i])...)
	}
	return out
}

func (fc *funcLower) scopeReturn(s *syntax.ReturnStmt, active []*scopeDeferCtx) []syntax.Stmt {
	if len(active) == 0 {
		return []syntax.Stmt{s}
	}
	pos := s.Pos()
	items := scopeItemsFor(active, -1)
	results := syntax.UnpackListExpr(s.Results)
	if len(results) == 0 || (len(results) != len(fc.specs) && !(len(results) == 1 && len(fc.specs) > 1)) {
		native, text, tw := fc.scopeStanza(pos, items)
		fc.fl.addT(fc.fl.off(pos), fc.fl.off(pos), renderText(text, tw.subs))
		return append(native, s)
	}
	names := make([]syntax.Expr, len(fc.specs))
	namesT := make([]syntax.Expr, len(fc.specs))
	for i := range fc.specs {
		names[i] = nameOf(pos, fc.specs[i].name.Value)
		namesT[i] = nameOf(pos, fc.specs[i].name.Value)
	}
	tw := &twin{}
	stanzaN, stanzaT, stw := fc.scopeStanza(pos, items)
	tw.absorb(stw.subs)
	marks := make([]syntax.Expr, len(results))
	for i, v := range results {
		marks[i] = tw.mark(v.Pos(), fc.fl.lazyText(span{start: fc.fl.off(syntax.StartPos(v)), end: fc.fl.exprEndOf(v)}))
	}
	if fc.elementFrame && len(results) == 1 {
		results = append(slices.Clone(results), nilOf(pos))
		marks = append(marks, nilOf(pos))
	}
	native := append([]syntax.Stmt{assignOf(pos, names, results)}, stanzaN...)
	native = append(native, returnOf(pos, nil))
	text := append([]syntax.Stmt{assignOf(pos, namesT, marks)}, stanzaT...)
	text = append(text, returnOf(pos, nil))
	fl := fc.fl
	fl.addT(fl.off(pos), stmtEnd(fl.src, fl.off(pos)), renderText(text, tw.subs))
	return native
}

func (fc *funcLower) scopeBranch(s *syntax.BranchStmt, active []*scopeDeferCtx, stack []scopeFrame) []syntax.Stmt {
	if len(active) == 0 {
		return []syntax.Stmt{s}
	}
	pos := s.Pos()
	var items []scopeItem
	switch s.Tok {
	case syntax.Break, syntax.Continue:
		if s.Label != nil {
			items = fc.scopeLabeledItems(s.Label.Value, active, stack)
			if items == nil {
				return []syntax.Stmt{s}
			}
		} else {
			d := scopeTargetDepth(s.Tok, stack)
			if d < 0 {
				return []syntax.Stmt{s}
			}
			items = scopeItemsFor(active, d)
		}
	case syntax.Goto:
		if s.Label == nil {
			return []syntax.Stmt{s}
		}
		target, ok := fc.scopeLabels[s.Label.Value]
		if !ok {
			return []syntax.Stmt{s}
		}
		items = reverseScopeItems(active, fc, target)
	case syntax.Fallthrough:
		items = scopeItemsFor(active, -1)
	default:
		return []syntax.Stmt{s}
	}
	if len(items) == 0 {
		return []syntax.Stmt{s}
	}
	native, text, tw := fc.scopeStanza(pos, items)
	fc.fl.addT(fc.fl.off(pos), fc.fl.off(pos), renderText(text, tw.subs))
	return append(native, s)
}

func reverseScopeItems(active []*scopeDeferCtx, fc *funcLower, target syntax.Node) []scopeItem {
	var out []scopeItem
	for i := len(active) - 1; i >= 0; i-- {
		if fc.scopeContains(active[i].owner, target) {
			continue
		}
		out = append(out, ownScopeItems(active[i])...)
	}
	return out
}

func (fc *funcLower) scopeLabeledItems(label string, active []*scopeDeferCtx, stack []scopeFrame) []scopeItem {
	for k := len(stack) - 1; k >= 0; k-- {
		if stack[k].label == label {
			return scopeItemsFor(active, k)
		}
	}
	if d, ok := fc.scopeDeferLabels[label]; ok {
		return scopeItemsFor(active, d)
	}
	return nil
}

func scopeTargetDepth(tok syntax.Token, stack []scopeFrame) int {
	if tok == syntax.Continue {
		for k := len(stack) - 1; k >= 0; k-- {
			if stack[k].loop {
				return k
			}
		}
		return -1
	}
	return len(stack) - 1
}

func (fc *funcLower) scopeExitStanza(pos syntax.Pos) (native, text []syntax.Stmt, subs []textSub) {
	if len(fc.scopeCtxs) == 0 {
		return nil, nil, nil
	}
	off := fc.fl.off(pos)
	var items []scopeItem
	for i := len(fc.scopeCtxs) - 1; i >= 0; i-- {
		ctx := fc.scopeCtxs[i]
		if ctx.startOff <= off && off < ctx.endOff {
			items = append(items, ownScopeItems(ctx)...)
		}
	}
	if len(items) == 0 {
		return nil, nil, nil
	}
	native, text, tw := fc.scopeStanza(pos, items)
	return native, text, tw.subs
}

func (fc *funcLower) collectScopeSyntax() (groups bool) {
	fc.scopeParents = make(map[syntax.Node]syntax.Node)
	fc.scopeLabels = make(map[string]*syntax.LabeledStmt)
	var path []syntax.Node
	syntax.Inspect(fc.body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case nil:
			path = path[:len(path)-1]
			return true
		case syntax.Expr, *syntax.GroupChild:
			return false
		case *syntax.GroupStmt:
			groups = true
		case *syntax.LabeledStmt:
			fc.scopeLabels[n.Label.Value] = n
		}
		if last := len(path) - 1; last >= 0 {
			fc.scopeParents[n] = path[last]
		}
		path = append(path, n)
		return true
	})
	return groups
}

func (w ctWalker) scopeSyntaxList(owner syntax.Node, list []syntax.Stmt, lower func([]syntax.Stmt) []syntax.Stmt) []syntax.Stmt {
	return w.scopeSyntaxListWithJoin(owner, list, lower, nil)
}

func (w ctWalker) groupScopeBody(body *syntax.BlockStmt, frame groupFrame) []syntax.Stmt {
	return w.scopeSyntaxListWithJoin(body, body.List, w.stmts, &frame)
}

func (w ctWalker) scopeSyntaxListWithJoin(owner syntax.Node, list []syntax.Stmt, lower func([]syntax.Stmt) []syntax.Stmt, join *groupFrame) []syntax.Stmt {
	ctx := w.scopeSyntaxContext(owner, list, join)
	if ctx == nil {
		return lower(list)
	}
	depth := len(w.scopeCtxs)
	w.scopeCtxs = append(w.scopeCtxs, ctx)
	lastExits := scopeEndsExiting(list)
	list = lower(list)
	if !lastExits && !w.groupTerminates(list) {
		pos := syntax.EndPos(owner)
		list = append(list, w.scopePartialExit(pos, depth)...)
	}
	w.scopeCtxs = w.scopeCtxs[:depth]
	return list
}

func (w ctWalker) scopeSyntaxContext(owner syntax.Node, list []syntax.Stmt, join *groupFrame) *scopeDeferCtx {
	var ctx *scopeDeferCtx
	if join != nil {
		ctx = &scopeDeferCtx{owner: owner, join: join}
	}
	if !w.hasScopeDefer {
		return ctx
	}
	for _, stmt := range list {
		source, _ := unwrapScopeLabels(stmt)
		sd, ok := source.(*syntax.ScopeDeferStmt)
		if !ok {
			continue
		}
		if ctx == nil {
			ctx = &scopeDeferCtx{owner: owner}
		}
		ctx.items = append(ctx.items, scopeItem{source: sd, pos: sd.Pos()})
	}
	if ctx == nil || len(ctx.items) == 0 {
		return ctx
	}
	ctx.repeated = w.scopeMayRepeat(owner)
	loop := w.scopeFallbackLoop(owner)
	for i := range ctx.items {
		item := &ctx.items[i]
		if i > 0 && ctx.repeated {
			item.armed, item.hoisted = ctx.items[0].armed, ctx.items[0].hoisted
			continue
		}
		item.armed = w.nextScope()
		w.hoistTemp(item.armed, at(&syntax.FuncType{}, item.pos), "")
		if loop != nil {
			item.hoisted = true
			w.scopeRegisterLoopGuard(loop, w.scopeSyntaxGuard(item.pos, item.armed, join))
		}
	}
	return ctx
}

func (w ctWalker) scopeMayRepeat(owner syntax.Node) bool {
	if len(w.scopeLabels) == 0 {
		return false
	}
	seen := make(map[*syntax.LabeledStmt]bool)
	repeats := false
	syntax.Inspect(owner, func(n syntax.Node) bool {
		switch n := n.(type) {
		case syntax.Expr, *syntax.GroupChild:
			return false
		case *syntax.LabeledStmt:
			seen[n] = true
		case *syntax.BranchStmt:
			repeats = repeats || n.Tok == syntax.Goto && n.Label != nil && seen[w.scopeLabels[n.Label.Value]]
		}
		return !repeats
	})
	return repeats
}

func (w ctWalker) scopeFallbackLoop(owner syntax.Node) *syntax.ForStmt {
	var loop *syntax.ForStmt
	for n := w.scopeParents[owner]; n != nil; n = w.scopeParents[n] {
		switch n := n.(type) {
		case *syntax.ForStmt:
			loop = n
		case *syntax.GroupStmt:
			return loop
		}
	}
	return loop
}

func (w ctWalker) scopeRegisterLoopGuard(loop *syntax.ForStmt, guard syntax.Stmt) {
	if w.groupCtxs == nil {
		w.groupCtxs = make(map[syntax.Node]*scopeDeferCtx)
	}
	ctx := w.groupCtxs[loop]
	if ctx == nil {
		ctx = new(scopeDeferCtx)
		w.groupCtxs[loop] = ctx
	}
	ctx.guards = append(ctx.guards, guard)
}

func (w ctWalker) scopeLoopSyntax(loop *syntax.ForStmt) []syntax.Stmt {
	if ctx := w.groupCtxs[loop]; ctx != nil {
		delete(w.groupCtxs, loop)
		return ctx.guards
	}
	return nil
}

func (w ctWalker) lowerScopeDeferSyntax(s *syntax.ScopeDeferStmt, lower func(syntax.Expr) (syntax.Expr, []syntax.Stmt)) []syntax.Stmt {
	ctx := w.scopeCtxs[len(w.scopeCtxs)-1]
	var slot scopeItem
	for _, item := range ctx.items {
		if item.source == s {
			slot = item
			break
		}
	}
	pos := s.Pos()
	call := syntax.Unparen(s.Call).(*syntax.CallExpr)
	if out, ok := w.ctOptionCleanup(call, func(call *syntax.CallExpr) []syntax.Stmt {
		return w.scopeArmSyntax(pos, slot, ctx, call, lower)
	}); ok {
		return []syntax.Stmt{blockEndingAt(pos, syntax.EndPos(s), out)}
	}
	return []syntax.Stmt{blockEndingAt(pos, syntax.EndPos(s), w.scopeArmSyntax(pos, slot, ctx, call, lower))}
}

func (w ctWalker) scopeArmSyntax(pos syntax.Pos, slot scopeItem, ctx *scopeDeferCtx, call *syntax.CallExpr, lower func(syntax.Expr) (syntax.Expr, []syntax.Stmt)) []syntax.Stmt {
	callee, _ := syntax.Unparen(call.Fun).(*syntax.Name)
	recovers := !ctx.repeated && namedValue(callee) == "recover" && isBuiltinExpr(w.funcLower, call.Fun)
	head := w.cleanupCapture(call, lower)
	holder := func() *syntax.Name { return nameOf(pos, slot.armed) }
	body := []syntax.Stmt{exprStmtOf(pos, call)}
	if ctx.repeated {
		old := w.nextScope()
		head = append(head, defineOf(pos, []syntax.Expr{nameOf(pos, old)}, []syntax.Expr{holder()}))
		cond := at(&syntax.Operation{Op: syntax.Neq, X: nameOf(pos, old), Y: w.fl.ctBuiltin(pos, "nil")}, pos)
		tail := deferOf(pos, callOf(pos, nameOf(pos, old), nil))
		body = append([]syntax.Stmt{ifOf(pos, nil, cond, []syntax.Stmt{tail})}, body...)
	}
	head = append(head, assignOf(pos, []syntax.Expr{holder()}, []syntax.Expr{deferClosure(pos, body)}))
	if !slot.hoisted {
		guard := w.scopeInvokeSyntax(pos, slot.armed, ctx.join)
		if recovers {
			list := guard.Then.List[1:]
			invoke := callOf(pos, w.fl.ctBuiltin(pos, "recover"), nil)
			list[len(list)-1] = exprStmtOf(pos, invoke)
			guard.Then.List = list
		}
		head = append(head, deferOf(pos, callOf(pos, deferClosure(pos, []syntax.Stmt{guard}), nil)))
	}
	return head
}

func (w ctWalker) scopeSyntaxGuard(pos syntax.Pos, holder string, join *groupFrame) syntax.Stmt {
	exit := w.scopeInvokeSyntax(pos, holder, join)
	return deferOf(pos, callOf(pos, deferClosure(pos, []syntax.Stmt{exit}), nil))
}

func (w ctWalker) scopeInvokeSyntax(pos syntax.Pos, holder string, join *groupFrame) *syntax.IfStmt {
	name := w.nextScope()
	get := func() *syntax.Name { return nameOf(pos, holder) }
	cond := at(&syntax.Operation{Op: syntax.Neq, X: get(), Y: w.fl.ctBuiltin(pos, "nil")}, pos)
	body := []syntax.Stmt{
		defineOf(pos, []syntax.Expr{nameOf(pos, name)}, []syntax.Expr{get()}),
		assignOf(pos, []syntax.Expr{get()}, []syntax.Expr{w.fl.ctBuiltin(pos, "nil")}),
	}
	if join != nil {
		body = append(body, w.groupWaitGuard(pos, *join))
	}
	return ifOf(pos, nil, cond, append(body, exprStmtOf(pos, callOf(pos, nameOf(pos, name), nil))))
}

func (w ctWalker) scopeSyntaxExit(pos syntax.Pos, from, to int, err string) []syntax.Stmt {
	var out []syntax.Stmt
	for i := from - 1; i >= to; i-- {
		ctx := w.scopeCtxs[i]
		if ctx.join != nil {
			out = append(out, w.groupWait(pos, *ctx.join, err))
		}
		for j := len(ctx.items) - 1; j >= 0; j-- {
			out = append(out, w.scopeInvokeSyntax(pos, ctx.items[j].armed, nil))
			if ctx.repeated {
				break
			}
		}
	}
	return out
}

func (w ctWalker) scopeHasJoin(to int) bool {
	for _, ctx := range w.scopeCtxs[to:] {
		if ctx.join != nil {
			return true
		}
	}
	return false
}

func (w ctWalker) scopePartialExit(pos syntax.Pos, to int) []syntax.Stmt {
	from := len(w.scopeCtxs)
	if !w.scopeHasJoin(to) {
		return w.scopeSyntaxExit(pos, from, to, "")
	}
	err := w.nextErr()
	out := []syntax.Stmt{typedVarDecl(pos, nameOf(pos, err), w.fl.ctBuiltin(pos, "error"))}
	out = append(out, w.scopeSyntaxExit(pos, from, to, err)...)
	failed := at(&syntax.Operation{Op: syntax.Neq, X: nameOf(pos, err), Y: w.fl.ctBuiltin(pos, "nil")}, pos)
	exit := w.scopeSyntaxExit(pos, to, 0, err)
	exit = append(exit, w.tryTraceSyntax(pos, err))
	exit = append(exit, w.tryReturnSyntax(pos, err)...)
	out = append(out, ifOf(pos, nil, failed, exit))
	return []syntax.Stmt{blockOf(pos, out)}
}

func (w ctWalker) cleanupReturn(s *syntax.ReturnStmt) ([]syntax.Stmt, syntax.Stmt) {
	if !w.hasErrdefer && len(w.scopeCtxs) == 0 {
		return nil, s
	}
	pos := s.Pos()
	var out []syntax.Stmt
	if s.Results != nil {
		names := make([]syntax.Expr, len(w.specs))
		for i, sp := range w.specs {
			names[i] = nameOf(pos, sp.name.Value)
		}
		out = append(out, assignOf(pos, names, syntax.UnpackListExpr(s.Results)))
	}
	out = append(out, w.scopePartialExit(pos, 0)...)
	out = append(out, w.cleanupReturned(pos)...)
	return nil, blockEndingAt(pos, syntax.EndPos(s), append(out, returnOf(pos, nil)))
}

func (w ctWalker) scopeBranchSyntax(s *syntax.BranchStmt) []syntax.Stmt {
	active := w.scopeCtxs
	if len(active) == 0 {
		return nil
	}
	if s.Tok == syntax.Fallthrough {
		if active[len(active)-1].owner == w.scopeParents[s] {
			return w.scopePartialExit(s.Pos(), len(active)-1)
		}
		return nil
	}
	target := w.scopeBranchTarget(s)
	if target == nil {
		return nil
	}
	depth := len(active)
	for depth > 0 {
		owner := active[depth-1].owner
		if s.Tok == syntax.Goto && w.scopeContains(owner, target) {
			break
		}
		if s.Tok != syntax.Goto && !w.scopeContains(target, owner) {
			break
		}
		depth--
	}
	if depth == len(active) {
		return nil
	}
	return w.scopePartialExit(s.Pos(), depth)
}

func (w ctWalker) scopeBranchTarget(s *syntax.BranchStmt) syntax.Node {
	if s.Tok == syntax.Goto && s.Label != nil {
		return w.scopeLabels[s.Label.Value]
	}
	if s.Label != nil {
		if label := w.scopeLabels[s.Label.Value]; label != nil {
			return label.Stmt
		}
		return nil
	}
	for n := w.scopeParents[s]; n != nil; n = w.scopeParents[n] {
		switch n.(type) {
		case *syntax.ForStmt:
			return n
		case *syntax.SwitchStmt, *syntax.SelectStmt:
			if s.Tok == syntax.Break {
				return n
			}
		}
	}
	return nil
}

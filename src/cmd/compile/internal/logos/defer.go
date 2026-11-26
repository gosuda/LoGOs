// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"cmp"
	"fmt"
	"go/token"
	"slices"
	"strings"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

type capturePiece struct {
	expr      syntax.Expr
	text      otext
	start     int
	end       int
	capture   bool
	single    string
	multi     []string
	takeAddr  bool
	takeDeref bool
	nilCheck  bool
}

type callPlan struct {
	fun      capturePiece
	args     []capturePiece
	typeArgs []capturePiece
	dots     bool
	end      int
	isMethod bool
	recv     capturePiece
	method   string
}

func typeFlagsOf(fl *fileLower, e syntax.Expr) (builtin, isType bool) {
	info := fl.pkg.info
	node := syntax.Unparen(e)
	if info.Types != nil {
		if tv, ok := info.Types[node]; ok {
			return tv.IsBuiltin(), tv.IsType()
		}
		if tv, ok := info.Types[e]; ok {
			return tv.IsBuiltin(), tv.IsType()
		}
		return false, false
	}
	flags := node.GetTypeInfo()
	return flags.IsBuiltin(), flags.IsType()
}

func isBuiltinExpr(fc *funcLower, e syntax.Expr) bool {
	builtin, _ := typeFlagsOf(fc.fl, e)
	return builtin
}

func (fc *funcLower) planCall(call *syntax.CallExpr) (*callPlan, bool) {
	fl := fc.fl
	src := fl.src
	lparen := fl.off(call.Pos())
	split, ok := splitCall(src, lparen)
	if !ok {
		return nil, false
	}
	plan := &callPlan{dots: call.HasDots, end: split.end}
	fun := capturePiece{expr: call.Fun}
	fun.start = fl.exprStart(call.Fun)
	fun.end = lastTokenEnd(src, fun.start, lparen)
	if _, isType := typeFlagsOf(fl, call.Fun); isType {
		return nil, false
	}
	if len(split.args) != len(call.ArgList) {
		return nil, false
	}
	fun.text = fl.lazyText(span{start: fun.start, end: fun.end})
	if recv, method, ok := fc.methodRecv(call); ok {
		plan.isMethod = true
		plan.recv = recv
		plan.method = method
		if index, ok := syntax.Unparen(call.Fun).(*syntax.IndexExpr); ok {
			args := syntax.UnpackListExpr(index.Index)
			types, ok := splitCall(src, fl.off(index.Pos()))
			if !ok || len(types.args) != len(args) {
				return nil, false
			}
			names := fc.nextTemps(len(args))
			for i, arg := range args {
				part := types.args[i]
				// An earlier type lowering may insert a prefix at this
				// boundary. It belongs to the type moved into the alias.
				head := drainOwnedAt(fl.ownerOfInsertion(part.start), part.start, 0)
				plan.typeArgs = append(plan.typeArgs, capturePiece{
					expr: arg, text: cat(head, fl.lazyText(part)), start: part.start,
					end: part.end, single: names[i],
				})
			}
		}
	} else {
		fun.capture = !isBuiltinExpr(fc, call.Fun) && !genericTarget(fc, call.Fun)
		if fun.capture {
			fun.single = fc.nextFun()
		}
	}
	plan.fun = fun
	for i, raw := range call.ArgList {
		arg := capturePiece{expr: raw}
		trimmed := trimSpan(src, span{start: split.args[i].start, end: split.args[i].end})
		arg.start = trimmed.start
		arg.end = trimmed.end
		arg.text = fl.lazyText(trimmed)
		switch argKind(fc, raw) {
		case argInline:
			arg.capture = false
		case argMulti:
			arg.capture = true
			arg.multi = fc.multiNames(raw)
		default:
			arg.capture = true
			arg.single = fc.nextArg()
		}
		plan.args = append(plan.args, arg)
	}
	return plan, true
}

func (fc *funcLower) calleeFunc(fun syntax.Expr) (*types2.Func, bool) {
	var name *syntax.Name
	switch n := syntax.Unparen(fun).(type) {
	case *syntax.Name:
		name = n
	case *syntax.SelectorExpr:
		name = n.Sel
	default:
		return nil, false
	}
	fn, ok := fc.fl.pkg.info.ObjectOf(name).(*types2.Func)
	return fn, ok
}

func genericTarget(fc *funcLower, fun syntax.Expr) bool {
	if index, ok := syntax.Unparen(fun).(*syntax.IndexExpr); ok {
		// Both indexing and instantiation use IndexExpr. Only a generic
		// function can remain at the call for argument type inference.
		fun = index.X
	}
	fn, ok := fc.calleeFunc(fun)
	if !ok {
		return false
	}
	sig, ok := fn.Type().(*types2.Signature)
	if !ok || sig.TypeParams() == nil {
		return false
	}
	return sig.TypeParams().Len() > 0
}

func (fc *funcLower) planScopeCall(call *syntax.CallExpr) (*callPlan, bool) {
	plan, ok := fc.planCall(call)
	if ok && plan.fun.capture {
		_, static := fc.calleeFunc(plan.fun.expr)
		plan.fun.capture = !static
	}
	return plan, ok
}

func (fc *funcLower) methodRecv(call *syntax.CallExpr) (capturePiece, string, bool) {
	fl := fc.fl
	fun := syntax.Unparen(call.Fun)
	if index, ok := fun.(*syntax.IndexExpr); ok {
		fun = syntax.Unparen(index.X)
	}
	sel, ok := fun.(*syntax.SelectorExpr)
	if !ok || sel.Sel == nil {
		return capturePiece{}, "", false
	}
	info := fl.pkg.info
	if info == nil {
		return capturePiece{}, "", false
	}
	s, ok := info.Selections[sel]
	if !ok || s.Kind() != types2.MethodVal {
		return capturePiece{}, "", false
	}
	receiverCall := *call
	receiverCall.Fun = sel
	rspan := fl.receiverSpan(&receiverCall)
	recv := capturePiece{expr: sel.X, capture: true}
	recv.start = rspan.start
	recv.end = rspan.end
	recv.text = fl.lazyText(rspan)
	recv.single = fc.nextFun()
	fc.bindRecv(&recv, s, sel.X)
	if recv.takeAddr {
		recv.text = cat(gen("&"), recv.text)
	}
	if recv.takeDeref {
		recv.text = cat(gen("*"), recv.text)
	}
	return recv, sel.Sel.Value, true
}

func (fc *funcLower) bindRecv(recv *capturePiece, s *types2.Selection, x syntax.Expr) {
	effX, effText, effT := fc.recvTarget(s, x, recv.text)
	if effT == nil {
		return
	}
	recv.expr = effX
	recv.text = effText
	if _, isParam := types2.Unalias(effT).(*types2.TypeParam); !isParam {
		if _, ok := effT.Underlying().(*types2.Interface); ok {
			recv.nilCheck = true
			return
		}
	}
	meth, ok := s.Obj().(*types2.Func)
	if !ok {
		return
	}
	sig, ok := meth.Type().(*types2.Signature)
	if !ok || sig.Recv() == nil {
		return
	}
	_, wantPtr := types2.Unalias(sig.Recv().Type()).(*types2.Pointer)
	_, havePtr := types2.Unalias(effT).(*types2.Pointer)
	switch {
	case wantPtr && !havePtr:
		recv.takeAddr = true
	case !wantPtr && havePtr:
		recv.takeDeref = true
	}
}

func (fc *funcLower) recvTarget(s *types2.Selection, x syntax.Expr, text otext) (syntax.Expr, otext, types2.Type) {
	typ := s.Recv()
	if typ == nil {
		return x, text, nil
	}
	idx := s.Index()
	if len(idx) == 0 {
		return x, text, typ
	}
	expr := x
	for _, fi := range idx[:len(idx)-1] {
		st := types2.Unalias(typ)
		if ptr, ok := st.(*types2.Pointer); ok {
			st = types2.Unalias(ptr.Elem())
		}
		strct, ok := st.Underlying().(*types2.Struct)
		if !ok || fi < 0 || fi >= strct.NumFields() {
			return x, text, nil
		}
		fld := strct.Field(fi)
		expr = selOf(x.Pos(), expr, fld.Name())
		text = cat(text, gen("."+fld.Name()))
		typ = fld.Type()
	}
	return expr, text, typ
}

type argClass int

const (
	argCapture argClass = iota
	argInline
	argMulti
)

func argKind(fc *funcLower, raw syntax.Expr) argClass {
	node := syntax.Unparen(raw)
	if _, expanded := node.(*syntax.ListExpr); expanded {
		return argInline
	}
	if name, ok := node.(*syntax.Name); ok && name.Value == "nil" {
		return argInline
	}
	if _, ok := typeConst(fc.fl.pkg.info, node); ok {
		return argInline
	}
	if _, ok := typeConst(fc.fl.pkg.info, raw); ok {
		return argInline
	}
	typ := cmp.Or(fc.fl.pkg.info.TypeOf(raw), fc.fl.pkg.info.TypeOf(node))
	if tup, ok := typ.(*types2.Tuple); ok && tup.Len() > 1 {
		return argMulti
	}
	return argCapture
}

func (fc *funcLower) multiNames(raw syntax.Expr) []string {
	typ := fc.fl.pkg.info.TypeOf(raw)
	tup, _ := typ.(*types2.Tuple)
	names := make([]string, tup.Len())
	for i := range names {
		names[i] = fc.nextArg()
	}
	return names
}

// Bind explicit type arguments where the call was registered. Each original
// type subtree moves into one alias; later calls use fresh names, including
// inside scope-exit stanzas that may shadow the source's type names.
func planTypeDecls(plan *callPlan, tw *twin) []syntax.Stmt {
	var decls []syntax.Stmt
	for _, arg := range plan.typeArgs {
		pos := arg.expr.Pos()
		typ := arg.expr
		if tw != nil {
			typ = tw.mark(pos, arg.text)
		}
		decl := at(&syntax.TypeDecl{Name: nameOf(pos, arg.single), Alias: true, Type: typ}, pos)
		decls = append(decls, at(&syntax.DeclStmt{DeclList: []syntax.Decl{decl}}, pos))
	}
	return decls
}

func (fc *funcLower) checkPair(plan *callPlan, cpos syntax.Pos) ([]syntax.Stmt, []syntax.Stmt, *twin) {
	fc.fl.needRT = true
	tw := &twin{}
	funN, argN, _, _ := fc.planUses(plan, nil)
	funT, argT, _, _ := fc.planUses(plan, tw)
	inner := "_logos_e"
	joinN := joinAssign(cpos, fc.errName, inner)
	callN := callOf(cpos, funN, argN)
	callN.HasDots = plan.dots
	checkN := ifOf(cpos, defineOf(cpos, []syntax.Expr{nameOf(cpos, inner)}, []syntax.Expr{callN}), notNil(cpos, nameOf(cpos, inner)), joinN)
	joinT := joinAssign(cpos, fc.errName, inner)
	callT := callOf(cpos, funT, argT)
	callT.HasDots = plan.dots
	checkT := ifOf(cpos, defineOf(cpos, []syntax.Expr{nameOf(cpos, inner)}, []syntax.Expr{callT}), notNil(cpos, nameOf(cpos, inner)), joinT)
	native, text := planTypeDecls(plan, nil), planTypeDecls(plan, tw)
	native = append(native, fc.definePieces(plan, nil, cpos)...)
	text = append(text, fc.definePieces(plan, tw, cpos)...)
	return append(native, checkN), append(text, checkT), tw
}

// definePieces emits one short declaration per captured piece, in use
// order, so a multi-result argument never shares its right-hand side
// with another capture. A call can supply all parameters of another
// call with its multiple results only when it is the sole expression,
// so combining the receiver with a two-result argument in one
// declaration rejects programs the language accepts.
func (fc *funcLower) definePieces(plan *callPlan, tw *twin, cpos syntax.Pos) []syntax.Stmt {
	var pieces []capturePiece
	if plan.isMethod {
		pieces = append(pieces, plan.recv)
	} else if plan.fun.capture {
		pieces = append(pieces, plan.fun)
	}
	pieces = append(pieces, plan.args...)
	var out []syntax.Stmt
	for _, piece := range pieces {
		if !piece.capture {
			continue
		}
		out = append(out, defineOf(cpos, nameList(piece), valList(piece, tw)))
	}
	return out
}

func (fc *funcLower) planUses(plan *callPlan, tw *twin) (syntax.Expr, []syntax.Expr, []syntax.Expr, []syntax.Expr) {
	use := func(piece capturePiece, names []string) syntax.Expr {
		if !piece.capture {
			if tw == nil {
				return piece.expr
			}
			return tw.mark(piece.expr.Pos(), piece.text)
		}
		if len(names) > 0 {
			out := make([]syntax.Expr, len(names))
			for i, spelling := range names {
				out[i] = nameOf(piece.expr.Pos(), spelling)
			}
			return listOf(piece.expr.Pos(), out)
		}
		return nameOf(piece.expr.Pos(), piece.single)
	}
	var fun syntax.Expr
	var args, names, vals []syntax.Expr
	if plan.isMethod {
		fun = selOf(plan.fun.expr.Pos(), use(plan.recv, pieceNames(plan.recv)), plan.method)
		if len(plan.typeArgs) > 0 {
			pos := syntax.Unparen(plan.fun.expr).Pos()
			args := make([]syntax.Expr, len(plan.typeArgs))
			for i, arg := range plan.typeArgs {
				args[i] = nameOf(arg.expr.Pos(), arg.single)
			}
			fun = at(&syntax.IndexExpr{X: fun, Index: listOf(pos, args)}, pos)
		}
		names = append(names, nameList(plan.recv)...)
		vals = append(vals, valList(plan.recv, tw)...)
	} else {
		fun = use(plan.fun, pieceNames(plan.fun))
		names = append(names, nameList(plan.fun)...)
		vals = append(vals, valList(plan.fun, tw)...)
	}
	for _, piece := range plan.args {
		var argNames []string
		if piece.capture {
			argNames = pieceNames(piece)
		}
		args = append(args, syntax.UnpackListExpr(use(piece, argNames))...)
		names = append(names, nameList(piece)...)
		vals = append(vals, valList(piece, tw)...)
	}
	return fun, args, names, vals
}

func wrapRecv(piece capturePiece) syntax.Expr {
	switch {
	case piece.takeAddr:
		return at(&syntax.Operation{Op: syntax.And, X: piece.expr}, piece.expr.Pos())
	case piece.takeDeref:
		return at(&syntax.Operation{Op: syntax.Mul, X: piece.expr}, piece.expr.Pos())
	default:
		return piece.expr
	}
}

func ifaceNilCheckStmt(pos syntax.Pos, recvTemp, method string) syntax.Stmt {
	cond := at(&syntax.Operation{Op: syntax.Eql, X: nameOf(pos, recvTemp), Y: nilOf(pos)}, pos)
	discard := assignOf(pos, []syntax.Expr{nameOf(pos, "_")}, []syntax.Expr{selOf(pos, nameOf(pos, recvTemp), method)})
	return ifOf(pos, nil, cond, []syntax.Stmt{discard})
}

func spliceRecvCheck(stmts []syntax.Stmt, chk syntax.Stmt) []syntax.Stmt {
	if chk == nil || len(stmts) == 0 {
		return stmts
	}
	out := make([]syntax.Stmt, 0, len(stmts)+1)
	out = append(out, stmts[:len(stmts)-1]...)
	out = append(out, chk)
	return append(out, stmts[len(stmts)-1])
}

func pieceNames(piece capturePiece) []string {
	if len(piece.multi) > 0 {
		return piece.multi
	}
	return []string{piece.single}
}

func nameList(piece capturePiece) []syntax.Expr {
	if !piece.capture {
		return nil
	}
	names := pieceNames(piece)
	out := make([]syntax.Expr, len(names))
	for i, spelling := range names {
		out[i] = nameOf(piece.expr.Pos(), spelling)
	}
	return out
}

func valList(piece capturePiece, tw *twin) []syntax.Expr {
	if !piece.capture {
		return nil
	}
	if tw == nil {
		return []syntax.Expr{wrapRecv(piece)}
	}
	return []syntax.Expr{tw.mark(piece.expr.Pos(), piece.text)}
}

func (fc *funcLower) lowerDeferTry(s *syntax.CallStmt, t *syntax.TryExpr, call *syntax.CallExpr) []syntax.Stmt {
	fl := fc.fl
	fc.fl.needRT = true
	cpos := syntax.StartPos(s)
	stmtOff := fl.off(cpos)
	spanEnd := skipCloseParens(fl.src, fl.off(t.Pos())+1)
	if w, ok := fl.optDeferGoPend[call]; ok && w.isTry {
		delete(fl.optDeferGoPend, call)
		return fc.lowerOptDeferTryGuarded(call, w, cpos, stmtOff, spanEnd)
	}
	plan, ok := fc.planCall(call)
	if !ok {
		return []syntax.Stmt{s}
	}
	_, textStmts, tw := fc.checkPair(plan, cpos)
	headN := []syntax.Stmt{deferOf(cpos, callOf(cpos, rtSel(cpos, "DeferTry"), []syntax.Expr{addrOf(cpos, nameOf(cpos, fc.errName)), call}))}
	closureT := deferClosure(cpos, []syntax.Stmt{textStmts[len(textStmts)-1]})
	headT := append(slices.Clone(textStmts[:len(textStmts)-1]), deferOf(cpos, callOf(cpos, closureT, nil)))
	if plan.isMethod && plan.recv.nilCheck {
		headT = spliceRecvCheck(headT, ifaceNilCheckStmt(cpos, plan.recv.single, plan.method))
	}
	fc.spliceCall(stmtOff, spanEnd, plan, scoped(cpos, headT), tw)
	return scoped(cpos, headN)
}

// lowerOptDeferTryGuarded lowers `defer recv?.Method(args)?` in one edit.
// The option pass claims the try-wrapped call (markOptDeferGo) without
// lowering it to an IIFE, so the defer-try rewrite reads the recorded
// wrapper span instead of deriving positions from a synthetic Fun.
func (fc *funcLower) lowerOptDeferTryGuarded(call *syntax.CallExpr, w optDeferGoWork, cpos syntax.Pos, stmtOff, spanEnd int) []syntax.Stmt {
	fl := fc.fl
	pos := w.osel.Pos()
	method := w.osel.Sel.Value
	xStart := fl.off(w.rpos)
	xEnd := lastTokenEnd(fl.src, xStart, fl.off(pos))
	recvText := fl.lazyText(span{start: xStart, end: xEnd})
	var cond syntax.Expr
	var condT string
	if types2.LogosOptionIsBare(w.ox.Elem) {
		cond, condT = notNil(pos, nameOf(pos, w.lv)), w.lv+" != nil"
	} else {
		cond, condT = selOf(pos, nameOf(pos, w.lv), "Present"), w.lv+".Present"
	}
	selEnd := fl.off(w.osel.Sel.Pos()) + len(method)
	plan := &callPlan{dots: call.HasDots, end: spanEnd, isMethod: true, method: method}
	plan.fun = capturePiece{expr: call.Fun, start: xStart, end: selEnd}
	plan.recv = capturePiece{expr: w.nx, capture: true, single: w.lv, start: xStart, end: xEnd, text: recvText}
	for _, raw := range call.ArgList {
		trimmed := trimSpan(fl.src, span{start: fl.off(raw.Pos()), end: fl.off(syntax.EndPos(raw))})
		arg := capturePiece{expr: raw, start: trimmed.start, end: trimmed.end, text: fl.lazyText(trimmed)}
		switch argKind(fc, raw) {
		case argInline:
		case argMulti:
			arg.capture = true
			arg.multi = fc.multiNames(raw)
		default:
			arg.capture = true
			arg.single = fc.nextArg()
		}
		plan.args = append(plan.args, arg)
	}
	funN, argN, _, _ := fc.planUses(plan, nil)
	nativeCall := callOf(cpos, funN, argN)
	nativeCall.HasDots = call.HasDots
	nativeStmts, textStmts, tw := fc.checkPair(plan, cpos)
	nativeInit := nativeStmts[:len(nativeStmts)-1]
	headN := []syntax.Stmt{deferOf(cpos, callOf(cpos, rtSel(cpos, "DeferTry"), []syntax.Expr{addrOf(cpos, nameOf(cpos, fc.errName)), nativeCall}))}
	closureT := deferClosure(cpos, []syntax.Stmt{textStmts[len(textStmts)-1]})
	headT := append(slices.Clone(textStmts[:len(textStmts)-1]), deferOf(cpos, callOf(cpos, closureT, nil)))
	prefix := leftoverComments(fl, span{start: stmtOff, end: spanEnd}, planSpans(plan))
	fl.addT(stmtOff, spanEnd, cat(prefix, renderText(headT[:len(headT)-1], tw.subs), gen("if "+condT+" { "), renderText(headT[len(headT)-1:], tw.subs), gen(" }")))
	return scoped(cpos, append(nativeInit, ifOf(cpos, nil, cond, headN)))
}

func (fc *funcLower) lowerErrdefer(s *syntax.ErrdeferStmt) []syntax.Stmt {
	fl := fc.fl
	cpos := syntax.StartPos(s)
	stmtOff := fl.off(cpos)
	inner := syntax.Unparen(s.Call)
	if t, ok := inner.(*syntax.TryExpr); ok {
		return fc.lowerErrdeferTry(s, t, cpos, stmtOff)
	}
	call, ok := inner.(*syntax.CallExpr)
	if !ok {
		return []syntax.Stmt{s}
	}
	plan, planned := fc.planCall(call)
	if !planned {
		return []syntax.Stmt{s}
	}
	tw := &twin{}
	funN, argN, capNamesN, capValsN := fc.planUses(plan, nil)
	funT, argT, capNamesT, capValsT := fc.planUses(plan, tw)
	callN := callOf(cpos, funN, argN)
	callN.HasDots = plan.dots
	callT := callOf(cpos, funT, argT)
	callT.HasDots = plan.dots
	bodyN := []syntax.Stmt{exprStmtOf(cpos, callN)}
	bodyT := []syntax.Stmt{exprStmtOf(cpos, callT)}
	native := append(planTypeDecls(plan, nil), fc.guardedDefer(cpos, bodyN, capNamesN, capValsN)...)
	if plan.isMethod && plan.recv.nilCheck {
		native = spliceRecvCheck(native, ifaceNilCheckStmt(cpos, plan.recv.single, plan.method))
	}
	text := append(planTypeDecls(plan, tw), fc.guardedDefer(cpos, bodyT, capNamesT, capValsT)...)
	if plan.isMethod && plan.recv.nilCheck {
		text = spliceRecvCheck(text, ifaceNilCheckStmt(cpos, plan.recv.single, plan.method))
	}
	spanEnd := skipCloseParens(fl.src, plan.end)
	fc.spliceCall(stmtOff, spanEnd, plan, scoped(cpos, text), tw)
	return scoped(cpos, native)
}

func (fc *funcLower) lowerErrdeferTry(s *syntax.ErrdeferStmt, t *syntax.TryExpr, cpos syntax.Pos, stmtOff int) []syntax.Stmt {
	fl := fc.fl
	call, ok := syntax.Unparen(t.X).(*syntax.CallExpr)
	if !ok {
		return []syntax.Stmt{s}
	}
	plan, planned := fc.planCall(call)
	if !planned {
		return []syntax.Stmt{s}
	}
	spanEnd := skipCloseParens(fl.src, fl.off(t.Pos())+1)
	native, textStmts, tw := fc.checkPair(plan, cpos)
	bodyN := []syntax.Stmt{native[len(native)-1]}
	bodyT := []syntax.Stmt{textStmts[len(textStmts)-1]}
	headN := append(slices.Clone(native[:len(native)-1]), fc.guardedDefer(cpos, bodyN, nil, nil)[0])
	headT := append(slices.Clone(textStmts[:len(textStmts)-1]), fc.guardedDefer(cpos, bodyT, nil, nil)[0])
	if plan.isMethod && plan.recv.nilCheck {
		headN = spliceRecvCheck(headN, ifaceNilCheckStmt(cpos, plan.recv.single, plan.method))
		headT = spliceRecvCheck(headT, ifaceNilCheckStmt(cpos, plan.recv.single, plan.method))
	}
	fc.spliceCall(stmtOff, spanEnd, plan, scoped(cpos, headT), tw)
	return scoped(cpos, headN)
}

func (fc *funcLower) guardedDefer(cpos syntax.Pos, body []syntax.Stmt, capNames, capVals []syntax.Expr) []syntax.Stmt {
	guarded := guardedJoin(cpos, fc.okName, fc.errName, body)
	closure := deferClosure(cpos, []syntax.Stmt{guarded})
	deferStmt := deferOf(cpos, callOf(cpos, closure, nil))
	if len(capNames) == 0 {
		return []syntax.Stmt{deferStmt}
	}
	return []syntax.Stmt{defineOf(cpos, capNames, capVals), deferStmt}
}

func scoped(pos syntax.Pos, stmts []syntax.Stmt) []syntax.Stmt {
	return []syntax.Stmt{blockOf(pos, stmts)}
}

func planSpans(plan *callPlan) []span {
	spans := []span{{start: plan.fun.start, end: plan.fun.end}}
	if len(plan.typeArgs) > 0 {
		spans[0] = span{start: plan.recv.start, end: plan.recv.end}
		for _, arg := range plan.typeArgs {
			spans = append(spans, span{start: arg.start, end: arg.end})
		}
	}
	for _, arg := range plan.args {
		spans = append(spans, span{start: arg.start, end: arg.end})
	}
	return spans
}

func leftoverComments(fl *fileLower, whole span, pieces []span) otext {
	var parts []otext
	for _, c := range commentsIn(fl.src, whole.start, whole.end) {
		if !insideAny(c, pieces) {
			parts = append(parts, fl.srcUser(c.start, c.end), gen("\n"))
		}
	}
	return cat(parts...)
}

func (fc *funcLower) spliceCall(stmtOff, spanEnd int, plan *callPlan, text []syntax.Stmt, tw *twin) {
	prefix := leftoverComments(fc.fl, span{start: stmtOff, end: spanEnd}, planSpans(plan))
	fc.fl.addT(stmtOff, spanEnd, cat(prefix, renderText(text, tw.subs)))
}

func (fc *funcLower) lowerReturn(s *syntax.ReturnStmt) []syntax.Stmt {
	fl := fc.fl
	scanForLiterals(fl, s.Results)
	retPos := s.Pos()
	retOff := fl.off(retPos)
	if s.Results == nil {
		fl.add(retOff, retOff, fc.okName+" = true; ")
		return []syntax.Stmt{assignOf(retPos, []syntax.Expr{nameOf(retPos, fc.okName)}, []syntax.Expr{nameOf(retPos, "true")}), s}
	}
	if len(fc.specs) == 0 || !returnKeywordAt(fl.src, retOff) {
		return []syntax.Stmt{s}
	}
	end := stmtEnd(fl.src, retOff)
	results := syntax.UnpackListExpr(s.Results)
	temps := fc.nextTemps(len(fc.specs))
	build := func(rhs []syntax.Expr) []syntax.Stmt {
		return scoped(retPos, fc.returnValues(retPos, temps, rhs, nil))
	}
	native := build(slices.Clone(results))
	const operandsMark = "_logos_return_operands"
	text := renderText(build([]syntax.Expr{nameOf(retPos, operandsMark)}), nil)
	cut := strings.Index(text.s, operandsMark)
	fl.addT(retOff, retOff+len("return"), text.slice(0, cut))
	fl.addAfterExpression(end, text.s[cut+len(operandsMark):])
	return native
}

func returnKeywordAt(src []byte, off int) bool {
	end := off + len("return")
	return end <= len(src) && string(src[off:end]) == "return" && (end == len(src) || !isIdentChar(src[end]))
}

func (fc *funcLower) returnValues(pos syntax.Pos, temps []string, rhs []syntax.Expr, mid []syntax.Stmt) []syntax.Stmt {
	out := make([]syntax.Stmt, 0, len(temps)+len(mid)+3)
	for i, spelling := range temps {
		out = append(out, typedVarDecl(pos, nameOf(pos, spelling), mustCloneType(fc.specs[i].typeExpr, pos)))
	}
	out = append(out, assignOf(pos, tempNodes(pos, temps), rhs))
	out = append(out, mid...)
	if fc.hasErrdefer {
		out = append(out, assignOf(pos, []syntax.Expr{nameOf(pos, fc.okName)}, []syntax.Expr{nameOf(pos, "true")}))
	}
	return append(out, returnOf(pos, tempNodes(pos, temps)))
}

func (fc *funcLower) nextTemps(n int) []string {
	temps := make([]string, n)
	for i := range temps {
		temps[i] = fmt.Sprintf("_logos_v%d", fc.tmpSeq)
		fc.tmpSeq++
	}
	return temps
}

func tempNodes(pos syntax.Pos, spellings []string) []syntax.Expr {
	nodes := make([]syntax.Expr, len(spellings))
	for i, spelling := range spellings {
		nodes[i] = nameOf(pos, spelling)
	}
	return nodes
}

func skipCloseParens(src []byte, from int) int {
	end := from
	scanSegment(src, from, len(src), func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		if t.kind == token.RPAREN {
			end = t.end
			return true
		}
		return false
	})
	return end
}

func (fc *funcLower) prepareCleanupSyntax() {
	fc.detectCleanups()
	groups := fc.collectScopeSyntax()
	if !fc.hasDeferTry && !fc.hasErrdefer && !fc.hasScopeDefer && !groups {
		return
	}
	names := make(map[types2.Object]string)
	for i := range fc.specs {
		sp := &fc.specs[i]
		spelling := fc.fl.cswName("ctresult")
		if sp.name == nil {
			sp.name = nameOf(sp.field.Pos(), spelling)
			sp.field.Name = sp.name
		} else {
			if obj := fc.fl.pkg.info.Defs[sp.name]; obj != nil && sp.name.Value != "_" {
				names[obj] = spelling
			}
			sp.name.Value = spelling
		}
	}
	if len(names) != 0 {
		syntax.Inspect(fc.body, func(n syntax.Node) bool {
			if id, ok := n.(*syntax.Name); ok {
				if spelling := names[fc.fl.pkg.info.Uses[id]]; spelling != "" {
					id.Value = spelling
				}
			}
			return true
		})
	}
	if len(fc.specs) != 0 {
		fc.errName = fc.specs[len(fc.specs)-1].name.Value
	}
	if fc.hasErrdefer {
		pos := fc.body.Pos()
		fc.okName = fc.fl.cswName("ctreturned")
		fc.hoistTemp(fc.okName, fc.fl.ctBuiltin(pos, "bool"), "")
		fc.armDeferredLiterals()
	}
}

func (fc *funcLower) finishCleanupSyntax() {
	if len(fc.hoisted) == 0 {
		return
	}
	pos := fc.body.Pos()
	list := make([]syntax.Stmt, 0, len(fc.hoisted)+len(fc.body.List))
	for _, slot := range fc.hoisted {
		list = append(list, typedVarDecl(pos, nameOf(pos, slot.name), slot.native))
	}
	fc.body.List = append(list, fc.body.List...)
}

func (fc *funcLower) cleanupReturned(pos syntax.Pos) []syntax.Stmt {
	if !fc.hasErrdefer {
		return nil
	}
	return []syntax.Stmt{assignOf(pos, []syntax.Expr{nameOf(pos, fc.okName)}, []syntax.Expr{fc.fl.ctBuiltin(pos, "true")})}
}

// unwindHeld names the copy of the frame's return flag that an armed
// deferred literal keeps while it runs.
const unwindHeld = "_logos_held"

// armDeferredLiterals arms each deferred function literal of an errdefer
// frame that has no results and defers no call of its own. An armed literal
// clears the frame's return flag while it runs and puts it back at each
// normal exit, so a panic or Goexit that starts in it after a successful
// return still runs errdefer [T-104.2]. The literal stays the deferred
// function, so recover() in it keeps its Go meaning. Without a deferred call
// of its own, no recover can end its panic and let it return normally.
func (fc *funcLower) armDeferredLiterals() {
	syntax.Inspect(fc.body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return n.Body == fc.body
		case *syntax.GroupChild:
			return n.Body == fc.body
		case *syntax.LambdaExpr:
			return false
		case *syntax.CallStmt:
			if lit := armableLiteral(n); lit != nil {
				if fc.fl.unwindFlags == nil {
					fc.fl.unwindFlags = make(map[*syntax.BlockStmt]string)
				}
				fc.fl.unwindFlags[lit.Body] = fc.okName
			}
		}
		return true
	})
}

func armableLiteral(s *syntax.CallStmt) *syntax.FuncLit {
	if s.Tok != syntax.Defer {
		return nil
	}
	call, ok := syntax.Unparen(s.Call).(*syntax.CallExpr)
	if !ok {
		return nil
	}
	lit, ok := syntax.Unparen(call.Fun).(*syntax.FuncLit)
	if !ok || len(lit.Type.ResultList) != 0 || defersIn(lit.Body) {
		return nil
	}
	return lit
}

// defersIn reports whether body defers a call in its own frame.
func defersIn(body *syntax.BlockStmt) bool {
	found := false
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return n.Body == body
		case *syntax.GroupChild:
			return n.Body == body
		case *syntax.LambdaExpr:
			return false
		case *syntax.CallStmt:
			found = found || n.Tok == syntax.Defer
		case *syntax.ErrdeferStmt, *syntax.ScopeDeferStmt:
			found = true
		}
		return !found
	})
	return found
}

func endsInReturn(list []syntax.Stmt) bool {
	if len(list) == 0 {
		return false
	}
	_, ok := list[len(list)-1].(*syntax.ReturnStmt)
	return ok
}

// armUnwindTree wraps the lowered body of an armed literal: it keeps the
// frame's return flag and clears it on entry, and puts it back at the end
// unless the body already ends in a return.
func (fc *funcLower) armUnwindTree(clear syntax.Expr, exits bool) {
	pos := fc.body.Pos()
	head := []syntax.Stmt{
		defineOf(pos, []syntax.Expr{nameOf(pos, unwindHeld)}, []syntax.Expr{nameOf(pos, fc.unwindFlag)}),
		assignOf(pos, []syntax.Expr{nameOf(pos, fc.unwindFlag)}, []syntax.Expr{clear}),
	}
	fc.body.List = append(head, fc.body.List...)
	if !exits {
		fc.body.List = append(fc.body.List, fc.unwindRestore(fc.body.Rbrace))
	}
}

func (fc *funcLower) armUnwindText(exits bool) {
	if fc.unwindFlag == "" {
		return
	}
	fl := fc.fl
	open := fl.off(fc.body.Pos()) + 1
	fl.add(open, open, " "+unwindHeld+" := "+fc.unwindFlag+"; "+fc.unwindFlag+" = false;")
	if !exits {
		end := fl.off(fc.body.Rbrace)
		fl.add(end, end, "; "+fc.unwindFlag+" = "+unwindHeld+"; ")
	}
	fc.armUnwindTree(nameOf(fc.body.Pos(), "false"), exits)
}

func (w ctWalker) armUnwindSyntax(exits bool) {
	if w.unwindFlag == "" {
		return
	}
	w.armUnwindTree(w.fl.ctBuiltin(w.body.Pos(), "false"), exits)
}

func (fc *funcLower) unwindRestore(pos syntax.Pos) syntax.Stmt {
	return assignOf(pos, []syntax.Expr{nameOf(pos, fc.unwindFlag)}, []syntax.Expr{nameOf(pos, unwindHeld)})
}

// unwindReturn puts the frame's return flag back before a return of an armed
// literal.
func (fc *funcLower) unwindReturn(s *syntax.ReturnStmt) []syntax.Stmt {
	if fc.unwindFlag == "" {
		return []syntax.Stmt{s}
	}
	off := fc.fl.off(s.Pos())
	fc.fl.add(off, off, fc.unwindFlag+" = "+unwindHeld+"; ")
	return []syntax.Stmt{fc.unwindRestore(s.Pos()), s}
}

func (w ctWalker) lowerCleanupCallSyntax(stmt syntax.Stmt, lower func(syntax.Expr) (syntax.Expr, []syntax.Stmt)) []syntax.Stmt {
	pos := stmt.Pos()
	var expr syntax.Expr
	var conditional bool
	switch s := stmt.(type) {
	case *syntax.CallStmt:
		expr = s.Call
	case *syntax.ErrdeferStmt:
		expr, conditional = s.Call, true
	}
	try, propagates := syntax.Unparen(expr).(*syntax.TryExpr)
	if propagates {
		expr = try.X
	}
	call := syntax.Unparen(expr).(*syntax.CallExpr)
	n := 0
	if propagates {
		n = w.tryValueCount(try, call)
	}
	lowerCall := func(call *syntax.CallExpr) []syntax.Stmt {
		return w.cleanupDeferSyntax(stmt, call, n, conditional, propagates, lower)
	}
	if out, ok := w.ctOptionCleanup(call, lowerCall); ok {
		return []syntax.Stmt{blockEndingAt(pos, syntax.EndPos(stmt), out)}
	}
	return []syntax.Stmt{blockEndingAt(pos, syntax.EndPos(stmt), lowerCall(call))}
}

func (w ctWalker) cleanupDeferSyntax(stmt syntax.Stmt, call *syntax.CallExpr, n int, conditional, propagates bool, lower func(syntax.Expr) (syntax.Expr, []syntax.Stmt)) []syntax.Stmt {
	pos := stmt.Pos()
	rt := func(name string) syntax.Expr { return w.fl.ctPkgSelector(pos, rtImportPath, name) }
	target := func() syntax.Expr { return addrOf(pos, nameOf(pos, w.errName)) }
	if !conditional && n == 0 {
		_, indexed := syntax.Unparen(call.Fun).(*syntax.IndexExpr)
		if indexed || w.cleanupMethodValue(call.Fun) != nil {
			call.Fun = w.cleanupInstantiatedTarget(call.Fun)
		}
		invoke, pre := lower(call)
		join := callOf(pos, rt("DeferTry"), []syntax.Expr{target(), invoke})
		deferred := deferOf(pos, join)
		deferred.DeferAt = stmt.(*syntax.CallStmt).DeferAt
		return append(pre, deferred)
	}
	pre := w.cleanupCapture(call, lower)
	var body []syntax.Stmt
	if propagates {
		err := w.nextErr()
		takeErr := errDefineOf(pos, blankNames(pos, n), call, nameOf(pos, err))
		join := callOf(pos, rt("DeferTry"), []syntax.Expr{target(), nameOf(pos, err)})
		body = []syntax.Stmt{takeErr, exprStmtOf(pos, join)}
	} else {
		body = []syntax.Stmt{exprStmtOf(pos, call)}
	}
	if conditional {
		aborted := at(&syntax.Operation{Op: syntax.Not, X: nameOf(pos, w.okName)}, pos)
		failed := at(&syntax.Operation{Op: syntax.Neq, X: nameOf(pos, w.errName), Y: w.fl.ctBuiltin(pos, "nil")}, pos)
		guard := at(&syntax.Operation{Op: syntax.OrOr, X: aborted, Y: failed}, pos)
		body = []syntax.Stmt{ifOf(pos, nil, guard, body)}
	}
	deferred := deferOf(pos, callOf(pos, deferClosure(pos, body), nil))
	if s, ok := stmt.(*syntax.CallStmt); ok {
		deferred.DeferAt = s.DeferAt
	}
	return append(pre, deferred)
}

func (w ctWalker) deferredChainSyntax() chainSyntaxLower {
	lower := w.chain()
	lower.capture = true
	return lower
}

func (w ctWalker) cleanupCapture(call *syntax.CallExpr, lower func(syntax.Expr) (syntax.Expr, []syntax.Stmt)) []syntax.Stmt {
	if w.fl.ctChainCall(call) {
		invoke, pre := w.fl.lowerCtChain(call, w.deferredChainSyntax())
		if deferred, ok := invoke.(*syntax.CallExpr); ok {
			*call = *deferred
			return pre
		}
		w.reject(call, "deferred chain did not retain its invocation")
		return pre
	}
	if invoke, pre, handled := w.ctPropertyCall(call); handled {
		*call = *invoke.(*syntax.CallExpr)
		return w.captureCleanupArgs(call, nil, pre)
	}
	fc := w.funcLower
	original := call.Fun
	fun, pre := lower(w.cleanupInstantiatedTarget(original))
	call.Fun = fun
	if !isBuiltinExpr(fc, original) && (w.cleanupMethodValue(original) != nil || !cleanupStaticFunction(fc, original)) {
		pre = append(pre, fc.cleanupBind(&call.Fun))
	}
	signature := *call
	signature.Fun = original
	return w.captureCleanupArgs(call, &signature, pre)
}

func (w ctWalker) captureCleanupArgs(call, signature *syntax.CallExpr, pre []syntax.Stmt) []syntax.Stmt {
	fc := w.funcLower
	if len(call.ArgList) == 0 {
		return pre
	}
	captured := make([]syntax.Expr, 0, len(call.ArgList))
	for i, arg := range call.ArgList {
		kind := argKind(fc, arg)
		value := arg
		var target types2.Type
		if signature != nil {
			var head []syntax.Stmt
			target = w.ctOptionTarget(signature, arg, i)
			value, head = w.expr(arg)
			pre = append(pre, head...)
		}
		switch kind {
		case argInline:
			got := w.ctOptionValue(arg, value, target)
			if got != value {
				pre = append(pre, fc.cleanupBind(&got))
			}
			captured = append(captured, syntax.UnpackListExpr(got)...)
		case argMulti:
			names := fc.multiNames(arg)
			pos := arg.Pos()
			pre = append(pre, defineOf(pos, tempNodes(pos, names), []syntax.Expr{value}))
			tup := w.fl.pkg.info.TypeOf(arg).(*types2.Tuple)
			for index, name := range names {
				var ref syntax.Expr = nameOf(pos, name)
				got := ref
				if signature != nil {
					got = w.ctOptionValueFrom(ref, tup.At(index).Type(), w.ctOptionTarget(signature, arg, index))
				}
				if got != ref {
					pre = append(pre, fc.cleanupBind(&got))
				}
				captured = append(captured, got)
			}
		default:
			got := w.ctOptionValue(arg, value, target)
			if got == arg {
				if typ := fc.groupDeclaredType(arg); typ != nil {
					pos := arg.Pos()
					name := fc.nextArg()
					d := at(&syntax.VarDecl{
						NameList: []*syntax.Name{nameOf(pos, name)},
						Type:     w.fl.ctTypeExpr(pos, typ),
						Values:   value,
					}, pos)
					pre = append(pre, at(&syntax.DeclStmt{DeclList: []syntax.Decl{d}}, pos))
					captured = append(captured, nameOf(pos, name))
					continue
				}
			}
			pre = append(pre, fc.cleanupBind(&got))
			captured = append(captured, got)
		}
	}
	call.ArgList = captured
	return pre
}

func (fc *funcLower) cleanupBind(dst *syntax.Expr) syntax.Stmt {
	value := *dst
	pos := value.Pos()
	name := fc.nextArg()
	*dst = nameOf(pos, name)
	return defineOf(pos, []syntax.Expr{nameOf(pos, name)}, []syntax.Expr{value})
}

func cleanupStaticFunction(fc *funcLower, fun syntax.Expr) bool {
	if index, ok := syntax.Unparen(fun).(*syntax.IndexExpr); ok && genericTarget(fc, fun) {
		fun = index.X
	}
	_, static := fc.calleeFunc(fun)
	return static
}

func (w ctWalker) cleanupMethodValue(fun syntax.Expr) *syntax.SelectorExpr {
	sel, _, ok := propCallSel(fun)
	if !ok {
		return nil
	}
	if selection := w.fl.pkg.info.Selections[sel]; selection != nil && selection.Kind() == types2.MethodVal {
		return sel
	}
	return nil
}

func (w ctWalker) cleanupInstantiatedTarget(fun syntax.Expr) syntax.Expr {
	if !genericTarget(w.funcLower, fun) {
		return fun
	}
	base := syntax.Unparen(fun)
	if index, ok := base.(*syntax.IndexExpr); ok {
		base = syntax.Unparen(index.X)
	}
	var ident *syntax.Name
	switch e := base.(type) {
	case *syntax.Name:
		ident = e
	case *syntax.SelectorExpr:
		ident = e.Sel
	}
	args := w.fl.pkg.info.Instances[ident].TypeArgs
	if args.Len() == 0 {
		return fun
	}
	// A prelude can require a method value before later arguments run.
	// The checked instantiation also covers types that source cannot name.
	pos := fun.Pos()
	types := make([]syntax.Expr, args.Len())
	for i := range types {
		types[i] = w.fl.ctTypeExpr(pos, args.At(i))
	}
	bound := at(&syntax.IndexExpr{X: base, Index: listOf(pos, types)}, pos)
	w.fl.carryType(bound, fun)
	return bound
}

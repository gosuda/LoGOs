// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

type freezeClass int

const (
	freezePlain freezeClass = iota
	freezeLeaf
	freezeRef
	freezeBody
	freezeDynamic
)

type freezeEntry struct {
	typ  types2.Type
	name string
}

type freezeCopy struct {
	text string
	expr syntax.Expr
}

type freezeSite struct {
	fl    *fileLower
	pos   syntax.Pos
	local []freezeEntry
	vars  []syntax.Stmt
	defs  []syntax.Stmt
}

func (fl *fileLower) lowerFreezes() {
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok {
			fl.lowerFreezeCall(call)
		}
		return true
	})
}

func (fl *fileLower) lowerFreezeCall(call *syntax.CallExpr) {
	t, name := fl.freezeType(call)
	if t == nil {
		return
	}
	cp, ok := fl.freezeCopier(t, syntax.StartPos(call))
	if !ok {
		return
	}
	start := fl.off(name.Pos())
	fl.add(start, start+len(name.Value), "FreezeRoot")
	name.Value = "FreezeRoot"
	fl.addAfterExpression(fl.exprEndOf(call.ArgList[0]), ", "+cp.text)
	call.ArgList = append(call.ArgList, cp.expr)
}

func (w ctWalker) ctFreezeCall(call *syntax.CallExpr) (syntax.Expr, []syntax.Stmt, bool) {
	t, _ := w.fl.freezeType(call)
	if t == nil {
		return nil, nil, false
	}
	pos := syntax.StartPos(call)
	site := &freezeSite{fl: w.fl, pos: pos}
	copier, ok := site.root(t)
	if !ok {
		return nil, nil, false
	}
	arg, pre := w.optionSink(call, call.ArgList[0], 0, false)
	fun := at(&syntax.IndexExpr{X: site.rt("FreezeRoot"), Index: site.typeExpr(t)}, pos)
	if len(site.defs) == 0 {
		return callOf(call.Pos(), fun, []syntax.Expr{arg, copier}), pre, true
	}
	src := "_logos_freeze_source"
	root := callOf(pos, fun, []syntax.Expr{nameOf(pos, src), copier})
	ft := at(&syntax.FuncType{
		ParamList:  []*syntax.Field{site.param(src, site.deref(site.typeExpr(t)))},
		ResultList: []*syntax.Field{site.param("", site.deref(site.typeExpr(t)))},
	}, pos)
	return callOf(call.Pos(), site.literal(ft, root), []syntax.Expr{arg}), pre, true
}

func (fl *fileLower) freezeType(call *syntax.CallExpr) (types2.Type, *syntax.Name) {
	name := freezeCallee(call.Fun)
	if name == nil || len(call.ArgList) != 1 || call.HasDots || !fl.isRTFreeze(name) {
		return nil, nil
	}
	inst, ok := fl.pkg.info.Instances[name]
	if !ok || inst.TypeArgs.Len() != 1 {
		return nil, nil
	}
	return inst.TypeArgs.At(0), name
}

func freezeCallee(fun syntax.Expr) *syntax.Name {
	fun = syntax.Unparen(fun)
	if ix, ok := fun.(*syntax.IndexExpr); ok {
		fun = syntax.Unparen(ix.X)
	}
	switch f := fun.(type) {
	case *syntax.SelectorExpr:
		return f.Sel
	case *syntax.Name:
		return f
	}
	return nil
}

func (fl *fileLower) isRTFreeze(name *syntax.Name) bool {
	fn, ok := fl.pkg.info.Uses[name].(*types2.Func)
	return ok && fn.Name() == "Freeze" && fn.Pkg() != nil && fn.Pkg().Path() == rtImportPath
}

func (fl *fileLower) freezeCopier(t types2.Type, pos syntax.Pos) (freezeCopy, bool) {
	site := &freezeSite{fl: fl, pos: pos}
	expr, ok := site.root(t)
	if !ok {
		return freezeCopy{}, false
	}
	if len(site.defs) != 0 {
		sig := site.signature(t, "", "", "")
		ft := at(&syntax.FuncType{ResultList: []*syntax.Field{site.param("", sig)}}, pos)
		expr = callOf(pos, site.literal(ft, expr), nil)
	}
	return freezeCopy{text: renderExpr(expr), expr: expr}, true
}

func (s *freezeSite) root(t types2.Type) (syntax.Expr, bool) {
	switch s.fl.freezeKind(t) {
	case freezePlain:
		if s.fl.ct != nil && !s.spellable(t) {
			return nil, false
		}
		return s.nilExpr(), true
	case freezeDynamic:
		return nil, false
	}
	_, known := freezeLookup(s.fl.pkg.freezeCopiers, t)
	if (!known || s.fl.ct != nil) && !s.spellable(t) {
		return nil, false
	}
	return nameOf(s.pos, s.ensure(t)), true
}

func (fl *fileLower) freezeCore(t types2.Type) types2.Type {
	for {
		if o := fl.asOption(t); o != nil && types2.LogosOptionIsBare(o.Elem) {
			t = o.Elem
			continue
		}
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

func (fl *fileLower) freezeUnder(t types2.Type) types2.Type {
	core := fl.freezeCore(t)
	if o := fl.asOption(core); o != nil {
		return o
	}
	if _, ok := core.(*types2.TypeParam); ok {
		return core
	}
	u := core.Underlying()
	if tup, ok := u.(*types2.LogosTuple); ok {
		return tup.Underlying()
	}
	return u
}

func (fl *fileLower) freezeKind(t types2.Type) freezeClass {
	switch u := fl.freezeUnder(t).(type) {
	case *types2.Basic:
		return freezeBasicKind(u)
	case *types2.Signature, *types2.Chan:
		return freezeLeaf
	case *types2.Pointer, *types2.Slice, *types2.Map:
		return freezeRef
	case *types2.Array:
		return fl.freezeHolderKind(u.Elem())
	case *types2.Option:
		return fl.freezeHolderKind(u.Elem)
	case *types2.Struct:
		return fl.freezeStructKind(u)
	case *types2.Interface:
		return fl.freezeInterfaceKind(t)
	}
	return freezeDynamic
}

func freezeBasicKind(b *types2.Basic) freezeClass {
	if b.Kind() == types2.UnsafePointer {
		return freezeLeaf
	}
	return freezePlain
}

func (fl *fileLower) freezeHolderKind(elem types2.Type) freezeClass {
	if fl.freezeKind(elem) == freezePlain {
		return freezePlain
	}
	return freezeBody
}

func (fl *fileLower) freezeStructKind(st *types2.Struct) freezeClass {
	kind := freezePlain
	for i := range st.NumFields() {
		f := st.Field(i)
		if fl.freezeKind(f.Type()) == freezePlain {
			continue
		}
		if !fl.freezeNameable(f) {
			return freezeDynamic
		}
		kind = freezeBody
	}
	return kind
}

func (fl *fileLower) freezeNameable(f *types2.Var) bool {
	return f.Name() != "_" && (f.Exported() || f.Pkg() == fl.pkg.tpkg)
}

func (fl *fileLower) freezeInterfaceKind(t types2.Type) freezeClass {
	if _, ok := types2.LogosUnionTerms(fl.freezeCore(t)); ok {
		return freezeBody
	}
	return freezeDynamic
}

func (fl *fileLower) freezeLeafName(t types2.Type) string {
	switch fl.freezeUnder(t).(type) {
	case *types2.Signature:
		return "func"
	case *types2.Chan:
		return "chan"
	}
	return "unsafe.Pointer"
}

func (s *freezeSite) spellable(t types2.Type) bool {
	if s.fl.ct == nil {
		_, ok := s.fl.spellLowered(t)
		return ok
	}
	if freezeContains(t, func(t types2.Type) bool {
		_, ok := t.(*types2.TypeParam)
		return ok
	}) {
		return false
	}
	return spellableType(s.fl.freezeCore(t), s.fl.pkg.tpkg)
}

func freezeContains(t types2.Type, match func(types2.Type) bool) bool {
	seen := make(map[types2.Type]bool)
	var walk func(types2.Type) bool
	walk = func(t types2.Type) bool {
		if t == nil || seen[t] {
			return false
		}
		seen[t] = true
		return match(t) || slices.ContainsFunc(freezeParts(t), walk)
	}
	return walk(t)
}

func freezeLocal(t types2.Type) bool {
	var obj *types2.TypeName
	switch u := t.(type) {
	case *types2.TypeParam:
		return true
	case *types2.Named:
		obj = u.Obj()
	case *types2.Alias:
		obj = u.Obj()
	default:
		return false
	}
	return obj.Pkg() != nil && obj.Parent() != obj.Pkg().Scope()
}

func freezeParts(t types2.Type) []types2.Type {
	switch u := t.(type) {
	case *types2.Named:
		return freezeTypeArgs(u.TypeArgs())
	case *types2.Alias:
		return freezeTypeArgs(u.TypeArgs())
	case *types2.Map:
		return []types2.Type{u.Key(), u.Elem()}
	case interface{ Elem() types2.Type }:
		return []types2.Type{u.Elem()}
	case *types2.ConstView:
		return []types2.Type{u.Elem}
	case *types2.ReadonlyDescendants:
		return []types2.Type{u.Elem}
	case *types2.Option:
		return []types2.Type{u.Elem}
	}
	return freezeMemberParts(t)
}

func freezeTypeArgs(list *types2.TypeList) []types2.Type {
	out := make([]types2.Type, list.Len())
	for i := range out {
		out[i] = list.At(i)
	}
	return out
}

func freezeMemberParts(t types2.Type) []types2.Type {
	var out []types2.Type
	switch u := t.(type) {
	case *types2.LogosTuple:
		return freezeMemberParts(u.Underlying())
	case *types2.Struct:
		for i := range u.NumFields() {
			out = append(out, u.Field(i).Type())
		}
	case *types2.Signature:
		out = append(freezeTupleTypes(u.Params()), freezeTupleTypes(u.Results())...)
	case *types2.Interface:
		for i := range u.NumMethods() {
			out = append(out, u.Method(i).Type())
		}
		for i := range u.NumEmbeddeds() {
			out = append(out, u.EmbeddedType(i))
		}
	}
	return out
}

func freezeTupleTypes(tup *types2.Tuple) []types2.Type {
	out := make([]types2.Type, tup.Len())
	for i := range out {
		out[i] = tup.At(i).Type()
	}
	return out
}

func freezeLookup(reg []freezeEntry, t types2.Type) (string, bool) {
	for _, e := range reg {
		if types2.Identical(e.typ, t) {
			return e.name, true
		}
	}
	return "", false
}

func (s *freezeSite) ensure(t types2.Type) string {
	hoist := s.fl.ct == nil && !freezeContains(t, freezeLocal)
	reg := &s.local
	if hoist {
		reg = &s.fl.pkg.freezeCopiers
	}
	if name, ok := freezeLookup(*reg, t); ok {
		return name
	}
	s.fl.pkg.freezeSeq++
	name := fmt.Sprintf("_logos_fz%d", s.fl.pkg.freezeSeq)
	*reg = append(*reg, freezeEntry{typ: t, name: name})
	ft := s.signature(t, "_logos_fr", "_logos_d", "_logos_s")
	body := blockOf(s.pos, s.body(t))
	if hoist {
		decl := at(&syntax.FuncDecl{Name: nameOf(s.pos, name), Type: ft, Body: body}, s.pos)
		var out strings.Builder
		_, _ = syntax.Fprint(&out, decl, 0)
		s.fl.constDecls = append(s.fl.constDecls, decl)
		s.fl.constText += "\n" + out.String() + "\n"
		return name
	}
	s.vars = append(s.vars, s.variable(name, s.signature(t, "", "", "")))
	lit := at(&syntax.FuncLit{Type: ft, Body: body}, s.pos)
	s.defs = append(s.defs, s.assign(nameOf(s.pos, name), lit))
	return name
}

func (s *freezeSite) literal(ft *syntax.FuncType, result syntax.Expr) *syntax.FuncLit {
	body := append(s.vars, s.defs...)
	body = append(body, returnOf(s.pos, []syntax.Expr{result}))
	return at(&syntax.FuncLit{Type: ft, Body: blockOf(s.pos, body)}, s.pos)
}

func (s *freezeSite) signature(typ types2.Type, fr, dst, src string) *syntax.FuncType {
	list := []*syntax.Field{
		s.param(fr, s.deref(s.rt("Freezer"))),
		s.param(dst, s.deref(s.typeExpr(typ))),
		s.param(src, s.deref(s.typeExpr(typ))),
	}
	return at(&syntax.FuncType{ParamList: list}, s.pos)
}

func (s *freezeSite) param(name string, typ syntax.Expr) *syntax.Field {
	f := at(&syntax.Field{Type: typ}, s.pos)
	if name != "" {
		f.Name = nameOf(s.pos, name)
	}
	return f
}

func (s *freezeSite) rt(name string) syntax.Expr {
	if s.fl.ct != nil {
		return s.fl.ctPkgSelector(s.pos, rtImportPath, name)
	}
	s.fl.needRT = true
	return rtSel(s.pos, name)
}

func (s *freezeSite) nilExpr() syntax.Expr {
	if s.fl.ct != nil {
		return s.fl.ctBuiltin(s.pos, "nil")
	}
	return nilOf(s.pos)
}

func (s *freezeSite) typeExpr(t types2.Type) syntax.Expr {
	if s.fl.ct != nil {
		return s.fl.ctTypeExpr(s.pos, t)
	}
	text, ok := s.fl.spellLowered(t)
	if ok {
		if typ, ok := typeExprAt(text, s.pos); ok {
			return typ
		}
	}
	base.Fatalf("logos: cannot spell frozen type %s at %s", t, s.pos.String())
	return nil
}

func (s *freezeSite) variable(name string, typ syntax.Expr) *syntax.DeclStmt {
	decl := at(&syntax.VarDecl{NameList: []*syntax.Name{nameOf(s.pos, name)}, Type: typ}, s.pos)
	return at(&syntax.DeclStmt{DeclList: []syntax.Decl{decl}}, s.pos)
}

func (s *freezeSite) deref(x syntax.Expr) *syntax.Operation {
	return at(&syntax.Operation{Op: syntax.Mul, X: x}, s.pos)
}

func (s *freezeSite) assign(dst, src syntax.Expr) *syntax.AssignStmt {
	return assignOf(s.pos, []syntax.Expr{dst}, []syntax.Expr{src})
}

func (s *freezeSite) body(t types2.Type) []syntax.Stmt {
	pos := s.pos
	dst := func() syntax.Expr { return s.deref(nameOf(pos, "_logos_d")) }
	src := func() syntax.Expr { return s.deref(nameOf(pos, "_logos_s")) }
	switch u := s.fl.freezeUnder(t).(type) {
	case *types2.Struct:
		body := []syntax.Stmt{s.assign(dst(), src())}
		for i := range u.NumFields() {
			f := u.Field(i)
			body = s.copyInto(body, f.Type(), selOf(pos, nameOf(pos, "_logos_d"), f.Name()), selOf(pos, nameOf(pos, "_logos_s"), f.Name()))
		}
		return body
	case *types2.Array:
		body := []syntax.Stmt{s.assign(dst(), src())}
		index := func(name string) syntax.Expr {
			return at(&syntax.IndexExpr{X: nameOf(pos, name), Index: nameOf(pos, "_logos_i")}, pos)
		}
		rng := at(&syntax.RangeClause{Lhs: nameOf(pos, "_logos_i"), Def: true, X: nameOf(pos, "_logos_s")}, pos)
		fill := s.copyInto(nil, u.Elem(), index("_logos_d"), index("_logos_s"))
		return append(body, at(&syntax.ForStmt{Init: rng, Body: blockOf(pos, fill)}, pos))
	case *types2.Option:
		body := []syntax.Stmt{s.assign(dst(), src())}
		return s.copyInto(body, u.Elem, selOf(pos, nameOf(pos, "_logos_d"), "Value"), selOf(pos, nameOf(pos, "_logos_s"), "Value"))
	case *types2.Interface:
		return []syntax.Stmt{s.unionBody(t, u)}
	default:
		return s.copyInto(nil, t, dst(), src())
	}
}

func (s *freezeSite) unionBody(t types2.Type, iface *types2.Interface) *syntax.SwitchStmt {
	pos := s.pos
	members, _ := types2.LogosUnionTerms(s.fl.freezeCore(t))
	plain := []syntax.Expr{s.nilExpr()}
	clause := func(cases syntax.Expr, body []syntax.Stmt) *syntax.CaseClause {
		return at(&syntax.CaseClause{Cases: cases, Body: body, Colon: pos}, pos)
	}
	var cases []*syntax.CaseClause
	for _, m := range members {
		if !s.spellable(m) || !types2.Implements(m, iface) {
			continue
		}
		if s.fl.freezeKind(m) == freezePlain {
			plain = append(plain, s.typeExpr(m))
			continue
		}
		body := []syntax.Stmt{s.variable("_logos_c", s.typeExpr(m))}
		body = s.copyInto(body, m, nameOf(pos, "_logos_c"), nameOf(pos, "_logos_v"))
		body = append(body, s.assign(s.deref(nameOf(pos, "_logos_d")), nameOf(pos, "_logos_c")))
		cases = append(cases, clause(s.typeExpr(m), body))
	}
	plainCopy := s.assign(s.deref(nameOf(pos, "_logos_d")), s.deref(nameOf(pos, "_logos_s")))
	guard := at(&syntax.TypeSwitchGuard{X: at(&syntax.ParenExpr{X: s.deref(nameOf(pos, "_logos_s"))}, pos)}, pos)
	if len(cases) != 0 {
		guard.Lhs = nameOf(pos, "_logos_v")
	}
	body := append([]*syntax.CaseClause{clause(listOf(pos, plain), []syntax.Stmt{plainCopy})}, cases...)
	dynamic := callOf(pos, s.rt("FreezeDynamic"), []syntax.Expr{nameOf(pos, "_logos_fr"), nameOf(pos, "_logos_d"), nameOf(pos, "_logos_s")})
	body = append(body, clause(nil, []syntax.Stmt{exprStmtOf(pos, dynamic)}))
	return at(&syntax.SwitchStmt{Tag: guard, Body: body, Rbrace: pos}, pos)
}

func (s *freezeSite) copyInto(body []syntax.Stmt, t types2.Type, dst, src syntax.Expr) []syntax.Stmt {
	pos := s.pos
	switch s.fl.freezeKind(t) {
	case freezePlain:
		return body
	case freezeLeaf:
		detail := litOf(pos, strconv.Quote(s.fl.freezeLeafName(t)), syntax.StringLit)
		reject := callOf(pos, s.rt("FreezeReject"), []syntax.Expr{detail})
		cond := at(&syntax.Operation{Op: syntax.Neq, X: src, Y: s.nilExpr()}, pos)
		return append(body, ifOf(pos, nil, cond, []syntax.Stmt{exprStmtOf(pos, reject)}))
	case freezeRef:
		return append(body, s.assign(dst, s.refCall(t, src)))
	default:
		args := []syntax.Expr{nameOf(pos, "_logos_fr"), addrOf(pos, dst), addrOf(pos, src)}
		return append(body, exprStmtOf(pos, callOf(pos, s.copier(t), args)))
	}
}

func (s *freezeSite) refCall(t types2.Type, src syntax.Expr) syntax.Expr {
	pos := s.pos
	args := []syntax.Expr{nameOf(pos, "_logos_fr"), src}
	switch u := s.fl.freezeUnder(t).(type) {
	case *types2.Pointer:
		return callOf(pos, s.rt("FreezePointer"), append(args, s.copier(u.Elem())))
	case *types2.Slice:
		return callOf(pos, s.rt("FreezeSlice"), append(args, s.copier(u.Elem())))
	case *types2.Map:
		return callOf(pos, s.rt("FreezeMap"), append(args, s.copier(u.Key()), s.copier(u.Elem())))
	default:
		base.Fatalf("logos: invalid frozen reference type %s at %s", t, pos.String())
		return nil
	}
}

func (s *freezeSite) copier(t types2.Type) syntax.Expr {
	switch kind := s.fl.freezeKind(t); {
	case kind == freezePlain:
		return s.nilExpr()
	case kind == freezeDynamic || !s.spellable(t):
		return s.rt("FreezeDynamic")
	default:
		return nameOf(s.pos, s.ensure(t))
	}
}

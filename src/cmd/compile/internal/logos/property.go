// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"bytes"
	"go/token"
	"slices"
	"strconv"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

func (fl *fileLower) lowerProperties() {
	fl.lowerPropertyInterfaces()
	fl.lowerPropertyDecls()
	orig := len(fl.file.DeclList)
	for _, decl := range fl.file.DeclList[:orig] {
		switch decl := decl.(type) {
		case *syntax.FuncDecl:
			if decl.Body != nil {
				decl.Body.List = fl.lowerPropBlock(decl.Body.List)
			}
		case *syntax.VarDecl:
			if decl.Values != nil {
				c := &propCollect{}
				decl.Values = fl.propReadIn(decl.Values, c)
				fl.emitPropReads(c)
			}
		}
	}
	fl.flushTypeImports()
}

func (fl *fileLower) lowerPropertyInterfaces() {
	for _, decl := range fl.file.DeclList {
		td, ok := decl.(*syntax.TypeDecl)
		if !ok {
			continue
		}
		it, ok := td.Type.(*syntax.InterfaceType)
		if !ok {
			continue
		}
		fl.lowerPropertyInterface(it)
	}
}

func (fl *fileLower) lowerPropertyInterface(it *syntax.InterfaceType) {
	var out []*syntax.Field
	changed := false
	rw := make(map[string]bool)
	for _, f := range it.MethodList {
		pt, ok := f.Type.(*syntax.PropertyType)
		if !ok || f.Name == nil {
			continue
		}
		if !pt.Const {
			rw[f.Name.Value] = true
		}
	}
	elemStart := func(f *syntax.Field) int {
		start := fl.off(syntax.StartPos(f))
		if pt, ok := f.Type.(*syntax.PropertyType); ok {
			start = min(start, fl.off(pt.Pos()))
		}
		return start
	}
	seen := make(map[string]bool)
	for i, f := range it.MethodList {
		pt, ok := f.Type.(*syntax.PropertyType)
		if !ok || f.Name == nil {
			out = append(out, f)
			continue
		}
		changed = true
		pos := f.Pos()
		name := f.Name.Value
		typeSp, ok := fl.platformSpan(pt.Type)
		if !ok {
			base.Fatalf("logos: cannot span property type at %s", pos.String())
		}
		start := elemStart(f)
		limit := typeSp.end
		if i+1 < len(it.MethodList) {
			limit = min(limit, elemStart(it.MethodList[i+1]))
		}
		afterName := span{start: fl.off(f.Name.Pos()) + len(name), end: lastTokenEnd(fl.src, typeSp.start, limit)}
		typeText := fl.textOf(afterName).trim()
		if seen[name] {
			fl.addT(start, afterName.end, gen(""))
			continue
		}
		seen[name] = true
		getResult := cloneType(pt.Type, pos)
		out = append(out, at(&syntax.Field{
			Name: nameOf(pos, "Logos_Get"+name),
			Type: at(&syntax.FuncType{ResultList: []*syntax.Field{at(&syntax.Field{Type: getResult}, pos)}}, pos),
		}, pos))
		parts := []otext{gen("Logos_Get" + name + "() "), typeText}
		if !pt.Const || rw[name] {
			setParam := cloneType(pt.Type, pos)
			out = append(out, at(&syntax.Field{
				Name: nameOf(pos, "Logos_Set"+name),
				Type: at(&syntax.FuncType{ParamList: []*syntax.Field{at(&syntax.Field{Type: setParam}, pos)}}, pos),
			}, pos))
			parts = append(parts, gen("; Logos_Set"+name+"("), typeText, gen(")"))
		}
		fl.addT(start, afterName.end, cat(parts...))
	}
	if changed {
		it.MethodList = out
	}
}

func (fl *fileLower) lowerPropertyDecls() {
	info := fl.pkg.info
	if len(info.LogosPropDecls) == 0 {
		return
	}
	byDef := make(map[*types2.TypeName][]types2.Type)
	for _, d := range info.LogosPropDecls {
		if d == nil || d.Def == nil {
			continue
		}
		byDef[d.Def] = append(byDef[d.Def], d.Ifaces...)
	}
	for _, decl := range fl.file.DeclList {
		td, ok := decl.(*syntax.TypeDecl)
		if !ok || td.Name == nil || td.Alias {
			continue
		}
		obj, ok := info.Defs[td.Name].(*types2.TypeName)
		if !ok {
			continue
		}
		ifaces, ok := byDef[obj]
		if !ok {
			continue
		}
		fl.emitAccessors(td, obj, ifaces)
	}
}

func (fl *fileLower) emitAccessors(td *syntax.TypeDecl, obj *types2.TypeName, ifaces []types2.Type) {
	named, ok := obj.Type().(*types2.Named)
	if !ok {
		base.Fatalf("logos: %s declares a property interface but is not a named type", td.Name.Value)
	}
	seen := make(map[string]bool)
	var props []types2.LogosProp
	for _, iface := range ifaces {
		set, ok := types2.LogosPropsOf(iface)
		if !ok {
			continue
		}
		for _, pr := range set {
			if seen[pr.Name] {
				continue
			}
			seen[pr.Name] = true
			props = append(props, pr)
		}
	}
	slices.SortFunc(props, func(a, b types2.LogosProp) int { return strings.Compare(a.Name, b.Name) })
	pos := td.Pos()
	typeName := td.Name.Value
	recvShape := selfShape(td, pos)
	var added []*syntax.FuncDecl
	var text bytes.Buffer
	for _, pr := range props {
		if _, promoted, _ := types2.LogosPropertySelection(fl.pkg.tpkg, types2.NewPointer(types2.LogosSelfInstance(named)), pr.Name); promoted {
			continue
		}
		getter, setter, ambig, getFn, setFn := propSourceOf(fl.pkg.tpkg, types2.NewPointer(types2.LogosSelfInstance(named)), pr.Name, pr.Typ)
		if ambig || !getter {
			base.Fatalf("logos: property %s of %s has no source at lowering", pr.Name, typeName)
		}
		getResult, ok := fl.spellPropType(pr.Typ, pos)
		if !ok {
			base.Fatalf("logos: property %s of %s has a type that cannot be spelled in this file", pr.Name, typeName)
		}
		getterRecv := recvTypeValue(recvShape, pos)
		if getFn != nil && isPointerRecv(getFn) {
			getterRecv = recvTypePointer(recvShape, pos)
		}
		var getBody syntax.Expr
		if getFn != nil {
			getBody = callOf(pos, selOf(pos, nameOf(pos, "m"), pr.Name), nil)
		} else {
			getBody = selOf(pos, nameOf(pos, "m"), pr.Name)
		}
		added = append(added, at(&syntax.FuncDecl{
			Recv: at(&syntax.Field{Name: nameOf(pos, "m"), Type: getterRecv}, pos),
			Name: nameOf(pos, "Logos_Get"+pr.Name),
			Type: at(&syntax.FuncType{ResultList: []*syntax.Field{at(&syntax.Field{Type: getResult}, pos)}}, pos),
			Body: blockOf(pos, []syntax.Stmt{returnOf(pos, []syntax.Expr{getBody})}),
		}, pos))
		if pr.ReadOnly {
			continue
		}
		if !setter {
			base.Fatalf("logos: mutable property %s of %s has no setter source at lowering", pr.Name, typeName)
		}
		setResult, ok := fl.spellPropType(pr.Typ, pos)
		if !ok {
			base.Fatalf("logos: property %s of %s has a type that cannot be spelled in this file", pr.Name, typeName)
		}
		setterRecv := recvTypePointer(recvShape, pos)
		if getFn != nil && setFn != nil && !isPointerRecv(setFn) {
			setterRecv = recvTypeValue(recvShape, pos)
		}
		setParam := nameOf(pos, "v")
		var setBody syntax.Stmt
		if getFn != nil {
			setBody = exprStmtOf(pos, callOf(pos, selOf(pos, nameOf(pos, "m"), "Set"+pr.Name), []syntax.Expr{setParam}))
		} else {
			lhs := selOf(pos, nameOf(pos, "m"), pr.Name)
			setBody = at(&syntax.AssignStmt{Lhs: lhs, Rhs: setParam}, pos)
		}
		added = append(added, at(&syntax.FuncDecl{
			Recv: at(&syntax.Field{Name: nameOf(pos, "m"), Type: setterRecv}, pos),
			Name: nameOf(pos, "Logos_Set"+pr.Name),
			Type: at(&syntax.FuncType{ParamList: []*syntax.Field{at(&syntax.Field{
				Name: setParam,
				Type: setResult,
			}, pos)}}, pos),
			Body: blockOf(pos, []syntax.Stmt{setBody}),
		}, pos))
	}
	if len(added) == 0 {
		return
	}
	for _, d := range added {
		var buf bytes.Buffer
		_, _ = syntax.Fprint(&buf, d, 0)
		text.WriteString("\n\n")
		text.Write(buf.Bytes())
	}
	for _, d := range added {
		fl.file.DeclList = append(fl.file.DeclList, d)
	}
	fl.add(fl.orig, fl.orig, text.String())
}

func selfShape(td *syntax.TypeDecl, pos syntax.Pos) syntax.Expr {
	var shape syntax.Expr = nameOf(pos, td.Name.Value)
	if len(td.TParamList) == 0 {
		return shape
	}
	args := make([]syntax.Expr, len(td.TParamList))
	for i, p := range td.TParamList {
		args[i] = nameOf(pos, p.Name.Value)
	}
	return at(&syntax.IndexExpr{X: shape, Index: listOf(pos, args)}, pos)
}

func recvTypeValue(base syntax.Expr, pos syntax.Pos) syntax.Expr {
	return respellRecv(base, pos, false)
}

func recvTypePointer(base syntax.Expr, pos syntax.Pos) syntax.Expr {
	return respellRecv(base, pos, true)
}

func respellRecv(shape syntax.Expr, pos syntax.Pos, pointer bool) syntax.Expr {
	text, ok := respellType(shape, pos)
	if !ok {
		base.Fatalf("logos: cannot respell receiver type %q", renderExpr(shape))
	}
	if !pointer {
		return text
	}
	return at(&syntax.Operation{Op: syntax.Mul, X: text}, pos)
}

func isPointerRecv(fn *types2.Func) bool {
	sig, ok := fn.Type().(*types2.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	_, ok = types2.Unalias(sig.Recv().Type()).(*types2.Pointer)
	return ok
}

func (fl *fileLower) spellPropType(t types2.Type, pos syntax.Pos) (syntax.Expr, bool) {
	text, ok := fl.spellLowered(t)
	if !ok {
		return nil, false
	}
	return typeExprAt(text, pos)
}

func propSourceOf(pkg *types2.Package, v types2.Type, name string, want types2.Type) (getter, setter, ambig bool, getFn, setFn *types2.Func) {
	obj, index, _ := types2.LookupFieldOrMethod(v, false, pkg, name)
	if obj == nil {
		if index != nil {
			return false, false, true, nil, nil
		}
	} else if fld, ok := obj.(*types2.Var); ok {
		if types2.Identical(fld.Type(), want) {
			return true, true, false, nil, nil
		}
		return false, false, false, nil, nil
	} else if m, ok := obj.(*types2.Func); ok {
		if sig, ok := m.Type().(*types2.Signature); ok && sig.Params().Len() == 0 && sig.Results().Len() == 1 && types2.Identical(sig.Results().At(0).Type(), want) {
			getter = true
			getFn = m
		}
	}
	sobj, sindex, _ := types2.LookupFieldOrMethod(v, false, pkg, "Set"+name)
	if sobj == nil {
		return getter, false, sindex != nil, getFn, nil
	}
	if sm, ok := sobj.(*types2.Func); ok {
		if sig, ok := sm.Type().(*types2.Signature); ok && sig.Params().Len() == 1 && sig.Results().Len() == 0 && types2.Identical(sig.Params().At(0).Type(), want) {
			return getter, true, false, getFn, sm
		}
	}
	return getter, false, false, getFn, nil
}

type propNeedle struct {
	start, end int
	name       string
	recv       span
	temp       string
}

type propHoist struct {
	pres  []syntax.Stmt
	posts []syntax.Stmt
}

type propCollect struct {
	needles []propNeedle
	hoist   *propHoist
}

func (fl *fileLower) propSelOf(e syntax.Expr) (recv syntax.Expr, pr types2.LogosProp, sel *syntax.SelectorExpr, ok bool) {
	var is bool
	if sel, is = syntax.Unparen(e).(*syntax.SelectorExpr); !is || sel.Sel == nil {
		return nil, types2.LogosProp{}, nil, false
	}
	recv = syntax.Unparen(sel.X)
	t := fl.pkg.info.TypeOf(recv)
	if t == nil {
		return nil, types2.LogosProp{}, nil, false
	}
	pr, _, found := types2.LogosPropertySelection(fl.pkg.tpkg, t, sel.Sel.Value)
	if !found {
		return nil, types2.LogosProp{}, nil, false
	}
	return recv, pr, sel, true
}

func (fl *fileLower) propSelSpan(sel *syntax.SelectorExpr) (whole, recv span, ok bool) {
	selOff := fl.off(sel.Sel.Pos())
	start := fl.off(leftmostOf(sel))
	if selOff <= start {
		return span{}, span{}, false
	}
	dot := -1
	scanSegment(fl.src, start, selOff, func(t tok) bool {
		if t.kind == token.PERIOD {
			dot = t.start
		}
		return true
	})
	if dot < 0 {
		return span{}, span{}, false
	}
	return span{start: start, end: selOff + len(sel.Sel.Value)}, span{start: start, end: dot}, true
}

func propTempName(selOff int) string {
	return "_logos_pt" + strconv.Itoa(selOff)
}

func propPinName(selOff int) string {
	return "_logos_pr" + strconv.Itoa(selOff)
}

func propTrivialRecv(recv syntax.Expr) bool {
	_, ok := syntax.Unparen(recv).(*syntax.Name)
	return ok
}

func (fl *fileLower) propReadIn(e syntax.Expr, c *propCollect) syntax.Expr {
	c.hoist = nil
	return fl.propWalk(e, c)
}

func (fl *fileLower) propWalk(e syntax.Expr, c *propCollect) syntax.Expr {
	switch e := e.(type) {
	case nil:
		return nil
	case *syntax.Name, *syntax.BasicLit:
		return e
	case *syntax.ParenExpr:
		e.X = fl.propWalk(e.X, c)
		return e
	case *syntax.TryExpr:
		e.X = fl.propWalk(e.X, c)
		return e
	case *syntax.SelectorExpr:
		if _, pr, sel, ok := fl.propSelOf(e); ok {
			fl.recordPropRead(c, pr, sel)
			e.X = fl.propWalk(e.X, c)
			pos := sel.Pos()
			sel.Sel.Value = "Logos_Get" + pr.Name
			return callOf(pos, sel, nil)
		}
		e.X = fl.propWalk(e.X, c)
		return e
	case *syntax.CallExpr:
		return fl.propWalkCall(e, c)
	case *syntax.IndexExpr:
		e.X = fl.propWalk(e.X, c)
		e.Index = fl.propWalk(e.Index, c)
		return e
	case *syntax.SliceExpr:
		e.X = fl.propWalk(e.X, c)
		for i, x := range e.Index {
			if x != nil {
				e.Index[i] = fl.propWalk(x, c)
			}
		}
		return e
	case *syntax.AssertExpr:
		e.X = fl.propWalk(e.X, c)
		return e
	case *syntax.Operation:
		e.X = fl.propWalk(e.X, c)
		if e.Y != nil {
			e.Y = fl.propWalk(e.Y, c)
		}
		return e
	case *syntax.CompositeLit:
		for i, x := range e.ElemList {
			e.ElemList[i] = fl.propWalk(x, c)
		}
		return e
	case *syntax.KeyValueExpr:
		e.Key = fl.propWalk(e.Key, c)
		e.Value = fl.propWalk(e.Value, c)
		return e
	case *syntax.ListExpr:
		for i, x := range e.ElemList {
			e.ElemList[i] = fl.propWalk(x, c)
		}
		return e
	case *syntax.InterpExpr:
		for i, x := range e.Exprs {
			e.Exprs[i] = fl.propWalk(x, c)
		}
		return e
	case *syntax.FuncLit:
		if e.Body != nil {
			e.Body.List = fl.lowerPropBlock(e.Body.List)
		}
		return e
	default:
		return e
	}
}

func (fl *fileLower) recordPropRead(c *propCollect, pr types2.LogosProp, sel *syntax.SelectorExpr) {
	whole, recvSp, ok := fl.propSelSpan(sel)
	if !ok {
		base.Fatalf("logos: cannot span property selection at %s", sel.Pos().String())
	}
	c.needles = append(c.needles, propNeedle{
		start: whole.start,
		end:   whole.end,
		name:  pr.Name,
		recv:  recvSp,
	})
}

func (fl *fileLower) chainPropGet(c *propCollect, pr types2.LogosProp, sel, inner *syntax.SelectorExpr) {
	fl.recordPropRead(c, pr, inner)
	pos := inner.Pos()
	inner.Sel.Value = "Logos_Get" + pr.Name
	sel.X = callOf(pos, inner, nil)
}

func (fl *fileLower) propWalkCall(e *syntax.CallExpr, c *propCollect) syntax.Expr {
	sel, targ, ok := propCallSel(e.Fun)
	if !ok {
		e.Fun = fl.propWalk(e.Fun, c)
		fl.propWalkArgs(e.ArgList, c)
		return e
	}
	recv, pr, inner, ok := fl.propSelOf(sel.X)
	if !ok {
		e.Fun = fl.propWalk(e.Fun, c)
		fl.propWalkArgs(e.ArgList, c)
		return e
	}
	if propSelIsField(fl.pkg.info, sel) || targ || c.hoist == nil {
		fl.chainPropGet(c, pr, sel, inner)
		fl.propWalkArgs(e.ArgList, c)
		return e
	}
	fl.hoistPropCall(c, propSite{sel: sel, recv: recv, pr: pr, inner: inner})
	fl.propWalkArgs(e.ArgList, c)
	return e
}

type propSite struct {
	sel   *syntax.SelectorExpr
	recv  syntax.Expr
	pr    types2.LogosProp
	inner *syntax.SelectorExpr
}

type propTemp struct {
	pos    syntax.Pos
	pin    string
	temp   string
	needle propNeedle
	recv   syntax.Expr
	pr     types2.LogosProp
}

func propCallSel(fun syntax.Expr) (sel *syntax.SelectorExpr, targ bool, ok bool) {
	for {
		switch inner := syntax.Unparen(fun).(type) {
		case *syntax.SelectorExpr:
			return inner, targ, true
		case *syntax.IndexExpr:
			targ = true
			fun = inner.X
		default:
			return nil, false, false
		}
	}
}

func propSelIsField(info *types2.Info, sel *syntax.SelectorExpr) bool {
	if info == nil || info.Selections == nil {
		return false
	}
	s, ok := info.Selections[sel]
	if !ok || s == nil {
		return false
	}
	_, ok = s.Obj().(*types2.Var)
	return ok
}

func (fl *fileLower) propWalkArgs(args []syntax.Expr, c *propCollect) {
	for i, a := range args {
		args[i] = fl.propWalk(a, c)
	}
}

func (fl *fileLower) hoistPropCall(c *propCollect, site propSite) string {
	whole, recvSp, ok := fl.propSelSpan(site.inner)
	if !ok {
		base.Fatalf("logos: cannot span property selection at %s", site.inner.Pos().String())
	}
	selOff := fl.off(site.inner.Sel.Pos())
	pos := site.inner.Pos()
	t := propTemp{
		pos:    pos,
		temp:   propTempName(selOff),
		needle: propNeedle{start: whole.start, end: whole.end, name: site.pr.Name, recv: recvSp, temp: propTempName(selOff)},
		recv:   site.recv,
		pr:     site.pr,
	}
	if !propTrivialRecv(site.recv) {
		t.pin = propPinName(selOff)
	}
	site.inner.Sel.Value = t.temp
	site.sel.X = nameOf(leftmostOf(site.recv), t.temp)
	c.needles = append(c.needles, t.needle)
	c.hoist.pres = append(c.hoist.pres, t.prelude()...)
	if !site.pr.ReadOnly && propNeedsWriteBack(fl.pkg.info, site.sel) {
		c.hoist.posts = append(c.hoist.posts, t.writeBack())
	}
	return t.temp
}

func propNeedsWriteBack(info *types2.Info, sel *syntax.SelectorExpr) bool {
	if info == nil || info.Selections == nil {
		return true
	}
	s, ok := info.Selections[sel]
	if !ok || s == nil {
		return true
	}
	fn, ok := s.Obj().(*types2.Func)
	if !ok {
		return true
	}
	return isPointerRecv(fn)
}

func (t propTemp) prelude() []syntax.Stmt {
	if t.pin == "" {
		return []syntax.Stmt{
			defineOf(t.pos, []syntax.Expr{nameOf(t.pos, t.temp)}, []syntax.Expr{t.getCall(t.recv)}),
		}
	}
	return []syntax.Stmt{
		defineOf(t.pos, []syntax.Expr{nameOf(t.pos, t.pin)}, []syntax.Expr{t.recv}),
		defineOf(t.pos, []syntax.Expr{nameOf(t.pos, t.temp)}, []syntax.Expr{t.getCall(nameOf(t.pos, t.pin))}),
	}
}

func (t propTemp) getCall(recv syntax.Expr) syntax.Expr {
	return callOf(t.pos, selOf(t.pos, recv, "Logos_Get"+t.pr.Name), nil)
}

func (t propTemp) writeBack() syntax.Stmt {
	base := t.recv
	if t.pin != "" {
		base = nameOf(t.pos, t.pin)
	}
	return exprStmtOf(t.pos, callOf(t.pos, selOf(t.pos, base, "Logos_Set"+t.pr.Name), []syntax.Expr{nameOf(t.pos, t.temp)}))
}

func (fl *fileLower) lowerPropBlock(list []syntax.Stmt) []syntax.Stmt {
	var out []syntax.Stmt
	for _, s := range list {
		out = append(out, fl.lowerPropStmt(s)...)
	}
	return out
}

func (fl *fileLower) lowerPropStmt(s syntax.Stmt) []syntax.Stmt {
	switch s := s.(type) {
	case *syntax.BlockStmt:
		s.List = fl.lowerPropBlock(s.List)
		return []syntax.Stmt{s}
	case *syntax.IfStmt:
		s.Init = fl.propClause(s.Init, -1)
		if s.Cond != nil {
			s.Cond = fl.propReadTop(s.Cond)
		}
		s.Then.List = fl.lowerPropBlock(s.Then.List)
		switch els := s.Else.(type) {
		case *syntax.BlockStmt:
			els.List = fl.lowerPropBlock(els.List)
		case *syntax.IfStmt:
			s.Else = fl.lowerPropStmt(els)[0]
		}
		return []syntax.Stmt{s}
	case *syntax.ForStmt:
		s.Init = fl.propClause(s.Init, -1)
		if s.Cond != nil {
			s.Cond = fl.propReadTop(s.Cond)
		}
		s.Post = fl.propClause(s.Post, fl.off(s.Body.Pos()))
		s.Body.List = fl.lowerPropBlock(s.Body.List)
		return []syntax.Stmt{s}
	case *syntax.SwitchStmt:
		s.Init = fl.propClause(s.Init, -1)
		if s.Tag != nil {
			if tg, ok := s.Tag.(*syntax.TypeSwitchGuard); ok {
				tg.X = fl.propReadTop(tg.X)
			} else {
				s.Tag = fl.propReadTop(s.Tag)
			}
		}
		for _, cc := range s.Body {
			if cc.Cases != nil {
				cc.Cases = fl.propReadTop(cc.Cases)
			}
			cc.Body = fl.lowerPropBlock(cc.Body)
		}
		return []syntax.Stmt{s}
	case *syntax.SelectStmt:
		for _, cc := range s.Body {
			fl.propReadSimple(cc.Comm)
			cc.Body = fl.lowerPropBlock(cc.Body)
		}
		return []syntax.Stmt{s}
	case *syntax.RangeClause:
		if s.Lhs != nil {
			s.Lhs = fl.propReadTop(s.Lhs)
		}
		s.X = fl.propReadTop(s.X)
		return []syntax.Stmt{s}
	case *syntax.LabeledStmt:
		inner := fl.lowerPropStmt(s.Stmt)
		s.Stmt = inner[0]
		return append([]syntax.Stmt{s}, inner[1:]...)
	case *syntax.DeclStmt:
		return fl.lowerPropDeclStmt(s)
	case *syntax.SendStmt:
		return fl.lowerPropSend(s)
	case *syntax.AssignStmt:
		return fl.lowerPropAssign(s)
	case *syntax.ExprStmt:
		return fl.lowerPropExprStmt(s)
	case *syntax.CallStmt:
		fl.propReadSimple(s)
		return []syntax.Stmt{s}
	case *syntax.ReturnStmt:
		fl.propReadSimple(s)
		return []syntax.Stmt{s}
	case *syntax.ErrdeferStmt:
		fl.propReadSimple(s)
		return []syntax.Stmt{s}
	default:
		return []syntax.Stmt{s}
	}
}

func (fl *fileLower) propReadTop(e syntax.Expr) syntax.Expr {
	if e == nil {
		return nil
	}
	c := &propCollect{}
	e = fl.propReadIn(e, c)
	fl.emitPropReads(c)
	return e
}

func (fl *fileLower) propReadSimple(s syntax.Stmt) {
	if s == nil {
		return
	}
	c := &propCollect{}
	switch s := s.(type) {
	case *syntax.AssignStmt:
		if s.Lhs != nil {
			s.Lhs = fl.propReadIn(s.Lhs, c)
		}
		if s.Rhs != nil {
			s.Rhs = fl.propReadIn(s.Rhs, c)
		}
	case *syntax.ExprStmt:
		s.X = fl.propReadIn(s.X, c)
	case *syntax.SendStmt:
		s.Chan = fl.propReadIn(s.Chan, c)
		s.Value = fl.propReadIn(s.Value, c)
	case *syntax.CallStmt:
		s.Call = fl.propReadIn(s.Call, c)
	case *syntax.ReturnStmt:
		if s.Results != nil {
			s.Results = fl.propReadIn(s.Results, c)
		}
	case *syntax.ErrdeferStmt:
		s.Call = fl.propReadIn(s.Call, c)
	case *syntax.DeclStmt:
		for _, d := range s.DeclList {
			if vd, ok := d.(*syntax.VarDecl); ok && vd.Values != nil {
				vd.Values = fl.propReadIn(vd.Values, c)
			}
		}
	case *syntax.RangeClause:
		if s.Lhs != nil {
			s.Lhs = fl.propReadIn(s.Lhs, c)
		}
		s.X = fl.propReadIn(s.X, c)
	}
	fl.emitPropReads(c)
}

func (fl *fileLower) emitPropReads(c *propCollect) {
	for _, n := range c.needles {
		if n.temp != "" || fl.propEmitted[n.end] {
			continue
		}
		if fl.propEmitted == nil {
			fl.propEmitted = make(map[int]bool)
		}
		fl.propEmitted[n.end] = true
		off := n.end - len(n.name)
		fl.add(off, n.end, "Logos_Get"+n.name+"()")
	}
}

func (fl *fileLower) lowerPropAssign(s *syntax.AssignStmt) []syntax.Stmt {
	lhs := syntax.UnpackListExpr(s.Lhs)
	if _, _, isTry := tryAssignOf(s); isTry && s.Op == 0 {
		for _, l := range lhs {
			if _, _, _, ok := fl.propSelOf(l); ok {
				c := &propCollect{}
				s.Rhs = fl.propWalk(s.Rhs, c)
				fl.emitPropReads(c)
				return []syntax.Stmt{s}
			}
		}
	}
	if s.Rhs == nil {
		if len(lhs) != 1 {
			return []syntax.Stmt{s}
		}
		if recv, pr, _, ok := fl.propSelOf(lhs[0]); ok {
			if pr.ReadOnly {
				return fl.lowerPropGeneric(s)
			}
			return fl.lowerPropCompound(s, recv, pr, nil)
		}
		return fl.lowerPropGeneric(s)
	}
	if s.Op != 0 && s.Op != syntax.Def {
		if len(lhs) != 1 {
			return []syntax.Stmt{s}
		}
		if recv, pr, _, ok := fl.propSelOf(lhs[0]); ok {
			if pr.ReadOnly {
				return fl.lowerPropGeneric(s)
			}
			rhs := syntax.UnpackListExpr(s.Rhs)
			if len(rhs) != 1 {
				return []syntax.Stmt{s}
			}
			return fl.lowerPropCompound(s, recv, pr, rhs[0])
		}
		return fl.lowerPropGeneric(s)
	}
	if len(lhs) == 1 {
		if recv, pr, _, ok := fl.propSelOf(lhs[0]); ok {
			if s.Op != 0 || pr.ReadOnly {
				return fl.lowerPropGeneric(s)
			}
			return fl.lowerPropWrite(s, recv, pr)
		}
	}
	if len(lhs) > 1 {
		roots := make([]int, 0, len(lhs))
		for i, l := range lhs {
			if _, _, _, ok := fl.propSelOf(l); ok {
				roots = append(roots, i)
			}
		}
		if len(roots) > 0 {
			if s.Op != 0 {
				return fl.lowerPropGeneric(s)
			}
			return fl.lowerPropMulti(s, lhs, roots)
		}
	}
	return fl.lowerPropGeneric(s)
}

func (fl *fileLower) propStmtSpan(s syntax.Stmt, first syntax.Expr) (span, bool) {
	sp, ok := fl.platformSpan(first)
	if !ok {
		return span{}, false
	}
	end := stmtEnd(fl.src, sp.start)
	if limit, ok := fl.stmtLimit[s]; ok {
		end = lastTokenEnd(fl.src, sp.start, min(end, limit))
	}
	if end <= sp.start {
		return span{}, false
	}
	return span{start: sp.start, end: end}, true
}

func (fl *fileLower) propClause(clause syntax.SimpleStmt, limit int) syntax.SimpleStmt {
	if clause == nil {
		return nil
	}
	if as, ok := clause.(*syntax.AssignStmt); ok && as.Op == syntax.Def {
		fl.propReadSimple(clause)
		return clause
	}
	if limit >= 0 {
		if fl.stmtLimit == nil {
			fl.stmtLimit = make(map[syntax.Stmt]int)
		}
		fl.stmtLimit[clause] = limit
	}
	var first syntax.Expr
	switch c := clause.(type) {
	case *syntax.AssignStmt:
		first = syntax.UnpackListExpr(c.Lhs)[0]
	case *syntax.ExprStmt:
		first = c.X
	case *syntax.SendStmt:
		first = c.Chan
	}
	var sp span
	spOK := false
	if first != nil {
		sp, spOK = fl.propStmtSpan(clause, first)
	}
	out := fl.lowerPropStmt(clause)
	if len(out) == 1 && out[0] == clause {
		return clause
	}
	if !spOK {
		base.Fatalf("logos: cannot span clause statement at %s", clause.Pos().String())
	}
	pos := clause.Pos()
	lit := at(&syntax.FuncLit{Type: at(&syntax.FuncType{}, pos), Body: blockOf(pos, out)}, pos)
	fl.addT(sp.start, sp.end, cat(gen("func() { "), fl.textOf(sp), gen(" }()")))
	return exprStmtOf(pos, callOf(pos, lit, nil))
}

func propNeedlesNest(needles []propNeedle) bool {
	for i := range needles {
		for j := range needles {
			if i != j && needles[i].start <= needles[j].start && needles[i].end >= needles[j].end &&
				(needles[i].start != needles[j].start || needles[i].end != needles[j].end) {
				return true
			}
		}
	}
	return false
}

type propSubst struct {
	needle otext
	repl   otext
	len    int
	start  int
	end    int
}

func (fl *fileLower) propSubstitutions(needles []propNeedle) []propSubst {
	subs := make([]propSubst, 0, len(needles))
	for _, n := range needles {
		if n.temp != "" {
			subs = append(subs, propSubst{needle: fl.textOf(span{start: n.start, end: n.end}), repl: gen(n.temp), len: n.end - n.start, start: n.start, end: n.end})
			continue
		}
		subs = append(subs, propSubst{
			needle: fl.textOf(span{start: n.start, end: n.end}),
			repl:   cat(fl.textOf(n.recv), gen(".Logos_Get"+n.name+"()")),
			len:    n.end - n.start,
			start:  n.start,
			end:    n.end,
		})
	}
	slices.SortFunc(subs, func(a, b propSubst) int {
		if d := b.len - a.len; d != 0 {
			return d
		}
		if a.start != b.start {
			if a.start < b.start {
				return -1
			}
			return 1
		}
		return 0
	})
	return subs
}

func propSubstText(text otext, subs []propSubst) otext {
	for _, s := range subs {
		if s.needle.s == "" {
			continue
		}
		text = propSubstOne(text, s)
	}
	return text
}

func propSubstOne(t otext, s propSubst) otext {
	needle := s.needle.s
	for off := 0; off+len(needle) <= len(t.s); {
		idx := strings.Index(t.s[off:], needle)
		if idx < 0 {
			return t
		}
		i := off + idx
		j := i + len(needle)
		if len(t.chunks) == 0 || propSliceMatches(t.slice(i, j), s.start, s.end) {
			return cat(t.slice(0, i), s.repl, t.slice(j, len(t.s)))
		}
		off = i + 1
	}
	return t
}

func propSliceMatches(sl otext, start, end int) bool {
	if len(sl.chunks) == 0 {
		return false
	}
	if sl.chunks[0].out != 0 || sl.chunks[0].orig != start {
		return false
	}
	last := sl.chunks[len(sl.chunks)-1]
	if last.out+last.n != len(sl.s) {
		return false
	}
	if last.orig+last.n != end {
		return false
	}
	off := 0
	for _, c := range sl.chunks {
		if c.out != off {
			return false
		}
		off += c.n
	}
	return off == len(sl.s)
}

func (fl *fileLower) lowerPropGeneric(s *syntax.AssignStmt) []syntax.Stmt {
	c := &propCollect{hoist: &propHoist{}}
	s.Lhs = fl.propWalk(s.Lhs, c)
	if s.Rhs != nil {
		s.Rhs = fl.propWalk(s.Rhs, c)
	}
	if len(c.hoist.pres) == 0 && len(c.hoist.posts) == 0 && !propNeedlesNest(c.needles) {
		fl.emitPropReads(c)
		return []syntax.Stmt{s}
	}
	sp, ok := fl.propStmtSpan(s, syntax.UnpackListExpr(s.Lhs)[0])
	if !ok {
		fl.emitPropReads(c)
		return []syntax.Stmt{s}
	}
	lhs := syntax.UnpackListExpr(s.Lhs)
	lhsSpans, ok := fl.propLhsSpans(s, lhs)
	if !ok {
		fl.emitPropReads(c)
		return []syntax.Stmt{s}
	}
	lhsTexts := make([]otext, 0, len(lhs))
	for _, lsp := range lhsSpans {
		lhsTexts = append(lhsTexts, fl.textOf(lsp))
	}
	rhsText := otext{}
	if s.Rhs != nil {
		rhsText = fl.propOperandText(s.Rhs, c)
	}
	subs := fl.propSubstitutions(c.needles)
	for i := range lhsTexts {
		lhsTexts[i] = propSubstText(lhsTexts[i], subs)
	}
	rhsText = propSubstText(rhsText, subs)
	op := "="
	if s.Op == syntax.Def {
		op = ":="
	} else if s.Op != 0 {
		op = s.Op.String()
	}
	main := cat(joinO(lhsTexts, ", "), gen(" "+op+" "), rhsText)
	if s.Rhs == nil {
		main = cat(joinO(lhsTexts, ", "), gen(op+op))
	}
	return fl.finishPropReplace(c, sp, []syntax.Stmt{s}, []otext{main})
}

func propMustSpan(fl *fileLower, e syntax.Expr) span {
	if list, ok := e.(*syntax.ListExpr); ok && len(list.ElemList) > 0 {
		first := propMustSpan(fl, list.ElemList[0])
		last := propMustSpan(fl, list.ElemList[len(list.ElemList)-1])
		return span{start: first.start, end: last.end}
	}
	sp, ok := fl.platformSpan(e)
	if !ok {
		base.Fatalf("logos: cannot span expression at %s", e.Pos().String())
	}
	return sp
}

func (fl *fileLower) propOperandText(e syntax.Expr, c *propCollect) otext {
	sp := propMustSpan(fl, e)
	if propNeedleWithin(c.needles, sp) {
		return fl.drainBodyWrapper(e, fl.textOf(sp), sp)
	}
	return fl.drainBodyWrapper(e, fl.lazyText(sp), sp)
}

func propNeedleWithin(needles []propNeedle, sp span) bool {
	for _, n := range needles {
		if n.start < sp.end && n.end > sp.start {
			return true
		}
	}
	return false
}

func (fl *fileLower) propRecvText(sel *syntax.SelectorExpr) otext {
	_, recvSp, ok := fl.propSelSpan(sel)
	if !ok {
		base.Fatalf("logos: cannot span property selection at %s", sel.Pos().String())
	}
	return fl.textOf(recvSp)
}

func (fl *fileLower) propLhsSpans(s *syntax.AssignStmt, lhs []syntax.Expr) ([]span, bool) {
	out := make([]span, 0, len(lhs))
	for i, l := range lhs {
		sp, ok := fl.platformSpan(l)
		if !ok {
			return nil, false
		}
		if i == len(lhs)-1 && s.Rhs != nil {
			sp.end = fl.off(s.Pos())
			if sp.end <= sp.start {
				return nil, false
			}
		}
		out = append(out, sp)
	}
	return out, true
}

func (fl *fileLower) finishPropReplace(c *propCollect, sp span, mains []syntax.Stmt, mainTexts []otext) []syntax.Stmt {
	_ = fl.textOf(sp)
	subs := fl.propSubstitutions(c.needles)
	var texts []otext
	var out []syntax.Stmt
	for _, st := range c.hoist.pres {
		texts = append(texts, propSubstText(gen(fl.propStmtText(st)), subs))
		out = append(out, st)
	}
	for i, m := range mains {
		texts = append(texts, propSubstText(mainTexts[i], subs))
		out = append(out, m)
	}
	for _, st := range c.hoist.posts {
		texts = append(texts, propSubstText(gen(fl.propStmtText(st)), subs))
		out = append(out, st)
	}
	fl.addT(sp.start, sp.end, joinO(texts, "; "))
	return out
}

func (fl *fileLower) propStmtText(st syntax.Stmt) string {
	var buf bytes.Buffer
	_, _ = syntax.Fprint(&buf, st, 0)
	return buf.String()
}

func (fl *fileLower) lowerPropWrite(s *syntax.AssignStmt, recv syntax.Expr, pr types2.LogosProp) []syntax.Stmt {
	sp, ok := fl.propStmtSpan(s, s.Lhs)
	if !ok {
		c := &propCollect{}
		s.Lhs = fl.propReadIn(s.Lhs, c)
		s.Rhs = fl.propReadIn(s.Rhs, c)
		fl.emitPropReads(c)
		return []syntax.Stmt{s}
	}
	c := &propCollect{hoist: &propHoist{}}
	recv = fl.propWalk(recv, c)
	rhs := fl.propWalk(s.Rhs, c)
	sel := mustPropSel(fl, s.Lhs)
	recvText := fl.propRecvText(sel)
	rhsText := fl.propOperandText(s.Rhs, c)
	set := exprStmtOf(s.Pos(), callOf(s.Pos(), selOf(s.Pos(), recv, "Logos_Set"+pr.Name), []syntax.Expr{rhs}))
	subs := fl.propSubstitutions(c.needles)
	main := cat(propSubstText(recvText, subs), gen(".Logos_Set"+pr.Name+"("), propSubstText(rhsText, subs), gen(")"))
	return fl.finishPropReplace(c, sp, []syntax.Stmt{set}, []otext{main})
}

func (fl *fileLower) lowerPropCompound(s *syntax.AssignStmt, recv syntax.Expr, pr types2.LogosProp, rhs syntax.Expr) []syntax.Stmt {
	sp, ok := fl.propStmtSpan(s, s.Lhs)
	if !ok {
		c := &propCollect{}
		s.Lhs = fl.propReadIn(s.Lhs, c)
		if s.Rhs != nil {
			s.Rhs = fl.propReadIn(s.Rhs, c)
		}
		fl.emitPropReads(c)
		return []syntax.Stmt{s}
	}
	c := &propCollect{hoist: &propHoist{}}
	recv = fl.propWalk(recv, c)
	if rhs != nil {
		rhs = fl.propWalk(rhs, c)
	}
	pos := s.Pos()
	recvText := fl.propRecvText(mustPropSel(fl, s.Lhs))
	pin := ""
	if !propTrivialRecv(recv) {
		pin = propPinName(fl.off(mustPropSel(fl, s.Lhs).Sel.Pos()))
		c.hoist.pres = append(c.hoist.pres, defineOf(pos, []syntax.Expr{nameOf(pos, pin)}, []syntax.Expr{recv}))
	}
	valueText := gen("1")
	if rhs != nil {
		valueText = fl.propOperandText(rhs, c)
	}
	bop := s.Op.String()
	getBase, setBase := recv, recv
	if pin != "" {
		getBase = nameOf(pos, pin)
		setBase = nameOf(pos, pin)
	} else if n, ok := syntax.Unparen(recv).(*syntax.Name); ok {
		setBase = nameOf(pos, n.Value)
	}
	var valueNode syntax.Expr = litOf(pos, "1", syntax.IntLit)
	if rhs != nil {
		valueNode = rhs
	}
	set := exprStmtOf(pos, callOf(pos, selOf(pos, setBase, "Logos_Set"+pr.Name),
		[]syntax.Expr{at(&syntax.Operation{Op: s.Op, X: callOf(pos, selOf(pos, getBase, "Logos_Get"+pr.Name), nil), Y: valueNode}, pos)}))
	ref := recvText
	if pin != "" {
		ref = gen(pin)
	}
	subs := fl.propSubstitutions(c.needles)
	main := cat(propSubstText(ref, subs), gen(".Logos_Set"+pr.Name+"("), propSubstText(ref, subs), gen(".Logos_Get"+pr.Name+"() "+bop+" "), propSubstText(valueText, subs), gen(")"))
	return fl.finishPropReplace(c, sp, []syntax.Stmt{set}, []otext{main})
}

func mustPropSel(fl *fileLower, e syntax.Expr) *syntax.SelectorExpr {
	_, _, sel, ok := fl.propSelOf(e)
	if !ok {
		base.Fatalf("logos: property target lost at %s", e.Pos().String())
	}
	return sel
}

func (fl *fileLower) lowerPropMulti(s *syntax.AssignStmt, lhs []syntax.Expr, roots []int) []syntax.Stmt {
	sp, spOK := fl.propStmtSpan(s, lhs[0])
	c := &propCollect{hoist: &propHoist{}}
	for i, l := range lhs {
		if slices.Contains(roots, i) {
			continue
		}
		fl.propWalk(l, c)
	}
	s.Rhs = fl.propWalk(s.Rhs, c)
	if !spOK {
		fl.emitPropReads(c)
		return []syntax.Stmt{s}
	}
	pos := s.Pos()
	lhsSpans, ok := fl.propLhsSpans(s, lhs)
	if !ok {
		fl.emitPropReads(c)
		return []syntax.Stmt{s}
	}
	lhsTexts := make([]otext, len(lhs))
	for _, i := range roots {
		temp, ok := fl.propRootHoist(c, pos, lhs[i])
		if !ok {
			fl.emitPropReads(c)
			return []syntax.Stmt{s}
		}
		fl.propRetarget(s, i, temp)
		lhsTexts[i] = gen(temp)
	}
	for i := range lhs {
		if slices.Contains(roots, i) {
			continue
		}
		lhsTexts[i] = fl.textOf(lhsSpans[i])
	}
	rhsText := fl.propOperandText(s.Rhs, c)
	subs := fl.propSubstitutions(c.needles)
	for i := range lhsTexts {
		lhsTexts[i] = propSubstText(lhsTexts[i], subs)
	}
	main := cat(joinO(lhsTexts, ", "), gen(" = "), propSubstText(rhsText, subs))
	return fl.finishPropReplace(c, sp, []syntax.Stmt{s}, []otext{main})
}

func (fl *fileLower) propRootHoist(c *propCollect, pos syntax.Pos, l syntax.Expr) (string, bool) {
	recv, pr, sel, ok := fl.propSelOf(l)
	if !ok || pr.ReadOnly {
		return "", false
	}
	fl.propWalk(recv, c)
	_, recvSp, ok := fl.propSelSpan(sel)
	if !ok {
		return "", false
	}
	_ = fl.textOf(recvSp)
	temp := propTempName(fl.off(sel.Sel.Pos()))
	typ, ok := fl.spellPropType(pr.Typ, pos)
	if !ok {
		base.Fatalf("logos: property %s has a type that cannot be spelled in this file", pr.Name)
	}
	setBase := recv
	if !propTrivialRecv(recv) {
		pin := temp + "r"
		c.hoist.pres = append(c.hoist.pres, defineOf(pos, []syntax.Expr{nameOf(pos, pin)}, []syntax.Expr{recv}))
		setBase = nameOf(pos, pin)
	}
	c.hoist.pres = append(c.hoist.pres, at(&syntax.DeclStmt{DeclList: []syntax.Decl{
		at(&syntax.VarDecl{NameList: []*syntax.Name{nameOf(pos, temp)}, Type: typ}, pos),
	}}, pos))
	c.hoist.posts = append(c.hoist.posts, exprStmtOf(pos, callOf(pos, selOf(pos, setBase, "Logos_Set"+pr.Name), []syntax.Expr{nameOf(pos, temp)})))
	return temp, true
}

func (fl *fileLower) propRetarget(s *syntax.AssignStmt, i int, temp string) {
	le, ok := s.Lhs.(*syntax.ListExpr)
	if !ok {
		base.Fatalf("logos: multi-assign target without a list at %s", s.Pos().String())
	}
	le.ElemList[i] = nameOf(s.Pos(), temp)
}

func (fl *fileLower) lowerPropExprStmt(s *syntax.ExprStmt) []syntax.Stmt {
	call, ok := syntax.Unparen(s.X).(*syntax.CallExpr)
	if !ok {
		c := &propCollect{}
		s.X = fl.propReadIn(s.X, c)
		fl.emitPropReads(c)
		return []syntax.Stmt{s}
	}
	sel, targ, ok := propCallSel(call.Fun)
	if !ok {
		return fl.lowerPropCallArgs(s, call)
	}
	recv, pr, inner, ok := fl.propSelOf(sel.X)
	if !ok {
		return fl.lowerPropCallArgs(s, call)
	}
	if propSelIsField(fl.pkg.info, sel) || targ {
		return fl.lowerPropCallArgs(s, call)
	}
	sp, spOK := fl.propStmtSpan(s, s.X)
	c := &propCollect{hoist: &propHoist{}}
	temp := fl.hoistPropCall(c, propSite{sel: sel, recv: recv, pr: pr, inner: inner})
	fl.propWalkArgs(call.ArgList, c)
	if !spOK {
		fl.emitPropReads(c)
		return []syntax.Stmt{s}
	}
	args := make([]otext, 0, len(call.ArgList))
	for _, a := range call.ArgList {
		args = append(args, fl.propOperandText(a, c))
	}
	subs := fl.propSubstitutions(c.needles)
	for i := range args {
		args[i] = propSubstText(args[i], subs)
	}
	main := cat(gen(temp+"."+sel.Sel.Value+"("), joinO(args, ", "), gen(")"))
	return fl.finishPropReplace(c, sp, []syntax.Stmt{s}, []otext{main})
}

func (fl *fileLower) lowerPropCallArgs(s *syntax.ExprStmt, call *syntax.CallExpr) []syntax.Stmt {
	sp, spOK := fl.propStmtSpan(s, s.X)
	c := &propCollect{hoist: &propHoist{}}
	s.X = fl.propWalk(s.X, c)
	if len(c.hoist.pres) == 0 && len(c.hoist.posts) == 0 && !propNeedlesNest(c.needles) {
		fl.emitPropReads(c)
		return []syntax.Stmt{s}
	}
	if !spOK {
		fl.emitPropReads(c)
		return []syntax.Stmt{s}
	}
	main := fl.textOf(sp)
	subs := fl.propSubstitutions(c.needles)
	return fl.finishPropReplace(c, sp, []syntax.Stmt{s}, []otext{propSubstText(main, subs)})
}

func (fl *fileLower) lowerPropDeclStmt(s *syntax.DeclStmt) []syntax.Stmt {
	v0 := propDeclValue(s)
	var sp span
	spOK := v0 != nil
	if spOK {
		sp, spOK = fl.propStmtSpan(s, v0)
	}
	c := &propCollect{hoist: &propHoist{}}
	for _, d := range s.DeclList {
		if vd, ok := d.(*syntax.VarDecl); ok && vd.Values != nil {
			vd.Values = fl.propWalk(vd.Values, c)
		}
	}
	if len(c.hoist.pres) == 0 && len(c.hoist.posts) == 0 && !propNeedlesNest(c.needles) {
		fl.emitPropReads(c)
		return []syntax.Stmt{s}
	}
	if v0 == nil || !spOK {
		fl.emitPropReads(c)
		return []syntax.Stmt{s}
	}
	main := fl.textOf(sp)
	subs := fl.propSubstitutions(c.needles)
	return fl.finishPropReplace(c, sp, []syntax.Stmt{s}, []otext{propSubstText(main, subs)})
}

func propDeclValue(s *syntax.DeclStmt) syntax.Expr {
	for _, d := range s.DeclList {
		if vd, ok := d.(*syntax.VarDecl); ok && vd.Values != nil {
			return vd.Values
		}
	}
	return nil
}

func (fl *fileLower) lowerPropSend(s *syntax.SendStmt) []syntax.Stmt {
	sp, spOK := fl.propStmtSpan(s, s.Chan)
	c := &propCollect{hoist: &propHoist{}}
	s.Chan = fl.propWalk(s.Chan, c)
	s.Value = fl.propWalk(s.Value, c)
	if len(c.hoist.pres) == 0 && len(c.hoist.posts) == 0 && !propNeedlesNest(c.needles) {
		fl.emitPropReads(c)
		return []syntax.Stmt{s}
	}
	if !spOK {
		fl.emitPropReads(c)
		return []syntax.Stmt{s}
	}
	main := fl.textOf(sp)
	subs := fl.propSubstitutions(c.needles)
	return fl.finishPropReplace(c, sp, []syntax.Stmt{s}, []otext{propSubstText(main, subs)})
}

type ctPropSite struct {
	sel      *syntax.SelectorExpr
	property types2.LogosProp
	recv     types2.Type
	getter   *types2.Func
	setter   *types2.Func
	promoted bool
}

func (w ctWalker) ctPropSite(e syntax.Expr) (ctPropSite, bool) {
	sel, ok := syntax.Unparen(e).(*syntax.SelectorExpr)
	if !ok || sel.Sel == nil {
		return ctPropSite{}, false
	}
	info := w.fl.pkg.info
	recv := info.TypeOf(sel.X)
	if recv == nil {
		return ctPropSite{}, false
	}
	pkg := w.fl.pkg.tpkg
	if checked, ok := info.LogosCtProperties[sel]; ok {
		_, promoted, _ := types2.LogosPropertySelection(pkg, recv, checked.Prop.Name)
		return ctPropSite{
			sel: sel, property: checked.Prop, recv: recv,
			getter: checked.Getter, setter: checked.Setter, promoted: promoted,
		}, true
	}
	if info.Selections[sel] != nil || info.Uses[sel.Sel] != nil {
		return ctPropSite{}, false
	}
	pr, promoted, ok := types2.LogosPropertySelection(pkg, recv, sel.Sel.Value)
	if !ok {
		return ctPropSite{}, false
	}
	get, _, _ := types2.LookupFieldOrMethod(recv, true, pkg, "Logos_Get"+pr.Name)
	getter, _ := get.(*types2.Func)
	var setter *types2.Func
	if !pr.ReadOnly {
		set, _, _ := types2.LookupFieldOrMethod(recv, true, pkg, "Logos_Set"+pr.Name)
		setter, _ = set.(*types2.Func)
	}
	return ctPropSite{sel: sel, property: pr, recv: recv, getter: getter, setter: setter, promoted: promoted}, true
}

func (p ctPropSite) accessor(base syntax.Expr, method *types2.Func) *syntax.SelectorExpr {
	return at(&syntax.SelectorExpr{
		X: base, Sel: nameOf(p.sel.Sel.Pos(), method.Name()),
	}, p.sel.Pos())
}

func (w ctWalker) ctPropertyExpr(sel *syntax.SelectorExpr) (syntax.Expr, []syntax.Stmt, bool) {
	p, ok := w.ctPropSite(sel)
	if !ok {
		return nil, nil, false
	}
	if p.getter == nil {
		w.reject(sel, "missing checked property getter")
		return sel, nil, true
	}
	recv, pre := w.expr(sel.X)
	return callOf(sel.Pos(), p.accessor(recv, p.getter), nil), pre, true
}

func (w ctWalker) ctPropertyInterface(n *syntax.InterfaceType) (syntax.Expr, bool) {
	for _, field := range n.MethodList {
		if _, ok := field.Type.(*syntax.PropertyType); ok {
			typ := w.fl.pkg.info.TypeOf(n)
			if typ == nil {
				w.reject(n, "missing checked property interface")
				return n, true
			}
			return w.fl.ctTypeExpr(n.Pos(), typ), true
		}
	}
	return nil, false
}

func (w ctWalker) ctPropPin(pre *[]syntax.Stmt, x syntax.Expr) *syntax.Name {
	pos := x.Pos()
	id := w.nextArg()
	*pre = append(*pre, defineOf(pos, []syntax.Expr{nameOf(pos, id)}, []syntax.Expr{x}))
	return nameOf(pos, id)
}

// Capture the interface carrying a promoted property, not the containing struct:
// another assignment or the RHS can replace an embedded interface or pointer.
func (w ctWalker) ctPropReceiver(p ctPropSite, pre *[]syntax.Stmt) *syntax.Name {
	recv, steps := w.expr(p.sel.X)
	*pre = append(*pre, steps...)
	if p.promoted {
		_, index, _ := types2.LookupFieldOrMethod(p.recv, true, w.fl.pkg.tpkg, p.getter.Name())
		if len(index) <= 1 {
			w.reject(p.sel, "missing checked promoted property path")
		} else {
			recv = w.ctPropProject(recv, p.recv, index[:len(index)-1], p.sel.Pos())
		}
	}
	return w.ctPropPin(pre, recv)
}

func (w ctWalker) ctPropProject(x syntax.Expr, typ types2.Type, path []int, pos syntax.Pos) syntax.Expr {
	for _, ix := range path {
		if ptr, ok := compositeUnderlying(typ).(*types2.Pointer); ok {
			typ = ptr.Elem()
		}
		st, ok := compositeUnderlying(typ).(*types2.Struct)
		if !ok || ix >= st.NumFields() {
			w.reject(x, "invalid checked embedded field path")
			return x
		}
		field := st.Field(ix)
		sel := selOf(pos, x, field.Name())
		if !field.Exported() && field.Pkg() != w.fl.pkg.tpkg {
			w.fl.ct.bindOwnedSelector(sel)
		}
		x, typ = sel, field.Type()
	}
	return x
}

func (p ctPropSite) setFrom(base *syntax.Name) *syntax.SelectorExpr {
	return p.accessor(nameOf(base.Pos(), base.Value), p.setter)
}

type ctPropTarget struct {
	dst    syntax.Expr
	typ    types2.Type
	setter bool
	nested *ctPropNested
}

type ctPropTargets struct {
	w       ctWalker
	pos     syntax.Pos
	pre     []syntax.Stmt
	list    []ctPropTarget
	results *types2.Tuple
}

func (w ctWalker) ctPropertyTargets(s *syntax.AssignStmt) (ctPropTargets, bool) {
	if s.Op != 0 {
		return ctPropTargets{}, false
	}
	for _, x := range syntax.UnpackListExpr(s.Lhs) {
		if w.ctPropValueTarget(x) {
			return w.ctPinAssignmentTargets(s), true
		}
	}
	return ctPropTargets{}, false
}

func (w ctWalker) ctPinAssignmentTargets(s *syntax.AssignStmt) ctPropTargets {
	lhs := syntax.UnpackListExpr(s.Lhs)
	out := ctPropTargets{w: w, pos: s.Pos(), list: make([]ctPropTarget, len(lhs))}
	source := s.Rhs
	if _, call, try := tryAssignOf(s); try {
		source = call
	}
	info := w.fl.pkg.info
	out.results, _ = info.TypeOf(source).(*types2.Tuple)
	for i, x := range lhs {
		p, property := w.ctPropSite(x)
		if !property {
			typ := w.ctOptionTarget(s, x, i)
			if w.ctPropValueTarget(x) {
				out.list[i] = ctPropTarget{dst: x, typ: typ, nested: w.ctPropNestedTarget(x, &out.pre)}
				continue
			}
			out.list[i] = ctPropTarget{dst: w.ctPropLvalue(x, &out.pre), typ: typ}
			continue
		}
		if p.property.ReadOnly {
			w.reject(x, "cannot assign to a read-only property")
			out.list[i] = ctPropTarget{dst: x}
			continue
		}
		if p.getter == nil || p.setter == nil {
			w.reject(x, "missing checked property accessor")
			out.list[i] = ctPropTarget{dst: x}
			continue
		}
		base := w.ctPropReceiver(p, &out.pre)
		out.list[i] = ctPropTarget{dst: p.setFrom(base), typ: p.property.Typ, setter: true}
	}
	return out
}

func (w ctWalker) ctPropValueTarget(e syntax.Expr) bool {
	info := w.fl.pkg.info
	for {
		switch n := syntax.Unparen(e).(type) {
		case *syntax.SelectorExpr:
			if _, property := w.ctPropSite(n); property {
				return true
			}
			typ := info.TypeOf(n.X)
			if typ == nil {
				return false
			}
			if _, ptr := compositeUnderlying(typ).(*types2.Pointer); ptr {
				return false
			}
			if sel := info.Selections[n]; sel != nil && sel.Indirect() {
				return false
			}
			e = n.X
		case *syntax.IndexExpr:
			if _, array := compositeUnderlying(info.TypeOf(n.X)).(*types2.Array); !array {
				return false
			}
			e = n.X
		default:
			return false
		}
	}
}

type ctPropPart struct {
	pos   syntax.Pos
	field string
	index *syntax.Name
}

type ctPropNested struct {
	pos    syntax.Pos
	root   string
	getter syntax.Expr
	setter syntax.Expr
	path   []ctPropPart
}

func (w ctWalker) ctPropNestedTarget(x syntax.Expr, pre *[]syntax.Stmt) *ctPropNested {
	switch n := syntax.Unparen(x).(type) {
	case *syntax.SelectorExpr:
		if p, ok := w.ctPropSite(n); ok {
			if p.getter == nil || p.setter == nil {
				w.reject(n, "missing checked writable property accessor")
				return nil
			}
			base := w.ctPropReceiver(p, pre)
			return &ctPropNested{pos: n.Pos(), root: w.nextArg(),
				getter: p.accessor(base, p.getter), setter: p.setFrom(base)}
		}
		if target := w.ctPropNestedTarget(n.X, pre); target != nil {
			field := tupleSelectionName(w.fl.pkg.info.TypeOf(n.X), n.Sel.Value)
			target.path = append(target.path, ctPropPart{pos: n.Pos(), field: field})
			return target
		}
	case *syntax.IndexExpr:
		if target := w.ctPropNestedTarget(n.X, pre); target != nil {
			index, steps := w.expr(n.Index)
			*pre = append(*pre, steps...)
			pos := index.Pos()
			if w.fl.pkg.info.Types[n.Index].Value != nil {
				index = callOf(pos, w.fl.ctBuiltin(pos, "int"), []syntax.Expr{index})
			}
			id := w.ctPropPin(pre, index)
			target.path = append(target.path, ctPropPart{pos: n.Pos(), index: id})
			return target
		}
	}
	return nil
}

func (n *ctPropNested) initialize() syntax.Stmt {
	return defineOf(n.pos, []syntax.Expr{nameOf(n.pos, n.root)}, []syntax.Expr{callOf(n.pos, n.getter, nil)})
}

func (n *ctPropNested) place() syntax.Expr {
	var x syntax.Expr = nameOf(n.pos, n.root)
	for _, part := range n.path {
		if part.index != nil {
			index := nameOf(part.index.Pos(), part.index.Value)
			x = at(&syntax.IndexExpr{X: x, Index: index}, part.pos)
		} else {
			x = selOf(part.pos, x, part.field)
		}
	}
	return x
}

func (n *ctPropNested) store(value syntax.Expr) []syntax.Stmt {
	lhs := n.place()
	return []syntax.Stmt{
		assignOf(lhs.Pos(), []syntax.Expr{lhs}, []syntax.Expr{value}),
		exprStmtOf(n.pos, callOf(n.pos, n.setter, []syntax.Expr{nameOf(n.pos, n.root)})),
	}
}

// Split an ordinary target at its reference operands, keeping the final
// dereference, bounds check, or map store in the assignment phase.
func (w ctWalker) ctPropLvalue(e syntax.Expr, pre *[]syntax.Stmt) syntax.Expr {
	info := w.fl.pkg.info
	switch n := syntax.Unparen(e).(type) {
	case *syntax.Name:
		return n
	case *syntax.IndexExpr:
		typ := compositeUnderlying(info.TypeOf(n.X))
		if _, array := typ.(*types2.Array); array {
			n.X = w.ctPropLvalue(n.X, pre)
		} else {
			n.X = w.ctPropCapture(n.X, pre)
		}
		if m, ok := typ.(*types2.Map); ok {
			key, steps := w.expr(n.Index)
			key = w.ctOptionValue(n.Index, key, m.Key())
			*pre = append(*pre, steps...)
			pos := key.Pos()
			id := w.nextArg()
			decl := at(&syntax.VarDecl{NameList: []*syntax.Name{nameOf(pos, id)},
				Type: w.fl.ctTypeExpr(pos, m.Key()), Values: key}, pos)
			*pre = append(*pre, at(&syntax.DeclStmt{DeclList: []syntax.Decl{decl}}, pos))
			n.Index = nameOf(pos, id)
		} else if info.Types[n.Index].Value == nil {
			n.Index = w.ctPropCapture(n.Index, pre)
		} else {
			idx, steps := w.expr(n.Index)
			*pre = append(*pre, steps...)
			n.Index = idx
		}
		return n
	case *syntax.SelectorExpr:
		return w.ctPropFieldLvalue(n, pre)
	case *syntax.Operation:
		if n.Op == syntax.Mul && n.Y == nil {
			n.X = w.ctPropCapture(n.X, pre)
			return n
		}
	}
	w.reject(e, "unsupported mixed property assignment target")
	return e
}

func (w ctWalker) ctPropFieldLvalue(n *syntax.SelectorExpr, pre *[]syntax.Stmt) syntax.Expr {
	info := w.fl.pkg.info
	typ := info.TypeOf(n.X)
	if typ == nil {
		return n
	}
	if selection := info.Selections[n]; selection != nil && len(selection.Index()) > 1 {
		if x, ok := w.ctPropPromotedField(n, typ, selection.Index(), pre); ok {
			return x
		}
	}
	n.Sel.Value = tupleSelectionName(typ, n.Sel.Value)
	if _, pointer := compositeUnderlying(typ).(*types2.Pointer); pointer {
		n.X = w.ctPropCapture(n.X, pre)
	} else {
		n.X = w.ctPropLvalue(n.X, pre)
	}
	return n
}

func (w ctWalker) ctPropPromotedField(n *syntax.SelectorExpr, typ types2.Type, path []int, pre *[]syntax.Stmt) (syntax.Expr, bool) {
	prefix := path[:len(path)-1]
	pointerAt := -1
	for i, ix := range prefix {
		if p, ok := compositeUnderlying(typ).(*types2.Pointer); ok {
			typ = p.Elem()
		}
		st, ok := compositeUnderlying(typ).(*types2.Struct)
		if !ok {
			return nil, false
		}
		typ = st.Field(ix).Type()
		if _, ptr := compositeUnderlying(typ).(*types2.Pointer); ptr {
			pointerAt = i
		}
	}
	if pointerAt < 0 {
		return nil, false
	}
	rootType := w.fl.pkg.info.TypeOf(n.X)
	pos := n.Pos()
	root, steps := w.expr(n.X)
	*pre = append(*pre, steps...)
	base := w.ctPropProject(root, rootType, prefix[:pointerAt+1], pos)
	ref := w.ctPropPin(pre, base)
	return at(&syntax.SelectorExpr{X: ref, Sel: n.Sel}, pos), true
}

func (w ctWalker) ctPropCapture(x syntax.Expr, pre *[]syntax.Stmt) syntax.Expr {
	e, steps := w.expr(x)
	*pre = append(*pre, steps...)
	return w.ctPropPin(pre, e)
}

func (t ctPropTargets) assign(rhs syntax.Expr, errName *syntax.Name) []syntax.Stmt {
	pos := t.pos
	if errName == nil && len(t.list) == 1 && t.list[0].setter {
		return []syntax.Stmt{t.list[0].commit(rhs)}
	}
	vals := make([]syntax.Expr, 0, len(t.list)+1)
	decls := make([]syntax.Decl, 0, len(t.list))
	commits := make([]syntax.Stmt, 0, len(t.list))
	_, rhsList := rhs.(*syntax.ListExpr)
	for i, slot := range t.list {
		if id, ok := slot.dst.(*syntax.Name); ok && id.Value == "_" {
			vals = append(vals, id)
			continue
		}
		if slot.typ == nil {
			t.w.reject(slot.dst, "missing checked mixed property assignment target type")
			return nil
		}
		atPos := slot.dst.Pos()
		id := t.w.nextArg()
		typ := slot.typ
		if !rhsList && t.results != nil && i < t.results.Len() && t.w.fl.asOption(typ) != nil {
			typ = t.results.At(i).Type()
		}
		vals = append(vals, nameOf(atPos, id))
		decls = append(decls, at(&syntax.VarDecl{
			NameList: []*syntax.Name{nameOf(atPos, id)},
			Type:     t.w.fl.ctTypeExpr(atPos, typ),
		}, atPos))
		value := t.w.ctOptionValueFrom(nameOf(atPos, id), typ, slot.typ)
		commits = append(commits, slot.commit(value))
	}
	if errName != nil {
		vals = append(vals, errName)
	}
	out := make([]syntax.Stmt, 0, 2+len(commits))
	out = append(out, at(&syntax.DeclStmt{DeclList: decls}, pos),
		at(&syntax.AssignStmt{Lhs: listOf(pos, vals), Rhs: rhs}, pos))
	return append(out, commits...)
}

func (w ctWalker) ctPropertyAssign(s *syntax.AssignStmt) ([]syntax.Stmt, bool) {
	if _, _, try := tryAssignOf(s); try || s.Op == syntax.Def {
		return nil, false
	}
	var list []syntax.Stmt
	if s.Op == 0 {
		targets, ok := w.ctPropertyTargets(s)
		if !ok {
			return nil, false
		}
		list = w.ctPropAssignmentValues(s, targets)
	} else {
		if p, ok := w.ctPropSite(s.Lhs); ok {
			list = w.ctPropCompound(s, p)
		} else if w.ctPropValueTarget(s.Lhs) {
			list = w.ctPropNestedCompound(s)
		} else {
			return nil, false
		}
	}
	if len(list) > 1 {
		list = []syntax.Stmt{blockEndingAt(syntax.StartPos(s), syntax.EndPos(s), list)}
	}
	return list, true
}

func (w ctWalker) ctPropAssignmentValues(s *syntax.AssignStmt, targets ctPropTargets) []syntax.Stmt {
	if len(targets.list) == 2 {
		if value, pre, receive := w.groupReceive(s.Rhs, 2); receive {
			values := syntax.UnpackListExpr(value)
			if targets.results != nil && targets.results.Len() == len(values) {
				for i, item := range values {
					values[i] = w.ctOptionValueFrom(item, targets.results.At(i).Type(), targets.list[i].typ)
				}
			}
			return append(append(targets.pre, pre...), targets.assign(listOf(s.Pos(), values), nil)...)
		}
	}
	if tuple, ok := syntax.Unparen(s.Rhs).(*syntax.TupleLit); ok && len(targets.list) > 1 && len(tuple.Elems) == len(targets.list) {
		s.Rhs = listOf(tuple.Pos(), tuple.Elems)
	}
	if len(targets.list) > 1 {
		if shape := w.ctTupleShapeOf(s.Rhs); shape != nil && shape.NumFields() == len(targets.list) {
			rhs, pre := w.expr(s.Rhs)
			pre = append(pre, w.ctTupleUnpack(s, &rhs, shape)...)
			targets.results = nil
			return append(append(targets.pre, pre...), targets.assign(rhs, nil)...)
		}
	}
	if len(targets.list) == 1 && targets.results != nil && targets.results.Len() > 1 {
		if shape := ctTupleShape(targets.list[0].typ); shape != nil && shape.NumFields() == targets.results.Len() {
			value, pre := w.expr(s.Rhs)
			rhs := w.ctTuplePack(value, targets.results, shape)
			return append(append(targets.pre, pre...), targets.assign(rhs, nil)...)
		}
	}
	pre := append(targets.pre, w.optionValues(s, &s.Rhs)...)
	return append(pre, targets.assign(s.Rhs, nil)...)
}

func (w ctWalker) ctPropCompound(s *syntax.AssignStmt, p ctPropSite) []syntax.Stmt {
	if p.property.ReadOnly {
		w.reject(p.sel, "cannot assign to a read-only property")
		return nil
	}
	if p.getter == nil || p.setter == nil {
		w.reject(p.sel, "missing checked property accessor")
		return nil
	}
	var pre []syntax.Stmt
	base := w.ctPropReceiver(p, &pre)
	set := p.setFrom(base)
	pos := s.Pos()
	var left syntax.Expr = callOf(p.sel.Pos(), p.accessor(base, p.getter), nil)
	var right syntax.Expr = litOf(pos, "1", syntax.IntLit)
	if s.Rhs != nil {
		var steps []syntax.Stmt
		right, steps = w.checkedOperand(s.Rhs, nil)
		if len(steps) != 0 {
			left = w.ctPropPin(&pre, left)
			pre = append(pre, steps...)
		}
	}
	value := at(&syntax.Operation{Op: s.Op, X: left, Y: right}, pos)
	return append(pre, exprStmtOf(pos, callOf(pos, set, []syntax.Expr{value})))
}

func (w ctWalker) ctPropNestedCompound(s *syntax.AssignStmt) []syntax.Stmt {
	var pre []syntax.Stmt
	n := w.ctPropNestedTarget(s.Lhs, &pre)
	if n == nil {
		return pre
	}
	pre = append(pre, n.initialize())
	pos := s.Pos()
	left := n.place()
	var right syntax.Expr = litOf(pos, "1", syntax.IntLit)
	reload := false
	if s.Rhs != nil {
		var steps []syntax.Stmt
		right, steps = w.checkedOperand(s.Rhs, nil)
		reload = len(steps) != 0 || (w.fl.pkg.info.Types[s.Rhs].Value == nil && ctPropReenters(right))
		if reload {
			left = w.ctPropPin(&pre, left)
			pre = append(pre, steps...)
		}
	}
	var result syntax.Expr = at(&syntax.Operation{Op: s.Op, X: left, Y: right}, pos)
	if reload {
		// RHS can update siblings of the destination; store into current storage.
		result = w.ctPropPin(&pre, result)
		pre = append(pre, assignOf(n.pos, []syntax.Expr{nameOf(n.pos, n.root)},
			[]syntax.Expr{callOf(n.pos, n.getter, nil)}))
	}
	return append(pre, n.store(result)...)
}

func ctPropReenters(e syntax.Expr) bool {
	reenters := false
	syntax.Inspect(e, func(n syntax.Node) bool {
		if !reenters {
			switch x := n.(type) {
			case *syntax.CallExpr:
				reenters = true
			case *syntax.Operation:
				reenters = x.Op == syntax.Recv
			case *syntax.FuncLit:
				return false
			}
		}
		return !reenters
	})
	return reenters
}

func (w ctWalker) ctPropPointerMethod(call *syntax.CallExpr) (*syntax.SelectorExpr, ctPropSite, *types2.Selection) {
	sel, _, ok := propCallSel(call.Fun)
	if !ok {
		return nil, ctPropSite{}, nil
	}
	p, ok := w.ctPropSite(sel.X)
	if !ok {
		return nil, ctPropSite{}, nil
	}
	selection := w.fl.pkg.info.Selections[sel]
	if selection == nil || selection.Kind() != types2.MethodVal {
		return nil, ctPropSite{}, nil
	}
	method, ok := selection.Obj().(*types2.Func)
	if !ok || !isPointerRecv(method) {
		return nil, ctPropSite{}, nil
	}
	return sel, p, selection
}

func (w ctWalker) ctPropertyCallStmt(s *syntax.CallStmt) ([]syntax.Stmt, bool) {
	call, ok := syntax.Unparen(s.Call).(*syntax.CallExpr)
	if !ok {
		return nil, false
	}
	sel, p, selection := w.ctPropPointerMethod(call)
	if selection == nil {
		return nil, false
	}
	if _, pointer := compositeUnderlying(p.property.Typ).(*types2.Pointer); !pointer {
		return nil, false
	}
	if p.getter == nil {
		w.reject(sel, "missing checked property getter")
		return nil, true
	}
	recv, pre := w.expr(p.sel.X)
	value := w.ctPropPin(&pre, callOf(p.sel.Pos(), p.accessor(recv, p.getter), nil))
	invoke, _ := w.ctPropBindMethod(call, sel, selection, value, &pre)
	if invoke != nil {
		// A Go method-value wrapper is recover-transparent; an ordinary
		// write-back closure would stop the method from recovering directly.
		call.Fun = invoke
	}
	return pre, true
}

func (w ctWalker) ctPropertyCall(call *syntax.CallExpr) (syntax.Expr, []syntax.Stmt, bool) {
	sel, p, selection := w.ctPropPointerMethod(call)
	if selection == nil {
		return nil, nil, false
	}
	if p.getter == nil || (!p.property.ReadOnly && p.setter == nil) {
		w.reject(sel, "missing checked property accessor")
		return call, nil, true
	}
	var pre []syntax.Stmt
	var set syntax.Expr
	var get syntax.Expr
	if p.property.ReadOnly {
		recv, steps := w.expr(p.sel.X)
		pre = steps
		get = p.accessor(recv, p.getter)
	} else {
		base := w.ctPropReceiver(p, &pre)
		set = p.setFrom(base)
		get = p.accessor(base, p.getter)
	}
	value := w.ctPropPin(&pre, callOf(p.sel.Pos(), get, nil))
	invoke, sig := w.ctPropBindMethod(call, sel, selection, value, &pre)
	if invoke == nil {
		return call, nil, true
	}
	if set == nil {
		call.Fun = invoke
		return call, pre, true
	}
	if sig == nil {
		w.reject(sel, "missing checked property method signature")
		return call, pre, true
	}
	return w.ctPropWriteBackCall(call, sig, invoke, value, set), pre, true
}

func (w ctWalker) ctPropBindMethod(call *syntax.CallExpr, sel *syntax.SelectorExpr, selection *types2.Selection, value *syntax.Name, pre *[]syntax.Stmt) (*syntax.Name, *types2.Signature) {
	var bound syntax.Expr = at(&syntax.SelectorExpr{
		X: nameOf(value.Pos(), value.Value), Sel: sel.Sel,
	}, sel.Pos())
	sig, _ := selection.Type().(*types2.Signature)
	if inst, ok := w.fl.pkg.info.Instances[sel.Sel]; ok && inst.TypeArgs.Len() != 0 {
		sig, _ = inst.Type.(*types2.Signature)
		args := make([]syntax.Expr, inst.TypeArgs.Len())
		for i := range args {
			args[i] = w.fl.ctTypeExpr(sel.Pos(), inst.TypeArgs.At(i))
		}
		bound = at(&syntax.IndexExpr{X: bound, Index: listOf(sel.Pos(), args)}, sel.Pos())
	} else if _, indexed := syntax.Unparen(call.Fun).(*syntax.IndexExpr); indexed {
		w.reject(sel, "missing checked property method instantiation")
		return nil, nil
	}
	invoke := w.ctPropPin(pre, bound)
	*pre = append(*pre, w.walkExprs(len(call.ArgList), func(i int) *syntax.Expr {
		return &call.ArgList[i]
	}, func(i int, old syntax.Expr) types2.Type {
		return w.ctOptionTarget(call, old, i)
	})...)
	return invoke, sig
}

func (w ctWalker) ctPropertySelect(clause *syntax.CommClause) bool {
	as, ok := clause.Comm.(*syntax.AssignStmt)
	if !ok {
		return false
	}
	targets, ok := w.ctPropertyTargets(as)
	if !ok {
		return false
	}
	pos := as.Pos()
	if len(targets.list) > 2 {
		shape := w.ctTupleShapeOf(as.Rhs)
		if shape == nil || shape.NumFields() != len(targets.list) {
			w.reject(as, "missing checked shape of tuple received into properties")
			return true
		}
		id := w.nextArg()
		clause.Comm = defineOf(pos, []syntax.Expr{nameOf(pos, id)}, []syntax.Expr{as.Rhs})
		pre := targets.pre
		for i, slot := range targets.list {
			if slot.blank() {
				continue
			}
			field := shape.Field(i)
			value := selOf(pos, nameOf(pos, id), field.Name())
			pre = append(pre, slot.commit(w.ctOptionValueFrom(value, field.Type(), slot.typ)))
		}
		clause.Body = append(pre, clause.Body...)
		return true
	}
	var types [2]types2.Type
	if sources := targets.results; sources != nil {
		for i := range min(len(targets.list), sources.Len()) {
			types[i] = sources.At(i).Type()
		}
	} else {
		types[0] = w.fl.pkg.info.TypeOf(as.Rhs)
		if len(targets.list) == 2 {
			types[1] = types2.Typ[types2.Bool]
		}
	}
	lhs, pre := targets.iteration(types[:])
	clause.Comm = at(&syntax.AssignStmt{Op: syntax.Def, Lhs: lhs, Rhs: as.Rhs}, pos)
	clause.Body = append(pre, clause.Body...)
	return true
}

func (w ctWalker) ctPropertyRange(rng *syntax.RangeClause, body *syntax.BlockStmt) {
	if rng.Def || rng.Lhs == nil {
		return
	}
	as := at(&syntax.AssignStmt{Lhs: rng.Lhs}, rng.Pos())
	targets, ok := w.ctPropertyTargets(as)
	if !ok {
		return
	}
	typ := w.fl.pkg.info.TypeOf(rng.X)
stripViews:
	for {
		switch t := types2.Unalias(typ).(type) {
		case *types2.ConstView:
			typ = t.Elem
		case *types2.ReadonlyDescendants:
			typ = t.Elem
		default:
			break stripViews
		}
	}
	if typ == nil {
		w.reject(rng, "missing checked range operand type")
		return
	}
	key, value := types2.RangeKeyVal(typ)
	if u, ok := compositeUnderlying(typ).(*types2.Basic); ok &&
		u.Info()&types2.IsInteger != 0 && !types2.Identical(key, types2.Typ[types2.Int]) {
		if tv := w.fl.pkg.info.Types[rng.X]; tv.Value != nil && types2.Identical(key, types2.Default(key)) {
			pos := rng.X.Pos()
			rng.X = callOf(pos, w.fl.ctTypeExpr(pos, key), []syntax.Expr{rng.X})
		}
	}
	var pre []syntax.Stmt
	rng.Lhs, pre = targets.iteration([]types2.Type{types2.Default(key), value})
	rng.Def = true
	body.List = append(pre, body.List...)
}

func (t ctPropTargets) iteration(sources []types2.Type) (syntax.Expr, []syntax.Stmt) {
	names := make([]syntax.Expr, len(t.list))
	pre := t.pre
	for i, slot := range t.list {
		pos := slot.dst.Pos()
		if slot.blank() {
			names[i] = nameOf(pos, "_")
			continue
		}
		id := t.w.nextArg()
		names[i] = nameOf(pos, id)
		value := t.w.ctOptionValueFrom(nameOf(pos, id), sources[i], slot.typ)
		pre = append(pre, slot.commit(value))
	}
	return listOf(t.pos, names), pre
}

func (t ctPropTarget) blank() bool {
	id, ok := t.dst.(*syntax.Name)
	return ok && id.Value == "_"
}

func (t ctPropTarget) commit(value syntax.Expr) syntax.Stmt {
	pos := t.dst.Pos()
	if t.setter {
		return exprStmtOf(pos, callOf(pos, t.dst, []syntax.Expr{value}))
	}
	if t.nested != nil {
		return blockOf(pos, append([]syntax.Stmt{t.nested.initialize()}, t.nested.store(value)...))
	}
	return assignOf(pos, []syntax.Expr{t.dst}, []syntax.Expr{value})
}

// The wrapper's arguments are evaluated at the original call site, including
// for deferred calls. Write-back runs only after the method returns.
func (w ctWalker) ctPropWriteBackCall(call *syntax.CallExpr, sig *types2.Signature, invoke, value *syntax.Name, set syntax.Expr) *syntax.CallExpr {
	pos := call.Pos()
	params := make([]*syntax.Field, sig.Params().Len())
	args := make([]syntax.Expr, len(params))
	for i := range params {
		typ := sig.Params().At(i).Type()
		var what syntax.Expr
		if sig.Variadic() && i == len(params)-1 {
			slice, _ := types2.CoreType(typ).(*types2.Slice)
			what = at(&syntax.DotsType{Elem: w.fl.ctTypeExpr(pos, slice.Elem())}, pos)
		} else {
			what = w.fl.ctTypeExpr(pos, typ)
		}
		id := w.nextArg()
		args[i] = nameOf(pos, id)
		params[i] = at(&syntax.Field{Name: nameOf(pos, id), Type: what}, pos)
	}
	results := make([]*syntax.Field, sig.Results().Len())
	resultDefs := make([]syntax.Expr, len(results))
	resultUses := make([]syntax.Expr, len(results))
	for i := range results {
		typ := sig.Results().At(i).Type()
		results[i] = at(&syntax.Field{Type: w.fl.ctTypeExpr(pos, typ)}, pos)
		id := w.nextArg()
		resultDefs[i] = nameOf(pos, id)
		resultUses[i] = nameOf(pos, id)
	}
	actual := callOf(pos, invoke, args)
	actual.HasDots = sig.Variadic()
	var body []syntax.Stmt
	if len(results) == 0 {
		body = append(body, exprStmtOf(pos, actual))
	} else {
		body = append(body, defineOf(pos, resultDefs, []syntax.Expr{actual}))
	}
	body = append(body, exprStmtOf(pos, callOf(pos, set, []syntax.Expr{value})))
	if len(results) != 0 {
		body = append(body, returnOf(pos, resultUses))
	}
	call.Fun = at(&syntax.FuncLit{
		Type: at(&syntax.FuncType{ParamList: params, ResultList: results}, pos),
		Body: blockOf(pos, body),
	}, pos)
	return call
}

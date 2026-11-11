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
	"cmd/compile/internal/logos/ctfe"
	"cmd/compile/internal/logos/facts"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)
type compositePathStep struct {
	field   string
	index   int
	isField bool
}

type compositeHidden struct {
	path    string
	steps   []compositePathStep
	mapType types2.Type
	val     ctfe.Value
	gen     *constMapGen
	keys    []string
	vals    []string
	order   []int
	hashes  []uint64
	canons  []string
	bearing bool
}

type compositeInfo struct {
	obj     *types2.Const
	val     ctfe.Value
	typ     types2.Type
	builder string
	ro      string
	hiddens map[string]*compositeHidden
	order   []string
}

func compositeUnderlying(t types2.Type) types2.Type {
	u := types2.Unalias(t)
	if n, ok := u.(*types2.Named); ok {
		return n.Underlying()
	}
	return u
}

func compositeTypeReachesMap(t types2.Type) bool {
	seen := map[types2.Type]bool{}
	var walk func(t types2.Type) bool
	walk = func(t types2.Type) bool {
		if t == nil || seen[t] {
			return false
		}
		seen[t] = true
		switch u := compositeUnderlying(t).(type) {
		case *types2.Map:
			return true
		case *types2.Struct:
			for i := range u.NumFields() {
				if walk(u.Field(i).Type()) {
					return true
				}
			}
		case *types2.Slice:
			return walk(u.Elem())
		case *types2.Array:
			return walk(u.Elem())
		case *types2.Pointer:
			return walk(u.Elem())
		}
		return false
	}
	return walk(t)
}

func compositeIsBare(v ctfe.Value, t types2.Type) bool {
	if _, ok := compositeUnderlying(t).(*types2.Map); !ok {
		return false
	}
	return v.Type() != nil && v.Type().Kind == ctfe.KMap
}

func compositeIsBearing(obj *types2.Const, v ctfe.Value) bool {
	if obj == nil || !v.IsValid() || obj.Type() == nil {
		return false
	}
	if compositeIsBare(v, obj.Type()) {
		return false
	}
	return compositeTypeReachesMap(obj.Type())
}

func compositePathString(steps []compositePathStep) string {
	var b strings.Builder
	for i, s := range steps {
		if i > 0 {
			b.WriteString(".")
		}
		if s.isField {
			b.WriteString(s.field)
		} else {
			b.WriteString(strconv.Itoa(s.index))
		}
	}
	return b.String()
}

func compositeSanitize(path string) string {
	var b strings.Builder
	for _, r := range path {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteString("_")
		}
	}
	if b.Len() == 0 {
		return "root"
	}
	return b.String()
}

func compositeCloneSteps(steps []compositePathStep) []compositePathStep {
	return slices.Clone(steps)
}

func (fl *fileLower) collectCompositeHiddens(val ctfe.Value, typ types2.Type, steps []compositePathStep, out *[]compositeHidden) {
	if val.IsNilValue() {
		return
	}
	switch u := compositeUnderlying(typ).(type) {
	case *types2.Map:
		cp := compositeCloneSteps(steps)
		*out = append(*out, compositeHidden{path: compositePathString(cp), steps: cp, mapType: typ, val: val})
	case *types2.Struct:
		fl.collectStructHiddens(val, u, steps, out)
	case *types2.Array:
		fl.collectArrayHiddens(val, u, steps, out)
	case *types2.Slice:
		fl.collectSliceHiddens(val, u, steps, out)
	}
}

func (fl *fileLower) collectStructHiddens(val ctfe.Value, u *types2.Struct, steps []compositePathStep, out *[]compositeHidden) {
	fields := val.Fields()
	for i := range u.NumFields() {
		if i >= len(fields) {
			return
		}
		ns := append(compositeCloneSteps(steps), compositePathStep{field: u.Field(i).Name(), isField: true})
		fl.collectCompositeHiddens(fields[i], u.Field(i).Type(), ns, out)
	}
}

func (fl *fileLower) collectArrayHiddens(val ctfe.Value, u *types2.Array, steps []compositePathStep, out *[]compositeHidden) {
	for i, e := range val.Elems() {
		ns := append(compositeCloneSteps(steps), compositePathStep{index: i})
		fl.collectCompositeHiddens(e, u.Elem(), ns, out)
	}
}

func (fl *fileLower) collectSliceHiddens(val ctfe.Value, u *types2.Slice, steps []compositePathStep, out *[]compositeHidden) {
	backing, lo, length, _, ok := val.SliceHeader()
	if !ok {
		return
	}
	for i := range length {
		if lo+i >= len(backing) {
			return
		}
		ns := append(compositeCloneSteps(steps), compositePathStep{index: i})
		fl.collectCompositeHiddens(backing[lo+i], u.Elem(), ns, out)
	}
}

func compositeHiddenMap(mt types2.Type) (*types2.Map, bool) {
	m, ok := compositeUnderlying(mt).(*types2.Map)
	return m, ok
}

func (fl *fileLower) buildCompositeHiddenParts(name string, mapType types2.Type, val ctfe.Value, pos syntax.Pos) (constMapParts, bool) {
	var zero constMapParts
	mmt, ok := compositeHiddenMap(mapType)
	if !ok {
		return zero, false
	}
	kT := fl.mustSpell(mmt.Key(), pos)
	vT := fl.mustSpell(mmt.Elem(), pos)
	mapT := fl.mustSpell(mapType, pos)
	keys, vals := val.MapEntries()
	keyText, valText := fl.renderHiddenEntries(keys, vals, mmt, pos)
	hashes, canons := hiddenHashes(keys)
	order := hiddenOrder(keys)
	entries := hiddenEntries(hashes, canons)
	bearing := constMapBearing(vals)
	text := finalizeConstMapEmission(pos, constMapEmission{
		name:        name,
		kT:          kT,
		vT:          vT,
		mapT:        mapT,
		keyTexts:    keyText,
		valTexts:    valText,
		builderBody: fl.mustRender(val, pos),
		hashExpr:    fl.constKeyHashText(mmt.Key(), "_logos_k", pos),
		order:       order,
		bearing:     bearing,
	}, entries)
	return constMapParts{text: text, mapT: mapT, keyTexts: keyText, valTexts: valText, hashes: hashes, canons: canons, order: order, bearing: bearing}, true
}

func (fl *fileLower) renderHiddenEntries(keys, vals []ctfe.Value, mmt *types2.Map, pos syntax.Pos) ([]string, []string) {
	keyText := make([]string, len(keys))
	valText := make([]string, len(vals))
	for i := range keys {
		keyText[i] = fl.mustRenderWith(keys[i], mmt.Key(), pos)
		valText[i] = fl.mustRenderWith(vals[i], mmt.Elem(), pos)
	}
	return keyText, valText
}

func hiddenHashes(keys []ctfe.Value) ([]uint64, []string) {
	hashes := make([]uint64, len(keys))
	canons := make([]string, len(keys))
	for i := range keys {
		hashes[i] = constKeyHash(keys[i])
		canons[i] = constKeyCanon(keys[i])
	}
	return hashes, canons
}

func hiddenOrder(keys []ctfe.Value) []int {
	order := make([]int, len(keys))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return constKeyCompare(keys[a], keys[b]) })
	return order
}

func hiddenEntries(hashes []uint64, canons []string) []constMapKey {
	entries := make([]constMapKey, len(hashes))
	for i := range hashes {
		entries[i] = constMapKey{hash: hashes[i], canon: canons[i]}
	}
	return entries
}

func (fl *fileLower) buildNilMappedValue(v ctfe.Value, t types2.Type, pos syntax.Pos) (syntax.Expr, string, bool) {
	if v.IsNilValue() {
		return fl.nilMappedNull(t, pos)
	}
	switch compositeUnderlying(t).(type) {
	case *types2.Map:
		return nilOf(pos), "nil", true
	case *types2.Struct:
		return fl.nilMappedStruct(v, t, pos)
	case *types2.Array:
		return fl.nilMappedArray(v, t, pos)
	case *types2.Slice:
		return fl.nilMappedSlice(v, t, pos)
	}
	return fl.buildMatValue(v, t, pos)
}

func (fl *fileLower) nilMappedNull(t types2.Type, pos syntax.Pos) (syntax.Expr, string, bool) {
	spell, ok := fl.spellLowered(t)
	if !ok {
		return nil, "", false
	}
	te, ok := reparseTypeAt(spell, pos)
	if !ok {
		return nil, "", false
	}
	return at(&syntax.CallExpr{Fun: te, ArgList: []syntax.Expr{nilOf(pos)}}, pos), spell + "(nil)", true
}

func (fl *fileLower) nilMappedStruct(v ctfe.Value, t types2.Type, pos syntax.Pos) (syntax.Expr, string, bool) {
	u, ok := compositeUnderlying(t).(*types2.Struct)
	if !ok {
		return nil, "", false
	}
	fields := v.Fields()
	if len(fields) != u.NumFields() {
		return nil, "", false
	}
	spell, ok := fl.spellLowered(t)
	if !ok {
		return nil, "", false
	}
	te, ok := reparseTypeAt(spell, pos)
	if !ok {
		return nil, "", false
	}
	elems := make([]syntax.Expr, len(fields))
	texts := make([]string, len(fields))
	for i := range fields {
		e, s, ok := fl.buildNilMappedValue(fields[i], u.Field(i).Type(), pos)
		if !ok {
			return nil, "", false
		}
		elems[i], texts[i] = e, s
	}
	lit := at(&syntax.CompositeLit{Type: te, ElemList: elems, Rbrace: pos}, pos)
	setRbrace(lit, pos)
	return lit, spell + "{" + strings.Join(texts, ", ") + "}", true
}

func (fl *fileLower) nilMappedArray(v ctfe.Value, t types2.Type, pos syntax.Pos) (syntax.Expr, string, bool) {
	u, ok := compositeUnderlying(t).(*types2.Array)
	if !ok {
		return nil, "", false
	}
	arr := v.Elems()
	if int64(len(arr)) != u.Len() {
		return nil, "", false
	}
	spell, ok := fl.spellLowered(t)
	if !ok {
		return nil, "", false
	}
	te, ok := reparseTypeAt(spell, pos)
	if !ok {
		return nil, "", false
	}
	elems := make([]syntax.Expr, len(arr))
	texts := make([]string, len(arr))
	for i := range arr {
		e, s, ok := fl.buildNilMappedValue(arr[i], u.Elem(), pos)
		if !ok {
			return nil, "", false
		}
		elems[i], texts[i] = e, s
	}
	lit := at(&syntax.CompositeLit{Type: te, ElemList: elems, Rbrace: pos}, pos)
	setRbrace(lit, pos)
	return lit, spell + "{" + strings.Join(texts, ", ") + "}", true
}

func (fl *fileLower) nilMappedSlice(v ctfe.Value, t types2.Type, pos syntax.Pos) (syntax.Expr, string, bool) {
	u, ok := compositeUnderlying(t).(*types2.Slice)
	if !ok {
		return nil, "", false
	}
	backing, lo, length, cap_, ok := v.SliceHeader()
	if !ok || lo < 0 || length < 0 || lo > cap_ || lo+length > cap_ || cap_ > len(backing) {
		return nil, "", false
	}
	spell, ok := fl.spellLowered(t)
	if !ok {
		return nil, "", false
	}
	te, ok := reparseTypeAt(spell, pos)
	if !ok {
		return nil, "", false
	}
	vis := backing[lo : lo+length]
	elems := make([]syntax.Expr, len(vis))
	texts := make([]string, len(vis))
	for i := range vis {
		e, s, ok := fl.buildNilMappedValue(vis[i], u.Elem(), pos)
		if !ok {
			return nil, "", false
		}
		elems[i], texts[i] = e, s
	}
	lit := at(&syntax.CompositeLit{Type: te, ElemList: elems, Rbrace: pos}, pos)
	setRbrace(lit, pos)
	return lit, spell + "{" + strings.Join(texts, ", ") + "}", true
}

func (fl *fileLower) ensureCompositeInfo(obj *types2.Const, val ctfe.Value, pos syntax.Pos) (*compositeInfo, bool) {
	if info, ok := fl.pkg.composites[obj]; ok {
		return info, true
	}
	seq := fl.pkg.compositeSeq
	fl.pkg.compositeSeq++
	base := fmt.Sprintf("_logos_cc%d_%s", seq, obj.Name())
	info := &compositeInfo{obj: obj, val: val, typ: obj.Type(), builder: base + "_build", ro: base + "_ro", hiddens: map[string]*compositeHidden{}}
	var hiddens []compositeHidden
	fl.collectCompositeHiddens(val, obj.Type(), nil, &hiddens)
	for i := range hiddens {
		if hiddens[i].val.IsNilValue() {
			continue
		}
		if !fl.emitCompositeHidden(info, &hiddens[i], base, pos) {
			return nil, false
		}
	}
	if !fl.emitCompositeBuilder(info, pos) {
		return nil, false
	}
	if !fl.emitCompositeRo(info, pos) {
		return nil, false
	}
	fl.pkg.composites[obj] = info
	return info, true
}

func (fl *fileLower) emitCompositeHidden(info *compositeInfo, h *compositeHidden, base string, pos syntax.Pos) bool {
	keys, _ := h.val.MapEntries()
	hname := fmt.Sprintf("%s_%s", base, compositeSanitize(h.path))
	gen := &constMapGen{name: hname, val: h.val, nkeys: len(keys)}
	fl.needRT = true
	parts, ok := fl.buildCompositeHiddenParts(hname, h.mapType, h.val, pos)
	if !ok {
		return false
	}
	decls := parseConstMapDecls(parts.text, pos)
	fl.markConstMapTables(decls, gen)
	_, vals := h.val.MapEntries()
	gen.bearing = constMapBearing(vals)
	fl.constDecls = append(fl.constDecls, decls...)
	fl.constText += "\n" + parts.text
	hc := *h
	hc.gen = gen
	hc.keys = parts.keyTexts
	hc.vals = parts.valTexts
	hc.order = parts.order
	hc.hashes = parts.hashes
	hc.canons = parts.canons
	hc.bearing = parts.bearing
	info.hiddens[h.path] = &hc
	info.order = append(info.order, h.path)
	return true
}

func (fl *fileLower) emitCompositeBuilder(info *compositeInfo, pos syntax.Pos) bool {
	spell, ok := fl.spellLowered(info.typ)
	if !ok {
		return false
	}
	_, matText, ok := fl.buildMatValue(info.val, info.typ, pos)
	if !ok {
		return false
	}
	buildText := fmt.Sprintf("func %s() %s { return %s }\n", info.builder, spell, matText)
	bdecls := parseConstMapDecls(buildText, pos)
	fl.constDecls = append(fl.constDecls, bdecls...)
	fl.constText += "\n" + buildText
	return true
}

func (fl *fileLower) emitCompositeRo(info *compositeInfo, pos syntax.Pos) bool {
	spell, ok := fl.spellLowered(info.typ)
	if !ok {
		return false
	}
	_, nilText, ok := fl.buildNilMappedValue(info.val, info.typ, pos)
	if !ok {
		return false
	}
	roText := fmt.Sprintf("%s\nvar %s %s = %s\n", rodataMarker, info.ro, spell, nilText)
	rdecls := parseConstMapDecls(roText, pos)
	for _, d := range rdecls {
		if vd, ok := d.(*syntax.VarDecl); ok {
			fl.rodataVars = append(fl.rodataVars, rodataVar{vd: vd})
		}
	}
	fl.constDecls = append(fl.constDecls, rdecls...)
	fl.constText += "\n" + roText
	RegisterRodata(fl.pkg.tpkg.Path(), info.ro)
	return true
}

func (fl *fileLower) lowerCompositeSpec(cd *syntax.ConstDecl, obj *types2.Const, val ctfe.Value) {
	pos := cd.Pos()
	info, ok := fl.ensureCompositeInfo(obj, val, pos)
	if !ok {
		base.Fatalf("logos: cannot lower map-bearing composite %s at %s", obj.Name(), pos.String())
	}
	if token.IsExported(cd.NameList[0].Value) {
		spell, ok := fl.spellLowered(obj.Type())
		if !ok {
			base.Fatalf("logos: cannot spell exported composite %s at %s", obj.Name(), pos.String())
		}
		text := fmt.Sprintf("\nvar %s %s = %s()\n", cd.NameList[0].Value, spell, info.builder)
		decls := parseConstMapDecls(text, pos)
		fl.constDecls = append(fl.constDecls, decls...)
		fl.constText += "\n" + text
	}
	fl.dropConstSpec(cd)
}
func (fl *fileLower) compositeRootConst(e syntax.Expr) (*types2.Const, []compositePathStep, bool) {
	switch e := syntax.Unparen(e).(type) {
	case *syntax.Name:
		return fl.compositeRootName(e)
	case *syntax.SelectorExpr:
		return fl.compositeRootSelector(e)
	case *syntax.IndexExpr:
		return fl.compositeRootIndex(e)
	}
	return nil, nil, false
}

func (fl *fileLower) compositeRootName(e *syntax.Name) (*types2.Const, []compositePathStep, bool) {
	if obj, ok := fl.pkg.info.Defs[e].(*types2.Const); ok {
		if fl.pkg.composites[obj] != nil {
			return obj, nil, true
		}
		return nil, nil, false
	}
	if obj, ok := fl.pkg.info.Uses[e].(*types2.Const); ok {
		if fl.pkg.composites[obj] != nil {
			return obj, nil, true
		}
		return nil, nil, false
	}
	return nil, nil, false
}

func (fl *fileLower) compositeRootSelector(e *syntax.SelectorExpr) (*types2.Const, []compositePathStep, bool) {
	if e.Sel == nil {
		return nil, nil, false
	}
	root, steps, ok := fl.compositeRootConst(e.X)
	if !ok {
		return nil, nil, false
	}
	return root, append(steps, compositePathStep{field: e.Sel.Value, isField: true}), true
}

func (fl *fileLower) compositeRootIndex(e *syntax.IndexExpr) (*types2.Const, []compositePathStep, bool) {
	xt := fl.pkg.info.TypeOf(e.X)
	if xt == nil {
		return nil, nil, false
	}
	if _, ok := compositeUnderlying(xt).(*types2.Map); ok {
		return nil, nil, false
	}
	root, steps, ok := fl.compositeRootConst(e.X)
	if !ok {
		return nil, nil, false
	}
	idx, ok := compositeConstIndex(e.Index)
	if !ok {
		return nil, nil, false
	}
	return root, append(steps, compositePathStep{index: idx}), true
}

func compositeConstIndex(e syntax.Expr) (int, bool) {
	lit, ok := syntax.Unparen(e).(*syntax.BasicLit)
	if !ok || lit.Kind != syntax.IntLit {
		return 0, false
	}
	n, err := strconv.Atoi(lit.Value)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

func (fl *fileLower) compositeHiddenFor(e syntax.Expr) (*constMapGen, bool) {
	root, steps, ok := fl.compositeRootConst(e)
	if !ok {
		return nil, false
	}
	info, ok := fl.pkg.composites[root]
	if !ok {
		return nil, false
	}
	h, ok := info.hiddens[compositePathString(steps)]
	if !ok || h.gen == nil {
		return nil, false
	}
	return h.gen, true
}

func (fl *fileLower) compositeRewriteRoot(e syntax.Expr) (syntax.Expr, bool) {
	root, _, ok := fl.compositeRootConst(e)
	if !ok {
		return nil, false
	}
	info, ok := fl.pkg.composites[root]
	if !ok {
		return nil, false
	}
	if et := fl.pkg.info.TypeOf(e); et != nil && !compositeTypeReachesMap(et) {
		return fl.compositeSwapToRo(e, info), true
	}
	return fl.compositeSwapToBuild(e, info), true
}

func (fl *fileLower) compositeSwapToRo(e syntax.Expr, info *compositeInfo) syntax.Expr {
	op := compositeSwapOp{info: info, text: info.ro, rep: nameOf(e.Pos(), info.ro)}
	return fl.compositeSwap(e, op)
}

func (fl *fileLower) compositeSwapToBuild(e syntax.Expr, info *compositeInfo) syntax.Expr {
	pos := e.Pos()
	op := compositeSwapOp{info: info, text: info.builder + "()", rep: callOf(pos, nameOf(pos, info.builder), nil)}
	return fl.compositeSwap(e, op)
}

type compositeSwapOp struct {
	info *compositeInfo
	text string
	rep  syntax.Expr
}

func (fl *fileLower) compositeSwap(e syntax.Expr, op compositeSwapOp) syntax.Expr {
	switch e := syntax.Unparen(e).(type) {
	case *syntax.Name:
		start := fl.off(e.Pos())
		fl.add(start, start+len(e.Value), op.text)
		return op.rep
	case *syntax.SelectorExpr:
		return fl.compositeSwapParent(e.X, e, op)
	case *syntax.IndexExpr:
		return fl.compositeSwapParent(e.X, e, op)
	}
	return e
}

func (fl *fileLower) compositeSwapParent(x syntax.Expr, parent syntax.Expr, op compositeSwapOp) syntax.Expr {
	if _, _, ok := fl.compositeRootConst(x); !ok {
		return parent
	}
	nx := fl.compositeSwap(x, op)
	switch p := syntax.Unparen(parent).(type) {
	case *syntax.SelectorExpr:
		p.X = nx
		return parent
	case *syntax.IndexExpr:
		p.X = nx
		return parent
	}
	return parent
}

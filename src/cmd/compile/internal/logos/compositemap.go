// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"go/constant"
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
	dynamic bool
}

type compositeHidden struct {
	steps   []compositePathStep
	mapType types2.Type
	val     ctfe.Value
	gen     *constMapGen
	parts   constMapParts
}

type compositeInfo struct {
	obj     *types2.Const
	val     ctfe.Value
	typ     types2.Type
	base    string
	hiddens []*compositeHidden
	byPath  map[string]*compositeHidden
}

func (c *compositeInfo) builder() string { return c.base + "_build" }
func (c *compositeInfo) ro() string      { return c.base + "_ro" }

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
		switch {
		case s.dynamic:
			b.WriteString("*")
		case s.isField:
			b.WriteString(s.field)
		default:
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

func collectCompositeHiddens(val ctfe.Value, typ types2.Type, steps []compositePathStep, visit func([]compositePathStep, types2.Type, ctfe.Value)) {
	if val.IsNilValue() {
		return
	}
	switch u := compositeUnderlying(typ).(type) {
	case *types2.Map:
		visit(slices.Clone(steps), typ, val)
	case *types2.Struct:
		fields := val.Fields()
		for i := range min(u.NumFields(), len(fields)) {
			ns := append(slices.Clone(steps), compositePathStep{field: u.Field(i).Name(), isField: true})
			collectCompositeHiddens(fields[i], u.Field(i).Type(), ns, visit)
		}
	case *types2.Array:
		for i, e := range val.Elems() {
			collectCompositeHiddens(e, u.Elem(), append(slices.Clone(steps), compositePathStep{index: i}), visit)
		}
	case *types2.Slice:
		backing, lo, length, _, ok := val.SliceHeader()
		if !ok {
			return
		}
		for i := range min(length, len(backing)-lo) {
			collectCompositeHiddens(backing[lo+i], u.Elem(), append(slices.Clone(steps), compositePathStep{index: i}), visit)
		}
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

func collectComposites(files []*syntax.File, info *types2.Info) (map[*types2.Const]*compositeInfo, int) {
	out := map[*types2.Const]*compositeInfo{}
	seq := 0
	for _, file := range files {
		syntax.Inspect(file, func(n syntax.Node) bool {
			cd, ok := n.(*syntax.ConstDecl)
			if !ok || len(cd.NameList) != 1 {
				return true
			}
			obj, ok := info.Defs[cd.NameList[0]].(*types2.Const)
			if !ok {
				return true
			}
			if v, ok := info.LogosCTFE[obj]; ok && compositeIsBearing(obj, v) {
				out[obj] = newCompositeInfo(obj, v, seq)
				seq++
			}
			return true
		})
	}
	return out, seq
}

func newCompositeInfo(obj *types2.Const, val ctfe.Value, seq int) *compositeInfo {
	c := &compositeInfo{obj: obj, val: val, typ: obj.Type(), base: fmt.Sprintf("_logos_cc%d_%s", seq, obj.Name()), byPath: map[string]*compositeHidden{}}
	collectCompositeHiddens(val, c.typ, nil, func(steps []compositePathStep, mt types2.Type, v ctfe.Value) {
		keys, vals := v.MapEntries()
		gen := &constMapGen{name: fmt.Sprintf("%s_h%d", c.base, len(c.hiddens)), val: v, nkeys: len(keys), bearing: constMapBearing(vals)}
		h := &compositeHidden{steps: steps, mapType: mt, val: v, gen: gen}
		c.hiddens = append(c.hiddens, h)
		c.byPath[compositePathString(steps)] = h
	})
	return c
}

func (fl *fileLower) emitComposite(info *compositeInfo, pos syntax.Pos) bool {
	for _, h := range info.hiddens {
		if !fl.emitCompositeHidden(h, pos) {
			return false
		}
	}
	return fl.emitCompositeBuilder(info, pos) && fl.emitCompositeRO(info, pos)
}

func (fl *fileLower) emitCompositeHidden(h *compositeHidden, pos syntax.Pos) bool {
	fl.needRT = true
	parts, ok := fl.buildCompositeHiddenParts(h.gen.name, h.mapType, h.val, pos)
	if !ok {
		return false
	}
	decls := parseConstMapDecls(parts.text, pos)
	fl.markConstMapTables(decls, h.gen)
	fl.constDecls = append(fl.constDecls, decls...)
	fl.constText += "\n" + parts.text
	h.parts = parts
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
	buildText := fmt.Sprintf("func %s() %s { return %s }\n", info.builder(), spell, matText)
	fl.constDecls = append(fl.constDecls, parseConstMapDecls(buildText, pos)...)
	fl.constText += "\n" + buildText
	return true
}

func (fl *fileLower) emitCompositeRO(info *compositeInfo, pos syntax.Pos) bool {
	spell, ok := fl.spellLowered(info.typ)
	if !ok {
		return false
	}
	_, nilText, ok := fl.buildNilMappedValue(info.val, info.typ, pos)
	if !ok {
		return false
	}
	roText := fmt.Sprintf("%s\nvar %s %s = %s\n", rodataMarker, info.ro(), spell, nilText)
	rdecls := parseConstMapDecls(roText, pos)
	for _, d := range rdecls {
		if vd, ok := d.(*syntax.VarDecl); ok {
			fl.rodataVars = append(fl.rodataVars, rodataVar{vd: vd})
		}
	}
	fl.constDecls = append(fl.constDecls, rdecls...)
	fl.constText += "\n" + roText
	return true
}

func (fl *fileLower) lowerCompositeSpec(cd *syntax.ConstDecl, info *compositeInfo) {
	pos := cd.Pos()
	if !fl.emitComposite(info, pos) {
		base.Fatalf("logos: cannot lower map-bearing composite %s at %s", info.obj.Name(), pos.String())
	}
	if name := cd.NameList[0].Value; token.IsExported(name) && fl.pkg.tpkg.Scope().Lookup(name) == info.obj {
		spell, ok := fl.spellLowered(info.typ)
		if !ok {
			base.Fatalf("logos: cannot spell exported composite %s at %s", info.obj.Name(), pos.String())
		}
		text := fmt.Sprintf("\nvar %s %s = %s()\n", name, spell, info.builder())
		fl.constDecls = append(fl.constDecls, parseConstMapDecls(text, pos)...)
		fl.constText += "\n" + text
		fl.recordCompositeFacts(info)
	}
	fl.dropConstSpec(cd)
}

func (fl *fileLower) recordCompositeFacts(info *compositeInfo) {
	f := facts.Current()
	if f == nil {
		return
	}
	for _, h := range info.hiddens {
		name := info.obj.Name() + "__" + compositeSanitize(compositePathString(h.steps))
		if slices.ContainsFunc(f.ConstMaps, func(c facts.ConstMapFact) bool { return c.Object == name }) {
			continue
		}
		p := h.parts
		f.ConstMaps = append(f.ConstMaps, facts.ConstMapFact{Object: name, Bearing: p.bearing, Order: slices.Clone(p.order), Hashes: slices.Clone(p.hashes), Canons: slices.Clone(p.canons), Keys: slices.Clone(p.keyTexts), Vals: slices.Clone(p.valTexts)})
	}
}

type compositeRef struct {
	info   *compositeInfo
	root   *syntax.Name
	steps  []compositePathStep
	method *types2.Func
	recv   syntax.Expr
}

func (r compositeRef) static() bool {
	return !slices.ContainsFunc(r.steps, func(s compositePathStep) bool { return s.dynamic })
}

func (fl *fileLower) compositeRefOf(e syntax.Expr) (compositeRef, bool) {
	switch n := syntax.Unparen(e).(type) {
	case *syntax.Name:
		obj, ok := fl.pkg.info.Uses[n].(*types2.Const)
		if !ok || n.Value != obj.Name() || fl.pkg.composites[obj] == nil {
			return compositeRef{}, false
		}
		return compositeRef{info: fl.pkg.composites[obj], root: n, recv: n}, true
	case *syntax.SelectorExpr:
		return fl.compositeSelectorRef(n)
	case *syntax.IndexExpr:
		return fl.compositeIndexRef(n)
	}
	return compositeRef{}, false
}

func (fl *fileLower) compositeSelectorRef(n *syntax.SelectorExpr) (compositeRef, bool) {
	sel := fl.pkg.info.Selections[n]
	if sel == nil {
		return compositeRef{}, false
	}
	ref, ok := fl.compositeRefOf(n.X)
	if !ok || ref.method != nil {
		return compositeRef{}, false
	}
	path := sel.Index()
	if sel.Kind() == types2.MethodVal {
		ref.method, _ = sel.Obj().(*types2.Func)
		path = path[:len(path)-1]
		ref.recv = n.X
	} else if sel.Kind() == types2.FieldVal {
		ref.recv = n
	} else {
		return compositeRef{}, false
	}
	steps, ok := compositeFieldSteps(sel.Recv(), path)
	ref.steps = append(slices.Clone(ref.steps), steps...)
	return ref, ok
}

func compositeFieldSteps(t types2.Type, path []int) ([]compositePathStep, bool) {
	steps := make([]compositePathStep, 0, len(path))
	for _, fi := range path {
		deref := false
		if p, ok := compositeUnderlying(t).(*types2.Pointer); ok {
			t, deref = p.Elem(), true
		}
		st, ok := compositeUnderlying(t).(*types2.Struct)
		if !ok || fi >= st.NumFields() {
			return nil, false
		}
		steps = append(steps, compositePathStep{field: st.Field(fi).Name(), isField: true, dynamic: deref})
		t = st.Field(fi).Type()
	}
	return steps, true
}

func (fl *fileLower) compositeIndexRef(n *syntax.IndexExpr) (compositeRef, bool) {
	switch compositeUnderlying(fl.pkg.info.TypeOf(n.X)).(type) {
	case *types2.Array, *types2.Slice:
	default:
		return compositeRef{}, false
	}
	ref, ok := fl.compositeRefOf(n.X)
	if !ok || ref.method != nil {
		return compositeRef{}, false
	}
	step := compositePathStep{dynamic: true}
	if cv, ok := typeConst(fl.pkg.info, n.Index); ok && cv.Kind() == constant.Int {
		if i, exact := constant.Int64Val(cv); exact && i >= 0 {
			step = compositePathStep{index: int(i)}
		}
	}
	ref.steps = append(slices.Clone(ref.steps), step)
	ref.recv = n
	return ref, true
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
	if g, ok := fl.compositeLocalHiddenFor(e); ok {
		return g, true
	}
	return fl.compositeImportedHiddenFor(e)
}

func (fl *fileLower) compositeLocalHiddenFor(e syntax.Expr) (*constMapGen, bool) {
	ref, ok := fl.compositeRefOf(e)
	if !ok || ref.method != nil || !ref.static() {
		return nil, false
	}
	h, ok := ref.info.byPath[compositePathString(ref.steps)]
	if !ok {
		return nil, false
	}
	return h.gen, true
}

func (fl *fileLower) compositeRewriteRoot(e syntax.Expr) (syntax.Expr, bool) {
	if out, ok := fl.compositeLocalRewrite(e); ok {
		return out, true
	}
	return fl.compositeImportedRewrite(e)
}

func (fl *fileLower) compositeLocalRewrite(e syntax.Expr) (syntax.Expr, bool) {
	ref, ok := fl.compositeRefOf(e)
	if !ok || fl.compositeDone[ref.root] {
		return nil, false
	}
	fl.compositeDone[ref.root] = true
	t := fl.pkg.info.TypeOf(ref.recv)
	switch {
	case t != nil && !compositeTypeReachesMap(t):
		fl.compositeRename(ref.root, ref.info.ro())
		return e, true
	case ref.method != nil && compositePointerReceiver(ref.method):
		return fl.compositeBuildAddr(e, ref), true
	}
	return fl.compositeBuild(e, ref), true
}

func (fl *fileLower) compositeShapeArg(call *syntax.CallExpr) {
	fn, ok := call.Fun.(*syntax.Name)
	if !ok || (fn.Value != "len" && fn.Value != "cap") || len(call.ArgList) != 1 {
		return
	}
	if _, ok := fl.pkg.info.Uses[fn].(*types2.Builtin); !ok {
		return
	}
	arg := call.ArgList[0]
	ref, ok := fl.compositeRefOf(arg)
	if !ok || fl.compositeDone[ref.root] {
		return
	}
	switch compositeUnderlying(fl.pkg.info.TypeOf(arg)).(type) {
	case *types2.Array, *types2.Slice:
		fl.compositeDone[ref.root] = true
		fl.compositeRename(ref.root, ref.info.ro())
	}
}

func (fl *fileLower) compositeRename(root *syntax.Name, to string) {
	start := fl.off(root.Pos())
	fl.add(start, start+len(root.Value), to)
	root.Value = to
}

func compositePointerReceiver(fn *types2.Func) bool {
	sig, ok := fn.Type().(*types2.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	t := types2.Unalias(sig.Recv().Type())
	if cv, ok := t.(*types2.ConstView); ok {
		t = types2.Unalias(cv.Elem)
	}
	_, ptr := t.(*types2.Pointer)
	return ptr
}

func (fl *fileLower) compositeBuild(e syntax.Expr, ref compositeRef) syntax.Expr {
	pos := ref.root.Pos()
	call := callOf(pos, nameOf(pos, ref.info.builder()), nil)
	call.SetTypeInfo(ref.root.GetTypeInfo())
	return fl.compositeSwapRoot(e, ref, ref.info.builder()+"()", call)
}

func (fl *fileLower) compositeBuildAddr(e syntax.Expr, ref compositeRef) syntax.Expr {
	pos := ref.root.Pos()
	fresh := callOf(pos, nameOf(pos, "new"), []syntax.Expr{callOf(pos, nameOf(pos, ref.info.builder()), nil)})
	tv := ref.root.GetTypeInfo()
	tv.Type = types2.NewPointer(tv.Type)
	fresh.SetTypeInfo(tv)
	return fl.compositeSwapRoot(e, ref, "new("+ref.info.builder()+"())", fresh)
}

func (fl *fileLower) compositeSwapRoot(e syntax.Expr, ref compositeRef, text string, repl syntax.Expr) syntax.Expr {
	start := fl.off(ref.root.Pos())
	fl.add(start, start+len(ref.root.Value), text)
	fl.wrapSpan[repl] = span{start: start, end: start + len(ref.root.Value)}
	return compositeReplaceRoot(e, ref.root, repl)
}

func compositeReplaceRoot(e syntax.Expr, root *syntax.Name, repl syntax.Expr) syntax.Expr {
	switch n := e.(type) {
	case *syntax.Name:
		if n == root {
			return repl
		}
	case *syntax.ParenExpr:
		n.X = compositeReplaceRoot(n.X, root, repl)
	case *syntax.SelectorExpr:
		n.X = compositeReplaceRoot(n.X, root, repl)
	case *syntax.IndexExpr:
		n.X = compositeReplaceRoot(n.X, root, repl)
	}
	return e
}

func (fl *fileLower) rejectCompositeSurvivors() {
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		if name, ok := n.(*syntax.Name); ok {
			if _, ok := fl.compositeRefOf(name); ok && !fl.compositeDone[name] {
				base.Fatalf("logos: const composite %s at %s was not lowered", name.Value, name.Pos().String())
			}
		}
		return true
	})
}

type importedComposite struct {
	obj     *types2.Var
	typ     types2.Type
	builder string
	hiddens map[string]*constMapGen
	order   []string
}

func importedCompositeFacts(obj *types2.Var) []facts.ConstMapFact {
	if obj.Pkg() == nil {
		return nil
	}
	f, ok := facts.Lookup(obj.Pkg().Path())
	if !ok || f == nil {
		return nil
	}
	prefix := obj.Name() + "__"
	var out []facts.ConstMapFact
	for _, c := range f.ConstMaps {
		if len(c.Object) > len(prefix) && c.Object[:len(prefix)] == prefix {
			out = append(out, c)
		}
	}
	return out
}

func importedCompositePath(object, prefix string) string {
	path := object[len(prefix):]
	var b strings.Builder
	for _, r := range path {
		if r == '_' {
			b.WriteString(".")
		} else {
			b.WriteRune(r)
		}
	}
	s := b.String()
	s = strings.ReplaceAll(s, "..", ".")
	return strings.Trim(s, ".")
}

func importedPathSteps(path string) []compositePathStep {
	if path == "" {
		return nil
	}
	parts := strings.Split(path, ".")
	steps := make([]compositePathStep, 0, len(parts))
	for _, p := range parts {
		if n, err := strconv.Atoi(p); err == nil && n >= 0 {
			steps = append(steps, compositePathStep{index: n})
			continue
		}
		steps = append(steps, compositePathStep{field: p, isField: true})
	}
	return steps
}

func importedMapType(root types2.Type, steps []compositePathStep) *types2.Map {
	t := root
	for _, s := range steps {
		u := compositeUnderlying(t)
		if s.isField {
			st, ok := u.(*types2.Struct)
			if !ok {
				return nil
			}
			found := false
			for i := range st.NumFields() {
				if st.Field(i).Name() == s.field {
					t = st.Field(i).Type()
					found = true
					break
				}
			}
			if !found {
				return nil
			}
			continue
		}
		u = compositeUnderlying(t)
		if sl, ok := u.(*types2.Slice); ok {
			t = sl.Elem()
			continue
		}
		if ar, ok := u.(*types2.Array); ok {
			t = ar.Elem()
			continue
		}
		return nil
	}
	m, _ := compositeUnderlying(t).(*types2.Map)
	return m
}

func joinPairs(keys, vals []string) string {
	pairs := make([]string, 0, len(keys))
	for i := range keys {
		if i >= len(vals) {
			break
		}
		pairs = append(pairs, keys[i]+": "+vals[i])
	}
	return strings.Join(pairs, ", ")
}

func constMapEntriesFromRec(rec facts.ConstMapFact) []constMapKey {
	entries := make([]constMapKey, 0, len(rec.Keys))
	for i := range rec.Keys {
		var h uint64
		var c string
		if i < len(rec.Hashes) {
			h = rec.Hashes[i]
		}
		if i < len(rec.Canons) {
			c = rec.Canons[i]
		}
		entries = append(entries, constMapKey{hash: h, canon: c})
	}
	return entries
}

func (fl *fileLower) importedCompositeFor(obj *types2.Var, pos syntax.Pos) (*importedComposite, bool) {
	if obj.Pkg() == nil || obj.Pkg() == fl.pkg.tpkg {
		return nil, false
	}
	if info, ok := fl.pkg.importedComposites[obj]; ok {
		return info, true
	}
	recs := importedCompositeFacts(obj)
	if len(recs) == 0 {
		return nil, false
	}
	seq := fl.pkg.compositeSeq
	fl.pkg.compositeSeq++
	base := fmt.Sprintf("_logos_icomp%d_%s", seq, obj.Name())
	info := &importedComposite{obj: obj, typ: obj.Type(), builder: base + "_build", hiddens: map[string]*constMapGen{}}
	prefix := obj.Name() + "__"
	for _, rec := range recs {
		path := importedCompositePath(rec.Object, prefix)
		steps := importedPathSteps(path)
		if steps == nil {
			continue
		}
		mt := importedMapType(obj.Type(), steps)
		if mt == nil {
			continue
		}
		hname := fmt.Sprintf("%s_%s", base, compositeSanitize(path))
		kT := fl.mustSpell(mt.Key(), pos)
		vT := fl.mustSpell(mt.Elem(), pos)
		mapT := fl.mustSpell(mt, pos)
		text := finalizeConstMapEmission(pos, constMapEmission{name: hname, kT: kT, vT: vT, mapT: mapT, keyTexts: slices.Clone(rec.Keys), valTexts: slices.Clone(rec.Vals), builderBody: mapT + "{" + joinPairs(rec.Keys, rec.Vals) + "}", hashExpr: fl.constKeyHashText(mt.Key(), "_logos_k", pos), order: slices.Clone(rec.Order), bearing: rec.Bearing}, constMapEntriesFromRec(rec))
		decls := parseConstMapDecls(text, pos)
		gen := &constMapGen{name: hname, nkeys: len(rec.Keys), bearing: rec.Bearing}
		fl.markConstMapTables(decls, gen)
		fl.constDecls = append(fl.constDecls, decls...)
		fl.constText += "\n" + text
		fl.needRT = true
		info.hiddens[path] = gen
		info.order = append(info.order, path)
	}
	if len(info.order) == 0 {
		return nil, false
	}
	if !fl.emitImportedBuilder(info, pos) {
		return nil, false
	}
	fl.pkg.importedComposites[obj] = info
	return info, true
}

func (fl *fileLower) emitImportedBuilder(info *importedComposite, pos syntax.Pos) bool {
	spell, ok := fl.spellLowered(info.typ)
	if !ok {
		return false
	}
	qual := info.obj.Pkg().Name() + "." + info.obj.Name()
	var b strings.Builder
	b.WriteString("func " + info.builder + "() " + spell + " {\n")
	b.WriteString("v := " + qual + "\n")
	for _, path := range importedSlicePrefixes(info) {
		elem, ok := importedSliceElem(info.typ, path)
		if !ok {
			return false
		}
		espell, ok := fl.spellLowered(elem)
		if !ok {
			return false
		}
		b.WriteString("v." + path + " = make([]" + espell + ", len(" + qual + "." + path + "))\n")
		b.WriteString("copy(v." + path + ", " + qual + "." + path + ")\n")
	}
	for _, path := range info.order {
		h := info.hiddens[path]
		if h == nil {
			return false
		}
		steps := importedPathSteps(path)
		if steps == nil {
			return false
		}
		b.WriteString("v" + stepsToSuffix(steps) + " = " + h.buildName() + "()\n")
	}
	b.WriteString("return v\n}\n")
	text := b.String()
	decls := parseConstMapDecls(text, pos)
	fl.constDecls = append(fl.constDecls, decls...)
	fl.constText += "\n" + text
	return true
}

func importedSlicePrefixes(info *importedComposite) []string {
	seen := map[string]bool{}
	var out []string
	for _, path := range info.order {
		steps := importedPathSteps(path)
		for i, s := range steps {
			if s.isField {
				continue
			}
			prefix := stepsToPrefix(steps[:i])
			if prefix == "" || seen[prefix] {
				break
			}
			seen[prefix] = true
			out = append(out, prefix)
			break
		}
	}
	return out
}

func stepsToPrefix(steps []compositePathStep) string {
	names := make([]string, 0, len(steps))
	for _, s := range steps {
		names = append(names, s.field)
	}
	return strings.Join(names, ".")
}

func importedSliceElem(root types2.Type, prefix string) (types2.Type, bool) {
	if prefix == "" {
		return nil, false
	}
	steps := importedPathSteps(prefix)
	t := root
	for _, s := range steps {
		u := compositeUnderlying(t)
		st, ok := u.(*types2.Struct)
		if !ok {
			return nil, false
		}
		found := false
		for i := range st.NumFields() {
			if st.Field(i).Name() == s.field {
				t = st.Field(i).Type()
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	u := compositeUnderlying(t)
	if sl, ok := u.(*types2.Slice); ok {
		return sl.Elem(), true
	}
	return nil, false
}

func stepsToSuffix(steps []compositePathStep) string {
	var b strings.Builder
	for _, s := range steps {
		if s.isField {
			b.WriteString("." + s.field)
			continue
		}
		b.WriteString("[" + strconv.Itoa(s.index) + "]")
	}
	return b.String()
}

func (fl *fileLower) compositeImportedRoot(e syntax.Expr) (*types2.Var, []compositePathStep, bool) {
	switch e := syntax.Unparen(e).(type) {
	case *syntax.SelectorExpr:
		if e.Sel == nil {
			return nil, nil, false
		}
		if obj, ok := fl.pkg.info.Uses[e.Sel].(*types2.Var); ok && len(importedCompositeFacts(obj)) > 0 {
			return obj, nil, true
		}
		root, steps, ok := fl.compositeImportedRoot(e.X)
		if !ok {
			return nil, nil, false
		}
		return root, append(steps, compositePathStep{field: e.Sel.Value, isField: true}), true
	case *syntax.IndexExpr:
		xt := fl.pkg.info.TypeOf(e.X)
		if xt == nil {
			return nil, nil, false
		}
		if _, ok := compositeUnderlying(xt).(*types2.Map); ok {
			return nil, nil, false
		}
		root, steps, ok := fl.compositeImportedRoot(e.X)
		if !ok {
			return nil, nil, false
		}
		idx, ok := compositeConstIndex(e.Index)
		if !ok {
			return nil, nil, false
		}
		return root, append(steps, compositePathStep{index: idx}), true
	}
	return nil, nil, false
}

func (fl *fileLower) compositeImportedHiddenFor(e syntax.Expr) (*constMapGen, bool) {
	root, steps, ok := fl.compositeImportedRoot(e)
	if !ok {
		return nil, false
	}
	info, ok := fl.importedCompositeFor(root, e.Pos())
	if !ok {
		return nil, false
	}
	h, ok := info.hiddens[compositePathString(steps)]
	if !ok || h == nil {
		return nil, false
	}
	return h, true
}

func (fl *fileLower) compositeImportedRewrite(e syntax.Expr) (syntax.Expr, bool) {
	root, steps, ok := fl.compositeImportedRoot(e)
	if !ok {
		return nil, false
	}
	info, ok := fl.importedCompositeFor(root, e.Pos())
	if !ok {
		return nil, false
	}
	et := fl.pkg.info.TypeOf(e)
	if et == nil || !compositeTypeReachesMap(et) {
		return nil, false
	}
	pos := e.Pos()
	path := compositePathString(steps)
	if path == "" {
		text := info.builder + "()"
		fl.add(fl.off(syntax.StartPos(e)), fl.off(syntax.EndPos(e)), text)
		return callOf(pos, nameOf(pos, info.builder), nil), true
	}
	if h, ok := info.hiddens[path]; ok && h != nil {
		text := h.buildName() + "()"
		fl.add(fl.off(syntax.StartPos(e)), fl.off(syntax.EndPos(e)), text)
		return callOf(pos, nameOf(pos, h.buildName()), nil), true
	}
	text := info.builder + "()"
	fl.add(fl.off(syntax.StartPos(e)), fl.off(syntax.EndPos(e)), text)
	return callOf(pos, nameOf(pos, info.builder), nil), true
}

func (fl *fileLower) rejectImportedCompositeStores() {
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.AssignStmt:
			for _, lhs := range assignLHSList(n) {
				if _, _, ok := fl.compositeImportedRoot(lhs); ok {
					base.Errorf("%s: [correctness::const_view_mutation] cannot assign through const view %s", lhs.Pos().String(), renderExpr(lhs))
					return false
				}
				if ix, ok := syntax.Unparen(lhs).(*syntax.IndexExpr); ok {
					if _, ok := fl.compositeImportedHiddenFor(ix.X); ok {
						base.Errorf("%s: [correctness::const_view_mutation] cannot assign through const view %s", lhs.Pos().String(), renderExpr(lhs))
						return false
					}
					if _, _, ok := fl.compositeImportedRoot(ix.X); ok {
						base.Errorf("%s: [correctness::const_view_mutation] cannot assign through const view %s", lhs.Pos().String(), renderExpr(lhs))
						return false
					}
				}
			}
		case *syntax.CallExpr:
			fn, ok := syntax.Unparen(n.Fun).(*syntax.Name)
			if !ok {
				return true
			}
			if fn.Value != "delete" && fn.Value != "clear" {
				return true
			}
			if len(n.ArgList) == 0 {
				return true
			}
			if _, _, ok := fl.compositeImportedRoot(n.ArgList[0]); ok {
				base.Errorf("%s: [correctness::const_view_mutation] cannot %s through const view %s", n.Pos().String(), fn.Value, renderExpr(n.ArgList[0]))
				return false
			}
		}
		return true
	})
}

func assignLHSList(n *syntax.AssignStmt) []syntax.Expr {
	if l, ok := syntax.Unparen(n.Lhs).(*syntax.ListExpr); ok {
		return l.ElemList
	}
	return []syntax.Expr{n.Lhs}
}

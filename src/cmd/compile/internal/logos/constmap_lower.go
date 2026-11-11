// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"cmd/compile/internal/base"
	"cmd/compile/internal/logos/ctfe"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const constMapSwitchMax = 8

type constMapGen struct {
	name        string
	obj         *types2.Const
	val         ctfe.Value
	init        syntax.Expr
	nativeDecls []syntax.Decl
}

func (g *constMapGen) lookupName() string { return g.name + "_lookup" }
func (g *constMapGen) sortedName() string { return g.name + "_sorted" }
func (g *constMapGen) mapName() string    { return g.name + "_map" }
func (g *constMapGen) tabName() string    { return g.name + "_tab" }
func (g *constMapGen) matName() string    { return g.name + "_mat" }
func (g *constMapGen) buildName() string  { return g.name + "_build" }
func (g *constMapGen) valName() string    { return g.name + "_val" }
func (g *constMapGen) allName() string    { return g.name + "_all" }

func constMapBearing(vals []ctfe.Value) bool {
	for _, v := range vals {
		if !rodataStaticValue(v) {
			return true
		}
	}
	return false
}

type constMapEmission struct {
	name        string
	kT          string
	vT          string
	mapT        string
	keyTexts    []string
	valTexts    []string
	builderBody string
	hashExpr    string
	order       []int
	layout      *constMapLayout
	isSwitch    bool
	bearing     bool
}

func emitConstMapDecls(e constMapEmission) string {
	var b strings.Builder
	name := e.name
	if !e.bearing {
		fmt.Fprintf(&b, "%s\nvar %s_sorted = []%s.ConstEntry[%s, %s]{", rodataMarker, name, rtAlias, e.kT, e.vT)
		for j, i := range e.order {
			if j > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "{Key: %s, Val: %s}", e.keyTexts[i], e.valTexts[i])
		}
		b.WriteString("}\n")
	} else {
		fmt.Fprintf(&b, "%s\nvar %s_sorted = []%s.ConstEntry[%s, uint32]{", rodataMarker, name, rtAlias, e.kT)
		for j, i := range e.order {
			if j > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "{Key: %s, Val: %d}", e.keyTexts[i], i)
		}
		b.WriteString("}\n")
	}
	fmt.Fprintf(&b, "var %s_mat %s.ConstMapMaterialization[%s, %s, %s]\n", name, rtAlias, e.mapT, e.kT, e.vT)
	fmt.Fprintf(&b, "func %s_build() %s { return %s }\n", name, e.mapT, e.builderBody)
	fmt.Fprintf(&b, "func %s_map() %s { return %s_mat.Get(%s_build) }\n", name, e.mapT, name, name)
	if e.bearing {
		fmt.Fprintf(&b, "func %s_val(_logos_idx uint32) %s {\n\tswitch _logos_idx {\n", name, e.vT)
		for i := range e.valTexts {
			fmt.Fprintf(&b, "\tcase %d:\n\t\treturn %s\n", i, e.valTexts[i])
		}
		fmt.Fprintf(&b, "\t}\n\tvar _logos_zero %s\n\treturn _logos_zero\n}\n", e.vT)
		fmt.Fprintf(&b, "func %s_all(_logos_yield func(%s, %s) bool) {\n\tfor _, _logos_e := range %s_sorted {\n\t\tif !_logos_yield(_logos_e.Key, %s_val(_logos_e.Val)) {\n\t\t\treturn\n\t\t}\n\t}\n}\n", name, e.kT, e.vT, name, name)
	}
	if e.isSwitch {
		if !e.bearing {
			fmt.Fprintf(&b, "func %s_lookup(_logos_k %s) (%s, bool) {\n\tswitch _logos_k {\n", name, e.kT, e.vT)
			for i := range e.keyTexts {
				fmt.Fprintf(&b, "\tcase %s:\n\t\treturn %s, true\n", e.keyTexts[i], e.valTexts[i])
			}
			fmt.Fprintf(&b, "\t}\n\tvar _logos_zero %s\n\treturn _logos_zero, false\n}\n", e.vT)
			return b.String()
		}
		fmt.Fprintf(&b, "func %s_lookup(_logos_k %s) (%s, bool) {\n\tswitch _logos_k {\n", name, e.kT, e.vT)
		for i := range e.keyTexts {
			fmt.Fprintf(&b, "\tcase %s:\n\t\treturn %s_val(%d), true\n", e.keyTexts[i], name, i)
		}
		fmt.Fprintf(&b, "\t}\n\tvar _logos_zero %s\n\treturn _logos_zero, false\n}\n", e.vT)
		return b.String()
	}
	if !e.bearing {
		fmt.Fprintf(&b, "%s\nvar %s = %s.ConstTable[%s, %s]{Salt: %d, Buckets: []%s.ConstBucket{", rodataMarker, name+"_tab", rtAlias, e.kT, e.vT, e.layout.salt, rtAlias)
		for i, bk := range e.layout.buckets {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "{Off: %d, Size: %d, Seed: %d}", bk.off, bk.size, bk.seed)
		}
		fmt.Fprintf(&b, "}, Slots: []%s.ConstSlot[%s, %s]{", rtAlias, e.kT, e.vT)
		for s, ki := range e.layout.slots {
			if s > 0 {
				b.WriteString(", ")
			}
			if ki < 0 {
				b.WriteString("{}")
				continue
			}
			fmt.Fprintf(&b, "{Key: %s, Val: %s, Occ: true}", e.keyTexts[ki], e.valTexts[ki])
		}
		b.WriteString("}}\n")
		fmt.Fprintf(&b, "func %s_lookup(_logos_k %s) (%s, bool) {\n\treturn %s.Get(%s, _logos_k)\n}\n", name, e.kT, e.vT, name+"_tab", e.hashExpr)
		return b.String()
	}
	fmt.Fprintf(&b, "%s\nvar %s = %s.ConstTable[%s, uint32]{Salt: %d, Buckets: []%s.ConstBucket{", rodataMarker, name+"_tab", rtAlias, e.kT, e.layout.salt, rtAlias)
	for i, bk := range e.layout.buckets {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "{Off: %d, Size: %d, Seed: %d}", bk.off, bk.size, bk.seed)
	}
	fmt.Fprintf(&b, "}, Slots: []%s.ConstSlot[%s, uint32]{", rtAlias, e.kT)
	for s, ki := range e.layout.slots {
		if s > 0 {
			b.WriteString(", ")
		}
		if ki < 0 {
			b.WriteString("{}")
			continue
		}
		fmt.Fprintf(&b, "{Key: %s, Val: %d, Occ: true}", e.keyTexts[ki], ki)
	}
	b.WriteString("}}\n")
	fmt.Fprintf(&b, "func %s_lookup(_logos_k %s) (%s, bool) {\n\tif _logos_idx, _logos_ok := %s.Get(%s, _logos_k); _logos_ok {\n\t\treturn %s_val(_logos_idx), true\n\t}\n\tvar _logos_zero %s\n\treturn _logos_zero, false\n}\n", name, e.kT, e.vT, name+"_tab", e.hashExpr, name, e.vT)
	return b.String()
}

func collectConstMaps(files []*syntax.File, info *types2.Info) map[*types2.Const]*constMapGen {
	clearConstMat()
	out := map[*types2.Const]*constMapGen{}
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
			v, ok := info.LogosCTFE[obj]
			if !ok || v.Type() == nil || v.Type().Kind != ctfe.KMap {
				return true
			}
			init := cd.Values
			out[obj] = &constMapGen{
				name: fmt.Sprintf("_logos_cm%d_%s", seq, cd.NameList[0].Value),
				obj:  obj,
				val:  v,
				init: init,
			}
			seq++
			return true
		})
	}
	return out
}

func (fl *fileLower) constMapUse(x syntax.Expr) (*constMapGen, bool) {
	name, ok := syntax.Unparen(x).(*syntax.Name)
	if !ok {
		return nil, false
	}
	obj, ok := fl.pkg.info.Uses[name].(*types2.Const)
	if !ok {
		return nil, false
	}
	g, ok := fl.pkg.constMaps[obj]
	return g, ok
}

func (g *constMapGen) constMapEntries() (keys, vals []ctfe.Value) {
	return g.val.MapEntries()
}

func constMapElem(t types2.Type) types2.Type {
	if cv, ok := t.(*types2.ConstView); ok {
		return cv.Elem
	}
	return t
}

func (fl *fileLower) lowerConstMapSpec(cd *syntax.ConstDecl, g *constMapGen) {
	fl.needRT = true
	if _, ejectText, ok := fl.constMapOwnedTexts(g, cd.Pos()); ok {
		decls := g.nativeDecls
		fl.markConstMapTables(decls, g)
		fl.constDecls = append(fl.constDecls, decls...)
		fl.constText += "\n" + ejectText
		fl.dropConstSpec(cd)
		return
	}
	text := fl.constMapText(g, cd.Pos())
	decls := parseConstMapDecls(text, cd.Pos())
	fl.markConstMapTables(decls, g)
	fl.constDecls = append(fl.constDecls, decls...)
	fl.constText += "\n" + text
	fl.dropConstSpec(cd)
}

func (fl *fileLower) markConstMapTables(decls []syntax.Decl, g *constMapGen) {
	for _, d := range decls {
		vd, ok := d.(*syntax.VarDecl)
		if !ok || len(vd.NameList) != 1 {
			continue
		}
		v := vd.NameList[0].Value
		if v != g.sortedName() && v != g.tabName() {
			continue
		}
		fl.rodataVars = append(fl.rodataVars, rodataVar{vd: vd})
	}
}

func (fl *fileLower) dropConstSpec(cd *syntax.ConstDecl) {
	if cd.Group == nil {
		if kw, ok := constKeywordStart(fl.src, fl.off(syntax.StartPos(cd))); ok {
			fl.add(kw, swallowSep(fl.src, fl.endOff(cd.Values)), "")
			return
		}
	}
	fl.deleteSpec(cd)
}

func (fl *fileLower) constMapText(g *constMapGen, pos syntax.Pos) string {
	elem := constMapElem(g.obj.Type())
	mt, ok := elem.Underlying().(*types2.Map)
	if !ok {
		base.Fatalf("logos: const map %s is not a map at %s", g.obj.Name(), pos.String())
	}
	kT := fl.mustSpell(mt.Key(), pos)
	vT := fl.mustSpell(mt.Elem(), pos)
	mapT := fl.mustSpell(elem, pos)
	keys, vals := g.constMapEntries()
	keyText := make([]string, len(keys))
	valText := make([]string, len(vals))
	for i := range keys {
		keyText[i] = fl.mustRenderWith(keys[i], mt.Key(), pos)
		valText[i] = fl.mustRenderWith(vals[i], mt.Elem(), pos)
	}
	order := make([]int, len(keys))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return constKeyCompare(keys[a], keys[b]) })
	bearing := constMapBearing(vals)
	isSwitch := len(keys) <= constMapSwitchMax
	var layout *constMapLayout
	if !isSwitch {
		entries := make([]constMapKey, len(keys))
		for i := range keys {
			entries[i] = constMapKey{hash: constKeyHash(keys[i]), canon: constKeyCanon(keys[i])}
		}
		var err error
		if base.Debug.LogosConstmapSeeds < 0 {
			base.Errorf("%s: invalid -d=logosconstmapseeds value %d (want a non-negative seed cap)", pos.String(), base.Debug.LogosConstmapSeeds)
			base.ExitIfErrors()
		}
		budget := defaultConstMapBudget
		if base.Debug.LogosConstmapSeeds > 0 {
			budget = constMapBudget{MaxSeeds: base.Debug.LogosConstmapSeeds, Rounds: defaultConstMapBudget.Rounds}
		}
		layout, err = buildConstMap(entries, budget)
		if err != nil {
			base.Errorf("%s: %v", pos.String(), err)
			base.ExitIfErrors()
		}
	}
	return emitConstMapDecls(constMapEmission{
		name:        g.name,
		kT:          kT,
		vT:          vT,
		mapT:        mapT,
		keyTexts:    keyText,
		valTexts:    valText,
		builderBody: fl.mustRender(g.val, pos),
		hashExpr:    fl.constKeyHashText(mt.Key(), "_logos_k", pos),
		order:       order,
		layout:      layout,
		isSwitch:    isSwitch,
		bearing:     bearing,
	})
}

func (fl *fileLower) constKeyHashText(t types2.Type, x string, pos syntax.Pos) string {
	switch u := t.Underlying().(type) {
	case *types2.Basic:
		switch {
		case u.Info()&types2.IsString != 0:
			return rtAlias + ".HashString(" + x + ")"
		case u.Info()&types2.IsBoolean != 0:
			return rtAlias + ".HashBool(" + x + ")"
		case u.Info()&types2.IsInteger != 0:
			return rtAlias + ".HashUint64(uint64(" + x + "))"
		case u.Info()&types2.IsFloat != 0:
			return rtAlias + ".HashFloat64(float64(" + x + "))"
		case u.Info()&types2.IsComplex != 0:
			return rtAlias + ".HashCombine(" + rtAlias + ".HashFloat64(float64(real(" + x + "))), " + rtAlias + ".HashFloat64(float64(imag(" + x + "))))"
		}
	case *types2.Struct:
		var b strings.Builder
		fmt.Fprintf(&b, "func(_logos_v %s) uint64 { _logos_h := uint64(0); ", fl.mustSpell(t, pos))
		for i := range u.NumFields() {
			f := u.Field(i)
			if f.Name() == "_" {
				continue
			}
			fmt.Fprintf(&b, "_logos_h = %s.HashCombine(_logos_h, %s); ", rtAlias, fl.constKeyHashText(f.Type(), "_logos_v."+f.Name(), pos))
		}
		fmt.Fprintf(&b, "return _logos_h }(%s)", x)
		return b.String()
	case *types2.Array:
		return fmt.Sprintf("func(_logos_v %s) uint64 { _logos_h := uint64(0); for _, _logos_e := range _logos_v { _logos_h = %s.HashCombine(_logos_h, %s) }; return _logos_h }(%s)",
			fl.mustSpell(t, pos), rtAlias, fl.constKeyHashText(u.Elem(), "_logos_e", pos), x)
	}
	base.Fatalf("logos: const map key type %s cannot be hashed at %s", t, pos.String())
	return ""
}

func (fl *fileLower) mustSpell(t types2.Type, pos syntax.Pos) string {
	s, ok := fl.spellLowered(t)
	if !ok {
		base.Fatalf("logos: cannot spell const map type %s at %s", t, pos.String())
	}
	return s
}

func (fl *fileLower) mustRender(v ctfe.Value, pos syntax.Pos) string {
	e, err := v.Expr(pos)
	if err != nil {
		base.Fatalf("logos: cannot spell const map element at %s: %v", pos.String(), err)
	}
	return renderExpr(e)
}

func (fl *fileLower) mustRenderWith(v ctfe.Value, t types2.Type, pos syntax.Pos) string {
	if e, err := v.Expr(pos); err == nil {
		return renderExpr(e)
	}
	if _, s, ok := fl.buildMatValue(v, t, pos); ok {
		return s
	}
	return fl.mustRender(v, pos)
}

func parseConstMapDecls(text string, pos syntax.Pos) []syntax.Decl {
	file, err := syntax.Parse(syntax.NewFileBase("logos-constmap"), strings.NewReader("package p\n"+text), nil, nil, 0)
	if err != nil {
		base.Fatalf("logos: cannot parse generated const map code at %s: %v", pos.String(), err)
	}
	for _, d := range file.DeclList {
		syntax.Inspect(d, func(n syntax.Node) bool {
			if n != nil {
				n.SetPos(pos)
				setRbrace(n, pos)
			}
			return true
		})
	}
	return file.DeclList
}

func setRbrace(n syntax.Node, pos syntax.Pos) {
	switch n := n.(type) {
	case *syntax.BlockStmt:
		n.Rbrace = pos
	case *syntax.SwitchStmt:
		n.Rbrace = pos
	case *syntax.SelectStmt:
		n.Rbrace = pos
	case *syntax.CompositeLit:
		n.Rbrace = pos
	}
}

func (fl *fileLower) lowerConstMapRanges() {
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		rc, ok := n.(*syntax.RangeClause)
		if !ok {
			return true
		}
		g, ok := fl.constMapUse(rc.X)
		if !ok {
			return true
		}
		pos := rc.X.Pos()
		start, end := fl.off(syntax.StartPos(rc.X)), fl.endOff(rc.X)
		_, vals := g.constMapEntries()
		if constMapBearing(vals) {
			fl.add(start, end, g.allName())
			fl.needRT = true
			rc.X = nameOf(pos, g.allName())
			return true
		}
		fl.add(start, end, rtAlias+".ConstAll("+g.sortedName()+")")
		fl.needRT = true
		rc.X = callOf(pos, selOf(pos, nameOf(pos, rtAlias), "ConstAll"), []syntax.Expr{nameOf(pos, g.sortedName())})
		return true
	})
}

func (fl *fileLower) lowerConstMapName(e *syntax.Name) (syntax.Expr, bool) {
	g, ok := fl.constMapUse(e)
	if !ok {
		return nil, false
	}
	start := fl.off(e.Pos())
	fl.add(start, start+len(e.Value), g.mapName()+"()")
	return callOf(e.Pos(), nameOf(e.Pos(), g.mapName()), nil), true
}

func (fl *fileLower) lowerConstMapLen(e *syntax.CallExpr) (syntax.Expr, bool) {
	fn, ok := e.Fun.(*syntax.Name)
	if !ok || fn.Value != "len" || len(e.ArgList) != 1 {
		return nil, false
	}
	if _, ok := fl.pkg.info.Uses[fn].(*types2.Builtin); !ok {
		return nil, false
	}
	g, ok := fl.constMapUse(e.ArgList[0])
	if !ok {
		return nil, false
	}
	keys, _ := g.constMapEntries()
	n := strconv.Itoa(len(keys))
	fl.add(fl.off(syntax.StartPos(e)), fl.endOff(e), n)
	return at(&syntax.BasicLit{Value: n, Kind: syntax.IntLit}, e.Pos()), true
}

func (fl *fileLower) lowerConstMapRead(e *syntax.IndexExpr, g *constMapGen) syntax.Expr {
	pos := e.Pos()
	id := fl.nextWrapID()
	mStart, lbrack, rbrack := fl.lookupSpans(e)
	e.Index = fl.lowerOptExpr(e.Index)
	lookup := callOf(pos, nameOf(pos, g.lookupName()), []syntax.Expr{e.Index})
	opt := fl.asOption(fl.optType(e))
	if opt == nil {
		fl.addOwned(id, mStart, lbrack, g.lookupName())
		fl.add(lbrack, lbrack+1, "(")
		fl.add(rbrack, rbrack+1, ")")
		fl.wrapSpan[lookup] = span{start: mStart, end: rbrack + 1}
		fl.wrapOwner[lookup] = id
		return lookup
	}
	wrap := "Opt"
	if keepMapRead(opt.Elem) {
		wrap = "Bare"
	}
	fl.needRT = true
	out := callOf(pos, selOf(pos, nameOf(pos, rtAlias), wrap), []syntax.Expr{lookup})
	fl.addOwned(id, mStart, lbrack, rtAlias+"."+wrap+"("+g.lookupName())
	fl.add(lbrack, lbrack+1, "(")
	fl.add(rbrack, rbrack+1, "))")
	fl.wrapSpan[out] = span{start: mStart, end: rbrack + 1}
	fl.wrapOwner[out] = id
	return out
}

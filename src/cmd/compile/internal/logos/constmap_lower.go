// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"cmd/compile/internal/base"
	"cmd/compile/internal/logos/ctfe"
	"cmd/compile/internal/logos/facts"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
	"fmt"
	"go/token"
	"slices"
	"strconv"
	"strings"
)

const constMapSwitchMax = 8

const (
	constMapHashRecv = "_logos_v"
	constMapHashAcc  = "_logos_h"
)

type constMapGen struct {
	name        string
	obj         *types2.Const
	val         ctfe.Value
	init        syntax.Expr
	nativeDecls []syntax.Decl
	nkeys       int
	bearing     bool
	lead        syntax.Expr
	owner       *types2.Var
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
		if constMapValueBearing(v.Type()) {
			return true
		}
	}
	return false
}

func constMapValueBearing(t *ctfe.Type) bool {
	if t == nil {
		return true
	}
	switch t.Kind {
	case ctfe.KSlice, ctfe.KMap:
		return true
	case ctfe.KArray:
		return constMapValueBearing(t.Elem)
	case ctfe.KStruct:
		for _, f := range t.Fields {
			if constMapValueBearing(f.Type) {
				return true
			}
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
		fmt.Fprintf(&b, "func %s_all(_logos_yield func(%s, %s) bool) { %s.ConstAll(%s_sorted)(_logos_yield) }\n", name, e.kT, e.vT, rtAlias, name)
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
	if e.builderBody != "" {
		fmt.Fprintf(&b, "var %s_mat %s.ConstMapMaterialization[%s, %s, %s]\n", name, rtAlias, e.mapT, e.kT, e.vT)
		fmt.Fprintf(&b, "func %s_build() %s { return %s }\n", name, e.mapT, e.builderBody)
		fmt.Fprintf(&b, "func %s_map() %s { return %s_mat.Get(%s_build) }\n", name, e.mapT, name, name)
	}
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
	for _, file := range files {
		collectConstMapDecls(file, info, out)
	}
	return out
}

func (fl *fileLower) enrollLoweredConstMaps() {
	collectConstMapDecls(fl.file, fl.pkg.info, fl.pkg.constMaps)
}

func collectConstMapDecls(root syntax.Node, info *types2.Info, out map[*types2.Const]*constMapGen) {
	syntax.Inspect(root, func(n syntax.Node) bool {
		cd, ok := n.(*syntax.ConstDecl)
		if !ok || len(cd.NameList) != 1 {
			return true
		}
		obj, ok := info.Defs[cd.NameList[0]].(*types2.Const)
		if !ok || out[obj] != nil {
			return true
		}
		v, ok := info.LogosCTFE[obj]
		if !ok || v.Type() == nil || v.Type().Kind != ctfe.KMap {
			return true
		}
		keys, vals := v.MapEntries()
		out[obj] = &constMapGen{
			name:    fmt.Sprintf("_logos_cm%d_%s", len(out), obj.Name()),
			obj:     obj,
			val:     v,
			init:    cd.Values,
			nkeys:   len(keys),
			bearing: constMapBearing(vals),
		}
		return true
	})
}

func (fl *fileLower) constMapReadUse(x syntax.Expr) (*constMapGen, bool) {
	if g, ok := fl.constMapUse(x); ok {
		return g, true
	}
	return fl.compositeFamilyFor(x)
}

func (fl *fileLower) constMapUse(x syntax.Expr) (*constMapGen, bool) {
	switch e := syntax.Unparen(x).(type) {
	case *syntax.Name:
		if obj, ok := fl.pkg.info.Uses[e].(*types2.Const); ok {
			if g, ok := fl.pkg.constMaps[obj]; ok {
				return g, true
			}
		}
		if obj, ok := fl.pkg.info.Uses[e].(*types2.Var); ok {
			return fl.importedConstMap(obj, e.Pos())
		}
	case *syntax.SelectorExpr:
		if e.Sel == nil {
			return nil, false
		}
		if obj, ok := fl.pkg.info.Uses[e.Sel].(*types2.Var); ok && !obj.IsField() {
			if g, ok := fl.importedConstMap(obj, e.Pos()); ok {
				return g, true
			}
		}
	}
	return fl.compositeHiddenFor(x)
}

func importedConstMapRecord(obj *types2.Var) (facts.ConstMapFact, bool) {
	var zero facts.ConstMapFact
	if obj.Pkg() == nil {
		return zero, false
	}
	if scope := obj.Pkg().Scope(); scope == nil || scope.Lookup(obj.Name()) != types2.Object(obj) {
		return zero, false
	}
	f, ok := facts.Lookup(obj.Pkg().Path())
	if !ok || f == nil {
		return zero, false
	}
	for i := range f.ConstMaps {
		rec := f.ConstMaps[i]
		if rec.Object != obj.Name() || rec.Path != "" {
			continue
		}
		if len(rec.Keys) != len(rec.Vals) || len(rec.Order) != len(rec.Keys) {
			return zero, false
		}
		return rec, true
	}
	return zero, false
}

func (fl *fileLower) importedConstMap(obj *types2.Var, pos syntax.Pos) (*constMapGen, bool) {
	view := constAsView(obj.Type())
	if view == nil || !view.ConstMap {
		return nil, false
	}
	if obj.Pkg() == nil || obj.Pkg() == fl.pkg.tpkg {
		return nil, false
	}
	if fl.pkg.importedMaps == nil {
		fl.pkg.importedMaps = map[*types2.Var]*constMapGen{}
	}
	if fl.pkg.importedDone == nil {
		fl.pkg.importedDone = map[string]bool{}
	}
	if g, ok := fl.pkg.importedMaps[obj]; ok {
		return g, true
	}
	rec, ok := importedConstMapRecord(obj)
	if !ok {
		return nil, false
	}
	mt, ok := constMapElem(obj.Type()).Underlying().(*types2.Map)
	if !ok {
		return nil, false
	}
	name := fmt.Sprintf("_logos_icm%d_%s", fl.pkg.importedSeq, obj.Name())
	fl.pkg.importedSeq++
	g, ok := fl.emitImportedConstMap(importedMapSpec{
		label: obj.Pkg().Path() + "." + obj.Name(), mapType: constMapElem(obj.Type()),
		mt: mt, rec: rec, name: name, owner: obj,
	}, pos)
	if ok {
		fl.pkg.importedMaps[obj] = g
	}
	return g, ok
}

type importedMapSpec struct {
	label   string
	mapType types2.Type
	mt      *types2.Map
	rec     facts.ConstMapFact
	name    string
	owner   *types2.Var
}

func (fl *fileLower) emitImportedConstMap(s importedMapSpec, pos syntax.Pos) (*constMapGen, bool) {
	rec, mt := s.rec, s.mt
	if why, ok := fl.unspellableImportedConstMap(s.label, s.mapType, mt); !ok {
		base.Errorf("%s: %s", pos.String(), why)
		base.ExitIfErrors()
		return nil, false
	}
	quals, ambiguous := fl.constMapImportQuals(mt)
	if ambiguous != "" {
		base.Errorf("%s: logos: imported const map %s uses %s for types from different packages", pos.String(), s.label, ambiguous)
		base.ExitIfErrors()
		return nil, false
	}
	keys, ok := fl.qualifyConstMapTexts(rec.Keys, quals, pos)
	if !ok {
		base.Errorf("%s: logos: imported const map %s has a key that cannot be spelled in this package", pos.String(), s.label)
		base.ExitIfErrors()
		return nil, false
	}
	vals, ok := fl.qualifyConstMapTexts(rec.Vals, quals, pos)
	if !ok {
		base.Errorf("%s: logos: imported const map %s has a value that cannot be spelled in this package", pos.String(), s.label)
		base.ExitIfErrors()
		return nil, false
	}
	kT := fl.mustSpell(mt.Key(), pos)
	vT := fl.mustSpell(mt.Elem(), pos)
	mapT := fl.mustSpell(s.mapType, pos)
	entries := make([]constMapKey, len(rec.Keys))
	for i := range rec.Keys {
		entries[i] = constMapKey{hash: rec.Hashes[i], canon: rec.Canons[i]}
	}
	var builderBody, materialized string
	if s.owner != nil {
		materialized = s.owner.Name()
		if qual, ok := fl.constMapImportQual(s.owner.Pkg()); ok {
			materialized = qual + "." + materialized
		}
	} else {
		pairs := make([]string, len(rec.Keys))
		for i := range pairs {
			pairs[i] = keys[i] + ": " + vals[i]
		}
		builderBody = mapT + "{" + strings.Join(pairs, ", ") + "}"
	}
	text := finalizeConstMapEmission(pos, constMapEmission{
		name:        s.name,
		kT:          kT,
		vT:          vT,
		mapT:        mapT,
		keyTexts:    keys,
		valTexts:    vals,
		builderBody: builderBody,
		hashExpr:    fl.constKeyHashText(mt.Key(), "_logos_k", pos),
		order:       rec.Order,
		bearing:     rec.Bearing,
	}, entries)
	if materialized != "" {
		text += fmt.Sprintf("func %s_map() %s { return %s }\n", s.name, mapT, materialized)
	}
	g := &constMapGen{name: s.name, nkeys: len(rec.Keys), bearing: rec.Bearing}
	if s.owner != nil && fl.pkg.info.LogosCtImportConstMap != nil {
		if row, ok := fl.pkg.info.LogosCtImportConstMap(s.owner.Pkg().Path(), s.owner.Name()); ok && row.Binding == s.owner {
			g.owner = s.owner
			g.nkeys = row.Count
		}
	}
	if !fl.pkg.importedDone[s.name] {
		fl.pkg.importedDone[s.name] = true
		if g.owner == nil {
			decls := parseConstMapDecls(text, pos)
			fl.markConstMapTables(decls, g)
			fl.constDecls = append(fl.constDecls, decls...)
			fl.needRT = true
		} else {
			fl.needRTText = true
			if fl.genImps != nil {
				for path, alias := range fl.genImps.qual {
					if q, imported := fl.existingImportName(path); imported && q != "" && q == alias && containsIdent(text, alias) {
						fl.ensureEjectImport(path, pos)
					}
				}
			}
		}
		fl.constText += "\n" + text
	}
	return g, true
}

func stripConstMapView(t types2.Type) types2.Type {
	for {
		cv, ok := t.(*types2.ConstView)
		if !ok {
			return t
		}
		t = cv.Elem
	}
}

func (fl *fileLower) unspellableImportedConstMap(label string, mapType types2.Type, mt *types2.Map) (string, bool) {
	what := "logos: imported const map " + label
	if _, ok := fl.spellLowered(mt.Key()); !ok {
		return what + " has a key type " + mt.Key().String() + " that cannot be named in this package", false
	}
	if _, ok := fl.spellLowered(mt.Elem()); !ok {
		return what + " has a value type " + mt.Elem().String() + " that cannot be named in this package", false
	}
	if _, ok := fl.spellLowered(mapType); !ok {
		return what + " has a map type that cannot be named in this package", false
	}
	if !matFieldsAssignable(stripConstMapView(mt.Key()), fl.pkg.tpkg) || !matFieldsAssignable(stripConstMapView(mt.Elem()), fl.pkg.tpkg) {
		return what + " refers to an unexported field that cannot be named in this package", false
	}
	unexported := ""
	walkConstMapTypes(mt, func(n *types2.Named) bool {
		obj := n.Obj()
		if obj == nil {
			return true
		}
		if pkg := obj.Pkg(); pkg != nil && pkg != fl.pkg.tpkg && !obj.Exported() {
			unexported = obj.Name()
			return false
		}
		return true
	})
	if unexported != "" {
		return what + " uses unexported type " + unexported + " that cannot be named in this package", false
	}
	return "", true
}

func walkConstMapTypes(mt *types2.Map, visit func(n *types2.Named) bool) {
	seen := map[types2.Type]bool{}
	var walk func(t types2.Type)
	walk = func(t types2.Type) {
		if t == nil || seen[t] {
			return
		}
		seen[t] = true
		if cv, ok := t.(*types2.ConstView); ok {
			walk(cv.Elem)
			return
		}
		u := types2.Unalias(t)
		if n, ok := u.(*types2.Named); ok {
			if !visit(n) {
				return
			}
			if args := n.TypeArgs(); args != nil {
				for i := range args.Len() {
					walk(args.At(i))
				}
			}
			u = n.Underlying()
		}
		switch u := u.(type) {
		case *types2.Struct:
			for i := range u.NumFields() {
				walk(u.Field(i).Type())
			}
		case *types2.Array:
			walk(u.Elem())
		case *types2.Slice:
			walk(u.Elem())
		case *types2.Map:
			walk(u.Key())
			walk(u.Elem())
		case *types2.Pointer:
			walk(u.Elem())
		}
	}
	walk(mt.Key())
	walk(mt.Elem())
}

func (fl *fileLower) constMapImportQuals(mt *types2.Map) (map[string]*types2.Package, string) {
	quals := map[string]*types2.Package{}
	ambiguous := ""
	walkConstMapTypes(mt, func(n *types2.Named) bool {
		obj := n.Obj()
		if obj == nil {
			return true
		}
		pkg := obj.Pkg()
		if pkg == nil || pkg == fl.pkg.tpkg {
			return true
		}
		if prev, dup := quals[obj.Name()]; dup {
			if prev != pkg {
				ambiguous = obj.Name()
				return false
			}
			return true
		}
		quals[obj.Name()] = pkg
		return true
	})
	return quals, ambiguous
}

func (fl *fileLower) constMapImportQual(pkg *types2.Package) (string, bool) {
	if q, ok := fl.importNames()[pkg]; ok && q == "" {
		return "", false
	}
	return fl.synthQualifier(pkg), true
}

func (fl *fileLower) constMapQualName(value string, quals map[string]*types2.Package) (string, bool) {
	pkg, ok := quals[value]
	if !ok {
		return "", false
	}
	return fl.constMapImportQual(pkg)
}

func (fl *fileLower) rewriteConstMapType(t syntax.Expr, quals map[string]*types2.Package, pos syntax.Pos) syntax.Expr {
	switch t := t.(type) {
	case *syntax.Name:
		if q, ok := fl.constMapQualName(t.Value, quals); ok {
			return selOf(pos, nameOf(pos, q), t.Value)
		}
		return t
	case *syntax.SelectorExpr:
		if _, ok := t.X.(*syntax.Name); ok {
			if q, ok := fl.constMapQualName(t.Sel.Value, quals); ok {
				t.X = nameOf(pos, q)
			}
		}
		return t
	case *syntax.SliceType:
		t.Elem = fl.rewriteConstMapType(t.Elem, quals, pos)
		return t
	case *syntax.ArrayType:
		t.Elem = fl.rewriteConstMapType(t.Elem, quals, pos)
		return t
	case *syntax.MapType:
		t.Key = fl.rewriteConstMapType(t.Key, quals, pos)
		t.Value = fl.rewriteConstMapType(t.Value, quals, pos)
		return t
	case *syntax.ParenExpr:
		t.X = fl.rewriteConstMapType(t.X, quals, pos)
		return t
	case *syntax.IndexExpr:
		t.X = fl.rewriteConstMapType(t.X, quals, pos)
		t.Index = fl.rewriteConstMapType(t.Index, quals, pos)
		return t
	default:
		return t
	}
}

func (fl *fileLower) rewriteConstMapValue(x syntax.Expr, quals map[string]*types2.Package, pos syntax.Pos) syntax.Expr {
	switch x := x.(type) {
	case *syntax.CompositeLit:
		if x.Type != nil {
			x.Type = fl.rewriteConstMapType(x.Type, quals, pos)
		}
		for i, e := range x.ElemList {
			x.ElemList[i] = fl.rewriteConstMapElem(e, quals, pos)
		}
		return x
	case *syntax.CallExpr:
		x.Fun = fl.rewriteConstMapType(x.Fun, quals, pos)
		for i, a := range x.ArgList {
			x.ArgList[i] = fl.rewriteConstMapValue(a, quals, pos)
		}
		return x
	case *syntax.Operation:
		x.X = fl.rewriteConstMapValue(x.X, quals, pos)
		if x.Y != nil {
			x.Y = fl.rewriteConstMapValue(x.Y, quals, pos)
		}
		return x
	case *syntax.SliceExpr:
		x.X = fl.rewriteConstMapValue(x.X, quals, pos)
		for i, idx := range x.Index {
			if idx != nil {
				x.Index[i] = fl.rewriteConstMapValue(idx, quals, pos)
			}
		}
		return x
	case *syntax.ParenExpr:
		x.X = fl.rewriteConstMapValue(x.X, quals, pos)
		return x
	default:
		return x
	}
}

func (fl *fileLower) rewriteConstMapElem(e syntax.Expr, quals map[string]*types2.Package, pos syntax.Pos) syntax.Expr {
	if kv, ok := e.(*syntax.KeyValueExpr); ok {
		if _, isName := kv.Key.(*syntax.Name); !isName {
			kv.Key = fl.rewriteConstMapValue(kv.Key, quals, pos)
		}
		kv.Value = fl.rewriteConstMapValue(kv.Value, quals, pos)
		return kv
	}
	return fl.rewriteConstMapValue(e, quals, pos)
}

func parseConstMapEntryExpr(text string, pos syntax.Pos) (syntax.Expr, bool) {
	file, err := syntax.Parse(syntax.NewFileBase("logos-constmap-entry"), strings.NewReader("package p\nvar _ = "+text), nil, nil, 0)
	if err != nil || file == nil || len(file.DeclList) != 1 {
		return nil, false
	}
	vd, ok := file.DeclList[0].(*syntax.VarDecl)
	if !ok || vd.Values == nil {
		return nil, false
	}
	e := vd.Values
	syntax.Inspect(e, func(n syntax.Node) bool {
		if n != nil {
			n.SetPos(pos)
			setRbrace(n, pos)
		}
		return true
	})
	return e, true
}

func (fl *fileLower) qualifyConstMapTexts(texts []string, quals map[string]*types2.Package, pos syntax.Pos) ([]string, bool) {
	out := make([]string, len(texts))
	for i, tx := range texts {
		e, ok := parseConstMapEntryExpr(tx, pos)
		if !ok {
			return nil, false
		}
		out[i] = renderExpr(fl.rewriteConstMapValue(e, quals, pos))
	}
	return out, true
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
	fl.recordCtConstMap(g)

	fl.needRT = true
	if _, ejectText, ok := fl.constMapOwnedTexts(g, cd.Pos()); ok {
		decls := g.nativeDecls
		fl.markConstMapTables(decls, g)
		fl.constDecls = append(fl.constDecls, decls...)
		fl.constText += "\n" + ejectText
		fl.dropConstSpec(cd)
		return
	}
	parts := fl.buildConstMapParts(g, cd.Pos())
	text := parts.text
	if name := cd.NameList[0].Value; token.IsExported(name) && fl.pkg.tpkg.Scope().Lookup(name) == g.obj {
		text += fmt.Sprintf("\nvar %s %s = %s()\n", name, parts.mapT, g.mapName())
		fl.recordConstMapFact(g, parts)
	}
	decls := parseConstMapDecls(text, cd.Pos())
	fl.markConstMapTables(decls, g)
	fl.constDecls = append(fl.constDecls, decls...)
	fl.constText += "\n" + text
	fl.dropConstSpec(cd)
}

func (fl *fileLower) recordCtConstMap(g *constMapGen) {
	if g.obj.Parent() != fl.pkg.tpkg.Scope() {
		return
	}
	info := fl.pkg.info
	if info.LogosCtExportConstMaps == nil {
		info.LogosCtExportConstMaps = make(map[string]types2.LogosCtConstMapExport)
	}
	info.LogosCtExportConstMaps[g.obj.Name()] = types2.LogosCtConstMapExport{
		Type: g.obj.Type(), Count: g.nkeys, Bearing: g.bearing,
		Lookup: g.lookupName(), Materialize: g.mapName(), Iterate: g.allName(),
	}
}

func (fl *fileLower) recordConstMapFact(g *constMapGen, parts constMapParts) {
	f := facts.Current()
	if f == nil {
		return
	}
	name := g.obj.Name()
	for i := range f.ConstMaps {
		if f.ConstMaps[i].Object == name {
			return
		}
	}
	f.ConstMaps = append(f.ConstMaps, facts.ConstMapFact{
		Object:  name,
		Bearing: parts.bearing,
		Order:   parts.order,
		Hashes:  parts.hashes,
		Canons:  parts.canons,
		Keys:    parts.keyTexts,
		Vals:    parts.valTexts,
	})
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
		if kw, ok := fl.keywords.Start(fl.src, token.CONST, fl.off(syntax.StartPos(cd))); ok {
			fl.add(kw, swallowSep(fl.src, fl.endOff(cd.Values)), "")
			return
		}
	}
	fl.deleteSpec(cd)
}

type constMapParts struct {
	text     string
	mapT     string
	keyTexts []string
	valTexts []string
	hashes   []uint64
	canons   []string
	order    []int
	bearing  bool
}

func (fl *fileLower) constMapText(g *constMapGen, pos syntax.Pos) string {
	return fl.buildConstMapParts(g, pos).text
}

func (fl *fileLower) buildConstMapParts(g *constMapGen, pos syntax.Pos) constMapParts {
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
	hashes := make([]uint64, len(keys))
	canons := make([]string, len(keys))
	for i := range keys {
		keyText[i] = fl.mustRenderWith(keys[i], mt.Key(), pos)
		valText[i] = fl.mustRenderWith(vals[i], mt.Elem(), pos)
		hashes[i] = constKeyHash(keys[i])
		canons[i] = constKeyCanon(keys[i])
	}
	order := make([]int, len(keys))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return ctfe.CompareKeys(keys[a], keys[b]) })
	entries := make([]constMapKey, len(keys))
	for i := range keys {
		entries[i] = constMapKey{hash: hashes[i], canon: canons[i]}
	}
	bearing := constMapBearing(vals)
	pairs := make([]string, len(keys))
	for i := range keys {
		pairs[i] = keyText[i] + ": " + valText[i]
	}
	body := mapT + "{" + strings.Join(pairs, ", ") + "}"
	if e, err := g.val.Expr(pos); err == nil {
		body = renderExpr(e)
	}
	text := finalizeConstMapEmission(pos, constMapEmission{
		name:        g.name,
		kT:          kT,
		vT:          vT,
		mapT:        mapT,
		keyTexts:    keyText,
		valTexts:    valText,
		builderBody: body,
		hashExpr:    fl.constKeyHashText(mt.Key(), "_logos_k", pos),
		order:       order,
		bearing:     bearing,
	}, entries)
	return constMapParts{
		text:     text,
		mapT:     mapT,
		keyTexts: keyText,
		valTexts: valText,
		hashes:   hashes,
		canons:   canons,
		order:    order,
		bearing:  bearing,
	}
}

func constMapSeedBudget(pos syntax.Pos) constMapBudget {
	if base.Debug.LogosConstmapSeeds < 0 {
		base.Errorf("%s: invalid -d=logosconstmapseeds value %d (want a non-negative seed cap)", pos.String(), base.Debug.LogosConstmapSeeds)
		base.ExitIfErrors()
	}
	if base.Debug.LogosConstmapSeeds > 0 {
		return constMapBudget{MaxSeeds: base.Debug.LogosConstmapSeeds, Rounds: defaultConstMapBudget.Rounds}
	}
	return defaultConstMapBudget
}

func finalizeConstMapEmission(pos syntax.Pos, e constMapEmission, entries []constMapKey) string {
	isSwitch := len(entries) <= constMapSwitchMax
	var layout *constMapLayout
	if !isSwitch {
		var err error
		layout, err = buildConstMap(entries, constMapSeedBudget(pos))
		if err != nil {
			base.Errorf("%s: %v", pos.String(), err)
			base.ExitIfErrors()
		}
	}
	e.layout = layout
	e.isSwitch = isSwitch
	return emitConstMapDecls(e)
}

func (fl *fileLower) constKeyHashText(t types2.Type, x string, pos syntax.Pos) string {
	switch u := t.Underlying().(type) {
	case *types2.Basic:
		switch {
		case u.Info()&types2.IsString != 0:
			return rtAlias + ".HashString(string(" + x + "))"
		case u.Info()&types2.IsBoolean != 0:
			return rtAlias + ".HashBool(bool(" + x + "))"
		case u.Info()&types2.IsInteger != 0:
			return rtAlias + ".HashUint64(uint64(" + x + "))"
		case u.Info()&types2.IsFloat != 0:
			return rtAlias + ".HashFloat64(float64(" + x + "))"
		case u.Info()&types2.IsComplex != 0:
			return rtAlias + ".HashCombine(" + rtAlias + ".HashFloat64(float64(real(" + x + "))), " + rtAlias + ".HashFloat64(float64(imag(" + x + "))))"
		}
	case *types2.Struct:
		var b strings.Builder
		fmt.Fprintf(&b, "func("+constMapHashRecv+" %s) uint64 { "+constMapHashAcc+" := uint64(0); ", fl.mustSpell(t, pos))
		for i := range u.NumFields() {
			f := u.Field(i)
			if f.Name() == "_" {
				continue
			}
			fmt.Fprintf(&b, constMapHashAcc+" = %s.HashCombine("+constMapHashAcc+", %s); ", rtAlias, fl.constKeyHashText(f.Type(), constMapHashRecv+"."+f.Name(), pos))
		}
		fmt.Fprintf(&b, "return "+constMapHashAcc+" }(%s)", x)
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
			fl.lowerSortedRange(rc)
			return true
		}
		pos := rc.X.Pos()
		start, end := fl.off(syntax.StartPos(rc.X)), fl.endOff(rc.X)
		if g.owner != nil {
			id, text := fl.constMapRef(pos, g, types2.LogosCtMapIterate)
			fl.add(start, end, text)
			rc.X = id
			return true
		}
		if g.bearing {
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

func (fl *fileLower) lowerSortedRange(rc *syntax.RangeClause) {
	mt, ok := fl.rangedConstMap(rc.X)
	if !ok {
		return
	}
	pos := rc.X.Pos()
	entry, compare := fl.sortedRangeEntry(mt.Key(), pos)
	args, suffix := []syntax.Expr{rc.X}, ")"
	if compare != "" {
		args, suffix = append(args, sortedRangeCompare(compare, pos)), ", "+compare+")"
	}
	start, end := fl.off(syntax.StartPos(rc.X)), fl.endOff(rc.X)
	fl.add(start, start, rtAlias+"."+entry+"(")
	fl.add(end, end, suffix)
	fl.needRT = true
	rc.X = callOf(pos, selOf(pos, nameOf(pos, rtAlias), entry), args)
}

func (fl *fileLower) lowerConstMapName(e *syntax.Name) (syntax.Expr, bool) {
	g, ok := fl.constMapUse(e)
	if !ok {
		return nil, false
	}
	start := fl.off(e.Pos())
	id, text := fl.constMapRef(e.Pos(), g, types2.LogosCtMapMaterialize)
	fl.add(start, start+len(e.Value), text+"()")
	return callOf(e.Pos(), id, nil), true
}
func (fl *fileLower) lowerConstMapSelector(e *syntax.SelectorExpr, g *constMapGen) syntax.Expr {
	id, text := fl.constMapRef(e.Pos(), g, types2.LogosCtMapMaterialize)
	fl.add(fl.off(syntax.StartPos(e)), fl.endOff(e), text+"()")
	return callOf(e.Pos(), id, nil)
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
	n := strconv.Itoa(g.nkeys)
	fl.add(fl.off(syntax.StartPos(e)), fl.endOff(e), n)
	return at(&syntax.BasicLit{Value: n, Kind: syntax.IntLit}, e.Pos()), true
}

func (fl *fileLower) lowerConstMapRead(e *syntax.IndexExpr, g *constMapGen) syntax.Expr {
	pos := e.Pos()
	id := fl.nextWrapID()
	mStart, lbrack, rbrack := fl.lookupSpans(e)
	e.Index = fl.lowerOptExpr(e.Index)
	args := []syntax.Expr{e.Index}
	fn, head := fl.constMapRef(pos, g, types2.LogosCtMapLookup)
	open := "("
	if g.lead != nil {
		args = []syntax.Expr{g.lead, e.Index}
		head += "(" + string(fl.src[fl.off(syntax.StartPos(g.lead)):fl.exprEndOf(g.lead)]) + ", "
		open = ""
	}
	lookup := callOf(pos, fn, args)
	opt := fl.asOption(fl.optType(e))
	if opt == nil {
		fl.addOwned(id, mStart, lbrack, head)
		fl.add(lbrack, lbrack+1, open)
		fl.add(rbrack, rbrack+1, ")")
		fl.wrapSpan[lookup] = span{start: mStart, end: rbrack + 1}
		fl.wrapOwner[lookup] = id
		return lookup
	}
	wrap := "Opt"
	if types2.LogosOptionIsBare(opt.Elem) {
		wrap = "Bare"
	}
	fl.needRT = true
	out := callOf(pos, selOf(pos, nameOf(pos, rtAlias), wrap), []syntax.Expr{lookup})
	fl.addOwned(id, mStart, lbrack, rtAlias+"."+wrap+"("+head)
	fl.add(lbrack, lbrack+1, open)
	fl.add(rbrack, rbrack+1, "))")
	fl.wrapSpan[out] = span{start: mStart, end: rbrack + 1}
	fl.wrapOwner[out] = id
	return out
}

type constMapTextRef struct {
	ref      types2.LogosCtConstMapRef
	portable string
}

type constMapFragmentMark struct {
	offset int
	text   constMapTextRef
}

func (fl *fileLower) constMapRef(pos syntax.Pos, g *constMapGen, role types2.LogosCtConstMapRole) (*syntax.Name, string) {
	var portable string
	switch role {
	case types2.LogosCtMapLookup:
		portable = g.lookupName()
	case types2.LogosCtMapMaterialize:
		portable = g.mapName()
	case types2.LogosCtMapIterate:
		portable = g.allName()
	default:
		panic("invalid imported const map helper role")
	}
	id := nameOf(pos, portable)
	if g.owner == nil {
		return id, portable
	}
	if fl.constMapMarks == nil {
		fl.constMapMarks = make(map[string]constMapTextRef)
	}
	ref := types2.LogosCtConstMapRef{Path: g.owner.Pkg().Path(), Name: g.owner.Name(), Role: role}
	mark := fmt.Sprintf("\x00constmap:%d\x00", len(fl.constMapMarks))
	fl.constMapMarks[mark] = constMapTextRef{ref: ref, portable: portable}
	fl.registerConstMapUse(id, ref)
	return id, mark
}

func (fl *fileLower) registerConstMapUse(id *syntax.Name, ref types2.LogosCtConstMapRef) {
	info := fl.pkg.info
	if info.LogosCtConstMapUses == nil {
		info.LogosCtConstMapUses = make(map[*syntax.Name]types2.LogosCtConstMapRef)
	}
	info.LogosCtConstMapUses[id] = ref
}

func (fl *fileLower) expandConstMapFragment(src string) (string, []constMapFragmentMark) {
	if len(fl.constMapMarks) == 0 || !strings.ContainsRune(src, 0) {
		return src, nil
	}
	var marks []constMapFragmentMark
	var out strings.Builder
	copied, scan := 0, 0
	for scan < len(src) {
		open := strings.IndexByte(src[scan:], 0)
		if open < 0 {
			break
		}
		open += scan
		close := strings.IndexByte(src[open+1:], 0)
		if close < 0 {
			break
		}
		close += open + 1
		scan = close + 1
		if ref, ok := fl.constMapMarks[src[open:scan]]; ok {
			out.WriteString(src[copied:open])
			marks = append(marks, constMapFragmentMark{offset: out.Len(), text: ref})
			out.WriteString(ref.portable)
			copied = scan
		}
	}
	if len(marks) == 0 {
		return src, nil
	}
	out.WriteString(src[copied:])
	return out.String(), marks
}

func (fl *fileLower) bindConstMapFragment(root syntax.Node, src string, marks []constMapFragmentMark) {
	if len(marks) == 0 {
		return
	}
	offsets := make(map[int]constMapTextRef, len(marks))
	for _, mark := range marks {
		offsets[mark.offset] = mark.text
	}
	starts := lineStarts([]byte(src))
	syntax.Inspect(root, func(n syntax.Node) bool {
		id, ok := n.(*syntax.Name)
		if !ok || len(offsets) == 0 {
			return len(offsets) != 0
		}
		pos := id.Pos()
		if pos.Line() == 0 || pos.Col() == 0 || int(pos.Line()) > len(starts) {
			return true
		}
		off := starts[pos.Line()-1] + int(pos.Col()) - 1
		if text, ok := offsets[off]; ok {
			if id.Value != text.portable {
				base.Fatalf("logos: malformed generated const map helper at %s", pos.String())
			}
			fl.registerConstMapUse(id, text.ref)
			delete(offsets, off)
		}
		return true
	})
	if len(offsets) != 0 {
		base.Fatalf("logos: reparsed fragment lost a generated const map helper reference")
	}
}

func (fl *fileLower) finishConstMapEjectText() {
	if len(fl.constMapMarks) == 0 {
		return
	}
	lookup := func(mark string) (otext, bool) {
		if ref, ok := fl.constMapMarks[mark]; ok {
			return gen(ref.portable), true
		}
		return otext{}, false
	}
	for i := range fl.splices {
		sp := &fl.splices[i]
		text := substNul(otext{s: sp.text, chunks: sp.chunks}, lookup)
		sp.text, sp.chunks = text.s, text.chunks
	}
}

func (w ctWalker) ctConstMapExpr(e syntax.Expr) (syntax.Expr, []syntax.Stmt, bool) {
	switch n := e.(type) {
	case *syntax.IndexExpr:
		if !w.fl.isMapRead(n) {
			break
		}
		g, ok := w.ctMapUse(n.X)
		if !ok {
			break
		}
		m := w.ctOptionMap(n.X)
		if m == nil {
			w.reject(n.X, "missing checked const map type")
			return e, nil, true
		}
		if !g.valid() {
			return e, nil, true
		}
		key, pre := w.optionOperand(n.Index, m.Key())
		pos := n.Pos()
		lookup := callOf(pos, w.ctMapHelper(pos, g, types2.LogosCtMapLookup), []syntax.Expr{key})
		t := w.fl.optType(n)
		if opt := w.fl.asOption(t); opt != nil && !types2.Identical(t, m.Elem()) {
			wrap := "Opt"
			if types2.LogosOptionIsBare(opt.Elem) {
				wrap = "Bare"
			}
			return callOf(pos, w.fl.ctPkgSelector(pos, rtImportPath, wrap), []syntax.Expr{lookup}), pre, true
		}
		return lookup, pre, true
	case *syntax.CallExpr:
		if len(n.ArgList) != 1 {
			break
		}
		fn, _ := syntax.Unparen(n.Fun).(*syntax.Name)
		if w.fl.pkg.info.Uses[fn] != types2.Universe.Lookup("len") {
			break
		}
		if w.ctOptionMap(n.ArgList[0]) != nil {
			if val, ok := typeConst(w.fl.pkg.info, n); ok {
				return litOf(n.Pos(), val.ExactString(), syntax.IntLit), nil, true
			}
		}
		if g, ok := w.ctMapUse(n.ArgList[0]); ok {
			if count, ok := w.ctMapCount(g); ok {
				return ctConstNumber(n.Pos(), uint64(count)), nil, true
			}
			w.reject(e, "missing checked const map count")
			return e, nil, true
		}
	case *syntax.Name, *syntax.SelectorExpr:
		if g, ok := w.ctMapUse(e); ok {
			if !g.valid() {
				return e, nil, true
			}
			pos := e.Pos()
			var value syntax.Expr = callOf(pos, w.ctMapHelper(pos, g, types2.LogosCtMapMaterialize), nil)
			t := constMapElem(w.fl.pkg.info.TypeOf(e))
			if _, named := types2.Unalias(t).(*types2.Named); named {
				value = callOf(pos, w.fl.ctTypeExpr(pos, t), []syntax.Expr{value})
			}
			return value, nil, true
		}
	}
	return nil, nil, false
}

func (w ctWalker) ctConstMapRange(s *syntax.ForStmt) ([]syntax.Stmt, bool) {
	rc, ok := s.Init.(*syntax.RangeClause)
	if !ok {
		return nil, false
	}
	pos := rc.X.Pos()
	b := ctMapSyntax{w, pos}
	if g, ok := w.ctMapUse(rc.X); ok {
		switch {
		case !g.valid():
		case g.owner != nil || g.local.bearing:
			rc.X = w.ctMapHelper(pos, g, types2.LogosCtMapIterate)
		default:
			rc.X = callOf(pos, b.rt("ConstAll"), []syntax.Expr{b.name(g.local.sortedName())})
		}
		return nil, true
	}
	mt, ok := w.fl.rangedConstMap(rc.X)
	if !ok {
		return nil, false
	}
	x, pre := w.expr(rc.X)
	entry := "SortedAll"
	args := []syntax.Expr{x}
	if b.dynamicOrder(mt.Key()) {
		entry = "SortedAllDynamic"
	} else {
		compare := b.compare(mt.Key())
		if compare == nil {
			compare = w.fl.ctBuiltin(pos, "nil")
		}
		args = append(args, compare)
	}
	rc.X = callOf(pos, b.rt(entry), args)
	return pre, true
}

type ctMapAccess struct {
	local *constMapGen
	owner types2.Object
}

func (g ctMapAccess) valid() bool { return g.local != nil || g.owner != nil }

func (w ctWalker) ctMapHelper(pos syntax.Pos, g ctMapAccess, role types2.LogosCtConstMapRole) *syntax.Name {
	if g.owner != nil {
		id := nameOf(pos, "_logos_ct_map")
		if w.fl.ct.constMapUses == nil {
			w.fl.ct.constMapUses = make(map[*syntax.Name]types2.LogosCtConstMapRef)
		}
		w.fl.ct.constMapUses[id] = types2.LogosCtConstMapRef{Path: g.owner.Pkg().Path(), Name: g.owner.Name(), Role: role}
		return id
	}
	switch role {
	case types2.LogosCtMapLookup:
		return nameOf(pos, g.local.lookupName())
	case types2.LogosCtMapMaterialize:
		return nameOf(pos, g.local.mapName())
	case types2.LogosCtMapIterate:
		return nameOf(pos, g.local.allName())
	}
	panic("invalid comptime const map helper role")
}

func (ct *ctLowerContext) bindConstMapUses(conf *types2.Config) error {
	for id, ref := range ct.constMapUses {
		var helper *types2.Func
		if conf.LogosCtImportConstMap != nil {
			if row, ok := conf.LogosCtImportConstMap(ref.Path, ref.Name); ok {
				helper = row.Helper(ref.Role)
			}
		}
		if helper == nil {
			return fmt.Errorf("%s: missing owner const map helper for %s.%s", id.Pos(), ref.Path, ref.Name)
		}
		ct.bindUse(id, helper)
	}
	return nil
}

func (w ctWalker) ctMapCount(g ctMapAccess) (int, bool) {
	if g.local != nil {
		return g.local.nkeys, true
	}
	info := w.fl.pkg.info
	if obj, ok := g.owner.(*types2.Const); ok {
		if val, ok := info.LogosCTFE[obj]; ok {
			keys, _ := val.MapEntries()
			return len(keys), true
		}
	}
	if g.owner != nil && info.LogosCtImportConstMap != nil {
		row, ok := info.LogosCtImportConstMap(g.owner.Pkg().Path(), g.owner.Name())
		return row.Count, ok
	}
	return 0, false
}

func (w ctWalker) ctMapUse(e syntax.Expr) (ctMapAccess, bool) {
	info := w.fl.pkg.info
	var obj types2.Object
	switch e := syntax.Unparen(e).(type) {
	case *syntax.Name:
		obj = info.Uses[e]
	case *syntax.SelectorExpr:
		if info.Selections[e] == nil {
			obj = info.Uses[e.Sel]
		}
	}
	switch obj := obj.(type) {
	case *types2.Const:
		if _, isMap := compositeUnderlying(obj.Type()).(*types2.Map); !isMap {
			break
		}
		if obj.Pkg() != nil && obj.Parent() == obj.Pkg().Scope() {
			return ctMapAccess{owner: obj}, true
		}
		if v, ok := info.LogosCTFE[obj]; ok && compositeIsBare(v, obj.Type()) {
			return ctMapAccess{local: w.ctMapConstant(obj, v, e.Pos())}, true
		}
	case *types2.Var:
		view := constAsView(obj.Type())
		if obj.Pkg() == nil || obj.IsField() || view == nil || !view.ConstMap {
			break
		}
		if obj.Parent() == obj.Pkg().Scope() {
			return ctMapAccess{owner: obj}, true
		}
		if info.LogosCtImportConstMap != nil {
			if row, ok := info.LogosCtImportConstMap(obj.Pkg().Path(), obj.Name()); ok && row.Binding == obj {
				return ctMapAccess{owner: obj}, true
			}
		}
	}
	return ctMapAccess{}, false
}

func (w ctWalker) ctMapConstant(obj *types2.Const, v ctfe.Value, pos syntax.Pos) *constMapGen {
	if g := w.fl.ct.constMaps[obj]; g != nil {
		return g
	}
	mapT := constMapElem(obj.Type())
	mt, ok := compositeUnderlying(mapT).(*types2.Map)
	if !ok {
		if w.fl.ct.err == nil {
			w.fl.ct.err = fmt.Errorf("%s: expected checked const map type for %s", pos, obj.Name())
		}
		return nil
	}
	keys, vals := v.MapEntries()
	g := &constMapGen{name: w.ctConstName(), nkeys: len(keys), bearing: constMapBearing(vals)}
	m := ctMapBuilder{
		ctMapSyntax: ctMapSyntax{w, pos}, constMapGen: g, mapT: mapT, mt: mt,
		value: v, keys: keys, vals: vals,
	}
	decls, err := m.declarations()
	if err != nil {
		if w.fl.ct.err == nil {
			w.fl.ct.err = fmt.Errorf("%s: const map %s: %w", pos, obj.Name(), err)
		}
		return nil
	}
	if w.fl.ct.constMaps == nil {
		w.fl.ct.constMaps = make(map[types2.Object]*constMapGen)
	}
	w.fl.ct.constMaps[obj] = g
	w.fl.constDecls = append(w.fl.constDecls, decls...)
	w.fl.markConstMapTables(decls, g)
	return g
}

type ctMapSyntax struct {
	w   ctWalker
	pos syntax.Pos
}

func (b ctMapSyntax) name(s string) *syntax.Name { return nameOf(b.pos, s) }

func (b ctMapSyntax) rt(s string) *syntax.SelectorExpr {
	return b.w.fl.ctPkgSelector(b.pos, rtImportPath, s)
}

func (b ctMapSyntax) typ(t types2.Type) syntax.Expr { return b.w.fl.ctTypeExpr(b.pos, t) }

func (b ctMapSyntax) number(n uint64) syntax.Expr { return ctConstNumber(b.pos, n) }

func (b ctMapSyntax) field(name string, typ syntax.Expr) *syntax.Field {
	f := at(&syntax.Field{Type: typ}, b.pos)
	if name != "" {
		f.Name = b.name(name)
	}
	return f
}

func (b ctMapSyntax) generic(fn syntax.Expr, args ...syntax.Expr) syntax.Expr {
	return at(&syntax.IndexExpr{X: fn, Index: listOf(b.pos, args)}, b.pos)
}

func (b ctMapSyntax) literal(typ syntax.Expr, elems ...syntax.Expr) *syntax.CompositeLit {
	return at(&syntax.CompositeLit{Type: typ, ElemList: elems, Rbrace: b.pos}, b.pos)
}

func (b ctMapSyntax) pair(key, val syntax.Expr) *syntax.KeyValueExpr {
	return at(&syntax.KeyValueExpr{Key: key, Value: val}, b.pos)
}

func (b ctMapSyntax) result(exprs ...syntax.Expr) *syntax.ReturnStmt { return returnOf(b.pos, exprs) }

func (b ctMapSyntax) do(fn syntax.Expr, args ...syntax.Expr) *syntax.CallExpr {
	return callOf(b.pos, fn, args)
}

func (b ctMapSyntax) fnType(params, results []*syntax.Field) *syntax.FuncType {
	return at(&syntax.FuncType{ParamList: params, ResultList: results}, b.pos)
}

func (b ctMapSyntax) switchOn(tag syntax.Expr, cases []*syntax.CaseClause) *syntax.SwitchStmt {
	return at(&syntax.SwitchStmt{Tag: tag, Body: cases, Rbrace: b.pos}, b.pos)
}

func (b ctMapSyntax) choice(value syntax.Expr, body ...syntax.Stmt) *syntax.CaseClause {
	return at(&syntax.CaseClause{Cases: value, Body: body, Colon: b.pos}, b.pos)
}

func (b ctMapSyntax) variable(name string, typ, value syntax.Expr) *syntax.VarDecl {
	return at(&syntax.VarDecl{NameList: []*syntax.Name{b.name(name)}, Type: typ, Values: value}, b.pos)
}

func (b ctMapSyntax) decl(name string, ft *syntax.FuncType, body ...syntax.Stmt) syntax.Decl {
	return at(&syntax.FuncDecl{Name: b.name(name), Type: ft, Body: blockOf(b.pos, body)}, b.pos)
}

func (b ctMapSyntax) local(v *syntax.VarDecl) *syntax.DeclStmt {
	return at(&syntax.DeclStmt{DeclList: []syntax.Decl{v}}, b.pos)
}

type ctMapBuilder struct {
	ctMapSyntax
	*constMapGen
	mapT  types2.Type
	mt    *types2.Map
	value ctfe.Value
	keys  []ctfe.Value
	vals  []ctfe.Value
}

func (m ctMapBuilder) name(s string) *syntax.Name { return m.ctMapSyntax.name(s) }

func (m ctMapBuilder) declarations() ([]syntax.Decl, error) {
	var lookup syntax.Decl
	var table syntax.Decl
	var err error
	if m.nkeys <= constMapSwitchMax {
		lookup, err = m.switchLookup()
	} else {
		table, lookup, err = m.tableLookup()
	}
	if err != nil {
		return nil, err
	}
	sorted, err := m.sorted()
	if err != nil {
		return nil, err
	}
	build, err := m.materialize()
	if err != nil {
		return nil, err
	}
	decls := append(build, lookup, sorted)
	if table != nil {
		decls = append(decls, table)
	}
	if m.bearing {
		val, err := m.values()
		if err != nil {
			return nil, err
		}
		decls = append(decls, val, m.all())
	}
	return decls, nil
}

func (m ctMapBuilder) materialize() ([]syntax.Decl, error) {
	value, err := m.w.ctConstValue(m.value, m.mapT, m.pos)
	if err != nil {
		return nil, err
	}
	memo := m.generic(m.rt("ConstMapMaterialization"), m.typ(m.mapT), m.typ(m.mt.Key()), m.typ(m.mt.Elem()))
	mapResult := func() *syntax.FuncType { return m.fnType(nil, []*syntax.Field{m.field("", m.typ(m.mapT))}) }
	get := m.do(selOf(m.pos, m.name(m.matName()), "Get"), m.name(m.buildName()))
	return []syntax.Decl{
		m.variable(m.matName(), memo, nil),
		m.decl(m.buildName(), mapResult(), m.result(value)),
		m.decl(m.mapName(), mapResult(), m.result(get)),
	}, nil
}

func (m ctMapBuilder) valueSlotType() syntax.Expr {
	if m.bearing {
		return m.w.fl.ctBuiltin(m.pos, "uint32")
	}
	return m.typ(m.mt.Elem())
}

func (m ctMapBuilder) valueSlot(i int) (syntax.Expr, error) {
	if m.bearing {
		return m.number(uint64(i)), nil
	}
	return m.w.ctConstValue(m.vals[i], m.mt.Elem(), m.pos)
}

func (m ctMapBuilder) entry(i int, fields ...syntax.Expr) (syntax.Expr, error) {
	key, err := m.w.ctConstValue(m.keys[i], m.mt.Key(), m.pos)
	if err != nil {
		return nil, err
	}
	val, err := m.valueSlot(i)
	if err != nil {
		return nil, err
	}
	elems := make([]syntax.Expr, 0, 2+len(fields))
	elems = append(elems, m.pair(m.name("Key"), key), m.pair(m.name("Val"), val))
	elems = append(elems, fields...)
	lit := m.literal(nil, elems...)
	lit.NKeys = len(elems)
	return lit, nil
}

func (m ctMapBuilder) sorted() (syntax.Decl, error) {
	order := make([]int, m.nkeys)
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return ctfe.CompareKeys(m.keys[a], m.keys[b]) })
	elems := make([]syntax.Expr, m.nkeys)
	for j, i := range order {
		e, err := m.entry(i)
		if err != nil {
			return nil, err
		}
		elems[j] = e
	}
	entry := m.generic(m.rt("ConstEntry"), m.typ(m.mt.Key()), m.valueSlotType())
	return m.variable(m.sortedName(), nil, m.literal(at(&syntax.SliceType{Elem: entry}, m.pos), elems...)), nil
}

func (m ctMapBuilder) lookupType() *syntax.FuncType {
	params := []*syntax.Field{m.field("_logos_key", m.typ(m.mt.Key()))}
	results := []*syntax.Field{m.field("", m.typ(m.mt.Elem())), m.field("", m.w.fl.ctBuiltin(m.pos, "bool"))}
	return m.fnType(params, results)
}

func (m ctMapBuilder) switchLookup() (syntax.Decl, error) {
	cases := make([]*syntax.CaseClause, m.nkeys)
	for i := range cases {
		key, err := m.w.ctConstValue(m.keys[i], m.mt.Key(), m.pos)
		if err != nil {
			return nil, err
		}
		val, err := m.valueSlot(i)
		if err != nil {
			return nil, err
		}
		if m.bearing {
			val = m.do(m.name(m.valName()), val)
		}
		cases[i] = m.choice(key, m.result(val, m.w.fl.ctBuiltin(m.pos, "true")))
	}
	return m.decl(m.lookupName(), m.lookupType(),
		m.switchOn(m.name("_logos_key"), cases),
		m.local(m.variable("_logos_zero", m.typ(m.mt.Elem()), nil)),
		m.result(m.name("_logos_zero"), m.w.fl.ctBuiltin(m.pos, "false"))), nil
}

func (m ctMapBuilder) tableLookup() (syntax.Decl, syntax.Decl, error) {
	entries := make([]constMapKey, m.nkeys)
	for i, key := range m.keys {
		entries[i] = constMapKey{hash: constKeyHash(key), canon: constKeyCanon(key)}
	}
	layout, err := buildConstMap(entries, constMapSeedBudget(m.pos))
	if err != nil {
		return nil, nil, err
	}
	buckets := make([]syntax.Expr, len(layout.buckets))
	for i, bk := range layout.buckets {
		e := m.literal(nil, m.pair(m.name("Off"), m.number(uint64(bk.off))),
			m.pair(m.name("Size"), m.number(uint64(bk.size))), m.pair(m.name("Seed"), m.number(bk.seed)))
		e.NKeys = 3
		buckets[i] = e
	}
	slots := make([]syntax.Expr, len(layout.slots))
	for i, k := range layout.slots {
		if k < 0 {
			slots[i] = m.literal(nil)
			continue
		}
		occ := m.pair(m.name("Occ"), m.w.fl.ctBuiltin(m.pos, "true"))
		slots[i], err = m.entry(k, occ)
		if err != nil {
			return nil, nil, err
		}
	}
	kt := m.mt.Key()
	bt := at(&syntax.SliceType{Elem: m.rt("ConstBucket")}, m.pos)
	st := at(&syntax.SliceType{Elem: m.generic(m.rt("ConstSlot"), m.typ(kt), m.valueSlotType())}, m.pos)
	tt := m.generic(m.rt("ConstTable"), m.typ(kt), m.valueSlotType())
	lit := m.literal(tt, m.pair(m.name("Salt"), m.number(layout.salt)),
		m.pair(m.name("Buckets"), m.literal(bt, buckets...)), m.pair(m.name("Slots"), m.literal(st, slots...)))
	lit.NKeys = 3
	table := m.variable(m.tabName(), nil, lit)
	hash := m.hashKey(kt, m.name("_logos_key"))
	lookup := m.do(selOf(m.pos, m.name(m.tabName()), "Get"), hash, m.name("_logos_key"))
	ft := m.lookupType()
	if !m.bearing {
		return table, m.decl(m.lookupName(), ft, m.result(lookup)), nil
	}
	init := defineOf(m.pos, []syntax.Expr{m.name("_logos_idx"), m.name("_logos_ok")}, []syntax.Expr{lookup})
	hit := ifOf(m.pos, init, m.name("_logos_ok"), []syntax.Stmt{
		m.result(m.do(m.name(m.valName()), m.name("_logos_idx")), m.w.fl.ctBuiltin(m.pos, "true")),
	})
	return table, m.decl(m.lookupName(), ft, hit,
		m.local(m.variable("_logos_zero", m.typ(m.mt.Elem()), nil)),
		m.result(m.name("_logos_zero"), m.w.fl.ctBuiltin(m.pos, "false"))), nil
}

func (m ctMapBuilder) values() (syntax.Decl, error) {
	cases := make([]*syntax.CaseClause, m.nkeys)
	for i := range cases {
		val, err := m.w.ctConstValue(m.vals[i], m.mt.Elem(), m.pos)
		if err != nil {
			return nil, err
		}
		cases[i] = m.choice(m.number(uint64(i)), m.result(val))
	}
	params := []*syntax.Field{m.field("_logos_idx", m.w.fl.ctBuiltin(m.pos, "uint32"))}
	results := []*syntax.Field{m.field("", m.typ(m.mt.Elem()))}
	return m.decl(m.valName(), m.fnType(params, results), m.switchOn(m.name("_logos_idx"), cases),
		m.local(m.variable("_logos_zero", m.typ(m.mt.Elem()), nil)), m.result(m.name("_logos_zero"))), nil
}

func (m ctMapBuilder) all() syntax.Decl {
	yield := m.fnType([]*syntax.Field{m.field("", m.typ(m.mt.Key())), m.field("", m.typ(m.mt.Elem()))},
		[]*syntax.Field{m.field("", m.w.fl.ctBuiltin(m.pos, "bool"))})
	ft := m.fnType([]*syntax.Field{m.field("_logos_yield", yield)}, nil)
	key := selOf(m.pos, m.name("_logos_e"), "Key")
	val := m.do(m.name(m.valName()), selOf(m.pos, m.name("_logos_e"), "Val"))
	keep := m.do(m.name("_logos_yield"), key, val)
	stop := ifOf(m.pos, nil, at(&syntax.Operation{Op: syntax.Not, X: keep}, m.pos), []syntax.Stmt{m.result()})
	rc := at(&syntax.RangeClause{
		Lhs: listOf(m.pos, []syntax.Expr{m.name("_"), m.name("_logos_e")}),
		Def: true,
		X:   m.name(m.sortedName()),
	}, m.pos)
	loop := at(&syntax.ForStmt{Init: rc, Body: blockOf(m.pos, []syntax.Stmt{stop})}, m.pos)
	return m.decl(m.allName(), ft, loop)
}

func (b ctMapSyntax) selector(x syntax.Expr, field *types2.Var) syntax.Expr {
	s := selOf(b.pos, x, field.Name())
	if !field.Exported() {
		b.w.fl.ct.bindOwnedSelector(s)
	}
	return s
}

func (b ctMapSyntax) dynamicOrder(t types2.Type) bool {
	if b.w.fl.asOption(t) != nil {
		return true
	}
	switch u := compositeUnderlying(t).(type) {
	case *types2.Interface:
		return true
	case *types2.Struct:
		for i := range u.NumFields() {
			f := u.Field(i)
			if f.Name() != "_" && b.dynamicMemberOrder(f.Type()) {
				return true
			}
		}
	case *types2.Array:
		return b.dynamicMemberOrder(u.Elem())
	}
	return false
}

func (b ctMapSyntax) dynamicMemberOrder(t types2.Type) bool {
	if _, ok := compositeUnderlying(t).(*types2.Interface); ok {
		return false
	}
	return b.dynamicOrder(t)
}

func (b ctMapSyntax) compare(t types2.Type) syntax.Expr {
	switch u := compositeUnderlying(t).(type) {
	case *types2.Basic:
		switch basicSortClass(u) {
		case sortOrdered:
			return b.generic(b.rt("CompareOrdered"), b.typ(t))
		case sortBool:
			return b.generic(b.rt("CompareBool"), b.typ(t))
		case sortComplex:
			return b.generic(b.rt("CompareComplex"), b.typ(t))
		}
	case *types2.Struct:
		var body []syntax.Stmt
		for i := range u.NumFields() {
			f := u.Field(i)
			if f.Name() == "_" {
				continue
			}
			if cmp := b.compare(f.Type()); cmp != nil {
				body = append(body, b.compareStep(cmp, b.selector(b.name(sortOrderLeft), f), b.selector(b.name(sortOrderRight), f)))
			}
		}
		if len(body) != 0 {
			return b.compareFunc(t, body)
		}
	case *types2.Array:
		if cmp := b.compare(u.Elem()); cmp != nil {
			index := func(name string) syntax.Expr {
				return at(&syntax.IndexExpr{X: b.name(name), Index: b.name(sortOrderIndex)}, b.pos)
			}
			body := []syntax.Stmt{b.compareStep(cmp, index(sortOrderLeft), index(sortOrderRight))}
			rng := at(&syntax.RangeClause{Lhs: b.name(sortOrderIndex), Def: true, X: b.name(sortOrderLeft)}, b.pos)
			return b.compareFunc(t, []syntax.Stmt{at(&syntax.ForStmt{Init: rng, Body: blockOf(b.pos, body)}, b.pos)})
		}
	}
	return nil
}

func (b ctMapSyntax) compareStep(cmp, left, right syntax.Expr) syntax.Stmt {
	init := defineOf(b.pos, []syntax.Expr{b.name(sortOrderDiff)}, []syntax.Expr{b.do(cmp, left, right)})
	ne := at(&syntax.Operation{Op: syntax.Neq, X: b.name(sortOrderDiff), Y: b.number(0)}, b.pos)
	return ifOf(b.pos, init, ne, []syntax.Stmt{b.result(b.name(sortOrderDiff))})
}

func (b ctMapSyntax) compareFunc(t types2.Type, body []syntax.Stmt) syntax.Expr {
	params := []*syntax.Field{b.field(sortOrderLeft, b.typ(t)), b.field(sortOrderRight, b.typ(t))}
	results := []*syntax.Field{b.field("", b.w.fl.ctBuiltin(b.pos, "int"))}
	return at(&syntax.FuncLit{Type: b.fnType(params, results), Body: blockOf(b.pos, append(body, b.result(b.number(0))))}, b.pos)
}

func (b ctMapSyntax) hashKey(t types2.Type, x syntax.Expr) syntax.Expr {
	switch u := compositeUnderlying(t).(type) {
	case *types2.Basic:
		convert := func(name string, arg syntax.Expr) syntax.Expr {
			return b.do(b.w.fl.ctBuiltin(b.pos, name), arg)
		}
		switch info := u.Info(); {
		case info&types2.IsString != 0:
			return b.do(b.rt("HashString"), convert("string", x))
		case info&types2.IsBoolean != 0:
			return b.do(b.rt("HashBool"), convert("bool", x))
		case info&types2.IsInteger != 0:
			return b.do(b.rt("HashUint64"), convert("uint64", x))
		case info&types2.IsFloat != 0:
			return b.do(b.rt("HashFloat64"), convert("float64", x))
		case info&types2.IsComplex != 0:
			realPart := b.do(b.rt("HashFloat64"), convert("float64", convert("real", b.name(constMapHashRecv))))
			imagPart := b.do(b.rt("HashFloat64"), convert("float64", convert("imag", b.name(constMapHashRecv))))
			ft := b.hashType(t)
			combine := b.do(b.rt("HashCombine"), realPart, imagPart)
			return b.do(at(&syntax.FuncLit{Type: ft, Body: blockOf(b.pos, []syntax.Stmt{b.result(combine)})}, b.pos), x)
		}
	case *types2.Struct:
		body := make([]syntax.Stmt, 0, u.NumFields()+2)
		for i := range u.NumFields() {
			f := u.Field(i)
			if f.Name() != "_" {
				body = append(body, b.hashFold(f.Type(), b.selector(b.name(constMapHashRecv), f)))
			}
		}
		return b.hashAggregate(t, x, body)
	case *types2.Array:
		rng := at(&syntax.RangeClause{
			Lhs: listOf(b.pos, []syntax.Expr{b.name("_"), b.name("_logos_e")}),
			Def: true,
			X:   b.name(constMapHashRecv),
		}, b.pos)
		body := blockOf(b.pos, []syntax.Stmt{b.hashFold(u.Elem(), b.name("_logos_e"))})
		return b.hashAggregate(t, x, []syntax.Stmt{at(&syntax.ForStmt{Init: rng, Body: body}, b.pos)})
	}
	b.w.reject(x, fmt.Sprintf("cannot hash checked const map key type %s", t))
	return x
}

func (b ctMapSyntax) hashType(t types2.Type) *syntax.FuncType {
	params := []*syntax.Field{b.field(constMapHashRecv, b.typ(t))}
	results := []*syntax.Field{b.field("", b.w.fl.ctBuiltin(b.pos, "uint64"))}
	return b.fnType(params, results)
}

func (b ctMapSyntax) hashFold(t types2.Type, value syntax.Expr) syntax.Stmt {
	combine := b.do(b.rt("HashCombine"), b.name(constMapHashAcc), b.hashKey(t, value))
	return assignOf(b.pos, []syntax.Expr{b.name(constMapHashAcc)}, []syntax.Expr{combine})
}

func (b ctMapSyntax) hashAggregate(t types2.Type, x syntax.Expr, steps []syntax.Stmt) syntax.Expr {
	acc := b.local(b.variable(constMapHashAcc, b.w.fl.ctBuiltin(b.pos, "uint64"), nil))
	body := []syntax.Stmt{acc}
	body = append(body, steps...)
	body = append(body, b.result(b.name(constMapHashAcc)))
	lit := at(&syntax.FuncLit{Type: b.hashType(t), Body: blockOf(b.pos, body)}, b.pos)
	return b.do(lit, x)
}

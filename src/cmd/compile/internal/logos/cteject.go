// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"bytes"
	"cmp"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
	"cmd/internal/logosowned"
)

// ctEjectInfix joins a comptime generic's name and the suffix that tells
// its instances apart in ejected source (see ctEjectNames).
const ctEjectInfix = "_logosct"

var ctEjectBlanked map[*syntax.File]map[string]bool

// ctEjectNames names every comptime instance of the compiled package for
// logos eject. An instance printed in its defining package (owners) is
// <origin>_logosct_<h>, h the first 12 hex digits of its instance ID, so
// every package that needs it spells the same name. Any other instance is
// <origin>_logosct<N>, where N counts the instances of one origin name that
// the package prints, in package path and instance id order.
func ctEjectNames(origins map[string]*types2.Func, owners map[string]*types2.Package) map[string]string {
	ids := slices.Collect(maps.Keys(origins))
	slices.SortFunc(ids, func(a, b string) int {
		oa, ob := origins[a], origins[b]
		return cmp.Or(cmp.Compare(oa.Name(), ob.Name()), cmp.Compare(oa.Pkg().Path(), ob.Pkg().Path()), cmp.Compare(a, b))
	})
	names := make(map[string]string, len(ids))
	seq := make(map[string]int)
	for _, id := range ids {
		origin := origins[id].Name()
		if owners[id] != nil {
			names[id] = origin + ctEjectInfix + "_" + id[:min(len(id), 12)]
			continue
		}
		names[id] = origin + ctEjectInfix + strconv.Itoa(seq[origin])
		seq[origin]++
	}
	return names
}

func (p *packageLower) ctEjectNames() map[string]string {
	if p.ejectNames == nil {
		recs := ctEjectRecords(p.info)
		p.ejectOwners = ctEjectOwners(recs)
		p.ejectNames = ctEjectNames(ctEjectOrigins(recs), p.ejectOwners)
	}
	return p.ejectNames
}

// ctEjectRecords returns the comptime instances of the package by instance
// ID. The checked record wins over the prepared one, so the call-site
// renames, made before preparation, and the printed instances decide names
// and owners from the same records.
func ctEjectRecords(info *types2.Info) map[string]*types2.LogosCtInst {
	recs := make(map[string]*types2.LogosCtInst, len(info.LogosCtInst)+len(info.LogosCtPrepared))
	for _, rec := range info.LogosCtPrepared {
		recs[rec.ID] = rec
	}
	for _, rec := range info.LogosCtInst {
		recs[rec.ID] = rec
	}
	return recs
}

func ctEjectOrigins(recs map[string]*types2.LogosCtInst) map[string]*types2.Func {
	origins := make(map[string]*types2.Func, len(recs))
	for id, rec := range recs {
		origins[id] = rec.Origin
	}
	return origins
}

// ctEjectOwners returns, by instance ID, the defining package of every
// instance that logos eject prints once, in that package: the package can
// be imported, lies outside the standard library, and can name every type
// argument, and every instance the instance calls is printed the same way.
// The rule reads only the instance and the defining package's imports, so
// every package that needs the instance decides it the same way.
func ctEjectOwners(recs map[string]*types2.LogosCtInst) map[string]*types2.Package {
	owners := make(map[string]*types2.Package)
	for id, rec := range recs {
		if ctEjectOwnable(rec) {
			owners[id] = rec.Origin.Pkg()
		}
	}
	for changed := true; changed; {
		changed = false
		for id := range owners {
			for _, child := range ctEjectChildren(recs[id], recs) {
				if owners[child] == nil {
					delete(owners, id)
					changed = true
					break
				}
			}
		}
	}
	return owners
}

// ctEjectOwnable reports whether the defining package of rec can hold its
// ejected text. Package main and external test packages cannot be
// imported, and a standard-library package (an import path whose first
// element has no dot) is not written by logos eject.
func ctEjectOwnable(rec *types2.LogosCtInst) bool {
	owner := rec.Origin.Pkg()
	if owner == nil || owner.Name() == "main" || strings.HasSuffix(owner.Path(), "_test") {
		return false
	}
	if first, _, _ := strings.Cut(owner.Path(), "/"); !strings.Contains(first, ".") {
		return false
	}
	imported := map[string]bool{owner.Path(): true}
	for _, imp := range owner.Imports() {
		imported[imp.Path()] = true
	}
	for _, targ := range rec.Targs {
		if !ctEjectNameable(targ, owner, imported) {
			return false
		}
	}
	return true
}

// ctEjectNameable reports whether package owner can spell t: each named
// type in t is a package-level type of owner or an exported type of a
// package owner imports, and each field or method of a type literal in t
// is exported or belongs to owner.
func ctEjectNameable(t types2.Type, owner *types2.Package, imported map[string]bool) bool {
	member := func(obj types2.Object) bool {
		return obj.Exported() || obj.Pkg() != nil && obj.Pkg().Path() == owner.Path()
	}
	typeName := func(obj *types2.TypeName, targs *types2.TypeList) bool {
		if pkg := obj.Pkg(); pkg != nil {
			if pkg.Scope().Lookup(obj.Name()) != obj || pkg.Path() != owner.Path() && (!obj.Exported() || !imported[pkg.Path()]) {
				return false
			}
		}
		for i := range targs.Len() {
			if !ctEjectNameable(targs.At(i), owner, imported) {
				return false
			}
		}
		return true
	}
	vars := func(tuple *types2.Tuple) bool {
		for i := range tuple.Len() {
			if !ctEjectNameable(tuple.At(i).Type(), owner, imported) {
				return false
			}
		}
		return true
	}
	switch t := t.(type) {
	case *types2.Basic:
		return true
	case *types2.Named:
		return typeName(t.Obj(), t.TypeArgs())
	case *types2.Alias:
		return typeName(t.Obj(), t.TypeArgs())
	case *types2.Pointer:
		return ctEjectNameable(t.Elem(), owner, imported)
	case *types2.Slice:
		return ctEjectNameable(t.Elem(), owner, imported)
	case *types2.Array:
		return ctEjectNameable(t.Elem(), owner, imported)
	case *types2.Chan:
		return ctEjectNameable(t.Elem(), owner, imported)
	case *types2.Map:
		return ctEjectNameable(t.Key(), owner, imported) && ctEjectNameable(t.Elem(), owner, imported)
	case *types2.Signature:
		return vars(t.Params()) && vars(t.Results())
	case *types2.Struct:
		for i := range t.NumFields() {
			if f := t.Field(i); !member(f) || !ctEjectNameable(f.Type(), owner, imported) {
				return false
			}
		}
		return true
	case *types2.Interface:
		for i := range t.NumExplicitMethods() {
			if m := t.ExplicitMethod(i); !member(m) || !ctEjectNameable(m.Type(), owner, imported) {
				return false
			}
		}
		for i := range t.NumEmbeddeds() {
			if !ctEjectNameable(t.EmbeddedType(i), owner, imported) {
				return false
			}
		}
		return true
	}
	return false
}

// ejectCtSites renames, in the ejected text only, every call site of a
// comptime instance in the file to the instance's ejected name. The
// syntax tree keeps the site, so the native build is unchanged.
func (fl *fileLower) ejectCtSites() {
	if len(fl.pkg.info.LogosCtSiteKeys) == 0 {
		return
	}
	parents := cfParents(fl.file)
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		if name, ok := n.(*syntax.Name); ok {
			fl.ejectCtSite(name, parents)
		}
		return true
	})
}

// ejectCtSite renames one keyed site, with its type arguments, to the
// ejected name of its instance in the ejected text. The site loses its
// qualifier unless the instance is printed in its defining package.
func (fl *fileLower) ejectCtSite(name *syntax.Name, parents map[syntax.Node]syntax.Node) {
	id := fl.pkg.info.LogosCtSiteKeys[name]
	eject, ok := fl.pkg.ctEjectNames()[id]
	if !ok {
		return
	}
	owned := fl.pkg.ejectOwners[id] != nil
	var site syntax.Expr = name
	replaced := []syntax.Node{name}
	if sel, ok := parents[site].(*syntax.SelectorExpr); ok && sel.Sel == name {
		site = sel
		if !owned {
			replaced = []syntax.Node{sel}
		}
	}
	if index, ok := parents[site].(*syntax.IndexExpr); ok && index.X == site {
		site = index
		if owned {
			replaced = append(replaced, index.Index)
		} else {
			replaced = []syntax.Node{index}
		}
	}
	ctx := cfCtx{buf: fl.srcMap, starts: fl.start}
	fl.add(offAt(fl.start, syntax.StartPos(replaced[0])), cfExactEnd(ctx, site), eject)
	if fl.ejectSites == nil {
		fl.ejectSites = make(map[syntax.Node]bool)
	}
	for _, n := range replaced {
		fl.ejectSites[n] = true
	}
	fl.changed = true
}

// ejectBlankImport blanks, in the ejected text only, an import that only
// renamed call sites used.
func (fl *fileLower) ejectBlankImport(imp *syntax.ImportDecl) {
	path, _ := strconv.Unquote(imp.Path.Value)
	if _, owned := ownedEjectImports[fl.file][path]; owned {
		return
	}
	fl.cfBlankImportText(imp)
	if ctEjectBlanked == nil {
		ctEjectBlanked = make(map[*syntax.File]map[string]bool)
	}
	if ctEjectBlanked[fl.file] == nil {
		ctEjectBlanked[fl.file] = make(map[string]bool)
	}
	ctEjectBlanked[fl.file][path] = true
	fl.changed = true
}

// EjectCtInstances appends to edits the ejected source of every checked
// comptime instance of the package. An instance whose defining package can
// hold it (ctEjectOwners) is printed once, in a generated file of that
// package, through the owned-request transport. Any other instance is
// printed as plain Go in the file of its first call site, non-test files
// first; an instance that only another instance calls joins the file of its
// caller. Names of the defining package are qualified through an import of
// that package, so such an instance that names an unexported declaration of
// another package cannot be ejected.
func EjectCtInstances(pkg *types2.Package, info *types2.Info, files []*syntax.File, src map[*syntax.File][]byte, edits map[*syntax.File][]Edit) error {
	recs := ctEjectRecords(info)
	owners := ctEjectOwners(recs)
	names := ctEjectNames(ctEjectOrigins(recs), owners)
	byID := make(map[string]*types2.LogosCtInst, len(info.LogosCtPrepared))
	for _, rec := range info.LogosCtPrepared {
		byID[rec.ID] = rec
	}
	sites := ctEjectTraceSites(files)
	order := make([]*syntax.File, 0, len(files))
	for _, file := range files {
		if _, ok := src[file]; ok {
			order = append(order, file)
		}
	}
	if len(order) == 0 {
		return nil
	}
	slices.SortStableFunc(order, func(a, b *syntax.File) int {
		return cmp.Compare(ctEjectTestRank(a), ctEjectTestRank(b))
	})
	placed, owned := ctEjectPlace(order, info.LogosCtSiteKeys, byID, owners)
	for i, file := range order {
		if len(placed[i]) == 0 {
			continue
		}
		p := newCtEjectPrinter(pkg, info, file, names, owners)
		p.sites = sites
		for _, id := range placed[i] {
			if err := p.instance(byID[id]); err != nil {
				return err
			}
		}
		edits[file] = append(edits[file], p.edits(src[file])...)
	}
	for _, id := range owned {
		if err := ctEjectOwned(byID[id], names, owners, sites); err != nil {
			return err
		}
	}
	return nil
}

// ctEjectOwned prints rec in its defining package as one generated file,
// requested through the owned-request transport. The text depends only on
// the instance, so the requests of every package that needs it carry the
// same source and merge into one file.
func ctEjectOwned(rec *types2.LogosCtInst, names map[string]string, owners map[string]*types2.Package, sites map[string]*syntax.VarDecl) error {
	owner := rec.Origin.Pkg()
	p := &ctEjectPrinter{consumer: owner, names: names, owners: owners, imports: make(map[string]string), taken: make(map[string]bool), sites: sites}
	if err := p.instance(rec); err != nil {
		return err
	}
	decls, ok := ownedDeclsForTypes(rec.Targs...)
	if !ok {
		decls = []logosowned.Declaration{}
	}
	if fb := rec.Origin.Pos().FileBase(); fb != nil {
		if file := ownedDeclBasename(fb.Filename()); file != "" && file != "." && file != ".." {
			decls = append(decls, logosowned.Declaration{Owner: owner.Path(), File: file})
		}
	}
	if !emitOwnedSource(owner.Path(), owner.Name(), "", "package "+owner.Name()+"\n"+p.text.String()+"\n", decls) {
		return fmt.Errorf("ejected comptime instance %s has no package clause", names[rec.ID])
	}
	return nil
}

func ctEjectTestRank(file *syntax.File) int {
	name := file.Pos().FileBase().Filename()
	if strings.HasSuffix(name, "_test.lgo") || strings.HasSuffix(name, "_test.go") {
		return 1
	}
	return 0
}

// ctEjectPlace returns, per file of order, the instances printed in that
// file, in breadth-first order from the file's own call sites, and the
// instances printed in their defining package.
func ctEjectPlace(order []*syntax.File, keys map[*syntax.Name]string, byID map[string]*types2.LogosCtInst, owners map[string]*types2.Package) ([][]string, []string) {
	rank := make(map[*syntax.PosBase]int, len(order))
	for i, f := range order {
		rank[f.Pos().FileBase()] = i
	}
	roots := make(map[int][]*syntax.Name)
	for name, id := range keys {
		if byID[id] == nil {
			continue
		}
		if i, ok := rank[name.Pos().FileBase()]; ok {
			roots[i] = append(roots[i], name)
		}
	}
	seen := make(map[string]bool)
	placed := make([][]string, len(order))
	var owned []string
	for i := range order {
		sites := roots[i]
		slices.SortFunc(sites, func(a, b *syntax.Name) int { return a.Pos().Cmp(b.Pos()) })
		queue := make([]string, 0, len(sites))
		for _, site := range sites {
			queue = append(queue, keys[site])
		}
		for len(queue) != 0 {
			id := queue[0]
			queue = queue[1:]
			if seen[id] {
				continue
			}
			seen[id] = true
			if owners[id] != nil {
				owned = append(owned, id)
			} else {
				placed[i] = append(placed[i], id)
			}
			queue = append(queue, ctEjectChildren(byID[id], byID)...)
		}
	}
	return placed, owned
}

func ctEjectChildren(rec *types2.LogosCtInst, byID map[string]*types2.LogosCtInst) []string {
	var ids []string
	visit := func(n syntax.Node) bool {
		if name, ok := n.(*syntax.Name); ok {
			if id := rec.Info.LogosCtSiteKeys[name]; byID[id] != nil {
				ids = append(ids, id)
			}
		}
		return true
	}
	syntax.Inspect(rec.Decl, visit)
	for _, helper := range rec.Helpers {
		syntax.Inspect(helper, visit)
	}
	return ids
}

// ctEjectPrinter prints instances into one file of the consumer package,
// or, with no file, into a generated file of the defining package
// (consumer), where every other package is named through its owner
// import qualifier.
type ctEjectPrinter struct {
	consumer *types2.Package
	file     *syntax.File
	names    map[string]string
	owners   map[string]*types2.Package
	imports  map[string]string
	taken    map[string]bool
	locals   map[string]bool
	boxed    map[types2.Object]bool
	added    []ctEjectImport
	text     strings.Builder

	sites     map[string]*syntax.VarDecl
	own       string
	siteNames map[*types2.Var]string
	siteText  []string
}

type ctEjectImport struct {
	name, path string
}

func newCtEjectPrinter(consumer *types2.Package, info *types2.Info, file *syntax.File, names map[string]string, owners map[string]*types2.Package) *ctEjectPrinter {
	p := &ctEjectPrinter{consumer: consumer, file: file, names: names, owners: owners, imports: make(map[string]string), taken: make(map[string]bool)}
	blanked := ctEjectBlanked[file]
	for _, decl := range file.DeclList {
		imp, ok := decl.(*syntax.ImportDecl)
		if !ok || imp.Path == nil || imp.LocalPkgName != nil && imp.LocalPkgName.Value == ctTraceRT {
			continue
		}
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || blanked[path] {
			continue
		}
		var pn *types2.PkgName
		if imp.LocalPkgName != nil {
			pn, _ = info.Defs[imp.LocalPkgName].(*types2.PkgName)
		} else {
			pn, _ = info.Implicits[imp].(*types2.PkgName)
		}
		if pn == nil || pn.Name() == "_" || pn.Name() == "." {
			continue
		}
		p.taken[pn.Name()] = true
		if _, ok := p.imports[path]; !ok {
			p.imports[path] = pn.Name()
		}
	}
	return p
}

func (p *ctEjectPrinter) alias(pkg *types2.Package) string {
	if p.file == nil {
		return ownedOwnerImport(p.consumer.Path(), pkg.Path())
	}
	if name, ok := p.imports[pkg.Path()]; ok && !p.locals[name] {
		return name
	}
	name := pkg.Name()
	for i := 2; p.taken[name] || p.locals[name] || p.consumer.Scope().Lookup(name) != nil; i++ {
		name = pkg.Name() + strconv.Itoa(i)
	}
	p.taken[name] = true
	p.imports[pkg.Path()] = name
	p.added = append(p.added, ctEjectImport{name, pkg.Path()})
	return name
}

func (p *ctEjectPrinter) qualify(pkg *types2.Package) string {
	if pkg.Path() == p.consumer.Path() {
		return ""
	}
	return p.alias(pkg)
}

// instName returns the ejected spelling of a call of instance id, or false
// when the package does not name that instance.
func (p *ctEjectPrinter) instName(id string) (string, bool) {
	name, ok := p.names[id]
	if !ok {
		return "", false
	}
	if owner := p.owners[id]; owner != nil {
		if q := p.qualify(owner); q != "" {
			return q + "." + name, true
		}
	}
	return name, true
}

type ctEjectSlot struct {
	slot, old reflect.Value
}

var ctEjectExprType = reflect.TypeFor[syntax.Expr]()

// instance prints rec as plain Go under its ejected name. Every rename is
// a temporary change of the checked syntax, undone before it returns,
// because the native backend still compiles the same declarations.
func (p *ctEjectPrinter) instance(rec *types2.LogosCtInst) (err error) {
	facts := rec.Info
	own := p.names[rec.ID]
	p.own, p.siteNames, p.siteText = own, nil, nil
	roots := make([]syntax.Node, 0, 1+len(rec.Helpers))
	roots = append(roots, rec.Decl)
	for _, helper := range rec.Helpers {
		roots = append(roots, helper)
	}
	type renamed struct {
		name *syntax.Name
		old  string
	}
	var slots []ctEjectSlot
	var values []renamed
	defer func() {
		for i := len(values) - 1; i >= 0; i-- {
			values[i].name.Value = values[i].old
		}
		for i := len(slots) - 1; i >= 0; i-- {
			slots[i].slot.Set(slots[i].old)
		}
	}()
	setValue := func(n *syntax.Name, value string) {
		if n.Value != value {
			values = append(values, renamed{n, n.Value})
			n.Value = value
		}
	}
	replace := func(slot reflect.Value) {
		e, _ := slot.Interface().(syntax.Expr)
		if e == nil {
			return
		}
		if id := ctEjectSiteID(e, facts); id != "" {
			if name, ok := p.instName(id); ok {
				slots = append(slots, ctEjectSlot{slot, reflect.ValueOf(slot.Interface())})
				slot.Set(reflect.ValueOf(syntax.NewName(e.Pos(), name)))
			}
		}
	}
	p.locals = ctEjectLocals(roots, facts)
	defer func() { p.locals, p.boxed = nil, nil }()
	renames := make(map[types2.Object]string)
	if obj := facts.Defs[rec.Decl.Name]; obj != nil {
		renames[obj] = own
	}
	for _, helper := range rec.Helpers {
		for _, name := range ctEjectDeclNames(helper) {
			if obj := facts.Defs[name]; obj != nil {
				renames[obj] = own + "_" + name.Value
			}
		}
	}
	for _, root := range roots {
		syntax.Inspect(root, func(n syntax.Node) bool {
			v := reflect.ValueOf(n)
			if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
				return true
			}
			v = v.Elem()
			for i := range v.NumField() {
				f := v.Field(i)
				switch {
				case !f.CanSet():
				case f.Type() == ctEjectExprType:
					replace(f)
				case f.Kind() == reflect.Slice && f.Type().Elem() == ctEjectExprType:
					for j := range f.Len() {
						replace(f.Index(j))
					}
				}
			}
			return true
		})
	}
	local, err := ctEjectPlanLocal(rec)
	if err != nil {
		return err
	}
	p.boxed = make(map[types2.Object]bool)
	for i := range local.bound {
		if d, ok := rec.Helpers[i].(*syntax.VarDecl); ok {
			p.boxed[facts.Defs[d.NameList[0]]] = true
		}
	}
	sels := make(map[*syntax.Name]bool)
	for _, root := range roots {
		syntax.Inspect(root, func(n syntax.Node) bool {
			if err != nil {
				return false
			}
			switch n := n.(type) {
			case *syntax.SelectorExpr:
				sels[n.Sel] = true
			case *syntax.Name:
				var value string
				value, err = p.nameText(n, facts, renames, sels)
				setValue(n, value)
			}
			return true
		})
	}
	if err != nil {
		return err
	}
	setValue(rec.Decl.Name, own)
	boxes, restore, err := p.placeLocal(rec, local, renames)
	defer restore()
	if err != nil {
		return err
	}
	var b strings.Builder
	if _, err := syntax.Fprint(&b, rec.Decl, 0); err != nil {
		return err
	}
	for i, helper := range rec.Helpers {
		if _, bound := local.bound[i]; bound {
			continue
		}
		b.WriteString("\n\n")
		if _, err := syntax.Fprint(&b, ctEjectUngrouped(helper), 0); err != nil {
			return err
		}
	}
	for _, box := range boxes {
		b.WriteString("\n\n")
		b.WriteString(box)
	}
	for _, site := range p.siteText {
		b.WriteString("\n\n")
		b.WriteString(site)
	}
	text := b.String()
	if _, err := syntax.Parse(syntax.NewFileBase(own), strings.NewReader("package p\n\n"+text), nil, nil, 0); err != nil {
		return fmt.Errorf("ejected comptime instance %s is not Go: %v\n%s", own, err, text)
	}
	p.text.WriteString("\n\n")
	p.text.WriteString(text)
	return nil
}

func ctEjectSiteID(e syntax.Expr, facts *types2.Info) string {
	if index, ok := e.(*syntax.IndexExpr); ok {
		e = index.X
	}
	switch x := e.(type) {
	case *syntax.Name:
		return facts.LogosCtSiteKeys[x]
	case *syntax.SelectorExpr:
		return facts.LogosCtSiteKeys[x.Sel]
	}
	return ""
}

func (p *ctEjectPrinter) nameText(n *syntax.Name, facts *types2.Info, renames map[types2.Object]string, sels map[*syntax.Name]bool) (string, error) {
	if n.Value == "_logos_ct_type" {
		typ := n.GetTypeInfo().Type
		if typ == nil {
			return "", fmt.Errorf("%s: comptime instance type without a checked type", n.Pos())
		}
		return types2.TypeString(typ, p.qualify), nil
	}
	if ref, ok := facts.LogosCtConstMapUses[n]; ok {
		return p.constMapText(n, ref, facts.Uses[n])
	}
	obj := facts.Uses[n]
	if obj == nil {
		obj = facts.Defs[n]
	}
	if obj == nil {
		return n.Value, nil
	}
	if v, ok := obj.(*types2.Var); ok && v.Pkg() != nil && v.Parent() == v.Pkg().Scope() && p.sites[v.Name()] != nil {
		return p.siteName(v)
	}
	if c, ok := obj.(*types2.Const); ok && c.Pkg() == nil && c.Parent() == nil {
		return p.constText(c), nil
	}
	if name, ok := renames[obj]; ok {
		if p.boxed[obj] {
			return "(*" + name + ")", nil
		}
		return name, nil
	}
	if pn, ok := obj.(*types2.PkgName); ok {
		return p.alias(pn.Imported()), nil
	}
	pkg := obj.Pkg()
	if pkg == nil {
		return n.Value, nil
	}
	member := sels[n] || ctEjectMember(obj)
	if !member && obj.Parent() != pkg.Scope() {
		return n.Value, nil
	}
	foreign := pkg.Path() != p.consumer.Path()
	if foreign && !obj.Exported() {
		return "", p.unexported(n.Pos(), obj.Name(), pkg.Path())
	}
	switch {
	case member:
		return n.Value, nil
	case foreign:
		return p.alias(pkg) + "." + obj.Name(), nil
	}
	return obj.Name(), nil
}

// ctEjectTraceSites returns the trace site declarations that the lowering of
// comptime instances adds to the package files, by name. Their names hold a
// middle dot, so the ejected text cannot spell them.
func ctEjectTraceSites(files []*syntax.File) map[string]*syntax.VarDecl {
	sites := make(map[string]*syntax.VarDecl)
	for _, file := range files {
		for _, decl := range file.DeclList {
			if d, ok := decl.(*syntax.VarDecl); ok && len(d.NameList) == 1 && strings.HasPrefix(d.NameList[0].Value, ctTraceSite) {
				sites[d.NameList[0].Value] = d
			}
		}
	}
	return sites
}

// siteName names, per printed instance, a trace site that a lowered
// failure path of the instance passes by address, and records its
// declaration for the text after the instance.
func (p *ctEjectPrinter) siteName(v *types2.Var) (string, error) {
	if name, ok := p.siteNames[v]; ok {
		return name, nil
	}
	fields := make(map[string]string)
	if lit, ok := p.sites[v.Name()].Values.(*syntax.CompositeLit); ok {
		for _, elem := range lit.ElemList {
			kv, _ := elem.(*syntax.KeyValueExpr)
			if kv == nil {
				continue
			}
			key, _ := kv.Key.(*syntax.Name)
			value, _ := kv.Value.(*syntax.BasicLit)
			if key != nil && value != nil {
				fields[key.Value] = value.Value
			}
		}
	}
	if fields["File"] == "" || fields["Line"] == "" {
		return "", fmt.Errorf("%s: comptime trace site %s has no file and line", v.Pos(), v.Name())
	}
	name := p.own + "_site" + strconv.Itoa(len(p.siteNames))
	if p.siteNames == nil {
		p.siteNames = make(map[*types2.Var]string)
	}
	p.siteNames[v] = name
	rt := p.alias(types2.NewPackage(rtImportPath, "rt"))
	p.siteText = append(p.siteText, fmt.Sprintf("var %s = %s.Site{File: %s, Line: %s}", name, rt, fields["File"], fields["Line"]))
	return name, nil
}

func (p *ctEjectPrinter) unexported(pos syntax.Pos, name, path string) error {
	return &CtEjectRefusal{pos, fmt.Sprintf("[comptime::unexported_in_exported] a comptime instance ejected into package %s names unexported %s of package %s", p.consumer.Path(), name, path)}
}

func (p *ctEjectPrinter) constText(c *types2.Const) string {
	text := renderExpr(constExactExpr(c.Pos(), c.Val()))
	if basic, ok := c.Type().(*types2.Basic); ok && basic.Info()&types2.IsUntyped != 0 {
		return text
	}
	return types2.TypeString(c.Type(), p.qualify) + "(" + text + ")"
}

func (p *ctEjectPrinter) constMapText(n *syntax.Name, ref types2.LogosCtConstMapRef, obj types2.Object) (string, error) {
	helper, ok := obj.(*types2.Func)
	if !ok || helper.Pkg() == nil {
		return "", fmt.Errorf("%s: comptime const map helper for %s.%s without a checked function", n.Pos(), ref.Path, ref.Name)
	}
	if ref.Path == p.consumer.Path() {
		return helper.Name(), nil
	}
	if m := helper.Pkg().Scope().Lookup(ref.Name); m == nil || !m.Exported() {
		return "", p.unexported(n.Pos(), ref.Name, ref.Path)
	}
	owner := p.alias(helper.Pkg()) + "." + ref.Name
	sig := helper.Signature()
	switch ref.Role {
	case types2.LogosCtMapLookup:
		key := types2.TypeString(sig.Params().At(0).Type(), p.qualify)
		val := types2.TypeString(sig.Results().At(0).Type(), p.qualify)
		return fmt.Sprintf("func(_logos_k %s) (%s, bool) { _logos_v, _logos_ok := %s[_logos_k]; return _logos_v, _logos_ok }", key, val, owner), nil
	case types2.LogosCtMapMaterialize:
		return fmt.Sprintf("func() %s { return %s }", types2.TypeString(sig.Results().At(0).Type(), p.qualify), owner), nil
	}
	return p.alias(types2.NewPackage(rtImportPath, "rt")) + ".SortedAllDynamic(" + owner + ")", nil
}

func ctEjectMember(obj types2.Object) bool {
	switch obj := obj.(type) {
	case *types2.Var:
		return obj.IsField()
	case *types2.Func:
		return obj.Signature().Recv() != nil
	}
	return false
}

// CtEjectRefusal is a comptime instance that logos eject cannot print.
type CtEjectRefusal struct {
	Pos syntax.Pos
	Msg string
}

func (e *CtEjectRefusal) Error() string { return fmt.Sprintf("%s: %s", e.Pos, e.Msg) }

func ctEjectDeclNames(decl syntax.Node) []*syntax.Name {
	switch d := decl.(type) {
	case *syntax.FuncDecl:
		if d.Recv == nil {
			return []*syntax.Name{d.Name}
		}
	case *syntax.VarDecl:
		return d.NameList
	case *syntax.ConstDecl:
		return d.NameList
	case *syntax.TypeDecl:
		return []*syntax.Name{d.Name}
	}
	return nil
}

func ctEjectUngrouped(decl syntax.Node) syntax.Node {
	switch d := decl.(type) {
	case *syntax.VarDecl:
		c := *d
		c.Group = nil
		return &c
	case *syntax.ConstDecl:
		c := *d
		c.Group = nil
		return &c
	case *syntax.TypeDecl:
		c := *d
		c.Group = nil
		return &c
	}
	return decl
}

func ctEjectLocals(roots []syntax.Node, facts *types2.Info) map[string]bool {
	locals := make(map[string]bool)
	for _, root := range roots {
		syntax.Inspect(root, func(n syntax.Node) bool {
			if name, ok := n.(*syntax.Name); ok {
				if obj := facts.Defs[name]; obj != nil && obj.Pkg() != nil && obj.Parent() != obj.Pkg().Scope() {
					locals[name.Value] = true
				}
			}
			return true
		})
	}
	return locals
}

func (p *ctEjectPrinter) edits(src []byte) []Edit {
	base := p.file.Pos().FileBase()
	var out []Edit
	if len(p.added) != 0 {
		var b strings.Builder
		for _, imp := range p.added {
			fmt.Fprintf(&b, "\n\nimport %s %q", imp.name, imp.path)
		}
		pos := p.file.PkgName.Pos()
		at := syntax.MakePos(base, pos.Line(), pos.Col()+uint(len(p.file.PkgName.Value)))
		out = append(out, Edit{Start: at, End: at, Text: b.String()})
	}
	line := uint(1 + bytes.Count(src, []byte{'\n'}))
	col := uint(len(src) - bytes.LastIndexByte(src, '\n'))
	end := syntax.MakePos(base, line, col)
	return append(out, Edit{Start: end, End: end, Text: p.text.String() + "\n"})
}

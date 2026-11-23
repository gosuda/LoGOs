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
)

// ctEjectInfix joins a comptime generic's name and the per-package
// sequence number of one of its instances in ejected source.
const ctEjectInfix = "_logosct"

var ctEjectBlanked map[*syntax.File]map[string]bool

// ctEjectNames names every comptime instance of the compiled package for
// logos eject: <origin>_logosct<N>, where N counts the instances of one
// origin name in package path and instance id order.
func ctEjectNames(origins map[string]*types2.Func) map[string]string {
	ids := slices.Collect(maps.Keys(origins))
	slices.SortFunc(ids, func(a, b string) int {
		oa, ob := origins[a], origins[b]
		return cmp.Or(cmp.Compare(oa.Name(), ob.Name()), cmp.Compare(oa.Pkg().Path(), ob.Pkg().Path()), cmp.Compare(a, b))
	})
	names := make(map[string]string, len(ids))
	seq := make(map[string]int)
	for _, id := range ids {
		origin := origins[id].Name()
		names[id] = origin + ctEjectInfix + strconv.Itoa(seq[origin])
		seq[origin]++
	}
	return names
}

func (p *packageLower) ctEjectNames() map[string]string {
	if p.ejectNames == nil {
		origins := make(map[string]*types2.Func, len(p.info.LogosCtInst))
		for _, inst := range p.info.LogosCtInst {
			origins[inst.ID] = inst.Origin
		}
		p.ejectNames = ctEjectNames(origins)
	}
	return p.ejectNames
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

// ejectCtSite renames one keyed site, with its qualifier and type
// arguments, to the ejected name of its instance in the ejected text.
func (fl *fileLower) ejectCtSite(name *syntax.Name, parents map[syntax.Node]syntax.Node) {
	eject, ok := fl.pkg.ctEjectNames()[fl.pkg.info.LogosCtSiteKeys[name]]
	if !ok {
		return
	}
	var site syntax.Expr = name
	if sel, ok := parents[site].(*syntax.SelectorExpr); ok && sel.Sel == name {
		site = sel
	}
	if index, ok := parents[site].(*syntax.IndexExpr); ok && index.X == site {
		site = index
	}
	ctx := cfCtx{buf: fl.srcMap, starts: fl.start}
	fl.add(offAt(fl.start, syntax.StartPos(site)), cfExactEnd(ctx, site), eject)
	if fl.ejectSites == nil {
		fl.ejectSites = make(map[syntax.Node]bool)
	}
	fl.ejectSites[site] = true
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
// comptime instance of the package. An instance is printed as plain Go in
// the file of its first call site, non-test files first; an instance that
// only another instance calls joins the file of its caller. Names of the
// defining package are qualified through an import of that package, so an
// instance that names an unexported declaration of another package cannot
// be ejected.
func EjectCtInstances(pkg *types2.Package, info *types2.Info, files []*syntax.File, src map[*syntax.File][]byte, edits map[*syntax.File][]Edit) error {
	byID := make(map[string]*types2.LogosCtInst, len(info.LogosCtPrepared))
	origins := make(map[string]*types2.Func, len(info.LogosCtPrepared))
	for _, rec := range info.LogosCtPrepared {
		byID[rec.ID] = rec
		origins[rec.ID] = rec.Origin
	}
	names := ctEjectNames(origins)
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
	for _, file := range order {
		ids := ctEjectPlaced(file, order, info.LogosCtSiteKeys, byID)
		if len(ids) == 0 {
			continue
		}
		p := newCtEjectPrinter(pkg, info, file, names)
		for _, id := range ids {
			if err := p.instance(byID[id]); err != nil {
				return err
			}
		}
		edits[file] = append(edits[file], p.edits(src[file])...)
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

// ctEjectPlaced returns the instances printed in file, in breadth-first
// order from the file's own call sites.
func ctEjectPlaced(file *syntax.File, order []*syntax.File, keys map[*syntax.Name]string, byID map[string]*types2.LogosCtInst) []string {
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
	var mine []string
	for i, f := range order {
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
			if f == file {
				mine = append(mine, id)
			}
			queue = append(queue, ctEjectChildren(byID[id], byID)...)
		}
	}
	return mine
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

type ctEjectPrinter struct {
	consumer *types2.Package
	file     *syntax.File
	names    map[string]string
	imports  map[string]string
	taken    map[string]bool
	locals   map[string]bool
	boxed    map[types2.Object]bool
	added    []ctEjectImport
	text     strings.Builder
}

type ctEjectImport struct {
	name, path string
}

func newCtEjectPrinter(consumer *types2.Package, info *types2.Info, file *syntax.File, names map[string]string) *ctEjectPrinter {
	p := &ctEjectPrinter{consumer: consumer, file: file, names: names, imports: make(map[string]string), taken: make(map[string]bool)}
	blanked := ctEjectBlanked[file]
	for _, decl := range file.DeclList {
		imp, ok := decl.(*syntax.ImportDecl)
		if !ok || imp.Path == nil {
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
			if name, ok := p.names[id]; ok {
				slots = append(slots, ctEjectSlot{slot, reflect.ValueOf(slot.Interface())})
				slot.Set(reflect.ValueOf(syntax.NewName(e.Pos(), name)))
			}
		}
	}
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
	p.locals = ctEjectLocals(roots, facts)
	defer func() { p.locals, p.boxed = nil, nil }()
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

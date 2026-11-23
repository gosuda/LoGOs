// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"errors"
	"fmt"
	"go/constant"
	"go/token"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/logos/facts"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
	"internal/buildcfg"
)

const (
	cfBindMarker = "_logos_bind"
	cfCopyInfix  = "_logosct"
)

// cfBind names the alias a copy declares at the top of its body for
// parameter n when a const switch over a type parameter exists: `type
// _logos_bind0 = <argument>`. References spelled with it always resolve
// to the parameter's argument, while the argument's own spelling
// materializes only in the bind's right-hand side: at body scope, where
// no case body declaration can capture it.
func cfBind(n int) string {
	return cfBindMarker + strconv.Itoa(n)
}

type cfImport struct {
	name string
	path string
}

// cfOwnerName is an unexported package-level identifier that an exported
// comptime generic names. Its own package can instantiate the generic; a
// copy in another package cannot reach the identifier.
type cfOwnerName struct {
	kind string
	name string
}

type cfRecord struct {
	name    string
	imports []cfImport
	owner   []cfOwnerName
	source  string
	sourceO otext
}

func (r *cfRecord) text() string {
	var b strings.Builder
	b.WriteString("//logosct\n")
	for _, imp := range r.imports {
		fmt.Fprintf(&b, "//logosimp %s %s\n", imp.name, imp.path)
	}
	for _, n := range r.owner {
		fmt.Fprintf(&b, "//logosowner %s %s\n", n.name, n.kind)
	}
	b.WriteString(r.source)
	return b.String()
}

func cfParseRecord(name, text string) (*cfRecord, error) {
	rec := &cfRecord{name: name}
	for strings.HasPrefix(text, "//logos") {
		line, rest, _ := strings.Cut(text, "\n")
		text = rest
		fields := strings.Fields(line)
		switch {
		case fields[0] == "//logosct":
		case fields[0] == "//logosimp" && len(fields) == 3:
			rec.imports = append(rec.imports, cfImport{name: fields[1], path: fields[2]})
		case fields[0] == "//logosowner" && len(fields) >= 3:
			rec.owner = append(rec.owner, cfOwnerName{name: fields[1], kind: strings.Join(fields[2:], " ")})
		default:
			return nil, fmt.Errorf("unknown record header %q", line)
		}
	}
	rec.source = text
	return rec, nil
}

func cfParseSource(text string) (*syntax.File, []error) {
	var errs []error
	file, _ := syntax.Parse(syntax.NewFileBase("cfrecord.lgo"), strings.NewReader(text), func(err error) {
		errs = append(errs, err)
	}, nil, syntax.CheckBranches|syntax.LogosMode)
	return file, errs
}

func cfFirstFunc(file *syntax.File) *syntax.FuncDecl {
	if file == nil {
		return nil
	}
	for _, decl := range file.DeclList {
		if fn, ok := decl.(*syntax.FuncDecl); ok {
			return fn
		}
	}
	return nil
}

type cfGenericDef struct {
	decl    *syntax.FuncDecl
	obj     *types2.Func
	file    *syntax.File
	record  *cfRecord
	forward bool
}

type cfFrame struct {
	key     string
	label   string
	forward bool
}

type cfExpander struct {
	defs      []*cfGenericDef
	byObj     map[*types2.Func]*cfGenericDef
	byDecl    map[*syntax.FuncDecl]*cfGenericDef
	foreign   map[string]*cfRecord
	memo      map[string]string
	stack     []cfFrame
	pkgs      map[string]*types2.Package
	roots     []*types2.Package
	seq       int
	copyFuncs []*types2.Func
	switches  map[*syntax.ConstSwitchStmt]*syntax.ConstSwitchCase
}

func newCfExpander(shared *packageLower, files []*syntax.File, src map[*syntax.File][]byte) {
	exp := &cfExpander{
		byObj:   make(map[*types2.Func]*cfGenericDef),
		byDecl:  make(map[*syntax.FuncDecl]*cfGenericDef),
		foreign: make(map[string]*cfRecord),
		memo:    make(map[string]string),
		pkgs:    make(map[string]*types2.Package),
	}
	shared.ctExp = exp
	f := facts.Current()
	var lowers []*fileLower
	for _, file := range files {
		data, ok := src[file]
		if !ok {
			continue
		}
		fl := &fileLower{pkg: shared, file: file, src: data, orig: len(data), start: lineStarts(data)}
		fl.srcMap = origBuf(data)
		f = fl.registerComptime(exp, f)
		lowers = append(lowers, fl)
	}
	for found := true; found; {
		found = false
		for _, fl := range lowers {
			if defs := fl.cfCollectForwarders(); len(defs) != 0 {
				f = fl.cfRegisterDefs(exp, f, defs)
				found = true
			}
		}
	}
	if f != nil {
		facts.SetCurrent(f)
	}
}

func (e *cfExpander) frameIndex(key string) int {
	return slices.IndexFunc(e.stack, func(f cfFrame) bool { return f.key == key })
}

func cfExpKey(name, pkgPath string, targs []types2.Type) string {
	var b strings.Builder
	b.WriteString(pkgPath)
	b.WriteByte(0)
	b.WriteString(name)
	for _, t := range targs {
		b.WriteByte(0)
		b.WriteString(types2.TypeString(t, func(p *types2.Package) string { return p.Path() }))
	}
	return b.String()
}

func cfShortKeys(targs []types2.Type) string {
	parts := make([]string, len(targs))
	for i, t := range targs {
		parts[i] = types2.TypeString(t, func(*types2.Package) string { return "" })
	}
	return strings.Join(parts, ", ")
}

func cfFactObject(obj *types2.Func) string {
	if obj == nil {
		return ""
	}
	if q := facts.ObjectName(obj); q != "" {
		return q
	}
	return obj.Name()
}

func (fl *fileLower) registerComptime(exp *cfExpander, f *facts.Facts) *facts.Facts {
	return fl.cfRegisterDefs(exp, f, fl.cfCollectGenerics())
}

func (fl *fileLower) cfRegisterDefs(exp *cfExpander, f *facts.Facts, defs []*cfGenericDef) *facts.Facts {
	if len(defs) == 0 {
		return f
	}
	var imports []cfImport
	for _, decl := range fl.file.DeclList {
		if imp, ok := decl.(*syntax.ImportDecl); ok {
			if pn := fl.pkg.info.PkgNameOf(imp); pn != nil && pn.Name() != "_" {
				imports = append(imports, cfImport{name: pn.Name(), path: pn.Imported().Path()})
			}
		}
	}
	for _, def := range defs {
		def.record = fl.cfRecordText(def, imports)
		exp.defs = append(exp.defs, def)
		exp.byDecl[def.decl] = def
		if def.obj != nil {
			exp.byObj[def.obj] = def
		}
		if def.forward || def.obj == nil || !def.obj.Exported() {
			continue
		}
		if f == nil {
			f = &facts.Facts{}
		}
		f.Comptimes = append(f.Comptimes, facts.ComptimeFact{Object: cfFactObject(def.obj), Source: []byte(def.record.text())})
	}
	return f
}

func (fl *fileLower) cfCollectGenerics() []*cfGenericDef {
	var out []*cfGenericDef
	for _, decl := range fl.file.DeclList {
		fn, ok := decl.(*syntax.FuncDecl)
		if !ok || fn.Body == nil || len(fn.TParamList) == 0 || !types2.LogosUsesComptime(fn, fl.pkg.info.Scopes[fl.file]) {
			continue
		}
		var obj *types2.Func
		if o, ok := fl.pkg.info.Defs[fn.Name]; ok {
			obj, _ = o.(*types2.Func)
		}
		out = append(out, &cfGenericDef{decl: fn, obj: obj, file: fl.file})
	}
	return out
}

func (fl *fileLower) cfCollectForwarders() []*cfGenericDef {
	var out []*cfGenericDef
	for _, decl := range fl.file.DeclList {
		fn, ok := decl.(*syntax.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil || len(fn.TParamList) == 0 {
			continue
		}
		if _, known := fl.pkg.ctExp.byDecl[fn]; known {
			continue
		}
		obj, _ := fl.pkg.info.Defs[fn.Name].(*types2.Func)
		if obj != nil && fl.cfForwards(fn.Body) {
			out = append(out, &cfGenericDef{decl: fn, obj: obj, file: fl.file, forward: true})
		}
	}
	return out
}

func (fl *fileLower) cfForwards(body *syntax.BlockStmt) bool {
	found := false
	syntax.Inspect(body, func(n syntax.Node) bool {
		if found {
			return false
		}
		name, ok := n.(*syntax.Name)
		if !ok {
			return true
		}
		inst, ok := fl.pkg.info.Instances[name]
		obj, isFunc := fl.pkg.info.Uses[name].(*types2.Func)
		found = ok && isFunc && fl.cfIsGeneric(obj) && slices.ContainsFunc(typeArgsOf(inst.TypeArgs), cfHasTypeParam)
		return true
	})
	return found
}

type cfRecordBuilder struct {
	fl      *fileLower
	decl    *syntax.FuncDecl
	subs    []splice
	parents map[syntax.Node]syntax.Node
	folded  map[*syntax.Name]bool
	export  bool
}

func (fl *fileLower) cfRecordText(def *cfGenericDef, imports []cfImport) *cfRecord {
	b := &cfRecordBuilder{
		fl:     fl,
		decl:   def.decl,
		export: !def.forward && def.obj != nil && def.obj.Exported() && fl.pkg.tpkg.Name() != "main",
	}
	syntax.Inspect(def.decl, b.visit)
	var owner []cfOwnerName
	if b.export {
		owner = b.ownerNames()
	}
	start := fl.cfFuncStart(fl.off(syntax.StartPos(def.decl)))
	end := fl.off(syntax.EndPos(def.decl)) + 1
	out, err := applyChunks(fl.srcMap.sliceO(start, end), shiftSplices(b.subs, -start))
	if err != nil {
		base.Fatalf("logos: overlapping comptime record at %s", def.decl.Pos().String())
	}
	return &cfRecord{
		name:    def.decl.Name.Value,
		imports: imports,
		owner:   owner,
		source:  out.s,
		sourceO: out,
	}
}

// ownerNames lists, in source order, the unexported package-level
// identifiers an exported comptime generic names. The record carries them
// so that an importer refuses its instantiation by name instead of failing
// on an undefined name in the copy. The walk follows cfRecordBuilder.visit:
// a const if condition that folds to a literal never reaches the record,
// and a name without a file position belongs to a synthesized node.
func (b *cfRecordBuilder) ownerNames() []cfOwnerName {
	var out []cfOwnerName
	seen := make(map[string]bool)
	var visit func(n syntax.Node) bool
	visit = func(n syntax.Node) bool {
		if s, ok := n.(*syntax.ConstIfStmt); ok {
			b.fl.cfCheckCond(s.Cond, visit)
			syntax.Inspect(s.Then, visit)
			if s.Else != nil {
				syntax.Inspect(s.Else, visit)
			}
			return false
		}
		name, ok := n.(*syntax.Name)
		if !ok || name.Pos().Base() == nil || b.folded[name] {
			return true
		}
		obj := b.recordObject(name)
		if obj == nil || obj.Exported() || !cfPackageLevel(b.fl, obj) || seen[obj.Name()] {
			return true
		}
		seen[obj.Name()] = true
		out = append(out, cfOwnerName{kind: cfObjKind(obj), name: obj.Name()})
		return true
	}
	syntax.Inspect(b.decl, visit)
	return out
}

// cfOwnerOnly reports an unexported identifier of another package that a
// comptime copy needs: only the defining package can instantiate the
// generic.
func (fl *fileLower) cfOwnerOnly(pos syntax.Pos, kind, name, pkgPath string) {
	fl.cfError(pos, "[comptime::unexported_in_exported] comptime source names unexported %s %s of package %s; only that package can instantiate it", kind, name, pkgPath)
}

func (b *cfRecordBuilder) recordObject(n *syntax.Name) types2.Object {
	info := b.fl.pkg.info
	if obj := info.Uses[n]; obj != nil {
		return obj
	}
	if info.Defs[n] != nil {
		return nil
	}
	obj := b.fl.pkg.tpkg.Scope().Lookup(n.Value)
	if obj == nil || obj.Exported() {
		return nil
	}
	if b.parents == nil {
		b.parents = cfParents(b.decl)
	}
	if b.fl.cfRecordNameUse(n, b.parents) {
		return obj
	}
	return nil
}

func (fl *fileLower) cfRecordNameUse(n *syntax.Name, parents map[syntax.Node]syntax.Node) bool {
	parent := parents[n]
	if fl.cfBareSkip(n, parent) {
		return false
	}
	if key, ok := parent.(*syntax.KeyValueExpr); ok && key.Key == n {
		lit, ok := parents[key].(*syntax.CompositeLit)
		if !ok || !fl.cfRecordIndexKeys(lit, parents) {
			return false
		}
	}
	if file := fl.pkg.info.Scopes[fl.file]; file != nil && file.Lookup(n.Value) != nil {
		return false
	}
	for child, scope := syntax.Node(n), parent; scope != nil; child, scope = scope, parents[scope] {
		if cfRecordScopeBinds(scope, child, parents[scope], n.Value) {
			return false
		}
	}
	return true
}

func (fl *fileLower) cfRecordIndexKeys(lit *syntax.CompositeLit, parents map[syntax.Node]syntax.Node) bool {
	switch syntax.Unparen(lit.Type).(type) {
	case *syntax.ArrayType, *syntax.SliceType, *syntax.MapType:
		return true
	}
	t := fl.pkg.info.TypeOf(lit)
	if id, ok := syntax.Unparen(lit.Type).(*syntax.Name); t == nil && ok && fl.cfRecordNameUse(id, parents) {
		if obj, ok := fl.pkg.tpkg.Scope().Lookup(id.Value).(*types2.TypeName); ok {
			t = obj.Type()
		}
	}
	if t == nil {
		return false
	}
	switch t.Underlying().(type) {
	case *types2.Array, *types2.Slice, *types2.Map:
		return true
	}
	return false
}

func cfRecordScopeBinds(scope, child, outer syntax.Node, name string) bool {
	switch s := scope.(type) {
	case *syntax.FuncDecl:
		return cfRecordParam(s.TParamList, name) || child == s.Body && cfRecordFuncParam(s.Type, name)
	case *syntax.FuncLit:
		return child == s.Body && cfRecordFuncParam(s.Type, name)
	case *syntax.LambdaExpr:
		return child == s.Body && cfRecordHasName(s.Params, name)
	case *syntax.BlockStmt:
		return cfRecordPriorStmt(s.List, child, name)
	case *syntax.DeclStmt:
		return cfRecordDecls(s.DeclList, child, name)
	case *syntax.TypeDecl:
		return s.Name.Value == name || cfRecordParam(s.TParamList, name)
	case *syntax.ConstForStmt:
		return child == s.Body && (s.Field.Value == name || s.Index != nil && s.Index.Value == name)
	case *syntax.ConstSwitchStmt:
		stmt, ok := child.(syntax.Stmt)
		if !ok {
			return false
		}
		for _, c := range s.Cases {
			if slices.Contains(c.Body, stmt) {
				return cfRecordPriorStmt(c.Body, stmt, name)
			}
		}
	case *syntax.IfStmt:
		return child != s.Init && cfRecordStmtBinds(s.Init, name)
	case *syntax.ForStmt:
		return child != s.Init && cfRecordStmtBinds(s.Init, name)
	case *syntax.SwitchStmt:
		return child != s.Init && cfRecordStmtBinds(s.Init, name)
	case *syntax.SwitchExpr:
		return child != s.Init && cfRecordStmtBinds(s.Init, name)
	case *syntax.CaseClause:
		if child != s.Cases {
			sw, _ := outer.(*syntax.SwitchStmt)
			return cfRecordPriorStmt(s.Body, child, name) || sw != nil && cfRecordTypeSwitchName(sw.Tag, name)
		}
	case *syntax.ArmClause:
		sw, _ := outer.(*syntax.SwitchExpr)
		return child == s.Body && sw != nil && cfRecordTypeSwitchName(sw.Tag, name)
	case *syntax.CommClause:
		return child != s.Comm && (cfRecordStmtBinds(s.Comm, name) || cfRecordPriorStmt(s.Body, child, name))
	}
	return false
}

func cfRecordTypeSwitchName(tag syntax.Expr, name string) bool {
	guard, ok := tag.(*syntax.TypeSwitchGuard)
	return ok && guard.Lhs != nil && guard.Lhs.Value == name
}

func cfRecordFuncParam(t *syntax.FuncType, name string) bool {
	return cfRecordParam(t.ParamList, name) || cfRecordParam(t.ResultList, name)
}

func cfRecordParam(fields []*syntax.Field, name string) bool {
	for _, f := range fields {
		if f.Name != nil && f.Name.Value == name {
			return true
		}
	}
	return false
}

func cfRecordHasName(names []*syntax.Name, name string) bool {
	for _, n := range names {
		if n.Value == name {
			return true
		}
	}
	return false
}

func cfRecordPriorStmt(stmts []syntax.Stmt, child syntax.Node, name string) bool {
	for _, stmt := range stmts {
		if stmt == child {
			break
		}
		if cfRecordStmtBinds(stmt, name) {
			return true
		}
	}
	return false
}

func cfRecordStmtBinds(stmt syntax.Stmt, name string) bool {
	var lhs syntax.Expr
	switch s := stmt.(type) {
	case *syntax.DeclStmt:
		return cfRecordDecls(s.DeclList, nil, name)
	case *syntax.LabeledStmt:
		return cfRecordStmtBinds(s.Stmt, name)
	case *syntax.AssignStmt:
		if s.Op == syntax.Def {
			lhs = s.Lhs
		}
	case *syntax.RangeClause:
		if s.Def {
			lhs = s.Lhs
		}
	}
	for _, expr := range syntax.UnpackListExpr(lhs) {
		if n, ok := expr.(*syntax.Name); ok && n.Value == name {
			return true
		}
	}
	return false
}

func cfRecordDecls(decls []syntax.Decl, child syntax.Node, name string) bool {
	for _, decl := range decls {
		if decl == child {
			break
		}
		switch d := decl.(type) {
		case *syntax.ConstDecl:
			if cfRecordHasName(d.NameList, name) {
				return true
			}
		case *syntax.VarDecl:
			if cfRecordHasName(d.NameList, name) {
				return true
			}
		case *syntax.TypeDecl:
			if d.Name.Value == name {
				return true
			}
		}
	}
	return false
}

// cfCheckCond walks a const if condition the way cfRecordBuilder.foldCond
// does: an operand that folds to a constant bool is replaced by a literal
// in the record, so its names are skipped, while the names of every
// remaining operand reach the record and are checked.
func (fl *fileLower) cfCheckCond(e syntax.Expr, visit func(syntax.Node) bool) {
	if val, ok := typeConst(fl.pkg.info, e); ok && val.Kind() == constant.Bool {
		return
	}
	switch e := e.(type) {
	case *syntax.ParenExpr:
		fl.cfCheckCond(e.X, visit)
	case *syntax.Operation:
		fl.cfCheckCond(e.X, visit)
		if e.Y != nil {
			fl.cfCheckCond(e.Y, visit)
		}
	default:
		syntax.Inspect(e, visit)
	}
}

func cfPackageLevel(fl *fileLower, obj types2.Object) bool {
	pkg := fl.pkg.tpkg
	return obj.Pkg() == pkg && pkg.Scope().Lookup(obj.Name()) == obj
}

func cfObjKind(obj types2.Object) string {
	switch obj := obj.(type) {
	case *types2.TypeName:
		if obj.IsAlias() {
			return "type alias"
		}
		return "type"
	case *types2.Func:
		return "func"
	case *types2.Var:
		return "var"
	case *types2.Const:
		return "const"
	case *types2.PkgName:
		return "package"
	}
	return "name"
}

func (b *cfRecordBuilder) visit(n syntax.Node) bool {
	if s, ok := n.(*syntax.ConstIfStmt); ok {
		b.foldCond(s.Cond)
		if b.export {
			b.foldDeferredBools(s.Cond)
		}
		syntax.Inspect(s.Then, b.visit)
		if s.Else != nil {
			syntax.Inspect(s.Else, b.visit)
		}
		return false
	}
	return true
}

func (b *cfRecordBuilder) foldDeferredBools(e syntax.Expr) {
	b.fl.cfCheckCond(e, func(n syntax.Node) bool {
		id, ok := n.(*syntax.Name)
		if !ok || id.Pos().Base() == nil || b.folded[id] {
			return false
		}
		obj, ok := b.recordObject(id).(*types2.Const)
		if !ok || obj.Val().Kind() != constant.Bool || obj.Exported() || !cfPackageLevel(b.fl, obj) {
			return false
		}
		start := b.fl.off(id.Pos())
		b.subs = append(b.subs, splice{start: start, end: start + len(id.Value), text: strconv.FormatBool(constant.BoolVal(obj.Val()))})
		if b.folded == nil {
			b.folded = make(map[*syntax.Name]bool)
		}
		b.folded[id] = true
		return false
	})
}

func (fl *fileLower) cfFuncStart(off int) int {
	kw, ok := fl.keywords.Start(fl.src, token.FUNC, off)
	if !ok {
		base.Fatalf("logos: no func keyword before offset %d", off)
	}
	return kw
}

func shiftSplices(subs []splice, off int) []splice {
	out := make([]splice, 0, len(subs))
	for _, s := range subs {
		s.start += off
		s.end += off
		out = append(out, s)
	}
	return out
}

type cfSite struct {
	fun   syntax.Expr
	targs []types2.Type
	obj   *types2.Func
	pos   syntax.Pos
}

func (fl *fileLower) expandComptime() {
	// Native owner-package emission resolves call sites in the writer
	// through LogosCtSiteKeys. No text copy is expanded. Comptime-generic
	// definitions become unreachable stubs; their templates travel in the
	// export and each instance is checked in the defining package.
	// Forwarding definitions keep their bodies: the backend stencils them
	// and resolves their nested calls through the instance dictionary.
	// Eject renames each call site in its text only; EjectCtInstances
	// prints the named instances after they are checked.
	if fl.pkg.info.LogosCtImport != nil {
		for _, def := range fl.pkg.ctExp.defs {
			if def.file != fl.file || def.forward {
				continue
			}
			fl.cfStubDef(def)
		}
		if base.LogosEject != "" {
			fl.ejectCtSites()
		}
		return
	}
	parents := cfParents(fl.file)
	for _, site := range fl.cfScanSites(parents) {
		fl.expandSite(site)
	}
	fl.flushOuterFirst()
	fl.cfRetireDefs()
}

func (fl *fileLower) lowerComptime() {
	fl.expandComptime()
	fl.lowerConstFor()
	fl.snapshotConstSwitchCases()
}

func (fl *fileLower) endComptime() {
	fl.dropComptimeImport(nil)
	if fl.changed {
		fl.cfDemoteErasedImports()
	}
}

func (fl *fileLower) cfDemoteErasedImports() {
	used := fl.cfNamesInUse(nil)
	var textUsed map[string]bool
	if fl.ejectSites != nil {
		textUsed = fl.cfNamesInUse(fl.ejectSites)
	}
	for _, decl := range fl.file.DeclList {
		imp, ok := decl.(*syntax.ImportDecl)
		if !ok || imp.Path == nil {
			continue
		}
		local, ok := fl.cfImportLocalName(imp)
		if !ok {
			continue
		}
		if used[local] {
			if textUsed != nil && !textUsed[local] {
				fl.ejectBlankImport(imp)
			}
			continue
		}
		path, _ := strconv.Unquote(imp.Path.Value)
		if fl.genImps != nil {
			if _, ok := fl.genImps.qual[path]; ok {
				fl.importsPendingGeneratedUse = append(fl.importsPendingGeneratedUse, imp)
				continue
			}
		}
		fl.cfDemoteImport(imp, path)
	}
}

func (fl *fileLower) cfDemoteImport(imp *syntax.ImportDecl, path string) {
	if _, ejectUsed := ownedEjectImports[fl.file][path]; !ejectUsed {
		fl.cfBlankImportText(imp)
	}
	imp.LocalPkgName = nameOf(imp.Path.Pos(), "_")
	fl.changed = true
}

func (fl *fileLower) cfDemoteImportsUnusedByGenerated() {
	used := fl.cfNamesInUse(nil)
	for _, imp := range fl.importsPendingGeneratedUse {
		local, ok := fl.cfImportLocalName(imp)
		if !ok || used[local] {
			continue
		}
		path, _ := strconv.Unquote(imp.Path.Value)
		fl.cfDemoteImport(imp, path)
	}
	fl.importsPendingGeneratedUse = nil
}

func (fl *fileLower) cfNamesInUse(skip map[syntax.Node]bool) map[string]bool {
	used := make(map[string]bool)
	var visit func(syntax.Node) bool
	visit = func(n syntax.Node) bool {
		if skip[n] {
			return false
		}
		switch n := n.(type) {
		case *syntax.ImportDecl:
			return false
		case *syntax.SelectorExpr:
			// A qualifier counts only when it resolves to a package
			// name: a local declaration that shadows an import alias
			// turns the selector into a field access and must not keep
			// the import alive. An unresolvable name (generated code)
			// keeps the import.
			if x, ok := n.X.(*syntax.Name); ok {
				if obj, found := fl.pkg.info.Uses[x]; found {
					if pn, ok := obj.(*types2.PkgName); ok {
						used[pn.Name()] = true
					}
				} else {
					used[x.Value] = true
				}
			}
			syntax.Inspect(n.X, visit)
			return false
		}
		return true
	}
	syntax.Inspect(fl.file, visit)
	return used
}

func (fl *fileLower) cfImportLocalName(imp *syntax.ImportDecl) (string, bool) {
	var obj types2.Object
	if imp.LocalPkgName != nil {
		obj = fl.pkg.info.Defs[imp.LocalPkgName]
	} else {
		obj = fl.pkg.info.Implicits[imp]
	}
	pn, ok := obj.(*types2.PkgName)
	if ok && pn.Name() != "_" && pn.Name() != "." && pn.Imported().Path() != "comptime" {
		return pn.Name(), true
	}
	if imp.LocalPkgName != nil && imp.LocalPkgName.Value != "" && imp.LocalPkgName.Value != "_" && imp.LocalPkgName.Value != "." {
		return imp.LocalPkgName.Value, true
	}
	return "", false
}

func (fl *fileLower) cfBlankImportText(imp *syntax.ImportDecl) {
	if imp.LocalPkgName != nil {
		start := fl.off(imp.LocalPkgName.Pos())
		fl.add(start, start+len(imp.LocalPkgName.Value), "_")
	} else {
		start := fl.off(imp.Path.Pos())
		fl.add(start, start, "_ ")
	}
}

func (fl *fileLower) cfScanSites(parents map[syntax.Node]syntax.Node) []*cfSite {
	var out []*cfSite
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		var fun syntax.Expr
		var pos syntax.Pos
		switch n := n.(type) {
		case *syntax.CallExpr:
			fun, pos = syntax.Unparen(n.Fun), n.Pos()
			if _, indexed := fun.(*syntax.IndexExpr); indexed {
				return true
			}
		case *syntax.IndexExpr:
			fun, pos = n, n.Pos()
			if call, ok := parents[n].(*syntax.CallExpr); ok && call.Fun == n {
				pos = call.Pos()
			}
		default:
			return true
		}
		if site := fl.cfSiteOf(fun, pos, parents); site != nil {
			out = append(out, site)
			_, indexed := n.(*syntax.IndexExpr)
			return !indexed
		}
		return true
	})
	return out
}

func (fl *fileLower) cfSiteOf(fun syntax.Expr, pos syntax.Pos, parents map[syntax.Node]syntax.Node) *cfSite {
	name := cfCalleeName(fun)
	if name == nil {
		return nil
	}
	inst, ok := fl.pkg.info.Instances[name]
	obj, isFunc := fl.pkg.info.Uses[name].(*types2.Func)
	if !ok || !isFunc || !fl.cfIsGeneric(obj) || fl.cfHandledElsewhere(fun, parents) {
		return nil
	}
	targs := typeArgsOf(inst.TypeArgs)
	if slices.ContainsFunc(targs, cfHasTypeParam) {
		fl.cfError(pos, "[comptime::generic_targ] cannot instantiate %s with a type parameter", name.Value)
		return nil
	}
	return &cfSite{fun: fun, targs: targs, obj: obj, pos: pos}
}

func (fl *fileLower) cfHandledElsewhere(n syntax.Node, parents map[syntax.Node]syntax.Node) bool {
	for p := parents[n]; p != nil; p = parents[p] {
		switch p := p.(type) {
		case *syntax.ConstForStmt:
			return true
		case *syntax.FuncDecl:
			_, isDef := fl.pkg.ctExp.byDecl[p]
			return isDef
		}
	}
	return false
}

func cfCalleeName(fun syntax.Expr) *syntax.Name {
	if idx, ok := fun.(*syntax.IndexExpr); ok {
		fun = idx.X
	}
	switch x := fun.(type) {
	case *syntax.Name:
		return x
	case *syntax.SelectorExpr:
		return x.Sel
	}
	return nil
}

func typeArgsOf(l *types2.TypeList) []types2.Type {
	out := make([]types2.Type, l.Len())
	for i := range l.Len() {
		out[i] = l.At(i)
	}
	return out
}

func (fl *fileLower) cfIsGeneric(obj *types2.Func) bool {
	if _, ok := fl.pkg.ctExp.byObj[obj]; ok {
		return true
	}
	if obj.Pkg() == nil || obj.Pkg() == fl.pkg.tpkg {
		return false
	}
	f, ok := facts.Lookup(obj.Pkg().Path())
	if !ok {
		return false
	}
	return slices.ContainsFunc(f.Comptimes, func(c facts.ComptimeFact) bool { return c.Object == cfFactObject(obj) })
}

func (fl *fileLower) cfRecordFor(obj *types2.Func) (*cfRecord, bool) {
	exp := fl.pkg.ctExp
	if def, ok := exp.byObj[obj]; ok {
		return def.record, true
	}
	key := obj.Pkg().Path() + "\x00" + cfFactObject(obj)
	if rec, ok := exp.foreign[key]; ok {
		return rec, true
	}
	f, ok := facts.Lookup(obj.Pkg().Path())
	if !ok {
		return nil, false
	}
	for _, c := range f.Comptimes {
		if c.Object != cfFactObject(obj) {
			continue
		}
		rec, err := cfParseRecord(obj.Name(), string(c.Source))
		if err != nil {
			base.Fatalf("logos: bad comptime record for %s: %v", obj.Name(), err)
		}
		exp.foreign[key] = rec
		return rec, true
	}
	return nil, false
}

func (fl *fileLower) expandSite(site *cfSite) {
	name := fl.cfCopyFor(site)
	ctx := cfCtx{buf: fl.srcMap, starts: fl.start}
	fl.add(offAt(fl.start, syntax.StartPos(site.fun)), cfExactEnd(ctx, site.fun), name)
	replacement := nameOf(syntax.StartPos(site.fun), name)
	replacement.SetTypeInfo(site.fun.GetTypeInfo())
	fl.stageReplacement(site.fun, replacement)
	fl.changed = true
}

func cfCopyName(obj *types2.Func, seq int) string {
	return obj.Name() + cfCopyInfix + strconv.Itoa(seq)
}

func (fl *fileLower) cfCopyFor(site *cfSite) string {
	exp := fl.pkg.ctExp
	key := cfExpKey(cfFactObject(site.obj), site.obj.Pkg().Path(), site.targs)
	if name, ok := exp.memo[key]; ok {
		return name
	}
	label := cfShortKeys(site.targs)
	if i := exp.frameIndex(key); i >= 0 {
		fl.cfError(site.pos, "[comptime::circular_instantiation] %s", cfChainText(exp.stack[i:], label))
		return cfCopyName(site.obj, exp.seq)
	}
	rec, ok := fl.cfRecordFor(site.obj)
	if !ok {
		fl.cfError(site.pos, "[comptime::generic_targ] no comptime source for %s", site.obj.Name())
		return cfCopyName(site.obj, exp.seq)
	}
	if pkg := site.obj.Pkg(); pkg != fl.pkg.tpkg && len(rec.owner) != 0 {
		for _, n := range rec.owner {
			fl.cfOwnerOnly(site.pos, n.kind, n.name, pkg.Path())
		}
		return cfCopyName(site.obj, exp.seq)
	}
	name := cfCopyName(site.obj, exp.seq)
	exp.seq++
	def := exp.byObj[site.obj]
	exp.stack = append(exp.stack, cfFrame{key: key, label: label, forward: def != nil && def.forward})
	reported := base.Errors()
	text, ok := fl.cfBuildCopy(site, rec, name)
	exp.stack = exp.stack[:len(exp.stack)-1]
	exp.memo[key] = name
	if ok && base.Errors() == reported {
		fl.cfEmitCopy(text, site)
	}
	return name
}

func cfChainText(frames []cfFrame, label string) string {
	parts := make([]string, 0, len(frames)+1)
	for _, f := range frames {
		parts = append(parts, f.label)
	}
	return strings.Join(append(parts, label), " -> ")
}

func cfTParamSpan(src string) (span, bool) {
	want := []token.Token{token.FUNC, token.IDENT, token.LBRACK}
	var open, depth, end int
	b := []byte(src)
	scanSegment(b, 0, len(b), func(t tok) bool {
		switch {
		case t.comment:
		case len(want) > 0:
			if t.kind != want[0] {
				return false
			}
			if want[0] == token.LBRACK {
				open, depth = t.start, 1
			}
			want = want[1:]
		case t.kind == token.LBRACK:
			depth++
		case t.kind == token.RBRACK:
			depth--
			if depth == 0 {
				end = t.end
				return false
			}
		}
		return true
	})
	return span{open, end}, end != 0
}

func (fl *fileLower) cfBuildCopy(site *cfSite, rec *cfRecord, copyName string) (otext, bool) {
	srcO := rec.sourceO
	if srcO.s == "" {
		srcO = gen(rec.source)
	}
	params, ok := cfTParamSpan(srcO.s)
	if !ok {
		fl.cfError(site.pos, "[comptime::generic_targ] malformed comptime source of %s", rec.name)
		return otext{}, false
	}
	const prefix = "package cfcopy\n"
	buf := cat(gen(prefix), srcO)
	src := []byte(buf.s)
	file, errs := cfParseSource(buf.s)
	fn := cfFirstFunc(file)
	if len(errs) > 0 || fn == nil {
		base.Fatalf("logos: cannot parse comptime source of %s: %v", rec.name, errs)
	}
	sites, info, ok := fl.cfCheckRecord(fn, copyName, site, rec)
	if !ok {
		return otext{}, false
	}
	run := &cfRun{
		ctx:     cfCtx{buf: srcBuf{b: src, chunks: buf.chunks}, starts: lineStarts(src), stamp: site.pos},
		info:    info,
		parents: cfParents(fn),
		sites:   sites,
	}
	params.start += len(prefix)
	params.end += len(prefix)
	subs := cfCaseShadowDecls(fn, run)
	subs = append(subs, fl.cfCopySplices(fn, copyName, run)...)
	subs = append(subs, splice{start: params.start, end: params.end})
	out, err := applyChunks(buf, subs)
	if err != nil {
		base.Fatalf("logos: overlapping comptime copy %s: %v", copyName, err)
	}
	return out.slice(len(prefix), len(out.s)), true
}

func (fl *fileLower) cfCopySplices(fn *syntax.FuncDecl, copyName string, run *cfRun) []splice {
	ctx := run.ctx
	nameStart := offAt(ctx.starts, fn.Name.Pos())
	subs := []splice{{start: nameStart, end: nameStart + len(fn.Name.Value), text: copyName}}
	var covered []span
	for _, cf := range cfInnerConstFors(fn.Body, run.info) {
		s := offAt(ctx.starts, syntax.StartPos(cf))
		e := offAt(ctx.starts, syntax.EndPos(cf)) + 1
		stmts, text := fl.unrollConstForFields(cf, run)
		fl.cfCountNodes(stmts, run.sites.at(cf.Pos()))
		subs = append(subs, splice{start: s, end: e, text: text.s, chunks: text.chunks})
		covered = append(covered, span{s, e})
	}
	for _, sub := range fl.cfLeafSubs(fn, nil, covered, run) {
		subs = append(subs, splice{start: sub.start, end: sub.end, text: sub.text})
	}
	return subs
}

// cfCaseShadowDecls marks each case of a const switch over a type
// parameter of the copy and declares the parameters' binds at the top of
// its body. Inside a marked case every parameter reference spells its
// bind (cfBind), so a case body type decl cannot capture the argument's
// spelling, an unnamed argument such as `*int` stays a conversion
// target, and an argument spelled like a parameter keeps the caller's
// meaning.
func cfCaseShadowDecls(fn *syntax.FuncDecl, run *cfRun) []splice {
	sites := run.sites
	sites.shadowed = cfShadowedCases(fn.Body, run.info, run)
	if len(sites.shadowed) == 0 {
		return nil
	}
	var text strings.Builder
	for i, obj := range sites.tparams {
		spelled, ok := sites.bindings[obj]()
		if !ok {
			sites.shadowed = nil
			return nil
		}
		fmt.Fprintf(&text, " type %s = %s;", cfBind(i), spelled)
	}
	p := offAt(run.ctx.starts, syntax.StartPos(fn.Body)) + 1
	return []splice{{start: p, end: p, text: text.String()}}
}

// cfShadowedCases returns the spans of the cases of every const switch
// over a type parameter under root. A const for body is read through the
// info of its first checked iteration: the copy check records the body
// only per iteration.
func cfShadowedCases(root syntax.Node, info *types2.Info, run *cfRun) []span {
	var out []span
	starts := run.ctx.starts
	syntax.Inspect(root, func(n syntax.Node) bool {
		switch s := n.(type) {
		case *syntax.ConstForStmt:
			loop := info.LogosConstFor[s]
			if loop == nil || loop.Struct == nil {
				return true
			}
			if i := slices.IndexFunc(loop.Iterations, func(it *types2.Info) bool { return it != nil }); i >= 0 {
				out = append(out, cfShadowedCases(s.Body, loop.Iterations[i], run)...)
			}
			return false
		case *syntax.ConstSwitchStmt:
			tag, ok := syntax.Unparen(s.Targ).(*syntax.Name)
			if obj := info.Uses[tag]; !ok || obj == nil || !slices.Contains(run.sites.tparams, obj) {
				return true
			}
			for i, cs := range s.Cases {
				end := offAt(starts, s.Rbrace)
				if i+1 < len(s.Cases) {
					end = offAt(starts, s.Cases[i+1].Case)
				}
				out = append(out, span{start: offAt(starts, cs.Case), end: end})
			}
		}
		return true
	})
	return out
}

// Check the original source with concrete bindings before any splice can
// discard a branch or reuse one syntax node for multiple emitted copies.
func (fl *fileLower) cfCheckRecord(fn *syntax.FuncDecl, copyName string, site *cfSite, rec *cfRecord) (*cfSites, *types2.Info, bool) {
	params := fn.TParamList
	if len(site.targs) != len(params) {
		fl.cfError(site.pos, "[comptime::generic_targ] %s needs %d type arguments, have %d", rec.name, len(params), len(site.targs))
		return nil, nil, false
	}
	origin := site.obj.Pkg()
	fl.pkg.ctExp.roots = append(fl.pkg.ctExp.roots, origin)
	ctx := types2.LogosCopyContext{
		Scope:     origin.Scope(),
		Version:   fl.pkg.info.FileVersions[fl.file.Pos().FileBase()],
		NodeLimit: base.LogosComptimeNodes,
		Sizes:     types2.SizesFor("gc", buildcfg.GOARCH),
		Record:    true,
		Bind: types2.LogosTypeBindings{
			Types: make(map[string]types2.Type, len(params)),
			Pkgs:  make(map[string]*types2.Package, len(rec.imports)),
		},
	}
	for i, p := range params {
		ctx.Bind.Types[p.Name.Value] = site.targs[i]
	}
	for _, imp := range rec.imports {
		if imp.path == "comptime" {
			ctx.Bind.Pkgs[imp.name] = nil
			continue
		}
		pkg, err := fl.cfLoadPackage(imp.path)
		if err != nil {
			fl.cfError(site.pos, "[comptime::generic_import] cannot import %s for the copy: %v", imp.path, err)
			return nil, nil, false
		}
		ctx.Bind.Pkgs[imp.name] = pkg
	}
	info := &types2.Info{
		Types:             make(map[syntax.Expr]types2.TypeAndValue),
		Uses:              make(map[*syntax.Name]types2.Object),
		Scopes:            make(map[syntax.Node]*types2.Scope),
		Instances:         make(map[*syntax.Name]types2.Instance),
		LogosImporter:     fl.pkg.info.LogosImporter,
		LogosContext:      fl.pkg.info.LogosContext,
		LogosCTFEMaxSteps: fl.pkg.info.LogosCTFEMaxSteps,
		LogosCTFE:         maps.Clone(fl.pkg.info.LogosCTFE),
		LogosConstFuncs:   maps.Clone(fl.pkg.info.LogosConstFuncs),
	}
	originalName := fn.Name.Value
	fn.Name.Value, fn.TParamList = copyName, nil
	_, errs := types2.LogosCheckCopyFunc(fl.pkg.tpkg, info, ctx, fn)
	fn.Name.Value = originalName
	for _, err := range errs {
		fl.cfError(site.pos, "%s", err.Msg)
	}
	if len(errs) != 0 {
		return nil, nil, false
	}
	sites := &cfSites{
		bindings: make(map[types2.Object]func() (string, bool), len(params)+len(rec.imports)),
		tparams:  make([]types2.Object, len(params)),
		errPos:   site.pos,
	}
	bound := info.Scopes[fn.Type].Parent()
	for i, p := range params {
		t := site.targs[i]
		obj := bound.Lookup(p.Name.Value)
		sites.tparams[i] = obj
		sites.bindings[obj] = fl.cfLazySpelling(func() (string, bool) { return fl.spellTypeAs(t, copyTypeSpelling) }, func() {
			fl.cfError(site.pos, "[comptime::generic_targ] cannot spell type argument %s of %s", t, rec.name)
		})
	}
	for _, imp := range rec.imports {
		if pn, ok := bound.Lookup(imp.name).(*types2.PkgName); ok && imp.path != "comptime" {
			sites.bindings[pn] = fl.cfLazySpelling(func() (string, bool) { return fl.cfCopyQualifier(imp.path, site.pos) }, nil)
		}
	}
	return sites, info, true
}

// cfCopyQualifier spells path for one copied body. A name in a copied
// body resolves to the same object as in the defining package.
func (fl *fileLower) cfCopyQualifier(path string, pos syntax.Pos) (string, bool) {
	q, ok := fl.cfImportQualifier(path, pos)
	if !ok {
		return "", false
	}
	if strings.HasPrefix(q, "_logos_") {
		return q, true
	}
	return fl.cfFreshCopyAlias(path, pos)
}

// cfFreshCopyAlias mints the reserved import alias the copy carries
// for path.
func (fl *fileLower) cfFreshCopyAlias(path string, pos syntax.Pos) (string, bool) {
	reg := fl.getGenImports()
	pkg := reg.pkgs[path]
	if pkg == nil {
		var err error
		if pkg, err = fl.cfLoadPackage(path); err != nil || pkg == nil {
			fl.cfError(pos, "[comptime::generic_import] cannot import %s for the copy: %v", path, err)
			return "", false
		}
		reg.pkgs[path] = pkg
	}
	var q string
	for i := 0; ; i++ {
		q = fmt.Sprintf("_logos_t%d", i)
		if !reg.used[q] {
			break
		}
	}
	reg.used[q] = true
	reg.qual[path] = q
	if !slices.Contains(reg.order, path) {
		reg.order = append(reg.order, path)
	}
	return q, true
}

func (fl *fileLower) cfRuntimeBindings() types2.LogosTypeBindings {
	if !fl.needReflect {
		return types2.LogosTypeBindings{}
	}
	return types2.LogosTypeBindings{Pkgs: map[string]*types2.Package{reflectAlias: fl.pkg.ctExp.pkgs["reflect"]}}
}

func (fl *fileLower) cfLazySpelling(compute func() (string, bool), fail func()) func() (string, bool) {
	var text string
	var ok, done bool
	return func() (string, bool) {
		if !done {
			done = true
			if text, ok = compute(); !ok && fail != nil {
				fail()
			}
		}
		return text, ok
	}
}

func (fl *fileLower) cfImportQualifier(path string, pos syntax.Pos) (string, bool) {
	if reg := fl.getGenImports(); reg != nil {
		if q, ok := reg.qual[path]; ok && q != "" {
			return q, true
		}
	}
	var own []string
	for pkg, spelling := range fl.importNames() {
		if pkg.Path() == path && spelling != "" {
			own = append(own, spelling)
		}
	}
	if len(own) > 0 {
		slices.Sort(own)
		return own[0], true
	}
	exp := fl.pkg.ctExp
	pkg := exp.pkgs[path]
	if pkg == nil {
		var err error
		if pkg, err = fl.cfLoadPackage(path); err != nil {
			fl.cfError(pos, "[comptime::generic_import] cannot import %s for the copy: %v", path, err)
			return "", false
		}
		exp.pkgs[path] = pkg
	}
	return fl.synthQualifier(pkg), true
}

func (fl *fileLower) cfLoadPackage(path string) (*types2.Package, error) {
	if pkg := fl.cfKnownPackage(path); pkg != nil && pkg.Complete() {
		return pkg, nil
	}
	imp := fl.pkg.info.LogosImporter
	if imp == nil {
		return nil, errors.New("the check recorded no importer")
	}
	return imp.Import(path)
}

func (fl *fileLower) cfKnownPackage(path string) *types2.Package {
	roots := append([]*types2.Package{fl.pkg.tpkg}, fl.pkg.ctExp.roots...)
	for _, root := range roots {
		if root.Path() == path {
			return root
		}
		for _, imp := range root.Imports() {
			if imp.Path() == path {
				return imp
			}
		}
	}
	return nil
}

func (fl *fileLower) cfNestedSite(call *syntax.CallExpr, run *cfRun) (int, int, string, bool) {
	fun, indexed := call.Fun.(*syntax.IndexExpr)
	callee := call.Fun
	if indexed {
		callee = fun.X
	}
	calleeName := cfCalleeName(callee)
	obj, _ := run.info.Uses[calleeName].(*types2.Func)
	if obj == nil || !fl.cfIsGeneric(obj) {
		return 0, 0, "", false
	}
	pos := run.sites.at(call.Pos())
	stack := fl.pkg.ctExp.stack
	inForwarder := len(stack) != 0 && stack[len(stack)-1].forward
	if !indexed && !inForwarder {
		fl.cfError(pos, "[comptime::generic_targ] comptime generic %s needs explicit type arguments here", obj.Name())
		return 0, 0, "", false
	}
	instance := run.info.Instances[calleeName]
	targs := typeArgsOf(instance.TypeArgs)
	if indexed && len(targs) != len(syntax.UnpackListExpr(fun.Index)) {
		fl.cfError(pos, "[comptime::generic_targ] cannot resolve the type arguments of %s", obj.Name())
		return 0, 0, "", false
	}
	if fl.pkg.info.LogosCtImport != nil && cfTargsMentionLocal(targs) {
		// Native owner-package emission resolves nested call sites in
		// the writer through LogosCtSiteKeys paired during unrolling.
		// A text copy is checked where a function-local type argument
		// is not in scope, so leave the call spelled and emit no copy.
		return 0, 0, "", false
	}
	name := fl.cfCopyFor(&cfSite{fun: call.Fun, targs: targs, obj: obj, pos: pos})
	return offAt(run.ctx.starts, syntax.StartPos(call.Fun)), cfExactEnd(run.ctx, call.Fun), name, true
}

func (fl *fileLower) cfEmitCopy(text otext, site *cfSite) {
	file, errs := cfParseSource("package cfcopy\n" + text.s)
	fn := cfFirstFunc(file)
	if len(errs) > 0 || fn == nil {
		base.Fatalf("logos: cannot parse comptime copy at %s: %v", site.pos.String(), errs)
	}
	if !fl.cfCheckCopyFunc(fn, site.pos) {
		return
	}
	region := fl.adoptRegion(text, site.pos, fn.Pos().FileBase(), 2, []syntax.Node{fn})
	fl.file.DeclList = append(fl.file.DeclList, fn)
	fl.addHost(fl.orig, fl.orig, &cfHost{regions: []*cfRegion{region}, head: "\n\n", tail: "\n"})
}

func (fl *fileLower) cfCheckCopyFunc(fn *syntax.FuncDecl, pos syntax.Pos) bool {
	exp := fl.pkg.ctExp
	ctx := types2.LogosCopyContext{
		Scope:     fl.pkg.info.Scopes[fl.file],
		Funcs:     exp.copyFuncs,
		Aliases:   fl.cfAliases(),
		MaxSteps:  fl.pkg.info.LogosCTFEMaxSteps,
		NodeLimit: base.LogosComptimeNodes,
		Importer:  fl.pkg.info.LogosImporter,
		Context:   fl.pkg.info.LogosContext,
		Bind:      fl.cfRuntimeBindings(),
	}
	obj, errs := types2.LogosCheckCopyFunc(fl.pkg.tpkg, fl.pkg.info, ctx, fn)
	if obj != nil {
		exp.copyFuncs = append(exp.copyFuncs, obj)
	}
	for _, err := range errs {
		fl.cfError(pos, "%s", err.Msg)
	}
	return len(errs) == 0
}

func (fl *fileLower) cfRetireDefs() {
	for _, def := range fl.pkg.ctExp.defs {
		if def.file != fl.file {
			continue
		}
		if def.obj != nil && def.obj.Exported() {
			fl.cfStubDef(def)
		} else {
			fl.cfDeleteDef(def.decl)
		}
	}
}

func (fl *fileLower) cfStubDef(def *cfGenericDef) {
	fn := def.decl
	call := "panic(" + strconv.Quote("logos: comptime generic "+fn.Name.Value+" runs only through loGo/s instantiation") + ")"
	fn.Body.List = fl.reparseStmts(call, fn.Pos())
	fl.add(fl.off(syntax.StartPos(fn.Body)), fl.off(syntax.EndPos(fn.Body))+1, "{\n\t"+call+"\n}")
	fl.changed = true
}

func (fl *fileLower) cfDeleteDef(decl *syntax.FuncDecl) {
	fl.file.DeclList = slices.DeleteFunc(fl.file.DeclList, func(d syntax.Decl) bool { return d == syntax.Decl(decl) })
	fl.add(fl.cfFuncStart(fl.off(syntax.StartPos(decl))), fl.off(syntax.EndPos(decl))+1, "")
	fl.changed = true
}

func cfHasTypeParam(t types2.Type) bool {
	switch t := t.(type) {
	case *types2.TypeParam:
		return true
	case *types2.Pointer:
		return cfHasTypeParam(t.Elem())
	case *types2.Slice:
		return cfHasTypeParam(t.Elem())
	case *types2.Array:
		return cfHasTypeParam(t.Elem())
	case *types2.Map:
		return cfHasTypeParam(t.Key()) || cfHasTypeParam(t.Elem())
	case *types2.Chan:
		return cfHasTypeParam(t.Elem())
	case *types2.Named:
		return slices.ContainsFunc(typeArgsOf(t.TypeArgs()), cfHasTypeParam)
	}
	return false
}

// cfTargsMentionLocal reports whether any type argument names a type
// declared below package scope (for example a function-local type). A
// text copy is checked at package scope and can never spell such an
// argument; those call sites lower natively instead.
func cfTargsMentionLocal(targs []types2.Type) bool {
	for _, t := range targs {
		if cfTypeMentionsLocal(t) {
			return true
		}
	}
	return false
}

func cfTypeMentionsLocal(t types2.Type) bool {
	switch t := t.(type) {
	case *types2.Named:
		if old := t.Obj(); old != nil && old.Pkg() != nil && old.Parent() != nil && old.Parent() != old.Pkg().Scope() {
			return true
		}
		return slices.ContainsFunc(typeArgsOf(t.TypeArgs()), cfTypeMentionsLocal)
	case *types2.Alias:
		if old := t.Obj(); old != nil && old.Pkg() != nil && old.Parent() != nil && old.Parent() != old.Pkg().Scope() {
			return true
		}
		return cfTypeMentionsLocal(t.Rhs())
	case *types2.Pointer:
		return cfTypeMentionsLocal(t.Elem())
	case *types2.Slice:
		return cfTypeMentionsLocal(t.Elem())
	case *types2.Array:
		return cfTypeMentionsLocal(t.Elem())
	case *types2.Map:
		return cfTypeMentionsLocal(t.Key()) || cfTypeMentionsLocal(t.Elem())
	case *types2.Chan:
		return cfTypeMentionsLocal(t.Elem())
	case *types2.Option:
		return cfTypeMentionsLocal(t.Elem)
	case *types2.ConstView:
		return cfTypeMentionsLocal(t.Elem)
	case *types2.Signature:
		if t.Params() != nil {
			for i := range t.Params().Len() {
				if cfTypeMentionsLocal(t.Params().At(i).Type()) {
					return true
				}
			}
		}
		if t.Results() != nil {
			for i := range t.Results().Len() {
				if cfTypeMentionsLocal(t.Results().At(i).Type()) {
					return true
				}
			}
		}
	}
	return false
}

// reduceComptimeInstance removes the compile-time forms from a detached
// declaration. Its Info still describes LoGOs types; it is not the final
// plain-Go check of the lowered instance.
func (fl *fileLower) reduceComptimeInstance(rec *types2.LogosCtInst) (*syntax.FuncDecl, *types2.Info, error) {
	old := rec.Info
	info := &types2.Info{
		Types:                      make(map[syntax.Expr]types2.TypeAndValue),
		Defs:                       make(map[*syntax.Name]types2.Object),
		Uses:                       make(map[*syntax.Name]types2.Object),
		Instances:                  make(map[*syntax.Name]types2.Instance),
		Selections:                 make(map[*syntax.SelectorExpr]*types2.Selection),
		Implicits:                  make(map[syntax.Node]types2.Object),
		Scopes:                     make(map[syntax.Node]*types2.Scope),
		LogosImporter:              old.LogosImporter,
		LogosContext:               old.LogosContext,
		LogosCTFEMaxSteps:          old.LogosCTFEMaxSteps,
		LogosCTFE:                  maps.Clone(old.LogosCTFE),
		LogosConstFuncs:            maps.Clone(old.LogosConstFuncs),
		LogosFieldDefaults:         maps.Clone(old.LogosFieldDefaults),
		LogosCtImportNamedDefaults: old.LogosCtImportNamedDefaults,
		LogosCtImportConstMap:      old.LogosCtImportConstMap,
		LogosCtDefaultOrigin:       old.LogosCtDefaultOrigin,
	}
	name := nameOf(rec.Decl.Name.Pos(), rec.Sym)
	fn := types2.NewFunc(name.Pos(), rec.Pkg, rec.Sym, rec.Sig)
	info.Defs[name] = fn
	r := ctReducer{fl: fl, owner: rec.Pkg, info: info, binders: make(map[types2.Object]types2.Type, len(rec.Decl.TParamList))}
	for i, param := range rec.Decl.TParamList {
		if binder := fn.Scope().Lookup(param.Name.Value); binder != nil && i < len(rec.Targs) {
			r.binders[binder] = rec.Targs[i]
		}
	}
	frame := newCtFrame(old, nil)
	body := r.clone(rec.Decl.Body, frame).(*syntax.BlockStmt)
	orig := rec.Decl.Type
	typ := at(&syntax.FuncType{
		ParamList:  r.signatureFields(orig.ParamList, rec.Sig.Params(), rec.Sig.Variadic()),
		ResultList: r.signatureFields(orig.ResultList, rec.Sig.Results(), false),
	}, orig.Pos())
	typ.SetTypeInfo(syntax.TypeAndValue{Type: rec.Sig})
	info.Scopes[typ] = fn.Scope()
	info.Scopes[body] = old.Scopes[rec.Decl]
	decl := at(&syntax.FuncDecl{Name: name, Type: typ, Body: body}, rec.Decl.Pos())
	if r.err != nil {
		return nil, nil, r.err
	}
	return decl, info, nil
}

type ctReducer struct {
	fl        *fileLower
	owner     *types2.Package
	info      *types2.Info
	binders   map[types2.Object]types2.Type
	nextLabel int
	err       error
}

type ctFrame struct {
	info      *types2.Info
	outer     *ctFrame
	nodes     map[syntax.Node]syntax.Node
	groups    map[*syntax.Group]*syntax.Group
	active    cfChain
	labels    map[string]string
	keepCalls map[*syntax.CallExpr]bool
}

func newCtFrame(info *types2.Info, outer *ctFrame) *ctFrame {
	return &ctFrame{info: info, outer: outer, nodes: make(map[syntax.Node]syntax.Node)}
}

func (r *ctReducer) signatureFields(fields []*syntax.Field, tuple *types2.Tuple, variadic bool) []*syntax.Field {
	if len(fields) == 0 {
		return nil
	}
	out := make([]*syntax.Field, len(fields))
	for i, src := range fields {
		obj := tuple.At(i)
		typ := obj.Type()
		if variadic && i == len(fields)-1 {
			typ = typ.(*types2.Slice).Elem()
		}
		e := r.fl.ctTypeExpr(src.Type.Pos(), typ)
		if variadic && i == len(fields)-1 {
			e = at(&syntax.DotsType{Elem: e}, src.Type.Pos())
		}
		dst := at(&syntax.Field{Type: e}, src.Pos())
		if src.Name != nil {
			dst.Name = nameOf(src.Name.Pos(), src.Name.Value)
			r.info.Defs[dst.Name] = obj
		}
		out[i] = dst
	}
	return out
}

func (r *ctReducer) invalid(n syntax.Node, why string) {
	if r.err == nil {
		r.err = fmt.Errorf("%s: %s", n.Pos(), why)
	}
}

func (r *ctReducer) clone(n syntax.Node, f *ctFrame) syntax.Node {
	if n == nil {
		return nil
	}
	if known, ok := f.nodes[n]; ok {
		return known
	}
	if stmt, ok := n.(syntax.Stmt); ok {
		if checked := f.info.LogosYieldForms[stmt]; checked != nil {
			out := r.clone(checked, f)
			f.nodes[n] = out
			r.copyFacts(n, out, f)
			return out
		}
	}
	var out syntax.Node
	switch n := n.(type) {
	case *syntax.ConstIfStmt:
		then, ok := f.info.LogosConstIf[n]
		if !ok {
			r.invalid(n, "missing concrete const if decision")
		}
		if then {
			out = r.clone(n.Then, f)
		} else {
			out = r.clone(n.Else, f)
		}
		if out == nil {
			out = blockOf(n.Pos(), nil)
		}
	case *syntax.ConstSwitchStmt:
		sel, static := types2.LogosConstSelectedCase(f.info, n)
		if !static {
			r.invalid(n, "missing concrete const switch decision")
		}
		body := blockOf(n.Lbrace, nil)
		body.Rbrace = n.Rbrace
		if sel != nil {
			body.List = r.stmts(sel.Body, f)
		}
		out = body
	case *syntax.ConstForStmt:
		out = r.constFor(n, f)
	case *syntax.FieldProjExpr:
		out = r.projectField(n, f)
	case syntax.Expr:
		out = r.reduceExpr(n, f)
	case *syntax.AssignStmt:
		if r.blankFieldAssign(n, f) {
			out = at(&syntax.EmptyStmt{}, n.Pos())
		}
		if n.Op != syntax.Def {
			r.checkIndexStore(n.Lhs, f)
		}
	case *syntax.RangeClause:
		if !n.Def {
			r.checkIndexStore(n.Lhs, f)
		}
	case *syntax.CallStmt:
		if call, ok := syntax.Unparen(n.Call).(*syntax.CallExpr); ok && f.info.LogosTagGet[call].Value != nil {
			if f.keepCalls == nil {
				f.keepCalls = make(map[*syntax.CallExpr]bool)
			}
			f.keepCalls[call] = true
		}
	case *syntax.ExprStmt:
		if call, ok := syntax.Unparen(n.X).(*syntax.CallExpr); ok && f.info.LogosTagGet[call].Value != nil {
			out = assignOf(n.Pos(), []syntax.Expr{nameOf(n.Pos(), "_")}, []syntax.Expr{r.clone(call, f).(syntax.Expr)})
		}
	}
	if out == nil {
		out = r.cloneNode(n, f)
	}
	f.nodes[n] = out
	r.copyFacts(n, out, f)
	return out
}

func (r *ctReducer) stmts(in []syntax.Stmt, f *ctFrame) []syntax.Stmt {
	if len(in) == 0 {
		return nil
	}
	out := make([]syntax.Stmt, 0, len(in))
	for _, s := range in {
		if !f.info.LogosConstIfDead[s] {
			out = append(out, r.clone(s, f).(syntax.Stmt))
		}
	}
	return out
}

var ctStmtListType = reflect.TypeFor[[]syntax.Stmt]()

func (r *ctReducer) cloneNode(n syntax.Node, f *ctFrame) syntax.Node {
	old := reflect.ValueOf(n).Elem()
	cloned := reflect.New(old.Type())
	dst := cloned.Elem()
	dst.Set(old)
	for i := range old.NumField() {
		field := old.Type().Field(i)
		if !field.IsExported() {
			continue
		}
		if _, branch := n.(*syntax.BranchStmt); branch && field.Name == "Target" {
			dst.Field(i).SetZero()
			continue
		}
		r.cloneField(old.Field(i), dst.Field(i), f)
	}
	out := cloned.Interface().(syntax.Node)
	if e, ok := out.(syntax.Expr); ok {
		e.SetTypeInfo(syntax.TypeAndValue{})
	}
	switch n := out.(type) {
	case *syntax.LabeledStmt:
		r.renameLabel(n.Label, f)
	case *syntax.BranchStmt:
		r.renameLabel(n.Label, f)
	}
	return out
}

func (r *ctReducer) cloneField(src, dst reflect.Value, f *ctFrame) {
	if src.Type() == ctStmtListType {
		dst.Set(reflect.ValueOf(r.stmts(src.Interface().([]syntax.Stmt), f)))
		return
	}
	switch src.Kind() {
	case reflect.Interface, reflect.Pointer:
		if src.IsNil() {
			return
		}
		switch val := src.Interface().(type) {
		case syntax.Node:
			dst.Set(reflect.ValueOf(r.clone(val, f)))
		case *syntax.Group:
			if f.groups == nil {
				f.groups = make(map[*syntax.Group]*syntax.Group)
			}
			group := f.groups[val]
			if group == nil {
				group = new(syntax.Group)
				f.groups[val] = group
			}
			dst.Set(reflect.ValueOf(group))
		}
	case reflect.Slice:
		if !src.IsNil() {
			dst.Set(reflect.MakeSlice(src.Type(), src.Len(), src.Len()))
			for i := range src.Len() {
				dst.Index(i).Set(src.Index(i))
				r.cloneField(src.Index(i), dst.Index(i), f)
			}
		}
	case reflect.Array:
		for i := range src.Len() {
			r.cloneField(src.Index(i), dst.Index(i), f)
		}
	}
}

func ctCopyFact[K comparable, V any](to *map[K]V, from map[K]V, old, new K) {
	if value, ok := from[old]; ok {
		if *to == nil {
			*to = make(map[K]V)
		}
		(*to)[new] = value
	}
}

func (r *ctReducer) copyFacts(old, new syntax.Node, f *ctFrame) {
	src, dst := f.info, r.info
	ctCopyFact(&dst.Implicits, src.Implicits, old, new)
	ctCopyFact(&dst.Scopes, src.Scopes, old, new)
	switch o := old.(type) {
	case *syntax.Name:
		if n, ok := new.(*syntax.Name); ok {
			ctCopyFact(&dst.Uses, src.Uses, o, n)
			ctCopyFact(&dst.Defs, src.Defs, o, n)
			ctCopyFact(&dst.Instances, src.Instances, o, n)
			ctCopyFact(&dst.LogosCtSiteKeys, src.LogosCtSiteKeys, o, n)
			if obj, ok := dst.Uses[n].(*types2.Const); ok {
				ctCopyFact(&dst.LogosCTFE, src.LogosCTFE, obj, obj)
			}
			if obj, ok := dst.Defs[n].(*types2.Const); ok {
				ctCopyFact(&dst.LogosCTFE, src.LogosCTFE, obj, obj)
			}
		}
	case *syntax.SelectorExpr:
		if n, ok := new.(*syntax.SelectorExpr); ok {
			ctCopyFact(&dst.Selections, src.Selections, o, n)
			ctCopyFact(&dst.LogosCtProperties, src.LogosCtProperties, o, n)
		}
	case *syntax.StructType:
		r.copyCtStructDefaultFacts(o, new.(*syntax.StructType), f)
	case *syntax.CompositeLit:
		n := new.(*syntax.CompositeLit)
		if old := src.LogosSpreads[o]; len(old) > 0 {
			if dst.LogosSpreads == nil {
				dst.LogosSpreads = make(map[*syntax.CompositeLit][]types2.LogosSpread)
			}
			spreads := slices.Clone(old)
			for i := range spreads {
				spreads[i].Src = f.nodes[spreads[i].Src].(syntax.Expr)
			}
			dst.LogosSpreads[n] = spreads
		}
	case *syntax.BlockStmt:
		ctCopyFact(&dst.LogosDisqual, src.LogosDisqual, o, new.(*syntax.BlockStmt))
	case *syntax.ReturnStmt:
		ctCopyFact(&dst.LogosPlatformRetSlots, src.LogosPlatformRetSlots, o, new.(*syntax.ReturnStmt))
	}
	if o, ok := old.(syntax.Expr); ok {
		n := new.(syntax.Expr)
		if tv, ok := src.Types[o]; ok {
			dst.Types[n] = tv
			n.SetTypeInfo(types2.LogosCtTypeAndValue(tv))
		}
		ctCopyFact(&dst.LogosChecks, src.LogosChecks, o, n)
		ctCopyFact(&dst.LogosAddrFresh, src.LogosAddrFresh, o, n)
		ctCopyFact(&dst.LogosConstSlices, src.LogosConstSlices, o, n)
		ctCopyFact(&dst.LogosConstCov, src.LogosConstCov, o, n)
		ctCopyFact(&dst.LogosZeroLit, src.LogosZeroLit, o, n)
	}
}

func (r *ctReducer) reduceExpr(e syntax.Expr, f *ctFrame) syntax.Expr {
	switch e := e.(type) {
	case *syntax.Name:
		if obj := f.info.Uses[e]; obj != nil {
			if targ, ok := r.binders[obj]; ok {
				return r.fl.ctTypeExpr(e.Pos(), targ)
			}
		}
		if binding := f.binding(e); binding != nil && binding.index {
			return litOf(e.Pos(), strconv.Itoa(binding.field.index), syntax.IntLit)
		}
	case *syntax.Operation:
		if e.Op == syntax.And && e.Y == nil {
			if name, ok := syntax.Unparen(e.X).(*syntax.Name); ok {
				if binding := f.binding(name); binding != nil && binding.index {
					r.invalid(name, "[comptime::const_for_assign] cannot take the address of const for variable "+name.Value)
				}
			}
		}
	case *syntax.SelectorExpr:
		if logosComptimeViewAs(f.info, e) {
			return r.fl.ctPkgSelector(e.Pos(), rtImportPath, "ViewAs")
		}
		if binding := f.fieldBinding(e.X); binding != nil {
			if e.Sel.Value == "Type" {
				return r.fl.ctTypeExpr(e.Pos(), binding.field.typ)
			}
			if tv := f.info.Types[e]; tv.Value != nil {
				return r.constant(e.Pos(), tv.Value)
			}
		}
		if selected := f.info.LogosConstSel[e]; selected != nil {
			pos := e.Pos()
			typ := f.caseType(selected)
			if typ == nil {
				r.invalid(e, "missing concrete interface case type")
				return nil
			}
			box := at(&syntax.IndexExpr{X: r.fl.ctPkgSelector(pos, rtImportPath, "Box"), Index: r.fl.ctTypeExpr(pos, typ)}, pos)
			recv := callOf(pos, box, []syntax.Expr{r.clone(e.X, f).(syntax.Expr)})
			return at(&syntax.SelectorExpr{X: recv, Sel: r.clone(e.Sel, f).(*syntax.Name)}, pos)
		}
	case *syntax.CallExpr:
		if fact, ok := f.info.LogosTagGet[e]; ok {
			if fact.Value != nil && !f.keepCalls[e] {
				return r.constant(e.Pos(), fact.Value)
			}
			return r.reduceTagGet(e, fact, f)
		}
		switch cfComptimeCall(e, f.info) {
		case "Error":
			if len(e.ArgList) == 1 {
				if msg, ok := cfComptimeErrorMsg(f.info, e.ArgList[0]); ok {
					r.invalid(e, "[comptime::error] "+msg)
				} else {
					r.invalid(e, "[comptime::error_not_constant] comptime.Error needs a constant message")
				}
			}
		case "IsStruct", "SizeOf", "AlignOf", "HasPointers":
			if tv := f.info.Types[e]; tv.Value != nil {
				return r.constant(e.Pos(), tv.Value)
			}
		case "TypeOf":
			pos := e.Pos()
			return at(&syntax.CompositeLit{Type: at(&syntax.StructType{}, pos), Rbrace: pos}, pos)
		}
	}
	return nil
}

func (f *ctFrame) caseType(c *syntax.ConstSwitchCase) types2.Type {
	for f != nil {
		if typ := f.info.TypeOf(c.Type); typ != nil {
			return typ
		}
		f = f.outer
	}
	return nil
}

func (r *ctReducer) constant(pos syntax.Pos, v constant.Value) syntax.Expr {
	switch v.Kind() {
	case constant.Int:
		return litOf(pos, v.ExactString(), syntax.IntLit)
	case constant.String:
		return litOf(pos, strconv.Quote(constant.StringVal(v)), syntax.StringLit)
	case constant.Bool:
		return r.fl.ctBuiltin(pos, strconv.FormatBool(constant.BoolVal(v)))
	default:
		base.Fatalf("logos: unexpected comptime constant %v at %s", v, pos)
		return nil
	}
}

type ctPreparedUnit struct {
	decl    *syntax.FuncDecl
	helpers []syntax.Decl
	rodata  []rodataVar
	info    *types2.Info
	context *ctLowerContext
}

func stageCtPreparedUnit(rec *types2.LogosCtInst, sites *ctTraceSites) (ctPreparedUnit, error) {
	ct := &ctLowerContext{owner: rec.Pkg, sites: sites, instanceName: rec.Sym}
	fl := &fileLower{ct: ct, pkg: &packageLower{tpkg: rec.Pkg, info: rec.Info}}
	decl, info, err := fl.reduceComptimeInstance(rec)
	if err != nil {
		return ctPreparedUnit{}, err
	}
	fl.pkg.info = info
	fl.lowerCtBody(decl, rec.Sig)
	if ct.err != nil {
		return ctPreparedUnit{}, ct.err
	}
	return ctPreparedUnit{decl: decl, helpers: fl.constDecls, rodata: fl.rodataVars, info: info, context: ct}, nil
}

func (u ctPreparedUnit) readonly(info *types2.Info) ([]*types2.Var, error) {
	if len(u.rodata) == 0 {
		return nil, nil
	}
	vars := make([]*types2.Var, 0, len(u.rodata))
	for _, data := range u.rodata {
		for _, name := range data.vd.NameList {
			obj, ok := info.Defs[name].(*types2.Var)
			if !ok || obj.Pkg() != u.context.owner {
				return nil, fmt.Errorf("%s: missing checked comptime readonly helper %s", name.Pos(), name.Value)
			}
			vars = append(vars, obj)
		}
	}
	return vars, nil
}

func (u ctPreparedUnit) checkInfo(conf *types2.Config) (*types2.Info, error) {
	semantic := u.info
	info := &types2.Info{
		StoreTypesInSyntax: true,
		Defs:               make(map[*syntax.Name]types2.Object),
		Uses:               make(map[*syntax.Name]types2.Object),
		Instances:          make(map[*syntax.Name]types2.Instance),
		Selections:         make(map[*syntax.SelectorExpr]*types2.Selection),
		Implicits:          make(map[syntax.Node]types2.Object),
		Scopes:             make(map[syntax.Node]*types2.Scope),
		FileVersions:       make(map[*syntax.PosBase]string),
	}
	var locals map[*types2.TypeName]*syntax.Name
	var err error
	visit := func(n syntax.Node) bool {
		if err != nil {
			return false
		}
		switch n := n.(type) {
		case *syntax.Name:
			ctCopyFact(&info.LogosCtSiteKeys, semantic.LogosCtSiteKeys, n, n)
		case *syntax.TypeDecl:
			obj, ok := semantic.Defs[n.Name].(*types2.TypeName)
			if !ok {
				return true
			}
			if previous := locals[obj]; previous != nil && previous != n.Name {
				err = fmt.Errorf("logos: local type %s has two reduced declarations at %s and %s", obj.Name(), previous.Pos(), n.Name.Pos())
				return false
			}
			if locals == nil {
				locals = make(map[*types2.TypeName]*syntax.Name)
			}
			locals[obj] = n.Name
		}
		return true
	}
	syntax.Inspect(u.decl, visit)
	for _, helper := range u.helpers {
		syntax.Inspect(helper, visit)
	}
	if err != nil {
		return nil, err
	}
	conf.LogosCtLocalTypes = locals
	return info, nil
}

type ctPreparedInstance struct {
	unit   ctPreparedUnit
	record *types2.LogosCtInst
	file   *ctPreparedFile
}

type ctPreparedFile struct {
	initial, final *types2.Scope
}

type ctPreparedPackage struct {
	instances []ctPreparedInstance
	sites     ctTraceSites
	localDefs map[*syntax.Name]*types2.TypeName
}

func stageCtPreparedPackage(pkg *types2.Package, info *types2.Info) (*ctPreparedPackage, error) {
	p := &ctPreparedPackage{instances: make([]ctPreparedInstance, 0, len(info.LogosCtInst))}
	var files map[*types2.Scope]*ctPreparedFile
	for _, key := range slices.Sorted(maps.Keys(info.LogosCtInst)) {
		record := info.LogosCtInst[key]
		var file *ctPreparedFile
		if record.Pkg == pkg {
			scope := info.LogosCtGenerics[record.Origin].Scope
			if scope == nil {
				return nil, fmt.Errorf("logos: missing defining scope for comptime function %s", record.Origin)
			}
			file = files[scope]
			if file == nil {
				if files == nil {
					files = make(map[*types2.Scope]*ctPreparedFile)
				}
				file = &ctPreparedFile{initial: scope}
				files[scope] = file
			}
		}
		unit, err := stageCtPreparedUnit(record, &p.sites)
		if err != nil {
			return nil, fmt.Errorf("logos: prepare %s: %w", record.Origin, err)
		}
		p.instances = append(p.instances, ctPreparedInstance{unit, record, file})
	}
	for name, obj := range info.Defs {
		if typ, ok := obj.(*types2.TypeName); ok && typ.Pkg() == pkg && typ.Parent() != pkg.Scope() {
			if p.localDefs == nil {
				p.localDefs = make(map[*syntax.Name]*types2.TypeName)
			}
			p.localDefs[name] = typ
		}
	}
	return p, nil
}

func (p *ctPreparedPackage) finish(pkg *types2.Package, info *types2.Info, conf *types2.Config) {
	if err := p.finalize(pkg, info, conf); err != nil {
		base.Fatalf("logos: finish comptime instances: %v", err)
	}
}

func (p *ctPreparedPackage) finalize(pkg *types2.Package, info *types2.Info, conf *types2.Config) error {
	if err := p.sites.bindUses(pkg, info); err != nil {
		return err
	}
	var locals map[*types2.TypeName]*types2.TypeName
	for name, old := range p.localDefs {
		if fresh, ok := info.Defs[name].(*types2.TypeName); ok {
			if locals == nil {
				locals = make(map[*types2.TypeName]*types2.TypeName)
			}
			locals[old] = fresh
		}
	}
	records := make(map[*types2.Func]*types2.LogosCtInst, len(p.instances))
	imports := make(map[string]bool, len(info.LogosCtImports))
	for _, path := range info.LogosCtImports {
		imports[path] = true
	}
	for _, inst := range p.instances {
		fn, record, err := inst.check(pkg, info, conf, locals)
		if err != nil {
			return fmt.Errorf("%s: %w", inst.record.Origin, err)
		}
		records[fn] = record
		ct := inst.unit.context
		if ct.owner != pkg {
			imports[ct.owner.Path()] = true
		}
		for _, path := range ct.importPaths() {
			imports[path] = true
		}
	}
	info.LogosCtPrepared = records
	info.LogosCtImports = slices.Sorted(maps.Keys(imports))
	info.LogosCtFinish = nil
	return nil
}

func (inst ctPreparedInstance) check(consumer *types2.Package, info *types2.Info, conf *types2.Config, locals map[*types2.TypeName]*types2.TypeName) (*types2.Func, *types2.LogosCtInst, error) {
	ct := inst.unit.context
	ct.consumer, ct.importer, ct.context, ct.localTypes = consumer, conf.Importer, conf.Context, locals
	parent, err := inst.ownerScope(info)
	if err != nil {
		return nil, nil, err
	}
	prepared := *conf
	prepared.Error = nil
	if version := inst.record.Info.FileVersions[inst.record.Decl.Pos().FileBase()]; version != "" {
		prepared.GoVersion = version
	}
	if err := ct.configure(&prepared); err != nil {
		return nil, nil, err
	}
	prepared.Context = ct.context
	facts, err := inst.unit.checkInfo(&prepared)
	if err != nil {
		return nil, nil, err
	}
	fn, err := types2.LogosCheckCtPrepared(&prepared, ct.owner, parent, inst.unit.decl, inst.unit.helpers, facts)
	if err != nil {
		return nil, nil, err
	}
	readonly, err := inst.unit.readonly(facts)
	if err != nil {
		return nil, nil, err
	}
	targs := make([]types2.Type, len(inst.record.Targs))
	for i, arg := range inst.record.Targs {
		targs[i], err = ct.abiType(arg)
		if err != nil {
			return nil, nil, fmt.Errorf("project type argument %d: %w", i+1, err)
		}
	}
	result := *inst.record
	result.Decl, result.Helpers, result.Readonly = inst.unit.decl, inst.unit.helpers, readonly
	result.Pkg, result.Info, result.Sig, result.ABITargs = ct.owner, facts, fn.Signature(), targs
	return fn, &result, nil
}

func (inst ctPreparedInstance) ownerScope(info *types2.Info) (*types2.Scope, error) {
	ct := inst.unit.context
	if inst.file != nil {
		ct.owner = ct.consumer
		return inst.localScope()
	}
	path := ct.owner.Path()
	owner, err := ct.importer.Import(path)
	if err != nil {
		return nil, fmt.Errorf("import canonical comptime owner %q: %w", path, err)
	}
	if owner == nil || !owner.Complete() {
		return nil, fmt.Errorf("no complete canonical comptime owner for %q", path)
	}
	ct.owner = owner
	origin, ok := owner.Scope().Lookup(inst.record.Origin.Name()).(*types2.Func)
	if !ok || info.LogosCtImport == nil {
		return nil, fmt.Errorf("missing canonical comptime origin %s", inst.record.Origin)
	}
	generic, ok := info.LogosCtImport(origin)
	if !ok || generic.Scope == nil || generic.Scope.Parent() != owner.Scope() {
		return nil, fmt.Errorf("missing canonical defining scope for %s", origin)
	}
	return generic.Scope, nil
}

func (inst ctPreparedInstance) localScope() (*types2.Scope, error) {
	ct := inst.unit.context
	if inst.file.final != nil {
		return inst.file.final, nil
	}
	original := inst.file.initial
	decl := inst.record.Decl
	scope := types2.NewScope(ct.owner.Scope(), decl.Pos(), syntax.EndPos(decl), "comptime owner file")
	for _, name := range original.Names() {
		obj := original.Lookup(name)
		if alias, ok := obj.(*types2.PkgName); ok {
			if alias.Imported() == types2.LogosComptimePackage() {
				continue
			}
			pkg, err := ct.importPackage(alias.Imported().Path())
			if err != nil {
				return nil, fmt.Errorf("rebind import %s: %w", alias.Name(), err)
			}
			scope.Insert(types2.NewPkgName(alias.Pos(), ct.owner, name, pkg))
			continue
		}
		if obj.Pkg() == types2.LogosComptimePackage() {
			continue
		}
		pkg, err := ct.importPackage(obj.Pkg().Path())
		if err != nil {
			return nil, fmt.Errorf("rebind dot import %s: %w", name, err)
		}
		canonical := pkg.Scope().Lookup(name)
		if canonical == nil {
			return nil, fmt.Errorf("missing canonical dot import %s.%s", pkg.Path(), name)
		}
		scope.Insert(canonical)
	}
	inst.file.final = scope
	return scope, nil
}

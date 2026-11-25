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

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

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
	forward bool
}

type cfExpander struct {
	defs     []*cfGenericDef
	byObj    map[*types2.Func]*cfGenericDef
	byDecl   map[*syntax.FuncDecl]*cfGenericDef
	pkgs     map[string]*types2.Package
	switches map[*syntax.ConstSwitchStmt]*syntax.ConstSwitchCase
}

func newCfExpander(shared *packageLower, files []*syntax.File, src map[*syntax.File][]byte) {
	exp := &cfExpander{
		byObj:  make(map[*types2.Func]*cfGenericDef),
		byDecl: make(map[*syntax.FuncDecl]*cfGenericDef),
		pkgs:   make(map[string]*types2.Package),
	}
	shared.ctExp = exp
	var lowers []*fileLower
	for _, file := range files {
		if _, ok := src[file]; !ok {
			continue
		}
		fl := &fileLower{pkg: shared, file: file}
		exp.cfRegisterDefs(fl.cfCollectGenerics())
		lowers = append(lowers, fl)
	}
	for found := true; found; {
		found = false
		for _, fl := range lowers {
			if defs := fl.cfCollectForwarders(); len(defs) != 0 {
				exp.cfRegisterDefs(defs)
				found = true
			}
		}
	}
}

func (exp *cfExpander) cfRegisterDefs(defs []*cfGenericDef) {
	for _, def := range defs {
		exp.defs = append(exp.defs, def)
		exp.byDecl[def.decl] = def
		if def.obj != nil {
			exp.byObj[def.obj] = def
		}
	}
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
		found = ok && isFunc && fl.cfIsGeneric(obj) && slices.ContainsFunc(typeArgsOf(inst.TypeArgs), func(t types2.Type) bool {
			return types2.LogosCtForwardedParam(t) != nil
		})
		return true
	})
	return found
}

func (fl *fileLower) cfFuncStart(off int) int {
	kw, ok := fl.keywords.Start(fl.src, token.FUNC, off)
	if !ok {
		base.Fatalf("logos: no func keyword before offset %d", off)
	}
	return kw
}

// expandComptime retires the comptime generics of the file. Each instance
// compiles natively in the defining package; eject renames each call site
// to its printed instance in the ejected text only.
func (fl *fileLower) expandComptime() {
	for _, def := range fl.pkg.ctExp.defs {
		if def.file == fl.file {
			fl.cfRetireDef(def)
		}
	}
	if base.LogosEject != "" {
		fl.ejectCtSites()
	}
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
	ejectOnly := make(map[syntax.Decl]bool)
	for _, imp := range fl.genImportsPendingNativeUse {
		if !used[imp.LocalPkgName.Value] {
			ejectOnly[imp] = true
		}
	}
	if len(ejectOnly) > 0 {
		fl.file.DeclList = slices.DeleteFunc(fl.file.DeclList, func(d syntax.Decl) bool { return ejectOnly[d] })
	}
	fl.genImportsPendingNativeUse = nil
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
	if obj.Pkg() == nil || obj.Pkg() == fl.pkg.tpkg || fl.pkg.info.LogosCtImport == nil {
		return false
	}
	generic, ok := fl.pkg.info.LogosCtImport(obj.Origin())
	return ok && generic.Decl != nil && types2.LogosUsesComptime(generic.Decl, generic.Scope)
}

func (fl *fileLower) cfRuntimeBindings() types2.LogosTypeBindings {
	if !fl.needReflect {
		return types2.LogosTypeBindings{}
	}
	return types2.LogosTypeBindings{Pkgs: map[string]*types2.Package{reflectAlias: fl.pkg.ctExp.pkgs["reflect"]}}
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
	root := fl.pkg.tpkg
	if root.Path() == path {
		return root
	}
	for _, imp := range root.Imports() {
		if imp.Path() == path {
			return imp
		}
	}
	return nil
}

// cfRetireDef keeps a comptime generic out of native code and out of
// ejected text. Ejected text keeps an exported generic as a stub, or with
// its run-time body when cfRuntimeBody allows one, and drops an unexported
// one; a forwarding function keeps its body natively.
func (fl *fileLower) cfRetireDef(def *cfGenericDef) {
	fn := def.decl
	call := "panic(" + strconv.Quote("logos: comptime generic "+fn.Name.Value+" runs only through loGo/s instantiation") + ")"
	runtime := base.LogosEject != "" && !def.forward && def.obj != nil && def.obj.Exported() && fl.cfRuntimeBody(fn)
	if !def.forward {
		fn.Body.List = fl.reparseStmts(call, fn.Pos())
	}
	switch {
	case base.LogosEject != "" && (def.obj == nil || !def.obj.Exported()):
		s := fl.cfDeclSpan(fn)
		fl.add(s.start, s.end, "")
		fl.ejectRetired = append(fl.ejectRetired, fn)
		if fl.ejectSites == nil {
			fl.ejectSites = make(map[syntax.Node]bool)
		}
		fl.ejectSites[fn] = true
	case runtime:
	case base.LogosEject != "" || !def.forward:
		fl.add(fl.off(syntax.StartPos(fn.Body)), fl.off(syntax.EndPos(fn.Body))+1, "{\n\t"+call+"\n}")
	default:
		return
	}
	fl.changed = true
}

// cfDeclSpan is the source text of fn from its func keyword through its
// closing brace.
func (fl *fileLower) cfDeclSpan(fn *syntax.FuncDecl) span {
	return span{fl.cfFuncStart(fl.off(syntax.StartPos(fn))), fl.off(syntax.EndPos(fn)) + 1}
}

// cfLayout is one comptime.SizeOf or comptime.AlignOf call of a comptime
// generic body, with the integer type its context gives it.
type cfLayout struct {
	call *syntax.CallExpr
	targ syntax.Expr
	op   string
	typ  string
}

// cfRuntimeBody keeps, in the ejected text only, the body of an exported
// comptime generic whose only compile-time forms are comptime.SizeOf and
// comptime.AlignOf calls whose context fixes a predeclared integer type.
// Each call becomes the unsafe operation on a zero value of its type
// argument, converted to that type. Ordinary generic code that reaches
// the generic with a type argument holding its own type parameter, such
// as a local type that captures one, then runs in ejected code as it does
// natively through an instance. The declaration check leaves the body to
// each instance, so the test is syntactic and admits only plain Go with
// no composite literal and no selector other than a package-qualified
// name. Any other body keeps the panicking stub.
func (fl *fileLower) cfRuntimeBody(fn *syntax.FuncDecl) bool {
	imported := make(map[string]*types2.Package)
	for _, decl := range fl.file.DeclList {
		imp, ok := decl.(*syntax.ImportDecl)
		if !ok {
			continue
		}
		obj := fl.pkg.info.Implicits[imp]
		if imp.LocalPkgName != nil {
			obj = fl.pkg.info.Defs[imp.LocalPkgName]
		}
		if pn, ok := obj.(*types2.PkgName); ok && pn.Name() != "_" && pn.Name() != "." {
			imported[pn.Name()] = pn.Imported()
		}
	}
	parents := cfParents(fn)
	var calls []cfLayout
	var paths []string
	ok := true
	syntax.Inspect(fn.Body, func(n syntax.Node) bool {
		if !ok {
			return false
		}
		switch n := n.(type) {
		case *syntax.TryExpr, *syntax.ErrdeferStmt, *syntax.SwitchExpr, *syntax.ArmClause, *syntax.AssertedStruct,
			*syntax.OptionType, *syntax.CoalesceExpr, *syntax.OptSelectExpr, *syntax.ConstType, *syntax.ConstFuncDecl,
			*syntax.YieldStmt, *syntax.ScopeDeferStmt, *syntax.FieldDefault, *syntax.SpreadExpr, *syntax.InterpExpr,
			*syntax.TupleType, *syntax.TupleLit, *syntax.LambdaExpr, *syntax.ConstIfStmt, *syntax.ConstSwitchStmt,
			*syntax.ConstForStmt, *syntax.FieldProjExpr, *syntax.GroupStmt, *syntax.GroupChild, *syntax.CompositeLit,
			*syntax.FuncLit:
			ok = false
		case *syntax.Name:
			if obj, isFunc := fl.pkg.tpkg.Scope().Lookup(n.Value).(*types2.Func); isFunc && fl.pkg.ctExp.byObj[obj] != nil {
				ok = false
			}
		case *syntax.SelectorExpr:
			var pkg *types2.Package
			if x, isName := n.X.(*syntax.Name); isName {
				pkg = imported[x.Value]
			}
			switch {
			case pkg == nil:
				ok = false
			case pkg != types2.LogosComptimePackage():
				paths = append(paths, pkg.Path())
				if callee, isFunc := pkg.Scope().Lookup(n.Sel.Value).(*types2.Func); isFunc && fl.pkg.info.LogosCtImport != nil {
					if _, comptime := fl.pkg.info.LogosCtImport(callee); comptime {
						ok = false
					}
				}
			default:
				layout, isLayout := fl.cfLayoutCall(n, fn, parents)
				calls = append(calls, layout)
				ok = isLayout
			}
			return false
		}
		return ok
	})
	if !ok || len(calls) == 0 {
		return false
	}
	const unsafeName = "_logos_ctunsafe"
	if !fl.ctRuntimeUnsafe {
		fl.ctRuntimeUnsafe = true
		end := fl.off(fl.file.PkgName.Pos()) + len(fl.file.PkgName.Value)
		fl.add(end, end, "\n\nimport "+unsafeName+` "unsafe"`)
	}
	for _, path := range paths {
		fl.ensureEjectImport(path, fn.Pos())
	}
	ctx := cfCtx{buf: fl.srcMap, starts: fl.start}
	for _, l := range calls {
		targ := string(fl.src[fl.off(syntax.StartPos(l.targ)):cfExactEnd(ctx, l.targ)])
		text := fmt.Sprintf("%s(%s.%s(*new(%s)))", l.typ, unsafeName, l.op, targ)
		end, want := cfExactEnd(ctx, l.call), token.LPAREN
		scanSegment(fl.src, end, len(fl.src), func(t tok) bool {
			switch {
			case t.comment:
				return true
			case t.kind != want:
				return false
			case want == token.RPAREN:
				end = t.end
				return false
			}
			want = token.RPAREN
			return true
		})
		fl.add(fl.off(syntax.StartPos(l.call)), end, text)
	}
	return true
}

// cfLayoutCall reports the layout call that sel selects from the comptime
// package when its context fixes a predeclared integer type: a return value
// of an integer result, a short variable declaration, or a variable
// declaration of an integer type or of none.
func (fl *fileLower) cfLayoutCall(sel *syntax.SelectorExpr, fn *syntax.FuncDecl, parents map[syntax.Node]syntax.Node) (cfLayout, bool) {
	op := map[string]string{"SizeOf": "Sizeof", "AlignOf": "Alignof"}[sel.Sel.Value]
	index, ok := parents[sel].(*syntax.IndexExpr)
	if op == "" || !ok || index.X != sel {
		return cfLayout{}, false
	}
	call, ok := parents[index].(*syntax.CallExpr)
	targs := syntax.UnpackListExpr(index.Index)
	if !ok || call.Fun != index || len(call.ArgList) != 0 || len(targs) != 1 {
		return cfLayout{}, false
	}
	var typ syntax.Expr
	switch p := parents[call].(type) {
	case *syntax.ReturnStmt:
		if len(fn.Type.ResultList) == 1 {
			typ = fn.Type.ResultList[0].Type
		}
	case *syntax.ListExpr:
		ret, isReturn := parents[p].(*syntax.ReturnStmt)
		if i := slices.Index(p.ElemList, syntax.Expr(call)); isReturn && ret.Results == p && len(p.ElemList) == len(fn.Type.ResultList) {
			typ = fn.Type.ResultList[i].Type
		}
	case *syntax.AssignStmt:
		if _, isName := p.Lhs.(*syntax.Name); isName && p.Op == syntax.Def && p.Rhs == call {
			typ = syntax.NewName(call.Pos(), "int")
		}
	case *syntax.VarDecl:
		if len(p.NameList) == 1 && p.Values == call {
			typ = p.Type
			if typ == nil {
				typ = syntax.NewName(call.Pos(), "int")
			}
		}
	}
	name, ok := typ.(*syntax.Name)
	if !ok || fl.pkg.tpkg.Scope().Lookup(name.Value) != nil {
		return cfLayout{}, false
	}
	obj, ok := types2.Universe.Lookup(name.Value).(*types2.TypeName)
	if !ok {
		return cfLayout{}, false
	}
	if basic, isBasic := obj.Type().(*types2.Basic); !isBasic || basic.Info()&types2.IsInteger == 0 {
		return cfLayout{}, false
	}
	return cfLayout{call: call, targ: targs[0], op: op, typ: name.Value}, true
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
	facts.LogosCtConstMapUses = ct.constMapUses
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

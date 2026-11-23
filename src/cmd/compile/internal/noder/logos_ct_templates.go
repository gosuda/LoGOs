// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package noder

import (
	"go/token"
	"internal/pkgbits"
	"slices"

	"cmd/compile/internal/base"
	"cmd/compile/internal/importer"
	"cmd/compile/internal/ir"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// Native owner-package emission is active: prepared instances are checked
// in the defining package and calls resolve to them. The text-copy path
// is still present and gets deleted as the blockers land.
const logosCtNative = true

const logosCtTemplateVersion = 1

func logosCtFreezeTemplates(info *types2.Info) {
	for fn, generic := range info.LogosCtGenerics {
		if generic.Decl != nil {
			generic.Decl = syntax.CloneLogosCtSyntax(generic.Decl)
			info.LogosCtGenerics[fn] = generic
		}
	}
}

func (w *writer) logosCtWriteTemplate(generic types2.LogosCtGeneric) {
	w.Len(logosCtTemplateVersion)
	w.String(generic.GoVersion)

	fileScope := generic.Scope
	names := slices.DeleteFunc(fileScope.Names(), func(name string) bool {
		return !token.IsIdentifier(name) || name == "_"
	})
	w.Len(len(names))
	comptime := types2.LogosComptimePackage()
	for _, name := range names {
		obj := fileScope.Lookup(name)
		w.String(obj.Name())
		imp := obj.Pkg()
		if pkgName, ok := obj.(*types2.PkgName); w.Bool(ok) {
			imp = pkgName.Imported()
			w.pos(pkgName)
		}
		if !w.Bool(imp == comptime) {
			w.pkg(imp)
		}
	}

	w.logosCtTemplateRoots(generic)
	syntax.WriteLogosCtSyntax(w.Encoder, generic.Decl, syntax.LogosCtSyntaxFields{
		Pos:    func(pos *syntax.Pos) { w.pos(*pos) },
		Pragma: w.logosCtWritePragma,
	})
}

// A deferred branch has no Uses entries. Consider its identifiers against the
// original scopes so a helper needed only by a later instance still has a
// native object relocation. Relocations use the lowered package's declarations
// rather than stale objects from the first check.
func (w *writer) logosCtTemplateRoots(generic types2.LogosCtGeneric) {
	seen := make(map[types2.Object]bool)
	fileScope := generic.Scope
	ownerScope := fileScope.Parent()
	comptime := types2.LogosComptimePackage()

	// The reader only needs the lexical names. The linker follows the
	// object relocations in this separate element.
	root := w.p.newWriter(pkgbits.SectionMeta, pkgbits.SyncPrivate)
	relocate := func(obj types2.Object) {
		if obj == nil || seen[obj] {
			return
		}
		seen[obj] = true
		if pkg := obj.Pkg(); pkg == nil || pkg == types2.Unsafe || pkg == comptime {
			return
		} else if pkg.Path() == w.p.curpkg.Path() {
			if obj.Exported() {
				return // Already a public root.
			}
			name := obj.Name()
			if _, ok := obj.(*types2.Const); ok && w.p.logosCtInfo.LogosCtExportConstMaps[name].Type != nil {
				return
			}
			obj = w.p.curpkg.Scope().Lookup(name)
			base.Assertf(obj != nil, "missing lowered comptime dependency %s.%s", pkg.Path(), name)
		}
		root.Reloc(pkgbits.SectionObj, w.p.objIdx(obj))
	}

	syntax.Inspect(generic.Decl, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.Name:
			// Dot-imported names and ordinary package declarations
			// can both appear unqualified.
			obj := fileScope.Lookup(n.Value)
			if obj == nil {
				obj = ownerScope.Lookup(n.Value)
			}
			if _, alias := obj.(*types2.PkgName); !alias {
				relocate(obj)
			}
		case *syntax.SelectorExpr:
			if qual, ok := n.X.(*syntax.Name); ok {
				if alias, ok := fileScope.Lookup(qual.Value).(*types2.PkgName); ok && token.IsExported(n.Sel.Value) {
					relocate(alias.Imported().Scope().Lookup(n.Sel.Value))
				}
			}
		}
		return true
	})
	w.Reloc(pkgbits.SectionMeta, root.Flush())
}

func (w *writer) logosCtWritePragma(slot *syntax.Pragma) {
	if !w.Bool(*slot != nil) {
		return
	}
	p := (*slot).(*pragmas)
	w.Int(int(p.Flag))
	w.Len(len(p.Pos))
	for _, at := range p.Pos {
		w.Int(int(at.Flag))
		w.pos(at.Pos)
	}
	w.Len(len(p.Embeds))
	for _, embed := range p.Embeds {
		w.pos(embed.Pos)
		w.Strings(embed.Patterns)
	}
	if w.Bool(p.WasmImport != nil) {
		w.pos(p.WasmImport.Pos)
		w.String(p.WasmImport.Module)
		w.String(p.WasmImport.Name)
	}
	if w.Bool(p.WasmExport != nil) {
		w.pos(p.WasmExport.Pos)
		w.String(p.WasmExport.Name)
	}
}

type logosCtImportObject struct {
	pr  *pkgReader
	idx index
}

type logosCtImportState struct {
	objects          map[logosCtOrigin]logosCtImportObject
	loaded           map[*types2.Func]types2.LogosCtGeneric
	constMaps        map[string]logosCtImportObject
	constMapPackages map[string]*logosCtConstMapPackage
}

func (st *logosCtImportState) record(pr *pkgReader, idx index, path, name string) {
	if !token.IsIdentifier(name) {
		return // The current template/edge contract is for package functions.
	}
	if st.objects == nil {
		st.objects = make(map[logosCtOrigin]logosCtImportObject)
	}
	key := logosCtOrigin{path, name}
	if st.objects[key].pr == nil || path == pr.PkgPath() {
		st.objects[key] = logosCtImportObject{pr: pr, idx: idx}
	}
}

func logosImportCtGeneric(m *gcimports, fn *types2.Func) (types2.LogosCtGeneric, bool) {
	owner := fn.Pkg()
	if owner == nil || fn.Signature().Recv() != nil || fn.Signature().TypeParams().Len() == 0 || m.packages[owner.Path()] != owner || owner.Scope().Lookup(fn.Name()) != fn {
		return types2.LogosCtGeneric{}, false
	}
	st := &m.logos.ct
	if generic, ok := st.loaded[fn]; ok {
		return generic, generic.Decl != nil || len(generic.Edges) != 0
	}
	obj := st.objects[logosCtOrigin{owner.Path(), fn.Name()}]
	if obj.pr == nil {
		return types2.LogosCtGeneric{}, false
	}
	input := obj.pr.PkgDecoder
	edges, payload := importer.ReadLogosCtEdges(m.ctxt, m.packages, input, obj.idx, fn)
	generic := types2.LogosCtGeneric{Edges: edges}
	if payload != nil {
		r := logosCtSyntaxReader{Decoder: payload, input: &input}
		generic.Decl, generic.Scope, generic.GoVersion = r.template(fn, m.packages)
		if maps := logosCtConstMapPackageFor(m, owner.Path()); maps != nil {
			for name := range maps.indices {
				if owner.Scope().Lookup(name) != nil {
					continue
				}
				generic.Scope.InsertLazy(name, func() types2.Object {
					row, ok := maps.lookup(owner, name)
					assert(ok)
					return row.Binding
				})
			}
		}
	}
	if st.loaded == nil {
		st.loaded = make(map[*types2.Func]types2.LogosCtGeneric)
	}
	st.loaded[fn] = generic
	return generic, generic.Decl != nil || len(generic.Edges) != 0
}

type logosCtBinding struct {
	name  string
	alias bool
	pos   syntax.Pos
	pkg   *types2.Package
}

type logosCtSyntaxReader struct {
	*pkgbits.Decoder
	input *pkgbits.PkgDecoder
	bases map[index]*syntax.PosBase
}

func (r *logosCtSyntaxReader) template(fn *types2.Func, packages map[string]*types2.Package) (*syntax.FuncDecl, *types2.Scope, string) {
	if version := r.Len(); version != logosCtTemplateVersion {
		base.Fatalf("unsupported comptime syntax template version %d for %v", version, fn)
	}
	goVersion := r.String()
	bindings := make([]logosCtBinding, r.Len())
	for i := range bindings {
		b := &bindings[i]
		b.name = r.String()
		if b.alias = r.Bool(); b.alias {
			b.pos = r.pos(r.Decoder)
		}
		b.pkg = r.pkg(packages)
	}
	_ = r.Reloc(pkgbits.SectionMeta) // Retained only for the native export linker.
	decl := syntax.ReadLogosCtSyntax(r.Decoder, syntax.LogosCtSyntaxFields{
		Pos:    func(pos *syntax.Pos) { *pos = r.pos(r.Decoder) },
		Pragma: r.pragma,
	})
	base.Assertf(decl != nil && decl.Name != nil && decl.Name.Value == fn.Name() && decl.Body != nil && r.Data.Len() == 0, "invalid comptime syntax template for %v", fn)

	owner := fn.Pkg()
	scope := types2.NewScope(owner.Scope(), decl.Pos(), syntax.EndPos(decl), "comptime template file")
	for _, b := range bindings {
		if b.alias {
			scope.Insert(types2.NewPkgName(b.pos, owner, b.name, b.pkg))
			continue
		}
		base.Assertf(token.IsExported(b.name), "unexported dot import %s.%s in comptime template", b.pkg.Path(), b.name)
		scope.InsertLazy(b.name, func() types2.Object {
			obj := b.pkg.Scope().Lookup(b.name)
			base.Assertf(obj != nil, "missing comptime dot import %s.%s", b.pkg.Path(), b.name)
			return obj
		})
	}
	return decl, scope, goVersion
}

func (r *logosCtSyntaxReader) pkg(packages map[string]*types2.Package) *types2.Package {
	if r.Bool() {
		return types2.LogosComptimePackage()
	}
	r.Sync(pkgbits.SyncPkg)
	path := r.input.PeekPkgPath(r.Reloc(pkgbits.SectionPkg))
	if path == "unsafe" {
		return types2.Unsafe
	}
	pkg := packages[path]
	base.Assertf(pkg != nil, "missing comptime template import %q", path)
	return pkg
}

func (r *logosCtSyntaxReader) pos(d *pkgbits.Decoder) syntax.Pos {
	d.Sync(pkgbits.SyncPos)
	if !d.Bool() {
		return syntax.Pos{}
	}
	base := r.posBase(d.Reloc(pkgbits.SectionPosBase))
	return syntax.MakePos(base, d.Uint(), d.Uint())
}

func (r *logosCtSyntaxReader) posBase(idx index) *syntax.PosBase {
	if b := r.bases[idx]; b != nil {
		return b
	}
	d := r.input.TempDecoder(pkgbits.SectionPosBase, idx, pkgbits.SyncPosBase)
	defer r.input.RetireDecoder(&d)
	filename := d.String()
	var b *syntax.PosBase
	if d.Bool() {
		b = syntax.NewTrimmedFileBase(filename, true)
	} else {
		b = syntax.NewLineBase(r.pos(&d), filename, true, d.Uint(), d.Uint())
	}
	if r.bases == nil {
		r.bases = make(map[index]*syntax.PosBase)
	}
	r.bases[idx] = b
	return b
}

func (r *logosCtSyntaxReader) pragma(slot *syntax.Pragma) {
	if !r.Bool() {
		return
	}
	p := &pragmas{Flag: ir.PragmaFlag(r.Int())}
	if n := r.Len(); n != 0 {
		p.Pos = make([]pragmaPos, n)
		for i := range p.Pos {
			p.Pos[i] = pragmaPos{Flag: ir.PragmaFlag(r.Int()), Pos: r.pos(r.Decoder)}
		}
	}
	if n := r.Len(); n != 0 {
		p.Embeds = make([]pragmaEmbed, n)
		for i := range p.Embeds {
			p.Embeds[i] = pragmaEmbed{Pos: r.pos(r.Decoder), Patterns: r.Strings()}
		}
	}
	if r.Bool() {
		p.WasmImport = &WasmImport{Pos: r.pos(r.Decoder), Module: r.String(), Name: r.String()}
	}
	if r.Bool() {
		p.WasmExport = &WasmExport{Pos: r.pos(r.Decoder), Name: r.String()}
	}
	*slot = p
}

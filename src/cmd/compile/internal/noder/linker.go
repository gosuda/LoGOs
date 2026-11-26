// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package noder

import (
	"internal/buildcfg"
	"internal/pkgbits"
	"io"
	"maps"
	"slices"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/ir"
	"cmd/compile/internal/reflectdata"
	"cmd/compile/internal/types"
	"cmd/internal/goobj"
	"cmd/internal/obj"
)

// This file implements the unified IR linker, which combines the
// local package's stub data with imported package data to produce a
// complete export data file. It also rewrites the compiler's
// extension data sections based on the results of compilation (e.g.,
// the function inlining cost and linker symbol index assignments).
//
// TODO(mdempsky): Using the name "linker" here is confusing, because
// readers are likely to mistake references to it for cmd/link. But
// there's a shortage of good names for "something that combines
// multiple parts into a cohesive whole"... e.g., "assembler" and
// "compiler" are also already taken.

// TODO(mdempsky): Should linker go into pkgbits? Probably the
// low-level linking details can be moved there, but the logic for
// handling extension data needs to stay in the compiler.

// A linker combines a package's stub export data with any referenced
// elements from imported packages into a single, self-contained
// export data file.
type linker struct {
	pw pkgbits.PkgEncoder

	pkgs   map[string]index
	decls  map[*types.Sym]index
	bodies map[*types.Sym]index

	logosCtReaders   map[*pkgReader]bool
	logosCtDecls     map[*types.Sym]bool
	logosCtHelpers   map[index]bool
	logosCtExact     map[string]index
	logosCtGraph     map[string][]string
	logosCtMapFuncs  map[index]bool
	logosCtConstMaps index
	logosCtMethods   map[logosCtMethodOrigin]index
}

// relocAll ensures that all elements specified by pr and relocs are
// copied into the output export data file, and returns the
// corresponding indices in the output.
func (l *linker) relocAll(pr *pkgReader, relocs []pkgbits.RefTableEntry) []pkgbits.RefTableEntry {
	res := make([]pkgbits.RefTableEntry, len(relocs))
	for i, rent := range relocs {
		rent.Idx = l.relocIdx(pr, rent.Kind, rent.Idx)
		res[i] = rent
	}
	return res
}

// relocIdx ensures a single element is copied into the output export
// data file, and returns the corresponding index in the output.
func (l *linker) relocIdx(pr *pkgReader, k pkgbits.SectionKind, idx index) index {
	assert(pr != nil)

	absIdx := pr.AbsIdx(k, idx)

	if newidx := pr.newindex[absIdx]; newidx != 0 {
		return ^newidx
	}

	var newidx index
	switch k {
	case pkgbits.SectionString:
		newidx = l.relocString(pr, idx)
	case pkgbits.SectionPkg:
		newidx = l.relocPkg(pr, idx)
	case pkgbits.SectionObj:
		newidx = l.relocObj(pr, idx)

	default:
		// Generic relocations.
		//
		// TODO(mdempsky): Deduplicate more sections? In fact, I think
		// every section could be deduplicated. This would also be easier
		// if we do external relocations.

		w := l.pw.NewEncoderRaw(k)
		l.relocCommon(pr, w, k, idx)
		newidx = w.Idx
	}

	pr.newindex[absIdx] = ^newidx
	if k == pkgbits.SectionBody {
		l.relocLogosCtTargets(pr)
	}

	return newidx
}

func (l *linker) relocLogosCtTargets(pr *pkgReader) {
	if l.logosCtReaders[pr] {
		return
	}
	if len(pr.logosCtExact) == 0 && len(pr.logosCtGraph) == 0 && len(pr.logosCtHelpers) == 0 && len(pr.logosCtMapFuncs) == 0 && len(pr.logosCtMethods) == 0 {
		return
	}
	if l.logosCtReaders == nil {
		l.logosCtReaders = make(map[*pkgReader]bool)
	}
	if l.logosCtExact == nil && len(pr.logosCtExact) != 0 {
		l.logosCtExact = make(map[string]index)
	}
	if l.logosCtGraph == nil && len(pr.logosCtGraph) != 0 {
		l.logosCtGraph = make(map[string][]string)
	}
	l.logosCtReaders[pr] = true

	for _, id := range slices.Sorted(maps.Keys(pr.logosCtExact)) {
		idx := l.relocIdx(pr, pkgbits.SectionObj, pr.logosCtExact[id])
		if prev, ok := l.logosCtExact[id]; ok && prev != idx {
			base.Fatalf("conflicting comptime definitions for %q", id)
		}
		l.logosCtExact[id] = idx
	}
	for id, children := range pr.logosCtGraph {
		if prev, ok := l.logosCtGraph[id]; ok && !slices.Equal(prev, children) {
			base.Fatalf("conflicting comptime edges for %q", id)
		}
		l.logosCtGraph[id] = children
	}
	if len(pr.logosCtHelpers) != 0 && l.logosCtHelpers == nil {
		l.logosCtHelpers = make(map[index]bool)
	}
	for _, root := range slices.Sorted(maps.Keys(pr.logosCtHelpers)) {
		idx := l.relocIdx(pr, pkgbits.SectionObj, root)
		readonly := pr.logosCtHelpers[root]
		if prev, ok := l.logosCtHelpers[idx]; ok && prev != readonly {
			base.Fatalf("conflicting readonly flags for comptime helper %d", idx)
		}
		l.logosCtHelpers[idx] = readonly
	}
	if len(pr.logosCtMapFuncs) != 0 && l.logosCtMapFuncs == nil {
		l.logosCtMapFuncs = make(map[index]bool)
	}
	for _, root := range slices.Sorted(maps.Keys(pr.logosCtMapFuncs)) {
		idx := l.relocIdx(pr, pkgbits.SectionObj, root)
		l.logosCtMapFuncs[idx] = true
	}
	if pr == localPkgReader && pr.logosCtConstMaps != 0 {
		l.logosCtConstMaps = l.relocIdx(pr, pkgbits.SectionMeta, pr.logosCtConstMaps)
	}
	if pr == localPkgReader && len(pr.logosCtMethods) != 0 {
		if l.logosCtMethods == nil {
			l.logosCtMethods = make(map[logosCtMethodOrigin]index, len(pr.logosCtMethods))
		}
		for _, key := range slices.SortedFunc(maps.Keys(pr.logosCtMethods), func(a, b logosCtMethodOrigin) int {
			if c := strings.Compare(a.recvPath, b.recvPath); c != 0 {
				return c
			}
			if c := strings.Compare(a.recvName, b.recvName); c != 0 {
				return c
			}
			return strings.Compare(a.name, b.name)
		}) {
			l.logosCtMethods[key] = l.relocIdx(pr, pkgbits.SectionObj, pr.logosCtMethods[key])
		}
	}
}

func (l *linker) writeLogosCtTargets(w *pkgbits.Encoder) {
	if len(l.logosCtExact) == 0 && len(l.logosCtGraph) == 0 && len(l.logosCtHelpers) == 0 && len(l.logosCtMapFuncs) == 0 && len(l.logosCtMethods) == 0 {
		return
	}
	w.Len(len(l.logosCtExact))
	for _, id := range slices.Sorted(maps.Keys(l.logosCtExact)) {
		w.String(id)
		w.Reloc(pkgbits.SectionObj, l.logosCtExact[id])
	}
	w.Len(len(l.logosCtGraph))
	for _, id := range slices.Sorted(maps.Keys(l.logosCtGraph)) {
		w.String(id)
		w.Strings(l.logosCtGraph[id])
	}
	if len(l.logosCtHelpers) != 0 || len(l.logosCtMapFuncs) != 0 || l.logosCtConstMaps != 0 || len(l.logosCtMethods) != 0 {
		w.Len(len(l.logosCtHelpers))
		for _, idx := range slices.Sorted(maps.Keys(l.logosCtHelpers)) {
			w.Reloc(pkgbits.SectionObj, idx)
			w.Bool(l.logosCtHelpers[idx])
		}
	}
	if len(l.logosCtMapFuncs) != 0 || l.logosCtConstMaps != 0 || len(l.logosCtMethods) != 0 {
		w.Len(len(l.logosCtMapFuncs))
		for _, idx := range slices.Sorted(maps.Keys(l.logosCtMapFuncs)) {
			w.Reloc(pkgbits.SectionObj, idx)
		}
		w.Bool(l.logosCtConstMaps != 0)
		if l.logosCtConstMaps != 0 {
			w.Reloc(pkgbits.SectionMeta, l.logosCtConstMaps)
		}
		w.Len(len(l.logosCtMethods))
		for _, key := range slices.SortedFunc(maps.Keys(l.logosCtMethods), func(a, b logosCtMethodOrigin) int {
			if c := strings.Compare(a.recvPath, b.recvPath); c != 0 {
				return c
			}
			if c := strings.Compare(a.recvName, b.recvName); c != 0 {
				return c
			}
			return strings.Compare(a.name, b.name)
		}) {
			w.String(key.recvPath)
			w.String(key.recvName)
			w.String(key.name)
			w.Reloc(pkgbits.SectionObj, l.logosCtMethods[key])
		}
	}
}

// relocString copies the specified string from pr into the output
// export data file, deduplicating it against other strings.
func (l *linker) relocString(pr *pkgReader, idx index) index {
	return l.pw.StringIdx(pr.StringIdx(idx))
}

// relocPkg copies the specified package from pr into the output
// export data file, rewriting its import path to match how it was
// imported.
//
// TODO(mdempsky): Since CL 391014, we already have the compilation
// unit's import path, so there should be no need to rewrite packages
// anymore.
func (l *linker) relocPkg(pr *pkgReader, idx index) index {
	path := pr.PeekPkgPath(idx)

	if newidx, ok := l.pkgs[path]; ok {
		return newidx
	}

	r := pr.NewDecoder(pkgbits.SectionPkg, idx, pkgbits.SyncPkgDef)
	w := l.pw.NewEncoder(pkgbits.SectionPkg, pkgbits.SyncPkgDef)
	l.pkgs[path] = w.Idx

	// TODO(mdempsky): We end up leaving an empty string reference here
	// from when the package was originally written as "". Probably not
	// a big deal, but a little annoying. Maybe relocating
	// cross-references in place is the way to go after all.
	w.Relocs = l.relocAll(pr, r.Relocs)

	_ = r.String() // original path
	w.String(path)

	io.Copy(&w.Data, &r.Data)

	return w.Flush()
}

// relocObj copies the specified object from pr into the output export
// data file, rewriting its compiler-private extension data (e.g.,
// adding inlining cost and escape analysis results for functions).
func (l *linker) relocObj(pr *pkgReader, idx index) index {
	path, name, tag := pr.PeekObj(idx)
	sym := types.NewPkg(path, "").Lookup(name)

	if newidx, ok := l.decls[sym]; ok {
		return newidx
	}

	if tag == pkgbits.ObjStub && path != "builtin" && path != "unsafe" {
		pri, ok := objReader[sym]
		if !ok {
			base.Fatalf("missing reader for %q.%v", path, name)
		}
		assert(ok)

		pr = pri.pr
		idx = pri.idx

		path2, name2, tag2 := pr.PeekObj(idx)
		sym2 := types.NewPkg(path2, "").Lookup(name2)
		assert(sym == sym2)
		assert(tag2 != pkgbits.ObjStub)
		tag = tag2
	} else if pr != localPkgReader && pr.logosCtDefinitions[idx] {
		if pri := objReader[sym]; pri.pr != nil && pri.pr == localPkgReader {
			pr, idx = pri.pr, pri.idx
			_, _, localTag := pr.PeekObj(idx)
			assert(tag == localTag && pr.logosCtDefinitions[idx])
		}
	}

	w := l.pw.NewEncoderRaw(pkgbits.SectionObj)
	wext := l.pw.NewEncoderRaw(pkgbits.SectionObjExt)
	wname := l.pw.NewEncoderRaw(pkgbits.SectionName)
	wdict := l.pw.NewEncoderRaw(pkgbits.SectionObjDict)

	l.decls[sym] = w.Idx
	assert(wext.Idx == w.Idx)
	assert(wname.Idx == w.Idx)
	assert(wdict.Idx == w.Idx)

	l.relocCommon(pr, w, pkgbits.SectionObj, idx)
	l.relocCommon(pr, wname, pkgbits.SectionName, idx)
	l.relocCommon(pr, wdict, pkgbits.SectionObjDict, idx)

	// Generic types and functions won't have definitions, and imported
	// objects may not either.
	obj, _ := sym.Def.(*ir.Name)
	local := sym.Pkg == types.LocalPkg
	prepared := pr.logosCtDefinitions[idx]
	private := prepared || pr.logosCtMapFuncs[idx]
	compiledHere := local
	if private {
		if l.logosCtDecls == nil {
			l.logosCtDecls = make(map[*types.Sym]bool)
		}
		l.logosCtDecls[sym] = true
	}
	if prepared {
		compiledHere = pr == localPkgReader
	}
	if prepared && compiledHere {
		assert(obj != nil)
		switch tag {
		case pkgbits.ObjFunc:
			assert(obj.Func != nil && obj.Defn == obj.Func && obj.Func.Dupok())
			body, ok := bodyReader[obj.Func]
			assert(ok && body.pr == pr && body.dict != nil && body.dict.logosCtBody)
		case pkgbits.ObjVar:
			readonly, ok := pr.logosCtHelpers[idx]
			assert(ok && obj.LogosCtGlobal() && obj.Readonly() == readonly)
		default:
			base.Fatalf("unexpected comptime object %v", obj)
		}
	}

	if compiledHere && obj != nil {
		wext.Sync(pkgbits.SyncObject1)
		switch tag {
		case pkgbits.ObjFunc:
			l.relocFuncExt(wext, obj)
		case pkgbits.ObjType:
			l.relocTypeExt(wext, obj)
		case pkgbits.ObjVar:
			l.relocVarExt(wext, obj)
		}
		wext.Flush()
	} else {
		l.relocCommon(pr, wext, pkgbits.SectionObjExt, idx)
	}

	// Check if we need to export the inline bodies for functions and
	// methods.
	if private && !compiledHere && tag == pkgbits.ObjFunc {
		if body, ok := importBodyReader[sym]; ok {
			l.bodies[sym] = l.relocIdx(body.pr, pkgbits.SectionBody, body.idx)
		}
	} else if obj != nil {
		if obj.Op() == ir.ONAME && obj.Class == ir.PFUNC {
			l.exportBody(obj, compiledHere)
		}

		if obj.Op() == ir.OTYPE && !obj.Alias() {
			if typ := obj.Type(); !typ.IsInterface() {
				for _, method := range typ.Methods() {
					l.exportBody(method.Nname.(*ir.Name), local)
				}
			}
		}
	}
	if private {
		l.relocLogosCtTargets(pr)
	}

	return w.Idx
}

// exportBody exports the given function or method's body, if
// appropriate. local indicates whether it's a local function or
// method available on a locally declared type. (Due to cross-package
// type aliases, a method may be imported, but still available on a
// locally declared type.)
func (l *linker) exportBody(obj *ir.Name, local bool) {
	assert(obj.Op() == ir.ONAME && obj.Class == ir.PFUNC)

	fn := obj.Func
	if fn.Inl == nil {
		return // not inlinable anyway
	}

	// As a simple heuristic, if the function was declared in this
	// package or we inlined it somewhere in this package, then we'll
	// (re)export the function body. This isn't perfect, but seems
	// reasonable in practice. In particular, it has the nice property
	// that in the worst case, adding a blank import ensures the
	// function body is available for inlining.
	//
	// TODO(mdempsky): Reimplement the reachable method crawling logic
	// from typecheck/crawler.go.
	exportBody := local || fn.Inl.HaveDcl
	if !exportBody {
		return
	}

	sym := obj.Sym()
	if _, ok := l.bodies[sym]; ok {
		// Due to type aliases, we might visit methods multiple times.
		base.AssertfAt(obj.Type().Recv() != nil, obj.Pos(), "expected method: %v", obj)
		return
	}

	pri, ok := bodyReaderFor(fn)
	assert(ok)
	l.bodies[sym] = l.relocIdx(pri.pr, pkgbits.SectionBody, pri.idx)
}

// relocCommon copies the specified element from pr into w,
// recursively relocating any referenced elements as well.
func (l *linker) relocCommon(pr *pkgReader, w *pkgbits.Encoder, k pkgbits.SectionKind, idx index) {
	r := pr.NewDecoderRaw(k, idx)
	w.Relocs = l.relocAll(pr, r.Relocs)
	io.Copy(&w.Data, &r.Data)
	w.Flush()
}

func (l *linker) pragmaFlag(w *pkgbits.Encoder, pragma ir.PragmaFlag) {
	w.Sync(pkgbits.SyncPragma)
	w.Int(int(pragma))
}

func (l *linker) relocFuncExt(w *pkgbits.Encoder, name *ir.Name) {
	w.Sync(pkgbits.SyncFuncExt)

	l.pragmaFlag(w, name.Func.Pragma)
	l.linkname(w, name)

	if buildcfg.GOARCH == "wasm" {
		if name.Func.WasmImport != nil {
			w.String(name.Func.WasmImport.Module)
			w.String(name.Func.WasmImport.Name)
		} else {
			w.String("")
			w.String("")
		}
		if name.Func.WasmExport != nil {
			w.String(name.Func.WasmExport.Name)
		} else {
			w.String("")
		}
	}

	// Relocated extension data.
	w.Bool(true)

	// Record definition ABI so cross-ABI calls can be direct.
	// This is important for the performance of calling some
	// common functions implemented in assembly (e.g., bytealg).
	w.Uint64(uint64(name.Func.ABI))

	// Escape analysis.
	for _, f := range name.Type().RecvParams() {
		w.String(f.Note)
	}

	if inl := name.Func.Inl; w.Bool(inl != nil) {
		w.Len(int(inl.Cost))
		w.Bool(inl.CanDelayResults)
		if buildcfg.Experiment.NewInliner {
			w.String(inl.Properties)
		}
	}

	w.Sync(pkgbits.SyncEOF)
}

func (l *linker) relocTypeExt(w *pkgbits.Encoder, name *ir.Name) {
	w.Sync(pkgbits.SyncTypeExt)

	typ := name.Type()

	l.pragmaFlag(w, name.Pragma())

	// For type T, export the index of type descriptor symbols of T and *T.
	l.lsymIdx(w, "", reflectdata.TypeLinksym(typ))
	l.lsymIdx(w, "", reflectdata.TypeLinksym(typ.PtrTo()))

	if typ.Kind() != types.TINTER {
		for _, method := range typ.Methods() {
			l.relocFuncExt(w, method.Nname.(*ir.Name))
		}
	}
}

func (l *linker) relocVarExt(w *pkgbits.Encoder, name *ir.Name) {
	w.Sync(pkgbits.SyncVarExt)
	l.linkname(w, name)
}

func (l *linker) linkname(w *pkgbits.Encoder, name *ir.Name) {
	w.Sync(pkgbits.SyncLinkname)

	linkname := name.Sym().Linkname
	if !l.lsymIdx(w, linkname, name.Linksym()) {
		w.String(linkname)
		w.Bool(name.Linksym().IsLinknameStd())
	}
}

func (l *linker) lsymIdx(w *pkgbits.Encoder, linkname string, lsym *obj.LSym) bool {
	if lsym.PkgIdx > goobj.PkgIdxSelf || (lsym.PkgIdx == goobj.PkgIdxInvalid && !lsym.Indexed()) || linkname != "" {
		w.Int64(-1)
		return false
	}

	// For a defined symbol, export its index.
	// For re-exporting an imported symbol, pass its index through.
	w.Int64(int64(lsym.SymIdx))
	return true
}

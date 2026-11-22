// Copyright 2021 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// package importer implements package reading for gc-generated object files.
package importer

import (
	"cmp"
	"slices"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types"
	"cmd/compile/internal/types2"
	"cmd/internal/src"
	"internal/pkgbits"
)

type pkgReader struct {
	pkgbits.PkgDecoder

	ctxt              *types2.Context
	imports           map[string]*types2.Package
	enableAlias       bool // whether to use aliases
	restoreUnionTypes bool

	posBases []*syntax.PosBase
	pkgs     []*types2.Package
	typs     []types2.Type
}

func (pr *pkgReader) initCaches() {
	pr.posBases = make([]*syntax.PosBase, pr.NumElems(pkgbits.SectionPosBase))
	pr.pkgs = make([]*types2.Package, pr.NumElems(pkgbits.SectionPkg))
	pr.typs = make([]types2.Type, pr.NumElems(pkgbits.SectionType))
}

func ReadPackage(ctxt *types2.Context, imports map[string]*types2.Package, input pkgbits.PkgDecoder) *types2.Package {
	return readPackage(pkgReader{PkgDecoder: input, ctxt: ctxt, imports: imports, enableAlias: true})
}

func ReadLogosPackage(ctxt *types2.Context, imports map[string]*types2.Package, input pkgbits.PkgDecoder) *types2.Package {
	return readPackage(pkgReader{
		PkgDecoder: input, ctxt: ctxt, imports: imports, enableAlias: true, restoreUnionTypes: true,
	})
}

func readPackage(pr pkgReader) *types2.Package {
	pr.initCaches()

	r := pr.newReader(pkgbits.SectionMeta, pkgbits.PublicRootIdx, pkgbits.SyncPublic)
	pkg := r.pkg()

	if r.Version().Has(pkgbits.HasInit) {
		r.Bool()
	}

	for i, n := 0, r.Len(); i < n; i++ {
		// As if r.obj(), but avoiding the Scope.Lookup call,
		// to avoid eager loading of imports.
		r.Sync(pkgbits.SyncObject)
		if r.Version().Has(pkgbits.DerivedFuncInstance) {
			assert(!r.Bool())
		}
		r.p.objIdx(r.Reloc(pkgbits.SectionObj))
		assert(r.Len() == 0)
	}

	r.Sync(pkgbits.SyncEOF)

	pkg.MarkComplete()
	pkg = logosOverlayHook(pkg) // logos M3-S7 hook: .lgo importers rebuild Option types from opt facts.
	return pkg
}

// LogosCtNamedTypeReader decodes type declaration provenance after import
// overlays select the canonical Named and its type parameters.
type LogosCtNamedTypeReader struct {
	reader pkgReader
}

func NewLogosCtNamedTypeReader(input pkgbits.PkgDecoder, ctxt *types2.Context, packages map[string]*types2.Package) *LogosCtNamedTypeReader {
	return &LogosCtNamedTypeReader{reader: pkgReader{
		PkgDecoder: input,
		ctxt:       ctxt, imports: packages, enableAlias: true,
	}}
}

func (wire *LogosCtNamedTypeReader) Read(idx pkgbits.Index, origin *types2.Named) types2.LogosCtNamedTypeDefaults {
	return wire.reader.logosNamedTypePayload(idx, origin.Obj().Pkg(), origin.TypeParams().At)
}

func (pr *pkgReader) logosNamedTypePayload(idx pkgbits.Index, owner *types2.Package, typeParam func(int) *types2.TypeParam) types2.LogosCtNamedTypeDefaults {
	r := pr.tempReader(pkgbits.SectionObjDict, idx, pkgbits.SyncObject1)
	defer pr.retireReader(r)

	receivers, params := r.objDictTypeParamCounts()
	assert(receivers == 0)
	for range params {
		r.typInfo()
	}
	for range r.Len() {
		r.derivedInfo()
	}
	r.skipLogosCtRuntimeDict(params)
	if r.Data.Len() == 0 {
		return types2.LogosCtNamedTypeDefaults{}
	}
	assert(!r.Bool() && !r.Bool())                       // no prepared body or function template
	assert(r.Len() == 0 && r.Len() == 0 && r.Len() == 0) // no function slots or edges

	row := types2.LogosCtNamedTypeDefaults{Fields: make([]types2.LogosCtNamedDefault, r.Len())}
	for i := range row.Fields {
		field := r.String()
		path, name, kind := pr.PeekObj(r.Reloc(pkgbits.SectionObj))
		pkg := pr.imports[path]
		assert(kind == pkgbits.ObjFunc && pkg != nil && path == owner.Path())
		factory, ok := pkg.Scope().Lookup(name).(*types2.Func)
		assert(ok)
		row.Fields[i] = types2.LogosCtNamedDefault{Field: field, Factory: factory}
	}
	if r.Bool() {
		if pr.typs == nil {
			pr.initCaches()
		}
		row.RHS = (&logosCtEdgeReader{reader: r, typeParam: typeParam}).semanticType()
	}
	assert(r.Data.Len() == 0)
	return row
}

func (wire *LogosCtNamedTypeReader) ConstMapIndex(idx pkgbits.Index) map[string]pkgbits.Index {
	pr := &wire.reader
	r := pr.tempReader(pkgbits.SectionMeta, idx, pkgbits.SyncPrivate)
	defer pr.retireReader(r)
	n := r.Len()
	rows := make(map[string]pkgbits.Index, n)
	for range n {
		name := r.String()
		_, duplicate := rows[name]
		assert(!duplicate)
		rows[name] = r.Reloc(pkgbits.SectionMeta)
	}
	assert(r.Data.Len() == 0)
	return rows
}

func (wire *LogosCtNamedTypeReader) ConstMap(idx pkgbits.Index, owner *types2.Package, name string) types2.LogosCtConstMap {
	pr := &wire.reader
	if pr.typs == nil {
		pr.initCaches()
	}
	r := pr.tempReader(pkgbits.SectionMeta, idx, pkgbits.SyncPrivate)
	defer pr.retireReader(r)
	row := types2.LogosCtConstMap{Count: r.Len(), Bearing: r.Bool()}
	row.Lookup = wire.constMapFunc(r.Reloc(pkgbits.SectionObj), owner)
	row.Materialize = wire.constMapFunc(r.Reloc(pkgbits.SectionObj), owner)
	row.Iterate = wire.constMapFunc(r.Reloc(pkgbits.SectionObj), owner)
	row.Type = (&logosCtEdgeReader{reader: r}).semanticType()
	row.Binding, _ = owner.Scope().Lookup(name).(*types2.Var)
	if row.Binding == nil {
		row.Binding = types2.NewVar(row.Materialize.Pos(), owner, name, row.Type)
	}
	assert(r.Data.Len() == 0)
	return row
}

func (wire *LogosCtNamedTypeReader) constMapFunc(idx pkgbits.Index, owner *types2.Package) *types2.Func {
	pr := &wire.reader
	path, name, kind := pr.PeekObj(idx)
	assert(kind == pkgbits.ObjFunc && path == owner.Path())
	if obj, ok := owner.Scope().Lookup(name).(*types2.Func); ok {
		return obj
	}
	r := pr.tempReader(pkgbits.SectionObj, idx, pkgbits.SyncObject1)
	defer pr.retireReader(r)
	r.dict = pr.objDictIdx(idx)
	assert(len(r.dict.tbounds)+len(r.dict.rtbounds) == 0)
	pos := r.pos()
	if r.Version().Has(pkgbits.GenericMethods) {
		assert(!r.Bool())
	}
	r.typeParamNames(false, false)
	return types2.NewFunc(pos, owner, name, r.signature(nil, nil, nil))
}

type reader struct {
	pkgbits.Decoder

	p *pkgReader

	dict    *readerDict
	delayed []func()
}

type readerDict struct {
	rtbounds []typeInfo
	rtparams []*types2.TypeParam

	tbounds []typeInfo
	tparams []*types2.TypeParam
	args    []types2.Type

	derived      []derivedInfo
	derivedTypes []types2.Type
}

type readerTypeBound struct {
	derived  bool
	boundIdx int
}

func (pr *pkgReader) newReader(k pkgbits.SectionKind, idx pkgbits.Index, marker pkgbits.SyncMarker) *reader {
	return &reader{
		Decoder: pr.NewDecoder(k, idx, marker),
		p:       pr,
	}
}

func (pr *pkgReader) tempReader(k pkgbits.SectionKind, idx pkgbits.Index, marker pkgbits.SyncMarker) *reader {
	return &reader{
		Decoder: pr.TempDecoder(k, idx, marker),
		p:       pr,
	}
}

func (pr *pkgReader) retireReader(r *reader) {
	pr.RetireDecoder(&r.Decoder)
}

// @@@ Positions

func (r *reader) pos() syntax.Pos {
	r.Sync(pkgbits.SyncPos)
	if !r.Bool() {
		return syntax.Pos{}
	}

	// TODO(mdempsky): Delta encoding.
	posBase := r.posBase()
	line := r.Uint()
	col := r.Uint()
	return syntax.MakePos(posBase, line, col)
}

func (r *reader) posBase() *syntax.PosBase {
	return r.p.posBaseIdx(r.Reloc(pkgbits.SectionPosBase))
}

func (pr *pkgReader) posBaseIdx(idx pkgbits.Index) *syntax.PosBase {
	if b := pr.posBases[idx]; b != nil {
		return b
	}
	var b *syntax.PosBase
	{
		r := pr.tempReader(pkgbits.SectionPosBase, idx, pkgbits.SyncPosBase)

		filename := r.String()

		if r.Bool() {
			b = syntax.NewTrimmedFileBase(filename, true)
		} else {
			pos := r.pos()
			line := r.Uint()
			col := r.Uint()
			b = syntax.NewLineBase(pos, filename, true, line, col)
		}
		pr.retireReader(r)
	}

	pr.posBases[idx] = b
	return b
}

// @@@ Packages

func (r *reader) pkg() *types2.Package {
	r.Sync(pkgbits.SyncPkg)
	return r.p.pkgIdx(r.Reloc(pkgbits.SectionPkg))
}

func (pr *pkgReader) pkgIdx(idx pkgbits.Index) *types2.Package {
	// TODO(mdempsky): Consider using some non-nil pointer to indicate
	// the universe scope, so we don't need to keep re-reading it.
	if pkg := pr.pkgs[idx]; pkg != nil {
		return pkg
	}

	pkg := pr.newReader(pkgbits.SectionPkg, idx, pkgbits.SyncPkgDef).doPkg()
	pr.pkgs[idx] = pkg
	return pkg
}

func (r *reader) doPkg() *types2.Package {
	path := r.String()
	switch path {
	case "":
		path = r.p.PkgPath()
	case "builtin":
		return nil // universe
	case "unsafe":
		return types2.Unsafe
	}

	if pkg := r.p.imports[path]; pkg != nil {
		return pkg
	}

	name := r.String()
	pkg := types2.NewPackage(path, name)
	r.p.imports[path] = pkg

	// TODO(mdempsky): The list of imported packages is important for
	// go/types, but we could probably skip populating it for types2.
	imports := make([]*types2.Package, r.Len())
	for i := range imports {
		imports[i] = r.pkg()
	}
	pkg.SetImports(imports)

	return pkg
}

// @@@ Types

func (r *reader) typ() types2.Type {
	return r.p.typIdx(r.typInfo(), r.dict)
}

func (r *reader) typInfo() typeInfo {
	r.Sync(pkgbits.SyncType)
	if r.Bool() {
		return typeInfo{idx: pkgbits.Index(r.Len()), derived: true}
	}
	return typeInfo{idx: r.Reloc(pkgbits.SectionType), derived: false}
}

func (pr *pkgReader) typIdx(info typeInfo, dict *readerDict) types2.Type {
	idx := info.idx
	var where *types2.Type
	if info.derived {
		where = &dict.derivedTypes[idx]
		idx = dict.derived[idx].idx
	} else {
		where = &pr.typs[idx]
	}

	if typ := *where; typ != nil {
		return typ
	}

	var typ types2.Type
	{
		r := pr.tempReader(pkgbits.SectionType, idx, pkgbits.SyncTypeIdx)
		r.dict = dict

		typ = r.doTyp()
		assert(typ != nil)
		pr.retireReader(r)
	}

	// See comment in pkgReader.typIdx explaining how this happens.
	if prev := *where; prev != nil {
		return prev
	}

	*where = typ
	return typ
}

func (r *reader) doTyp() (res types2.Type) {
	switch tag := pkgbits.CodeType(r.Code(pkgbits.SyncType)); tag {
	default:
		base.FatalfAt(src.NoXPos, "unhandled type tag: %v", tag)
		panic("unreachable")

	case pkgbits.TypeBasic:
		return types2.Typ[r.Len()]

	case pkgbits.TypeNamed:
		return r.namedType(false)

	case pkgbits.TypeNamedCaptured:
		return r.namedType(true)

	case pkgbits.TypeTypeParam:
		n := r.Len()
		if r.dict.args != nil {
			return r.dict.args[n]
		}
		if n < len(r.dict.rtbounds) {
			return r.dict.rtparams[n]
		}
		return r.dict.tparams[n-len(r.dict.rtbounds)]

	case pkgbits.TypeArray:
		len := int64(r.Uint64())
		return types2.NewArray(r.typ(), len)
	case pkgbits.TypeChan:
		dir := types2.ChanDir(r.Len())
		return types2.NewChan(dir, r.typ())
	case pkgbits.TypeMap:
		return types2.NewMap(r.typ(), r.typ())
	case pkgbits.TypePointer:
		return types2.NewPointer(r.typ())
	case pkgbits.TypeSignature:
		return r.signature(nil, nil, nil)
	case pkgbits.TypeSlice:
		return types2.NewSlice(r.typ())
	case pkgbits.TypeStruct:
		return r.structType()
	case pkgbits.TypeInterface:
		return r.interfaceType()
	case pkgbits.TypeUnion:
		return r.unionType()
	}
}

func (r *reader) structType() *types2.Struct {
	fields := make([]*types2.Var, r.Len())
	var tags []string
	for i := range fields {
		pos := r.pos()
		pkg, name := r.selector()
		ftyp := r.typ()
		tag := r.String()
		embedded := r.Bool()

		fields[i] = types2.NewField(pos, pkg, name, ftyp, embedded)
		if tag != "" {
			for len(tags) < i {
				tags = append(tags, "")
			}
			tags = append(tags, tag)
		}
	}
	return types2.NewStruct(fields, tags)
}

func (r *reader) unionType() *types2.Union {
	terms := make([]*types2.Term, r.Len())
	for i := range terms {
		terms[i] = types2.NewTerm(r.Bool(), r.typ())
	}
	return types2.NewUnion(terms)
}

func (r *reader) interfaceType() *types2.Interface {
	methods := make([]*types2.Func, r.Len())
	embeddeds := make([]types2.Type, r.Len())
	implicit := len(methods) == 0 && len(embeddeds) == 1 && r.Bool()

	for i := range methods {
		pos := r.pos()
		pkg, name := r.selector()
		mtyp := r.signature(nil, nil, nil)
		methods[i] = types2.NewFunc(pos, pkg, name, mtyp)
	}

	for i := range embeddeds {
		embeddeds[i] = r.typ()
	}

	iface := types2.NewInterfaceType(methods, embeddeds)
	if implicit {
		iface.MarkImplicit()
	}
	return iface
}

func (r *reader) signature(recv *types2.Var, rtparams, tparams []*types2.TypeParam) *types2.Signature {
	r.Sync(pkgbits.SyncSignature)

	params := r.params()
	results := r.params()
	variadic := r.Bool()

	return types2.NewSignatureType(recv, rtparams, tparams, params, results, variadic)
}

func (r *reader) params() *types2.Tuple {
	r.Sync(pkgbits.SyncParams)
	params := make([]*types2.Var, r.Len())
	for i := range params {
		params[i] = r.param()
	}
	return types2.NewTuple(params...)
}

func (r *reader) param() *types2.Var {
	r.Sync(pkgbits.SyncParam)

	pos := r.pos()
	pkg, name := r.localIdent()
	typ := r.typ()

	return types2.NewParam(pos, pkg, name, typ)
}

// @@@ Objects

func (r *reader) obj() (types2.Object, []types2.Type) {
	pkg, name := r.p.objIdx(r.objReloc())
	obj := pkg.Scope().Lookup(name)

	targs := make([]types2.Type, r.Len())
	for i := range targs {
		targs[i] = r.typ()
	}

	return obj, targs
}

func (r *reader) namedType(captured bool) types2.Type {
	idx := r.objReloc()
	pkg, name, tag := r.p.objName(idx)
	explicit := make([]types2.Type, r.Len())
	for i := range explicit {
		explicit[i] = r.typ()
	}
	if baseName, suffix := types.SplitVargenSuffix(name); tag == pkgbits.ObjType && suffix != "" {
		var captures []types2.Type
		if captured {
			captures = make([]types2.Type, r.Len())
			for i := range captures {
				captures[i] = r.typ()
			}
		} else if dict := r.dict; dict != nil {
			captures = dict.args
			if captures == nil {
				for _, p := range dict.rtparams {
					captures = append(captures, p)
				}
				for _, p := range dict.tparams {
					captures = append(captures, p)
				}
			}
		}
		return r.p.logosCtReadABINamed(idx, pkg, baseName, name, captures, explicit, captured)
	}
	assert(!captured)
	r.p.declareObj(idx, pkg, name, tag)
	typ := pkg.Scope().Lookup(name).Type()
	if len(explicit) != 0 {
		typ, _ = types2.Instantiate(r.p.ctxt, typ, explicit, false)
	}
	return typ
}

func (r *reader) objReloc() pkgbits.Index {
	r.Sync(pkgbits.SyncObject)

	if r.Version().Has(pkgbits.DerivedFuncInstance) {
		assert(!r.Bool())
	}

	return r.Reloc(pkgbits.SectionObj)
}

func (pr *pkgReader) objIdx(idx pkgbits.Index) (*types2.Package, string) {
	pkg, name, tag := pr.objName(idx)
	pr.declareObj(idx, pkg, name, tag)
	return pkg, name
}

func (pr *pkgReader) objName(idx pkgbits.Index) (*types2.Package, string, pkgbits.CodeObj) {
	var objPkg *types2.Package
	var objName string
	var tag pkgbits.CodeObj
	{
		rname := pr.tempReader(pkgbits.SectionName, idx, pkgbits.SyncObject1)

		objPkg, objName = rname.qualifiedIdent()
		assert(objName != "")

		tag = pkgbits.CodeObj(rname.Code(pkgbits.SyncCodeObj))
		pr.retireReader(rname)
	}
	return objPkg, objName, tag
}

func (pr *pkgReader) declareObj(idx pkgbits.Index, objPkg *types2.Package, objName string, tag pkgbits.CodeObj) {
	if tag == pkgbits.ObjStub {
		base.Assertf(objPkg == nil || objPkg == types2.Unsafe, "unexpected stub package: %v", objPkg)
		return
	}

	if _, suffix := types.SplitVargenSuffix(objName); tag == pkgbits.ObjType && suffix != "" {
		return // Local declarations have bitstream identity, not package bindings.
	}

	objPkg.Scope().InsertLazy(objName, func() types2.Object {
		dict := pr.objDictIdx(idx)

		r := pr.newReader(pkgbits.SectionObj, idx, pkgbits.SyncObject1)
		r.dict = dict

		switch tag {
		default:
			panic("weird")

		case pkgbits.ObjAlias:
			pos := r.pos()
			var tparams []*types2.TypeParam
			if r.Version().Has(pkgbits.AliasTypeParamNames) {
				tparams = r.typeParamNames(false, false)
			}
			typ := r.typ()
			return newAliasTypeName(pr.enableAlias, pos, objPkg, objName, typ, tparams)

		case pkgbits.ObjConst:
			pos := r.pos()
			typ := r.typ()
			val := r.Value()
			return types2.NewConst(pos, objPkg, objName, typ, val)

		case pkgbits.ObjFunc:
			pos := r.pos()
			if r.Version().Has(pkgbits.GenericMethods) {
				assert(!r.Bool()) // generic methods are read in their defining type
			}
			tparams := r.typeParamNames(false, false)
			sig := r.signature(nil, nil, tparams)
			return types2.NewFunc(pos, objPkg, objName, sig)

		case pkgbits.ObjType:
			pos := r.pos()

			return types2.NewTypeNameLazy(pos, objPkg, objName, func(_ *types2.Named) ([]*types2.TypeParam, types2.Type, []*types2.Func, []func()) {
				tparams := r.typeParamNames(true, false)

				// TODO(mdempsky): Rewrite receiver types to underlying is an
				// Interface? The go/types importer does this (I think because
				// unit tests expected that), but cmd/compile doesn't care
				// about it, so maybe we can avoid worrying about that here.
				underlying := r.typ().Underlying()
				if pr.restoreUnionTypes {
					if _, ok := underlying.(*types2.Interface); ok {
						row := pr.logosNamedTypePayload(idx, objPkg, func(i int) *types2.TypeParam { return tparams[i] })
						if row.RHS != nil {
							iface, ok := row.RHS.(*types2.Interface)
							assert(ok)
							underlying = iface
						}
					}
				}

				type indexedMethod struct {
					index int // (or -1 in V4)
					fn    *types2.Func
				}
				var methods []indexedMethod

				if r.Version().Has(pkgbits.GenericMethods) {
					// V4 (go1.27.0) emitted all non-generic methods
					// before all generic ones, discarding source
					// order: a bug (go.dev/issue/81188).
					// V5 (go1.27.x) fixes it by emitting an explicit
					// index along with each method.

					// ordinary methods
					for range r.Len() {
						idx, m := r.method(true)
						methods = append(methods, indexedMethod{idx, m})
					}

					// generic methods
					for range r.Len() {
						// Careful: objIdx is used to read in package-scoped declarations, which
						// methods are not. Instead, decode it here. This makes it easier to
						// associate it with the type and avoids the main objIdx loop.
						idx := r.Reloc(pkgbits.SectionObj)

						t := pr.tempReader(pkgbits.SectionObj, idx, pkgbits.SyncObject1)
						t.dict = pr.objDictIdx(idx)

						pos := t.pos()
						assert(t.Bool()) // generic method
						pkg, name := t.selector()
						rtparams := t.typeParamNames(true, true)
						recv := t.param()
						methodIdx := -1
						if r.Version().Has(pkgbits.PreserveMethodOrder) {
							methodIdx = t.Len()
						}
						tparams := t.typeParamNames(true, false)
						sig := t.signature(recv, rtparams, tparams)

						r.delayed = append(r.delayed, t.delayed...) // propagate before retiring
						pr.retireReader(t)
						methods = append(methods, indexedMethod{methodIdx, types2.NewFunc(pos, pkg, name, sig)})
					}

					if r.Version().Has(pkgbits.PreserveMethodOrder) {
						slices.SortFunc(methods, func(a, b indexedMethod) int {
							return cmp.Compare(a.index, b.index)
						})
					}
				} else {
					for range r.Len() {
						_, m := r.method(true)
						methods = append(methods, indexedMethod{-1, m})
					}
				}

				funcs := make([]*types2.Func, len(methods))
				for i, m := range methods {
					funcs[i] = m.fn
				}

				return tparams, underlying, funcs, r.delayed
			})

		case pkgbits.ObjVar:
			pos := r.pos()
			typ := r.typ()
			return types2.NewVar(pos, objPkg, objName, typ)
		}
	})
}

func (r *reader) logosCtLocal(pos syntax.Pos, pkg *types2.Package, name, abiName, discriminator string, captures, explicit []types2.Type) (*types2.Named, bool) {
	return types2.LogosCtNewLocal(pos, pkg, name, abiName, discriminator, captures, explicit)
}

func (pr *pkgReader) logosCtReadABINamed(idx pkgbits.Index, pkg *types2.Package, name, abiName string, captures, explicit []types2.Type, captured bool) *types2.Named {
	d := pr.tempReader(pkgbits.SectionObjDict, idx, pkgbits.SyncObject1)
	nimplicit, nrecv, nexplicit := d.objDictCounts()
	assert(nrecv == 0 && nexplicit == len(explicit))
	assert(nimplicit <= len(captures) && (!captured || nimplicit == len(captures)))
	captures = captures[:nimplicit:nimplicit]
	for range nexplicit {
		d.typInfo()
	}
	dict := &readerDict{args: slices.Concat(captures, explicit)}
	dict.derived = make([]derivedInfo, d.Len())
	dict.derivedTypes = make([]types2.Type, len(dict.derived))
	for i := range dict.derived {
		dict.derived[i] = d.derivedInfo()
	}
	pr.retireReader(d)

	r := pr.newReader(pkgbits.SectionObj, idx, pkgbits.SyncObject1)
	r.dict = dict
	pos := r.pos()
	t, fresh := types2.LogosCtNewABILocal(pos, pkg, name, abiName, captures, explicit)
	if !fresh {
		return t
	}
	r.Sync(pkgbits.SyncTypeParamNames)
	for range explicit {
		r.pos()
		r.localIdent()
	}
	underlying := r.typ().Underlying()
	methods := make([]*types2.Func, r.Len())
	for i := range methods {
		_, methods[i] = r.method(true)
	}
	if r.Version().Has(pkgbits.GenericMethods) {
		assert(r.Len() == 0) // Methods cannot be declared on a function-local receiver.
	}
	types2.LogosCtSetLocal(t, underlying, methods)
	return t
}

func (pr *pkgReader) objDictIdx(idx pkgbits.Index) *readerDict {
	var dict readerDict
	{
		r := pr.tempReader(pkgbits.SectionObjDict, idx, pkgbits.SyncObject1)

		nreceivers, nexplicits := r.objDictTypeParamCounts()

		dict.rtbounds = make([]typeInfo, nreceivers)
		for i := range dict.rtbounds {
			dict.rtbounds[i] = r.typInfo()
		}

		dict.tbounds = make([]typeInfo, nexplicits)
		for i := range dict.tbounds {
			dict.tbounds[i] = r.typInfo()
		}

		dict.derived = make([]derivedInfo, r.Len())
		dict.derivedTypes = make([]types2.Type, len(dict.derived))
		for i := range dict.derived {
			dict.derived[i] = r.derivedInfo()
		}

		pr.retireReader(r)
	}
	// function references follow, but reader doesn't need those

	return &dict
}

func (r *reader) objDictTypeParamCounts() (receivers, explicits int) {
	implicits, receivers, explicits := r.objDictCounts()
	if implicits != 0 {
		base.Fatalf("unexpected package-scope object with %v implicit type parameter(s)", implicits)
	}
	return receivers, explicits
}

func (r *reader) objDictCounts() (implicits, receivers, explicits int) {
	implicits = r.Len()
	if r.Version().Has(pkgbits.GenericMethods) {
		receivers = r.Len()
	}
	return implicits, receivers, r.Len()
}

func (r *reader) derivedInfo() derivedInfo {
	info := derivedInfo{idx: r.Reloc(pkgbits.SectionType)}
	if r.Version().Has(pkgbits.DerivedInfoNeeded) {
		assert(!r.Bool())
	}
	return info
}

func (r *reader) typeParamNames(isLazy bool, isGenMeth bool) []*types2.TypeParam {
	r.Sync(pkgbits.SyncTypeParamNames)

	// Note: This code assumes there are no implicit type parameters.
	// This is fine since it only reads exported declarations, which
	// never have implicits.

	var in []typeInfo
	var out *[]*types2.TypeParam
	if isGenMeth {
		in = r.dict.rtbounds
		out = &r.dict.rtparams
	} else {
		in = r.dict.tbounds
		out = &r.dict.tparams
	}

	if len(in) == 0 {
		return nil
	}

	// Careful: Type parameter lists may have cycles. To allow for this,
	// we construct the type parameter list in two passes: first we
	// create all the TypeNames and TypeParams, then we construct and
	// set the bound type.

	// We have to save tparams outside of the closure, because typeParamNames
	// can be called multiple times with the same dictionary instance.
	tparams := make([]*types2.TypeParam, len(in))
	*out = tparams

	for i := range in {
		pos := r.pos()
		pkg, name := r.localIdent()

		tname := types2.NewTypeName(pos, pkg, name, nil)
		tparams[i] = types2.NewTypeParam(tname, nil)
	}

	// Type parameters that are read by lazy loaders cannot have their
	// constraints set eagerly; do them after loading (go.dev/issue/63285).
	if isLazy {
		// The reader dictionary will continue mutating before we have time
		// to call delayed functions; make a local copy of the constraints.
		types := make([]types2.Type, len(in))
		for i, info := range in {
			types[i] = r.p.typIdx(info, r.dict)
		}

		r.delayed = append(r.delayed, func() {
			for i, typ := range types {
				tparams[i].SetConstraint(typ)
			}
		})
	} else {
		for i, info := range in {
			tparams[i].SetConstraint(r.p.typIdx(info, r.dict))
		}
	}

	return tparams
}

func (r *reader) method(isLazy bool) (int, *types2.Func) {
	r.Sync(pkgbits.SyncMethod)
	idx := -1
	if r.Version().Has(pkgbits.PreserveMethodOrder) {
		idx = r.Len()
	}
	pos := r.pos()
	pkg, name := r.selector()

	rtparams := r.typeParamNames(isLazy, false)
	sig := r.signature(r.param(), rtparams, nil)

	_ = r.pos() // TODO(mdempsky): Remove; this is a hacker for linker.go.
	return idx, types2.NewFunc(pos, pkg, name, sig)
}

func (r *reader) qualifiedIdent() (*types2.Package, string) { return r.ident(pkgbits.SyncSym) }
func (r *reader) localIdent() (*types2.Package, string)     { return r.ident(pkgbits.SyncLocalIdent) }
func (r *reader) selector() (*types2.Package, string)       { return r.ident(pkgbits.SyncSelector) }

func (r *reader) ident(marker pkgbits.SyncMarker) (*types2.Package, string) {
	r.Sync(marker)
	return r.pkg(), r.String()
}

// newAliasTypeName returns a new TypeName, with a materialized *types2.Alias if supported.
func newAliasTypeName(aliases bool, pos syntax.Pos, pkg *types2.Package, name string, rhs types2.Type, tparams []*types2.TypeParam) *types2.TypeName {
	// Copied from x/tools/internal/aliases.NewAlias via
	// GOROOT/src/go/internal/gcimporter/ureader.go.
	if aliases {
		tname := types2.NewTypeName(pos, pkg, name, nil)
		a := types2.NewAlias(tname, rhs) // form TypeName -> Alias cycle
		a.SetTypeParams(tparams)
		return tname
	}
	assert(len(tparams) == 0)
	return types2.NewTypeName(pos, pkg, name, rhs)
}

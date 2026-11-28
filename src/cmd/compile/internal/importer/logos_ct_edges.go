// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package importer

import (
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
	"internal/pkgbits"
)

// Tags in the compiler-only LoGOs forwarding type graph, not pkgbits types.
const (
	LogosCtTypeReloc = iota
	LogosCtTypeParam
	LogosCtNamed
	LogosCtOption
	LogosCtConstView
	LogosCtReadonlyDescendants
	LogosCtTuple
	LogosCtArray
	LogosCtChan
	LogosCtMap
	LogosCtPointer
	LogosCtSlice
	LogosCtSignature
	LogosCtStruct
	LogosCtInterface
	LogosCtUnion
	LogosCtLocalDef
	LogosCtLocalRef
)

// ReadLogosCtEdges reads the optional LoGOs trailer for a package-level
// generic. origin must belong to the post-overlay packages map and match idx.
// A non-nil decoder is positioned at the comptime syntax template.
func ReadLogosCtEdges(ctxt *types2.Context, packages map[string]*types2.Package, input pkgbits.PkgDecoder, idx pkgbits.Index, origin *types2.Func) ([]types2.LogosCtEdge, *pkgbits.Decoder) {
	pr := pkgReader{PkgDecoder: input, ctxt: ctxt, imports: packages, enableAlias: true}
	r := reader{Decoder: pr.NewDecoder(pkgbits.SectionObjDict, idx, pkgbits.SyncObject1), p: &pr}

	tparams := origin.Signature().TypeParams()
	rtparams := origin.Signature().RecvTypeParams()
	nreceivers, nexplicits := r.objDictTypeParamCounts()
	assert(nreceivers == rtparams.Len() && nexplicits == tparams.Len())
	for range nreceivers {
		r.typInfo()
	}
	for range nexplicits {
		r.typInfo()
	}
	for range r.Len() {
		r.derivedInfo()
	}

	r.skipLogosCtRuntimeDict(nreceivers + nexplicits)
	if r.Data.Len() == 0 {
		return nil, nil
	}
	r.Bool() // concrete instance
	template := r.Bool()
	for range r.Len() {
		r.Len() // exact-function slot
		r.typInfo()
	}
	for range r.Len() {
		r.Len() // forwarding slot
		r.Len() // semantic edge ordinal
	}

	var edges []types2.LogosCtEdge
	if n := r.Len(); n != 0 {
		assert(packages[origin.Pkg().Path()] == origin.Pkg())
		pr.initCaches()
		edges = (&logosCtEdgeReader{reader: &r, typeParam: func(i int) *types2.TypeParam {
			if i < rtparams.Len() {
				return rtparams.At(i)
			}
			return tparams.At(i - rtparams.Len())
		}}).readEdges(n)
	}
	if template {
		decoder := r.Decoder
		return edges, &decoder
	}
	assert(r.Data.Len() == 0)
	return edges, nil
}

type logosCtEdgeReader struct {
	*reader
	typeParam func(int) *types2.TypeParam
	locals    []*types2.Named
}

func (r *logosCtEdgeReader) readEdges(n int) []types2.LogosCtEdge {
	edges := make([]types2.LogosCtEdge, n)
	for i := range edges {
		obj, abiArgs := r.obj()
		assert(len(abiArgs) == 0)
		fn, ok := obj.(*types2.Func)
		assert(ok)
		args := r.typeList()
		sig := fn.Signature()
		want := sig.TypeParams().Len()
		if sig.Recv() != nil {
			want++
		}
		assert(want != 0 && want == len(args))
		edges[i] = types2.LogosCtEdge{Origin: fn, Targs: args, Pos: r.pos()}
	}
	return edges
}

func (r *logosCtEdgeReader) typeList() []types2.Type {
	ts := make([]types2.Type, r.Len())
	for i := range ts {
		ts[i] = r.semanticType()
	}
	return ts
}

func (r *logosCtEdgeReader) semanticType() types2.Type {
	switch r.Len() {
	case LogosCtTypeReloc:
		info := r.typInfo()
		assert(!info.derived)
		return r.p.typIdx(info, nil)
	case LogosCtTypeParam:
		return r.typeParam(r.Len())
	case LogosCtNamed:
		idx := r.objReloc()
		assert(r.Len() == 0)
		pkg, name, tag := r.p.objName(idx)
		assert(tag == pkgbits.ObjType)
		r.p.declareObj(idx, pkg, name, tag)
		obj, ok := pkg.Scope().Lookup(name).(*types2.TypeName)
		assert(ok)
		named, ok := obj.Type().(*types2.Named)
		assert(ok)
		// Reading binder count from the dictionary avoids unpacking a type
		// whose recursive RHS is still being decoded.
		dict := r.p.tempReader(pkgbits.SectionObjDict, idx, pkgbits.SyncObject1)
		nrecv, nargs := dict.objDictTypeParamCounts()
		r.p.retireReader(dict)
		args := r.typeList()
		assert(nrecv == 0 && nargs != 0 && nargs == len(args))
		// A shared instantiation cache could replace a tuple argument with an
		// identical struct, losing its original form.
		t, _ := types2.Instantiate(nil, named, args, false)
		return t
	case LogosCtOption:
		return &types2.Option{Elem: r.semanticType()}
	case LogosCtConstView:
		constMap := r.Bool()
		return &types2.ConstView{Elem: r.semanticType(), ConstMap: constMap}
	case LogosCtReadonlyDescendants:
		return &types2.ReadonlyDescendants{Elem: r.semanticType()}
	case LogosCtTuple:
		n := r.Len()
		names := make([]string, n)
		var pos syntax.Pos
		var pkg *types2.Package
		if n != 0 {
			pos = r.pos()
			pkg = r.pkg()
		}
		ts := make([]types2.Type, n)
		for i := range ts {
			names[i] = r.String()
			ts[i] = r.semanticType()
		}
		return types2.NewLogosTuple(pos, pkg, names, ts)
	case LogosCtArray:
		n := int64(r.Uint64())
		return types2.NewArray(r.semanticType(), n)
	case LogosCtChan:
		dir := types2.ChanDir(r.Len())
		return types2.NewChan(dir, r.semanticType())
	case LogosCtMap:
		return types2.NewMap(r.semanticType(), r.semanticType())
	case LogosCtPointer:
		return types2.NewPointer(r.semanticType())
	case LogosCtSlice:
		return types2.NewSlice(r.semanticType())
	case LogosCtSignature:
		return r.semanticSignature(nil)
	case LogosCtStruct:
		return r.semanticStruct()
	case LogosCtInterface:
		return r.semanticInterface()
	case LogosCtUnion:
		terms := make([]*types2.Term, r.Len())
		for i := range terms {
			terms[i] = types2.NewTerm(r.Bool(), r.semanticType())
		}
		return types2.NewUnion(terms)
	case LogosCtLocalDef:
		return r.semanticLocal()
	case LogosCtLocalRef:
		t := r.locals[r.Len()]
		assert(t != nil)
		return t
	default:
		panic("unknown comptime forwarding type tag")
	}
}

func (r *logosCtEdgeReader) semanticStruct() *types2.Struct {
	fields := make([]*types2.Var, r.Len())
	var tags []string
	for i := range fields {
		pos := r.pos()
		pkg, name := r.selector()
		t := r.semanticType()
		tag := r.String()
		fields[i] = types2.NewField(pos, pkg, name, t, r.Bool())
		if tag != "" {
			for len(tags) < i {
				tags = append(tags, "")
			}
			tags = append(tags, tag)
		}
	}
	return types2.NewStruct(fields, tags)
}

func (r *logosCtEdgeReader) semanticSignature(recv *types2.Var) *types2.Signature {
	params := r.semanticParams()
	results := r.semanticParams()
	return types2.NewSignatureType(recv, nil, nil, params, results, r.Bool())
}

func (r *logosCtEdgeReader) semanticParams() *types2.Tuple {
	params := make([]*types2.Var, r.Len())
	for i := range params {
		pos := r.pos()
		pkg, name := r.localIdent()
		params[i] = types2.NewParam(pos, pkg, name, r.semanticType())
	}
	return types2.NewTuple(params...)
}

func (r *logosCtEdgeReader) semanticMethods(n int, owner *types2.Named) []*types2.Func {
	methods := make([]*types2.Func, n)
	for i := range methods {
		var recv *types2.Var
		if owner != nil {
			var t types2.Type = owner
			if r.Bool() {
				t = types2.NewPointer(t)
			}
			pos := r.pos()
			pkg, name := r.localIdent()
			recv = types2.NewParam(pos, pkg, name, t)
			recv.SetKind(types2.RecvVar)
		}
		pos := r.pos()
		pkg, name := r.selector()
		readonly := r.Bool()
		methods[i] = types2.NewFunc(pos, pkg, name, r.semanticSignature(recv))
		methods[i].SetLogosReadonly(readonly)
	}
	return methods
}

func (r *logosCtEdgeReader) semanticInterface() *types2.Interface {
	nMethods, nEmbeddeds := r.Len(), r.Len()
	implicit := r.Bool()
	methods := r.semanticMethods(nMethods, nil)
	embeddeds := make([]types2.Type, nEmbeddeds)
	for i := range embeddeds {
		embeddeds[i] = r.semanticType()
	}
	t := types2.NewInterfaceType(methods, embeddeds)
	if implicit {
		t.MarkImplicit()
	}
	return t
}

func (r *logosCtEdgeReader) semanticLocal() *types2.Named {
	i := len(r.locals)
	r.locals = append(r.locals, nil)
	pos := r.pos()
	pkg, name := r.selector()
	discriminator := r.String()
	abiPath, abiName, tag := r.p.PeekObj(r.Reloc(pkgbits.SectionObj))
	assert(pkg != nil && abiPath == pkg.Path() && tag == pkgbits.ObjType && abiName != "")
	captures := r.typeList()
	explicit := r.typeList()
	t, fresh := r.logosCtLocal(pos, pkg, name, abiName, discriminator, captures, explicit)
	r.locals[i] = t
	underlying := r.semanticType()
	methods := r.semanticMethods(r.Len(), t)
	if fresh {
		types2.LogosCtSetLocal(t, underlying, methods)
	}
	return t
}

func (r *reader) skipLogosCtRuntimeDict(nparams int) {
	for range nparams {
		r.Bool() // basic-interface bound
	}
	for range r.Len() { // type parameter method expressions
		r.Len()
		r.Sync(pkgbits.SyncSelector)
		r.Sync(pkgbits.SyncPkg)
		r.Reloc(pkgbits.SectionPkg)
		_ = r.String()
	}
	for range r.Len() { // subdictionaries
		r.objReloc()
		for range r.Len() {
			r.typInfo()
		}
	}
	for range r.Len() { // rtypes
		r.typInfo()
	}
	for range r.Len() { // itabs
		r.typInfo()
		r.typInfo()
	}
}

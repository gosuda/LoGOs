// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package noder

import (
	"fmt"
	"internal/pkgbits"
	"math/big"
	"strconv"

	"cmd/compile/internal/base"
	"cmd/compile/internal/importer"
	"cmd/compile/internal/types2"
	"cmd/internal/hash"
)

func (pw *pkgWriter) logosLocalObj(obj types2.Object) types2.Object {
	if pw.logosCtInfo == nil && len(pw.info.LogosUnionTypes) == 0 {
		return obj
	}
	pkg := obj.Pkg()
	if pkg == nil || pkg == pw.curpkg || pkg.Path() != pw.curpkg.Path() || obj.Parent() != pkg.Scope() {
		return obj
	}
	local := pw.curpkg.Scope().Lookup(obj.Name())
	base.Assertf(local != nil, "missing checked declaration for %v", obj)
	return local
}

func (pw *pkgWriter) logosCtLocalDecl(obj *types2.TypeName) *types2.TypeName {
	if obj.Pkg().Path() != pw.curpkg.Path() {
		return nil
	}
	if _, ok := pw.typDecls[obj]; ok {
		return obj
	}
	pos := obj.Pos()
	named, ok := obj.Type().(*types2.Named)
	if !ok {
		return nil
	}
	discriminator := types2.LogosCtLocalDiscriminator(named)
	var found *types2.TypeName
	for checked, decl := range pw.typDecls {
		if decl.gen == 0 || checked.Name() != obj.Name() {
			continue
		}
		candidate, ok := checked.Type().(*types2.Named)
		if !ok || types2.LogosCtLocalDiscriminator(candidate) != discriminator {
			continue
		}
		other := checked.Pos()
		if pos.Line() == other.Line() && pos.Col() == other.Col() &&
			pos.FileBase().Filename() == other.FileBase().Filename() {
			base.Assertf(found == nil, "ambiguous ABI declaration for comptime local type %v", obj)
			found = checked
		}
	}
	return found
}

func logosCtLocalABISuffix(decl typeDeclGen) string {
	if decl.ctInstance != "" {
		sum := hash.Sum32(fmt.Appendf(nil, "%s\x00%d", decl.ctInstance, decl.gen))
		var suffix big.Int
		return suffix.SetBytes(sum[:]).String()
	}
	if decl.gen != 0 {
		return strconv.Itoa(decl.gen)
	}
	return ""
}

func (w *writer) logosCtWriteEdges(edges []types2.LogosCtEdge) {
	var plain []types2.LogosCtEdge
	for _, edge := range edges {
		if edge.Origin.Signature().Recv() != nil {
			continue
		}
		plain = append(plain, edge)
	}
	e := logosCtEdgeWriter{writer: w}
	w.Len(len(plain))
	for _, edge := range plain {
		sig := edge.Origin.Signature()
		want := sig.TypeParams().Len()
		base.Assertf(want != 0 && len(edge.Targs) == want, "invalid comptime forwarding target %v", edge.Origin)
		w.obj(edge.Origin, nil)
		e.typeList(edge.Targs)
		w.pos(edge.Pos)
	}
}

type logosCtEdgeWriter struct {
	*writer
	locals map[*types2.Named]int
}

func (w *logosCtEdgeWriter) typeList(ts []types2.Type) {
	w.Len(len(ts))
	for _, t := range ts {
		w.semanticType(t)
	}
}

func (w *logosCtEdgeWriter) semanticType(typ types2.Type) {
	switch t := types2.Unalias(typ).(type) {
	case *types2.Basic:
		w.typeReloc(t)
	case *types2.TypeParam:
		w.Len(importer.LogosCtTypeParam)
		w.Len(w.dict.typeParamIndex(t))
	case *types2.Named:
		w.named(t)
	case *types2.Option:
		w.Len(importer.LogosCtOption)
		w.semanticType(t.Elem)
	case *types2.ConstView:
		w.Len(importer.LogosCtConstView)
		w.Bool(t.ConstMap)
		w.semanticType(t.Elem)
	case *types2.ReadonlyDescendants:
		w.Len(importer.LogosCtReadonlyDescendants)
		w.semanticType(t.Elem)
	case *types2.LogosTuple:
		w.Len(importer.LogosCtTuple)
		w.Len(t.Len())
		if t.Len() != 0 {
			first := t.Underlying().(*types2.Struct).Field(0)
			w.pos(first)
			w.pkg(first.Pkg())
		}
		for i := range t.Len() {
			w.String(t.Name(i))
			w.semanticType(t.Elem(i))
		}
	case *types2.Array:
		w.Len(importer.LogosCtArray)
		w.Uint64(uint64(t.Len()))
		w.semanticType(t.Elem())
	case *types2.Chan:
		w.Len(importer.LogosCtChan)
		w.Len(int(t.Dir()))
		w.semanticType(t.Elem())
	case *types2.Map:
		w.Len(importer.LogosCtMap)
		w.semanticType(t.Key())
		w.semanticType(t.Elem())
	case *types2.Pointer:
		w.Len(importer.LogosCtPointer)
		w.semanticType(t.Elem())
	case *types2.Slice:
		w.Len(importer.LogosCtSlice)
		w.semanticType(t.Elem())
	case *types2.Signature:
		w.Len(importer.LogosCtSignature)
		w.semanticSignature(t)
	case *types2.Struct:
		w.Len(importer.LogosCtStruct)
		w.Len(t.NumFields())
		for i := range t.NumFields() {
			f := t.Field(i)
			w.pos(f)
			w.selector(f)
			w.semanticType(f.Type())
			w.String(t.Tag(i))
			w.Bool(f.Embedded())
		}
	case *types2.Interface:
		w.semanticInterface(t)
	case *types2.Union:
		w.Len(importer.LogosCtUnion)
		w.Len(t.Len())
		for i := range t.Len() {
			term := t.Term(i)
			w.Bool(term.Tilde())
			w.semanticType(term.Type())
		}
	default:
		base.Fatalf("unexpected comptime forwarding type %v (%T)", typ, typ)
	}
}

func (w *logosCtEdgeWriter) typeReloc(t types2.Type) {
	w.Len(importer.LogosCtTypeReloc)
	w.typInfo(w.p.typIdx(t, nil))
}

func (w *logosCtEdgeWriter) named(t *types2.Named) {
	origin, captures, explicit := types2.LogosCtLocalNamed(t)
	if origin != nil {
		w.local(t, origin, captures, explicit)
		return
	}
	if t.TypeArgs().Len() == 0 {
		w.typeReloc(t)
		return
	}
	w.Len(importer.LogosCtNamed)
	w.obj(t.Origin().Obj(), nil)
	args := t.TypeArgs()
	w.Len(args.Len())
	for i := range args.Len() {
		w.semanticType(args.At(i))
	}
}

func (w *logosCtEdgeWriter) local(t, origin *types2.Named, captures, explicit []types2.Type) {
	if i, ok := w.locals[t]; ok {
		w.Len(importer.LogosCtLocalRef)
		w.Len(i)
		return
	}
	if w.locals == nil {
		w.locals = make(map[*types2.Named]int)
	}
	w.locals[t] = len(w.locals)
	w.Len(importer.LogosCtLocalDef)
	obj := origin.Obj()
	w.pos(obj)
	w.selector(obj)
	abi := w.p.logosCtLocalDecl(obj)
	if abi == nil {
		base.Assertf(obj.Pkg().Path() != w.p.curpkg.Path() && types2.LogosCtLocalABIName(origin) != "", "missing ABI declaration for comptime local type %v", obj)
		abi = obj
	}
	w.String(types2.LogosCtLocalDiscriminator(t))
	w.Reloc(pkgbits.SectionObj, w.p.objIdx(abi))
	w.typeList(captures)
	w.typeList(explicit)
	w.semanticType(t.Underlying())
	w.Len(t.NumMethods())
	for i := range t.NumMethods() {
		m := t.Method(i)
		receiver := m.Signature().Recv()
		_, pointer := types2.Unalias(receiver.Type()).(*types2.Pointer)
		w.Bool(pointer)
		w.pos(receiver)
		w.localIdent(receiver)
		w.semanticMethod(m)
	}
}

func (w *logosCtEdgeWriter) semanticSignature(sig *types2.Signature) {
	assert(sig.TypeParams().Len() == 0 && sig.RecvTypeParams().Len() == 0)
	for _, tuple := range [...]*types2.Tuple{sig.Params(), sig.Results()} {
		w.Len(tuple.Len())
		for i := range tuple.Len() {
			v := tuple.At(i)
			w.pos(v)
			w.localIdent(v)
			w.semanticType(v.Type())
		}
	}
	w.Bool(sig.Variadic())
}

func (w *logosCtEdgeWriter) semanticMethod(m *types2.Func) {
	w.pos(m)
	w.selector(m)
	w.Bool(m.LogosReadonly())
	w.semanticSignature(m.Signature())
}

func (w *logosCtEdgeWriter) semanticInterface(t *types2.Interface) {
	if t.NumExplicitMethods() == 0 && t.NumEmbeddeds() == 0 {
		w.typeReloc(t)
		return
	}
	w.Len(importer.LogosCtInterface)
	w.Len(t.NumExplicitMethods())
	w.Len(t.NumEmbeddeds())
	w.Bool(t.IsImplicit())
	for i := range t.NumExplicitMethods() {
		w.semanticMethod(t.ExplicitMethod(i))
	}
	for i := range t.NumEmbeddeds() {
		w.semanticType(t.EmbeddedType(i))
	}
}

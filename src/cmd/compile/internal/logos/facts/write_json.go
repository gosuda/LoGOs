// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package facts

import (
	"cmd/compile/internal/types2"
)

func writeJSON(pkg *types2.Package, info *types2.Info, f *Facts) {
	if pkg == nil || pkg.Scope() == nil || info == nil {
		return
	}
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types2.TypeName)
		if !ok || obj == nil || !obj.Exported() || obj.IsAlias() {
			continue
		}
		named, ok := obj.Type().(*types2.Named)
		if !ok {
			continue
		}
		if tp := named.TypeParams(); tp != nil && tp.Len() > 0 {
			continue
		}
		if st, ok := named.Underlying().(*types2.Struct); ok {
			writeJSONStruct(pkg, info, f, name, named, st)
			continue
		}
		if iface, ok := named.Underlying().(*types2.Interface); ok {
			writeJSONUnion(f, name, iface)
			continue
		}
		if hasUnmarshal(pkg, named) {
			f.JsonMetas = append(f.JsonMetas, JsonMetaFact{Type: name, HasDecoder: true})
		}
	}
}

func hasUnmarshal(pkg *types2.Package, named *types2.Named) bool {
	if named == nil || named.Obj() == nil {
		return false
	}
	obj, _, _ := types2.LookupFieldOrMethod(types2.NewPointer(named), false, pkg, "UnmarshalJSON")
	fn, ok := obj.(*types2.Func)
	if !ok {
		return false
	}
	sig, ok := fn.Type().(*types2.Signature)
	if !ok || sig.Params().Len() != 1 || sig.Variadic() || sig.Results().Len() != 1 {
		return false
	}
	byteObj := types2.Universe.Lookup("byte")
	if byteObj == nil {
		return false
	}
	if !types2.Identical(types2.Unalias(sig.Params().At(0).Type()), types2.NewSlice(byteObj.Type())) {
		return false
	}
	errObj := types2.Universe.Lookup("error")
	if errObj == nil {
		return false
	}
	return types2.Identical(types2.Unalias(sig.Results().At(0).Type()), errObj.Type())
}

func writeJSONStruct(pkg *types2.Package, info *types2.Info, f *Facts, name string, named *types2.Named, st *types2.Struct) {
	eligible := info.LogosStaticJSON != nil && info.LogosStaticJSON[named]
	decoder := hasUnmarshal(pkg, named)
	if !eligible && !decoder {
		return
	}
	f.JsonMetas = append(f.JsonMetas, JsonMetaFact{Type: name, HasDecoder: decoder, Eligible: eligible})
	if !eligible {
		return
	}
	n := 0
	for i := range st.NumFields() {
		field, ok := types2.LogosJSONFieldOf(st, i)
		if !ok {
			continue
		}
		f.JsonKeys = append(f.JsonKeys, JsonKeyFact{Type: name, Index: n, Key: field.Name})
		n++
	}
}

func writeJSONUnion(f *Facts, name string, iface *types2.Interface) {
	terms, ok := jsonUnionTerms(iface)
	if !ok {
		return
	}
	flat := flattenJSONTerms(terms)
	if len(flat) == 0 {
		return
	}
	for i, m := range flat {
		text, ok := jsonMemberText(m)
		if !ok {
			return
		}
		f.UnionMembers = append(f.UnionMembers, UnionMemberFact{Type: name, Index: i, Member: text})
	}
}

func jsonUnionTerms(iface *types2.Interface) ([]types2.Type, bool) {
	var out []types2.Type
	for i := range iface.NumEmbeddeds() {
		u, ok := iface.EmbeddedType(i).(*types2.Union)
		if !ok {
			continue
		}
		for j := range u.Len() {
			term := u.Term(j)
			if term.Tilde() {
				return nil, false
			}
			out = append(out, term.Type())
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

func flattenJSONTerms(terms []types2.Type) []types2.Type {
	var out []types2.Type
	for _, term := range terms {
		elem := term
		if ptr, ok := types2.Unalias(term).(*types2.Pointer); ok {
			elem = ptr.Elem()
		}
		iface, ok := types2.Unalias(elem).Underlying().(*types2.Interface)
		if !ok {
			out = append(out, term)
			continue
		}
		sub, ok := jsonUnionTerms(iface)
		if !ok || len(sub) == 0 {
			out = append(out, term)
			continue
		}
		out = append(out, flattenJSONTerms(sub)...)
	}
	return out
}

func jsonMemberText(t types2.Type) (string, bool) {
	ptr := false
	u := types2.Unalias(t)
	if p, ok := u.(*types2.Pointer); ok {
		ptr = true
		u = types2.Unalias(p.Elem())
	}
	named, ok := u.(*types2.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return "", false
	}
	if tp := named.TypeParams(); tp != nil && tp.Len() > 0 {
		return "", false
	}
	if args := named.TypeArgs(); args != nil && args.Len() > 0 {
		for i := range args.Len() {
			if _, ok := types2.Unalias(args.At(i)).(*types2.TypeParam); ok {
				return "", false
			}
		}
	}
	path := named.Obj().Pkg().Path()
	if path == "" || named.Obj().Name() == "" {
		return "", false
	}
	text := path + "." + named.Obj().Name()
	if ptr {
		text = "*" + text
	}
	return text, true
}

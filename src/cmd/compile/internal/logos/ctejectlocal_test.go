// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"testing"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// TestCtEjectMentionsTypeForms checks that a helper type names a body type
// through every type form that can hold one, so the helper moves into the
// instance body with that type.
func TestCtEjectMentionsTypeForms(t *testing.T) {
	pkg := types2.NewPackage("example.com/p", "p")
	local := types2.NewTypeName(syntax.Pos{}, pkg, "Local", nil)
	named := types2.NewNamed(local, types2.Typ[types2.Int], nil)
	sites := map[*types2.TypeName]ctEjectTypeSite{local: {}}

	get := types2.NewFunc(syntax.Pos{}, pkg, "Get", types2.NewSignatureType(nil, nil, nil, nil,
		types2.NewTuple(types2.NewVar(syntax.Pos{}, pkg, "", named)), false))
	union := types2.NewUnion([]*types2.Term{types2.NewTerm(false, named), types2.NewTerm(false, types2.Typ[types2.String])})
	forms := map[string]types2.Type{
		"interface method": types2.NewInterfaceType([]*types2.Func{get}, nil),
		"interface union":  types2.NewInterfaceType(nil, []types2.Type{union}),
		"option":           &types2.Option{Elem: named},
		"const view":       &types2.ConstView{Elem: types2.NewSlice(named)},
		"readonly":         &types2.ReadonlyDescendants{Elem: types2.NewPointer(named)},
		"tuple":            types2.NewLogosTuple(syntax.Pos{}, pkg, []string{"n", "local"}, []types2.Type{types2.Typ[types2.Int], named}),
	}
	for label, typ := range forms {
		out := make(map[*types2.TypeName]bool)
		ctEjectMentions(typ, sites, out, make(map[*types2.Named]bool))
		if !out[local] {
			t.Errorf("%s %s: body type Local not found", label, types2.TypeString(typ, nil))
		}
	}
}

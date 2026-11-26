// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import "testing"

func TestHasUnexportedInlineWrappers(t *testing.T) {
	pkg := NewPackage("example.com/layout", "layout")
	hidden := NewStruct([]*Var{NewField(nopos, pkg, "hidden", Typ[Int32], false)}, nil)
	public := NewStruct([]*Var{NewField(nopos, pkg, "Public", Typ[Int32], false)}, nil)
	wrapped := NewStruct([]*Var{NewField(nopos, pkg, "Public", &Option{Elem: hidden}, false)}, nil)
	tests := []struct {
		name string
		typ  Type
		want bool
	}{
		{"hidden", hidden, true},
		{"public", public, false},
		{"const", &ConstView{Elem: hidden}, true},
		{"readonly", &ReadonlyDescendants{Elem: hidden}, true},
		{"option", &Option{Elem: hidden}, true},
		{"nested", wrapped, true},
		{"array-option", NewArray(&Option{Elem: hidden}, 2), true},
		{"tuple", NewLogosTuple(nopos, pkg, nil, []Type{hidden, Typ[Int32]}), true},
		{"option-pointer", &Option{Elem: NewPointer(hidden)}, false},
		{"zero-array", NewArray(&Option{Elem: hidden}, 0), false},
		{"slice", NewSlice(hidden), false},
		{"map", NewMap(Typ[String], hidden), false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := LogosHasUnexported(test.typ); got != test.want {
				t.Errorf("hidden field = %v, want %v", got, test.want)
			}
		})
	}
}

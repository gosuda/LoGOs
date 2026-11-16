// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import "testing"

func TestStructDefaultGenericDerivedOrigins(t *testing.T) {
	const src = `package main

type Base[Element any] struct {
	Count int = 17
	Items []Element = []Element{}
}
type Middle[Item any] Base[Item]
type Leaf[Value any] Middle[Value]

func main() {
	a := Leaf[string]{}
	b := Leaf[int]{Count: 23}
	if a.Count != 17 || b.Count != 23 || a.Items == nil || b.Items == nil {
		panic("derived generic defaults lost their substitutions")
	}
	a.Items = append(a.Items, "a")
	b.Items = append(b.Items, 2)
	if a.Items[0] != "a" || b.Items[0] != 2 {
		panic("derived generic defaults have the wrong element type")
	}
	println("ok derived generic defaults")
}
`
	runStructDefaultLowerings(t, src, "ok derived generic defaults\n")
}

func TestStructDefaultInlineAnonymousVar(t *testing.T) {
	const src = `package main

func main() {
	const count = 17
	var x = struct { Count int = count }{}
	if x.Count != 17 {
		panic("anonymous var initializer lost its declaration binding")
	}
	println("ok anonymous var defaults")
}
`
	runStructDefaultLowerings(t, src, "ok anonymous var defaults\n")
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import "testing"

// A local definition has a distinct named type but retains the struct's
// defaults. Passing through the producer helper must preserve that type.
func TestStructDefaultLocalDerivedType(t *testing.T) {
	const src = `package main

type Cfg struct {
	Count int = 17
	Items []int = []int{17}
}

func main() {
	type Local Cfg
	type Alias = Local
	a := Local{}
	b := Local{Count: 23}
	c := &Alias{}
	var exact Local = a
	if exact.Count != 17 || b.Count != 23 || c.Count != 17 {
		panic("local derived type lost its defaults or explicit initializer")
	}
	if a.Items[0] != 17 || b.Items[0] != 17 || c.Items[0] != 17 {
		panic("local derived type lost its composite default")
	}
	a.Items[0] = 1
	if b.Items[0] != 17 || c.Items[0] != 17 {
		panic("local derived type defaults share mutable storage")
	}
	println("ok local derived defaults")
}
`
	runStructDefaultLowerings(t, src, "ok local derived defaults\n")
}

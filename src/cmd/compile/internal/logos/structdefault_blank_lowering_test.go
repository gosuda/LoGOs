// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import "testing"

// Blank defaults are checked at their declaration, but cannot become literal
// keys. Ignoring them must preserve named defaults and explicit initializers.
func TestStructDefaultBlankFieldLowering(t *testing.T) {
	const src = `package main

type Unused struct {
	_ int
	_ string = "unused"
	Value int = 31
}

type Cfg struct {
	_ int = 1
	_ string = "padding"
	Value int = 17
}

type OnlyBlank struct {
	_ int = 1
	_ string = "padding"
}

func main() {
	a := Cfg{}
	b := Cfg{Value: 23}
	c := &Cfg{}
	if a.Value != 17 || b.Value != 23 || c.Value != 17 {
		panic("blank fields changed named defaults or explicit values")
	}
	_ = OnlyBlank{}
	_ = &OnlyBlank{}
	println("ok blank defaults")
}
`
	runStructDefaultLowerings(t, src, "ok blank defaults\n")
}

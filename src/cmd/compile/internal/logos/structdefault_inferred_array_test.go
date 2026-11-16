// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import "testing"

// The inferred length belongs to the composite literal. Capturing its type
// must retain the checked array length and the constant's declaration binding.
func TestStructDefaultLocalInferredArray(t *testing.T) {
	const src = `package main

const defaultValue = 17

func main() {
	type Cfg struct {
		Values [3]int = [...]int{defaultValue, 2, 3}
	}
	a := Cfg{}
	b := &Cfg{}
	c := Cfg{Values: [3]int{7, 8, 9}}
	if a.Values != [3]int{17, 2, 3} || b.Values != a.Values ||
		c.Values != [3]int{7, 8, 9} {
		panic("inferred array default lost its length, values, or explicit override")
	}
	a.Values[0] = 99
	if b.Values[0] != 17 {
		panic("inferred array defaults share storage")
	}
	println("ok inferred array defaults")
}
`
	runStructDefaultLowerings(t, src, "ok inferred array defaults\n")
}

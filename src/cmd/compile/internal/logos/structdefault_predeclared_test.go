// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import "testing"

func TestStructDefaultPredeclaredShadows(t *testing.T) {
	for _, tc := range []struct {
		name string
		pkg  string
		body string
	}{
		{"package type and value", "type bool = int\nconst true = 7\n", ""},
		{"local type and value", "", "type bool = int\nconst true = 7\n_ = true\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "package main\n" + tc.pkg + `
type Cfg struct {
	First int = 17
	Second int = 31
	Unused map[int]int // Keep automatic serializers outside this mask test.
}
func main() {
` + tc.body + `
	a := Cfg{}
	b := Cfg{First: 23}
	if a.First != 17 || a.Second != 31 || b.First != 23 || b.Second != 31 {
		panic("predeclared shadows changed defaults")
	}
	println("ok predeclared shadows")
}
`
			runStructDefaultLowerings(t, src, "ok predeclared shadows\n")
		})
	}
}

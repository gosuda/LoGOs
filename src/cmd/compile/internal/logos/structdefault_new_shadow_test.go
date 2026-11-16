// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import "testing"

// Applying defaults must not introduce a reference to a shadowed builtin.
// Both explicit addresses and implicit pointer elements keep their exact type.
func TestStructDefaultNewShadow(t *testing.T) {
	const src = `package main

type Cfg struct { N int = 17 }

func value(c *Cfg) int { return c.N }

func main() {
	new := 0
	_ = new
	a := &Cfg{}
	b := &Cfg{N: 23}
	rows := []*Cfg{{}, {N: 29}}
	var exact *Cfg = a
	var explicit *Cfg = b
	var exactRows []*Cfg = rows
	if value(exact) != 17 || value(explicit) != 23 ||
		value(exactRows[0]) != 17 || value(exactRows[1]) != 29 {
		panic("shadowed new changed pointer defaults or explicit fields")
	}
	a.N = 41
	if value(exactRows[0]) != 17 || value(explicit) != 23 {
		panic("defaulted pointer literals share storage")
	}
	println("ok shadowed new")
}
`
	runStructDefaultLowerings(t, src, "ok shadowed new\n")
}

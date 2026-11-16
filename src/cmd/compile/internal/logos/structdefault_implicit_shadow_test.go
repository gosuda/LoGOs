// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import "testing"

func TestStructDefaultImplicitElementTypeShadow(t *testing.T) {
	const src = `package main

type Cfg struct { N int = 17 }
type Rows []Cfg
type Alias = Cfg
type AliasRows []Alias

func value(c Cfg) int { return c.N }

func main() {
	Cfg := 0
	Alias := 0
	_ = Cfg
	_ = Alias
	a := Rows{{}}
	b := Rows{{N: 23}}
	c := AliasRows{{}}
	d := AliasRows{{N: 29}}
	var exact Rows = a
	var aliasExact AliasRows = c
	if value(exact[0]) != 17 || value(b[0]) != 23 || value(aliasExact[0]) != 17 || value(d[0]) != 29 {
		panic("implicit element type captured a constructor-local name")
	}
	println("ok implicit element type")
}
`
	runStructDefaultLowerings(t, src, "ok implicit element type\n")
}

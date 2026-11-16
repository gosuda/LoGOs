// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import "testing"

func TestStructDefaultLocalGenericCapture(t *testing.T) {
	const src = `package main

func main() {
	type Box[T0, T1, T2, T3, T4, T5, T6, T7, T8, T9, T10 any] struct {
		Count int = 17
		Items []T10 = []T10{}
	}
	c := Box[int, int, int, int, int, int, int, int, int, int, string]{}
	var _ []string = c.Items
	if c.Count != 17 || c.Items == nil || len(c.Items) != 0 {
		panic("local generic default lost its type argument")
	}
	println("ok local generic capture")
}
`
	runStructDefaultLowerings(t, src, "ok local generic capture\n")
}

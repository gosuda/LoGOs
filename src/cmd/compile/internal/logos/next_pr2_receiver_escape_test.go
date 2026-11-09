// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"testing"
)

// TestNextLoopEscapedReceiverHeaderStaysUnedited pins the regression where
// receiverIsPackage assumes a local receiver xs cannot be rebound by a
// top-level function. If &xs escaped to a package variable prior to the loop,
// a top-level function d invoked inside the loop can rebind or clear the
// slice header through the pointer, causing the original loop over len(xs) to
// terminate early. Auto-editing to a captured-header map changes semantics
// and must not occur.
func TestNextLoopEscapedReceiverHeaderStaysUnedited(t *testing.T) {
	imp := newTestImporter(t)
	src := `package p

var saved *[]int

func d(x int) int {
	*saved = nil
	return x
}

func g(xs []int) []int {
	saved = &xs
	var ys []int
	for i := 0; i < len(xs); i++ {
		ys = append(ys, d(xs[i]))
	}
	return ys
}
`
	edits, tasks := scanSnippet(t, imp, src)
	if len(edits) != 0 || len(tasks) == 0 {
		t.Fatalf("escaped receiver header: edits=%d tasks=%d, want 0 edits and a task", len(edits), len(tasks))
	}
}

// TestNextLoopEscapedCapturingClosureStaysUnedited pins the regression where
// a closure capturing local receiver xs escapes to a package function variable
// prior to the loop. When top-level function d invokes that stored closure,
// it mutates the local receiver during iteration. The scanner must not treat
// top-level call d as safe simply because xs is a local variable.
func TestNextLoopEscapedCapturingClosureStaysUnedited(t *testing.T) {
	imp := newTestImporter(t)
	src := `package p

var savedHook func()

func d(x int) int {
	if savedHook != nil {
		savedHook()
	}
	return x
}

func g(xs []int) []int {
	savedHook = func() { xs = nil }
	var ys []int
	for i := 0; i < len(xs); i++ {
		ys = append(ys, d(xs[i]))
	}
	return ys
}
`
	edits, tasks := scanSnippet(t, imp, src)
	if len(edits) != 0 || len(tasks) == 0 {
		t.Fatalf("escaped capturing closure: edits=%d tasks=%d, want 0 edits and a task", len(edits), len(tasks))
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package escape

import (
	"cmd/compile/internal/base"
	"cmd/compile/internal/ir"
)

// LogosFuncAllocs maps each top-level function of the package to whether
// escape analysis decided a heap allocation inside it, including inside its
// function literals and the bodies inlined into it. The noder arms it with
// -logos.allocfacts so the AllocFree facts and the recursive
// //logos:zeroalloc check can read it; it is nil for every other compile.
var LogosFuncAllocs map[LogosFuncKey]bool

// logosRecordFuncAlloc marks the top-level function that lexically holds fn
// as allocating.
func logosRecordFuncAlloc(fn *ir.Func) {
	if LogosFuncAllocs == nil {
		return
	}
	for fn.ClosureParent != nil {
		fn = fn.ClosureParent
	}
	pos := base.Ctxt.InnermostPos(fn.Pos())
	LogosFuncAllocs[LogosFuncKey{File: pos.RelFilename(), Line: pos.Line()}] = true
}

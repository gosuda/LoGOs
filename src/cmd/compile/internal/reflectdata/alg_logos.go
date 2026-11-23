// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package reflectdata

import "cmd/compile/internal/ir"

// markSharedAlg makes every package emit the same code for a generated
// equality or hash function. These functions are DUPOK and named by their
// signature, so runtime packages, which are never instrumented, and
// instrumented packages emit the same symbol. Sanitizer or checkptr
// instrumentation in only some copies makes the payloads differ, and the
// linker keeps an arbitrary one. Instrumented callers check the operands of
// an equality call instead (see walk.instrumentEqOperands).
func markSharedAlg(fn *ir.Func) {
	fn.Pragma |= ir.Norace | ir.NoCheckPtr
}

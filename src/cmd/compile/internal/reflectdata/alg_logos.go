// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package reflectdata

import (
	"go/constant"

	"cmd/compile/internal/base"
	"cmd/compile/internal/ir"
	"cmd/compile/internal/typecheck"
	"cmd/compile/internal/types"
	"cmd/internal/src"
)

// markSharedAlg makes every package emit the same code for a generated
// equality or hash function. These functions are DUPOK and named by their
// signature, so runtime packages, which are never instrumented, and
// instrumented packages emit the same symbol. Sanitizer or checkptr
// instrumentation in only some copies makes the payloads differ, and the
// linker keeps an arbitrary one. The payloads stay uninstrumented;
// explicit operand checks are emitted inside them instead (see
// emitEqLoadChecks), which also covers indirect callers, and ASan
// callers check operands at the call site as well for attribution (see
// walk.instrumentEqOperands).
func markSharedAlg(fn *ir.Func) {
	fn.Pragma |= ir.Norace | ir.NoCheckPtr
}

// eqCheck records one compared operand range of a generated equality
// function: the two addresses and a byte count expression. Checks for the
// recorded ranges are emitted alongside the loads they guard, under the
// same early exits, so they report exactly the memory the comparison
// reads. That covers every caller, including the indirect ones that
// never pass walkCompare (reflect.Value.IsZero, map key comparisons,
// interface comparisons reaching efaceeq), and never checks memory a
// short-circuited comparison skips.
type eqCheck struct {
	p, q ir.Node
	size ir.Node
	// msan reports whether an MSan read applies to this range. Only
	// loads qualify: a memequal content range stops at the first
	// difference, so an upfront MSan read would check memory the
	// comparison may never reach.
	msan bool
}

// noteEqLoad records the operand range load is about to compare. The
// recorded checks are flushed from test, after the previous
// comparison's exit branch, so each check runs exactly when the
// comparison reaches the corresponding load.
func noteEqLoad(pos src.XPos, pending []eqCheck, p, q ir.Node, size int64) []eqCheck {
	if size == 0 {
		return pending
	}
	sz := ir.NewBasicLit(pos, types.Types[types.TUINTPTR], constant.MakeInt64(size))
	return append(pending, eqCheck{p: p, q: q, size: sz, msan: true})
}

// noteEqContent records a memequal content range flushed like a load
// range, but for ASan only (see eqCheck.msan).
func noteEqContent(pending []eqCheck, p, q, size ir.Node) []eqCheck {
	return append(pending, eqCheck{p: p, q: q, size: size})
}

// emitEqChecks appends explicit sanitizer reads for every recorded
// operand range and reports whether it emitted any. The emitted calls
// are identical in every package: sanitizer builds of NoInstrument
// packages have their flags forced off, so the condition keys off the
// requested mode captured before that exemption instead of the flags.
func emitEqChecks(fn *ir.Func, pos src.XPos, pending []eqCheck) bool {
	asan := base.Flag.Cfg.ASanRequested
	msan := base.Flag.Cfg.MSanRequested
	if !asan && !msan {
		return false
	}
	for _, c := range pending {
		if asan {
			emitEqRead(fn, pos, "asanread", c.p, c.q, c.size)
		}
		if msan && c.msan {
			emitEqRead(fn, pos, "msanread", c.p, c.q, c.size)
		}
	}
	return len(pending) != 0
}

// emitEqRead appends runtime read checks of size bytes at p and q. The
// runtime source takes unsafe.Pointer, but the compiler's universe
// declares the address as uintptr, so convert like walk does.
func emitEqRead(fn *ir.Func, pos src.XPos, name string, p, q, size ir.Node) {
	read := typecheck.LookupRuntime(name)
	pa := typecheck.Conv(typecheck.Conv(p, types.Types[types.TUNSAFEPTR]), types.Types[types.TUINTPTR])
	qa := typecheck.Conv(typecheck.Conv(q, types.Types[types.TUNSAFEPTR]), types.Types[types.TUINTPTR])
	fn.Body.Append(typecheck.Call(pos, read, []ir.Node{pa, size}, false))
	fn.Body.Append(typecheck.Call(pos, read, []ir.Node{qa, size}, false))
}

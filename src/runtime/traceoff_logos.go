// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime

import (
	"internal/abi"
)

// Execution-tracer no-op stubs.
//
// When a wasm binary does not link the runtime/trace package (and does not
// call runtime.StartTrace/StopTrace/ReadTrace), the execution tracer can
// never activate: there is no API left that starts it. The linker
// (traceOffTrim in cmd/link) then rewrites direct calls to the tracer's
// writer hooks below into these stubs, so the whole writer backend becomes
// unreachable and drops out of the binary. Every stub returns the zero
// value the hook's disabled path would produce, so a program that never
// starts a trace behaves exactly as before.
//
// The stubs stay unreferenced (and are dropped by dead code elimination)
// whenever the tracer is linked.

//go:nosplit
func traceOffAcquire() traceLocker { return traceLocker{} }

//go:nosplit
func traceOffAdvance(bool) {}

//go:nosplit
func traceOffFalse() bool { return false }

//go:nosplit
func traceOffVoid() {}

//go:nosplit
func traceOffG() *g { return nil }

//go:nosplit
func traceOffRelease(traceLocker) {}

//go:nosplit
func traceOffM(*m) {}

//go:nosplit
func traceOffLocker(tl traceLocker) { _ = tl }

//go:nosplit
func traceOffLockerUintptr(tl traceLocker, _ uintptr) { _ = tl }

//go:nosplit
func traceOffLockerUint64(tl traceLocker, _ uint64) { _ = tl }

//go:nosplit
func traceOffLockerInt32(tl traceLocker, _ int32) { _ = tl }

//go:nosplit
func traceOffLockerBool(tl traceLocker, _ bool) { _ = tl }

//go:nosplit
func traceOffLockerFalse(tl traceLocker) bool { _ = tl; return false }

//go:nosplit
func traceOffLocker2Uintptr(tl traceLocker, _, _ uintptr) { _ = tl }

//go:nosplit
func traceOffLockerP(tl traceLocker, _ *p) { _ = tl }

//go:nosplit
func traceOffLockerMspan(tl traceLocker, _ *mspan) { _ = tl }

//go:nosplit
func traceOffGoCreate(tl traceLocker, _ *g, _ uintptr, _ bool) { _ = tl }

//go:nosplit
func traceOffGoPark(tl traceLocker, _ traceBlockReason, _ int) { _ = tl }

//go:nosplit
func traceOffGoStop(tl traceLocker, _ traceGoStopReason) { _ = tl }

//go:nosplit
func traceOffGoUnpark(tl traceLocker, _ *g, _ int) { _ = tl }

//go:nosplit
func traceOffHeapObjectAlloc(tl traceLocker, _ uintptr, _ *abi.Type) { _ = tl }

//go:nosplit
func traceOffSTWStart(tl traceLocker, _ stwReason) { _ = tl }

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ld

import (
	"cmd/internal/objabi"
	"cmd/link/internal/loader"
	"strings"
)

// Execution-tracer trim for wasm.
//
// A wasm binary that links neither the runtime/trace package nor any other
// caller of the tracer's writer hooks can never start a trace: no linked
// code starts one. The writer backend (about sixty kilobytes of scheduler,
// GC, allocator and memory-profiling hooks plus the generation, buffer and
// CPU-reader machinery behind them) is then dead weight pulled in only
// through direct calls from always-linked core runtime paths.
//
// traceOffTrim rewrites those direct calls to the matching traceOff stubs
// in the runtime (same signature, zero behavior) before the dead-code flood,
// so the backend drops out like any other unreachable code. Programs that
// link runtime/trace, call runtime.StartTrace/StopTrace/ReadTrace, or reach
// a hook from outside the runtime (for example through //go:linkname) keep
// every edge and link exactly as before.

// traceOffHook names a writer hook and its zero-behavior stub.
type traceOffHook struct {
	hook string
	stub string
}

// traceOffHooks lists every tracer writer entry reachable from core runtime
// paths outside the tracer subsystem itself. Deliberately absent:
// runtime.trace (the boot print, not the tracer), runtime.traceback* (panic
// traces), runtime.tracemalloc (the memory profiler), and the enabled-state
// guards (traceEnabled, traceShuttingDown, traceAllocFreeEnabled is in the
// list because malloc treats it as an event gate; its stub returns false).
var traceOffHooks = []traceOffHook{
	{"runtime.traceAcquire", "runtime.traceOffAcquire"},
	{"runtime.traceAcquireEnabled", "runtime.traceOffAcquire"},
	{"runtime.traceAdvance", "runtime.traceOffAdvance"},
	{"runtime.traceAllocFreeEnabled", "runtime.traceOffFalse"},
	{"runtime.traceLockInit", "runtime.traceOffVoid"},
	{"runtime.traceLocker.GCDone", "runtime.traceOffLocker"},
	{"runtime.traceLocker.GCMarkAssistDone", "runtime.traceOffLocker"},
	{"runtime.traceLocker.GCMarkAssistStart", "runtime.traceOffLocker"},
	{"runtime.traceLocker.GCStart", "runtime.traceOffLocker"},
	{"runtime.traceLocker.GCSweepDone", "runtime.traceOffLocker"},
	{"runtime.traceLocker.GCSweepSpan", "runtime.traceOffLockerUintptr"},
	{"runtime.traceLocker.GCSweepStart", "runtime.traceOffLocker"},
	{"runtime.traceLocker.GoCreate", "runtime.traceOffGoCreate"},
	{"runtime.traceLocker.GoEnd", "runtime.traceOffLocker"},
	{"runtime.traceLocker.GoPark", "runtime.traceOffGoPark"},
	{"runtime.traceLocker.GoStart", "runtime.traceOffLocker"},
	{"runtime.traceLocker.GoStop", "runtime.traceOffGoStop"},
	{"runtime.traceLocker.GoSysCall", "runtime.traceOffLocker"},
	{"runtime.traceLocker.GoSysExit", "runtime.traceOffLockerBool"},
	{"runtime.traceLocker.GoUnpark", "runtime.traceOffGoUnpark"},
	{"runtime.traceLocker.Gomaxprocs", "runtime.traceOffLockerInt32"},
	{"runtime.traceLocker.GoroutineStackAlloc", "runtime.traceOffLocker2Uintptr"},
	{"runtime.traceLocker.GoroutineStackFree", "runtime.traceOffLockerUintptr"},
	{"runtime.traceLocker.HeapAlloc", "runtime.traceOffLockerUint64"},
	{"runtime.traceLocker.HeapGoal", "runtime.traceOffLocker"},
	{"runtime.traceLocker.HeapObjectAlloc", "runtime.traceOffHeapObjectAlloc"},
	{"runtime.traceLocker.HeapObjectFree", "runtime.traceOffLockerUintptr"},
	{"runtime.traceLocker.ProcStart", "runtime.traceOffLocker"},
	{"runtime.traceLocker.ProcSteal", "runtime.traceOffLockerP"},
	{"runtime.traceLocker.ProcStop", "runtime.traceOffLockerP"},
	{"runtime.traceLocker.STWDone", "runtime.traceOffLocker"},
	{"runtime.traceLocker.STWStart", "runtime.traceOffSTWStart"},
	{"runtime.traceLocker.SpanAlloc", "runtime.traceOffLockerMspan"},
	{"runtime.traceLocker.SpanFree", "runtime.traceOffLockerMspan"},
	{"runtime.traceLocker.ok", "runtime.traceOffLockerFalse"},
	{"runtime.traceReader", "runtime.traceOffG"},
	{"runtime.traceReaderAvailable", "runtime.traceOffG"},
	{"runtime.traceRelease", "runtime.traceOffRelease"},
	{"runtime.traceThreadDestroy", "runtime.traceOffM"},
}

// traceOffAnchors are symbols that exist only when the runtime/trace package
// is linked. Any one of them disables the trim.
var traceOffAnchors = []string{
	"runtime/trace..inittask",
	"runtime/trace.Start",
	"runtime/trace.NewTask",
}

func traceOffTrim(d *deadcodePass) {
	if d.ctxt.HeadType != objabi.Hjs && d.ctxt.HeadType != objabi.Hwasip1 {
		return
	}
	ldr := d.ldr
	for _, a := range traceOffAnchors {
		if ldr.Lookup(a, 0) != 0 || (abiInternalVer != 0 && ldr.Lookup(a, abiInternalVer) != 0) {
			return
		}
	}
	stubFor := make(map[string]string, len(traceOffHooks))
	for _, h := range traceOffHooks {
		stubFor[h.hook] = h.stub
	}
	type rewrite struct {
		src  loader.Sym
		idx  int
		stub loader.Sym
	}
	var rewrites []rewrite
	n := ldr.NSym()
	for i := 1; i < n; i++ {
		s := loader.Sym(i)
		if !ldr.SymType(s).IsText() {
			continue
		}
		relocs := ldr.Relocs(s)
		if relocs.Count() == 0 {
			continue
		}
		pkg := ldr.SymPkg(s)
		if pkg == "" {
			name := ldr.SymName(s)
			if k := strings.Index(name, "."); k >= 0 {
				pkg = name[:k]
			}
		}
		for j := 0; j < relocs.Count(); j++ {
			r := relocs.At(j)
			if !r.Type().IsDirectCall() {
				continue
			}
			stubName, ok := stubFor[ldr.SymName(r.Sym())]
			if !ok {
				continue
			}
			if pkg != "" && pkg != "runtime" {
				// Reached from outside the runtime (for example through
				// //go:linkname): the tracer may be driven there.
				return
			}
			ver := ldr.SymVersion(r.Sym())
			stub := ldr.Lookup(stubName, ver)
			if stub == 0 {
				stub = ldr.Lookup(stubName, 0)
			}
			if stub == 0 {
				continue
			}
			rewrites = append(rewrites, rewrite{src: s, idx: j, stub: stub})
		}
	}
	bySrc := make(map[loader.Sym]*loader.SymbolBuilder)
	for _, w := range rewrites {
		su := bySrc[w.src]
		if su == nil {
			su = ldr.MakeSymbolUpdater(w.src)
			bySrc[w.src] = su
		}
		su.SetRelocSym(w.idx, w.stub)
		if d.ctxt.Debugvlog > 1 {
			d.ctxt.Logf("traceoff: rewrite %s ref to %s\n", ldr.SymName(w.src), ldr.SymName(w.stub))
		}
	}
	// Mark the stubs reachable; everything they replace drops out in the flood.
	seen := make(map[loader.Sym]bool)
	for _, w := range rewrites {
		if !seen[w.stub] {
			seen[w.stub] = true
			d.mark(w.stub, 0)
		}
	}
}

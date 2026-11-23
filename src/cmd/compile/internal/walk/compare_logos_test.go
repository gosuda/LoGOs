// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package walk

import (
	"testing"

	"cmd/compile/internal/ir"
	"cmd/compile/internal/typecheck"
	"cmd/compile/internal/types"
	"cmd/internal/src"
)

// eqLoopPaddedElem builds struct{ Tag uint8; Data T } with computed layout.
// The alignment gap after Tag selects field-wise MemorySanitizer checks,
// the shape the nested-array instrumentation must stay bounded on.
func eqLoopPaddedElem(t *testing.T, data *types.Type) *types.Type {
	t.Helper()
	elem := types.NewStruct([]*types.Field{
		types.NewField(src.NoXPos, types.LocalPkg.Lookup("Tag"), types.Types[types.TUINT8]),
		types.NewField(src.NoXPos, types.LocalPkg.Lookup("Data"), data),
	})
	types.CalcSize(elem)
	return elem
}

func eqLoopBody(t *testing.T, typ *types.Type) ir.Nodes {
	t.Helper()
	fn := regionTestFn()
	defer func() { ir.CurFunc = nil }()
	read := typecheck.LookupRuntime("msanread")
	if read == nil {
		t.Fatal("no runtime msanread")
	}
	addrBase := typecheck.TempAt(src.NoXPos, fn, types.Types[types.TUINTPTR])
	idx := typecheck.TempAt(src.NoXPos, fn, types.Types[types.TUINTPTR])
	var body ir.Nodes
	instrumentEqLoopElem(&body, read, addrBase, idx, typ, 0)
	return body
}

// eqLoopReads counts the read calls and nested loops joined directly into
// body, following one block wrapper each. Base-address assigns are setup,
// not checks. A nested runtime loop counts as one node here; its own body
// is checked separately.
func eqLoopReads(t *testing.T, body ir.Nodes) (reads int, loops int) {
	t.Helper()
	var one func(n ir.Node)
	one = func(n ir.Node) {
		switch n.Op() {
		case ir.OCALL, ir.OCALLFUNC:
			reads++
		case ir.OFOR:
			loops++
		case ir.OAS:
		default:
			t.Fatalf("loop body holds %v, want a read call, a nested loop, or a base assign", n.Op())
		}
	}
	for _, n := range body {
		if blk, ok := n.(*ir.BlockStmt); ok {
			for _, m := range blk.List {
				one(m)
			}
			continue
		}
		one(n)
	}
	return reads, loops
}

// A nested array the equality compares contiguously needs one range read,
// not one read per entry: [65536]uint64 must not unroll into 65536 calls.
func TestInstrumentEqNestedArrayBounded(t *testing.T) {
	data := types.NewArray(types.Types[types.TUINT64], 65536)
	types.CalcSize(data)
	elem := eqLoopPaddedElem(t, data)
	body := eqLoopBody(t, elem)
	reads, loops := eqLoopReads(t, body)
	if len(body) != 2 || reads != 2 || loops != 0 {
		t.Fatalf("contiguous nested array holds %d nodes with %d reads and %d loops, want 2 nodes with 2 reads and no loop", len(body), reads, loops)
	}
	var found *ir.CallExpr
	if blk, ok := body[1].(*ir.BlockStmt); ok {
		for _, n := range blk.List {
			if call, ok := n.(*ir.CallExpr); ok {
				found = call
			}
		}
	}
	if found == nil || len(found.Args) != 2 {
		t.Fatalf("second node is not a single range read: %v", body[1])
	}
	if got := ir.Int64Val(found.Args[1]); got != data.Size() {
		t.Fatalf("range read covers %d bytes, want the whole %d-byte array", got, data.Size())
	}
}

// A nested array that skips bytes needs its own runtime loop over the
// elements, not a compile-time unroll of every entry.
func TestInstrumentEqNestedSkipsLoop(t *testing.T) {
	sub := types.NewStruct([]*types.Field{
		types.NewField(src.NoXPos, types.LocalPkg.Lookup("B"), types.Types[types.TUINT8]),
		types.NewField(src.NoXPos, types.LocalPkg.Lookup("X"), types.Types[types.TUINT64]),
	})
	types.CalcSize(sub)
	subs := types.NewArray(sub, 4)
	types.CalcSize(subs)
	elem := eqLoopPaddedElem(t, subs)
	body := eqLoopBody(t, elem)
	reads, loops := eqLoopReads(t, body)
	if len(body) != 3 || reads != 1 || loops != 1 {
		t.Fatalf("skipped nested array holds %d nodes with %d reads and %d loops, want 3 nodes with 1 read and 1 loop", len(body), reads, loops)
	}
	blk, ok := body[2].(*ir.BlockStmt)
	if !ok {
		t.Fatalf("third node is not a block: %T", body[2])
	}
	var loop *ir.ForStmt
	for _, n := range blk.List {
		if f, ok := n.(*ir.ForStmt); ok {
			loop = f
		}
	}
	if loop == nil {
		t.Fatalf("third node holds no loop: %v", body[2])
	}
	innerReads, innerLoops := eqLoopReads(t, loop.Body)
	if innerReads != 2 || innerLoops != 0 {
		t.Fatalf("nested loop body holds %d reads and %d loops, want the 2 field reads", innerReads, innerLoops)
	}
}

// A zero-sized subtree emits no check at all, however deep it nests: the
// compiler must not walk a trillion empty entries to learn that.
func TestInstrumentEqZeroSizedSkipped(t *testing.T) {
	zero := types.NewArray(types.NewStruct(nil), 8)
	types.CalcSize(zero)
	elem := types.NewStruct([]*types.Field{
		types.NewField(src.NoXPos, types.LocalPkg.Lookup("Z"), zero),
		types.NewField(src.NoXPos, types.LocalPkg.Lookup("Tag"), types.Types[types.TUINT8]),
	})
	types.CalcSize(elem)
	body := eqLoopBody(t, elem)
	reads, loops := eqLoopReads(t, body)
	if len(body) != 1 || reads != 1 || loops != 0 {
		t.Fatalf("zero-sized subtree holds %d nodes with %d reads and %d loops, want 1 node with the tag read only", len(body), reads, loops)
	}
}

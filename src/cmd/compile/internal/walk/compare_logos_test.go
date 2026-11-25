// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package walk

import (
	"testing"

	"cmd/compile/internal/base"
	"cmd/compile/internal/ir"
	"cmd/compile/internal/typecheck"
	"cmd/compile/internal/types"
	"cmd/internal/src"
)

// eqPinStruct builds struct{ Tag uint8; ID uint64 } with computed layout:
// one tag byte, seven padding bytes, then the id.
func eqPinStruct(t *testing.T) *types.Type {
	t.Helper()
	typ := types.NewStruct([]*types.Field{
		types.NewField(src.NoXPos, types.LocalPkg.Lookup("Tag"), types.Types[types.TUINT8]),
		types.NewField(src.NoXPos, types.LocalPkg.Lookup("ID"), types.Types[types.TUINT64]),
	})
	types.CalcSize(typ)
	return typ
}

// eqPinOffset reads the byte offset of an operand address: the base plus
// any field offsets added onto it.
func eqPinOffset(t *testing.T, n ir.Node) int64 {
	t.Helper()
	switch n.Op() {
	case ir.OADD:
		b := n.(*ir.BinaryExpr)
		return eqPinOffset(t, b.X) + eqPinOffset(t, b.Y)
	case ir.OCONVNOP, ir.OCONV:
		return eqPinOffset(t, n.(*ir.ConvExpr).X)
	case ir.ONAME:
		return 0
	case ir.OLITERAL:
		return ir.Int64Val(n)
	}
	t.Fatalf("operand address holds %v, want base plus offsets", n.Op())
	return 0
}

// eqPinEmit runs instrumentEqOperands under the given sanitizer flags and
// returns the (offset, size) pairs of the emitted operand checks.
func eqPinEmit(t *testing.T, asan, msan bool, typ *types.Type) [][2]int64 {
	t.Helper()
	fn := regionTestFn()
	defer func() { ir.CurFunc = nil }()
	defer func(asan, msan bool) { base.Flag.ASan, base.Flag.MSan = asan, msan }(base.Flag.ASan, base.Flag.MSan)
	base.Flag.ASan, base.Flag.MSan = asan, msan
	addr := typecheck.TempAt(src.NoXPos, fn, types.Types[types.TUNSAFEPTR])
	var init ir.Nodes
	call := new(ir.CallExpr)
	instrumentEqOperands(call, &init, typ, addr)
	var out [][2]int64
	for _, n := range *call.PtrInit() {
		c, ok := n.(*ir.CallExpr)
		if !ok {
			t.Fatalf("operand check holds %v, want a call", n.Op())
		}
		name, ok := c.Fun.(*ir.Name)
		if !ok || name.Sym() == nil || name.Sym().Name != "asanread" {
			t.Fatalf("operand check calls %v, want runtime.asanread", c.Fun)
		}
		if len(c.Args) != 2 {
			t.Fatalf("operand check has %d args, want address and size", len(c.Args))
		}
		out = append(out, [2]int64{eqPinOffset(t, c.Args[0]), ir.Int64Val(c.Args[1])})
	}
	return out
}

func eqPinCheck(t *testing.T, got, want [][2]int64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("operand checks cover %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("operand checks cover %v, want %v", got, want)
		}
	}
}

// Each read form the call site still emits is pinned: a struct is
// checked field by field, an array by one range read, and zero-sized
// and blank parts emit nothing.
func TestInstrumentEqReadForms(t *testing.T) {
	sub := eqPinStruct(t)
	nested := types.NewStruct([]*types.Field{
		types.NewField(src.NoXPos, types.LocalPkg.Lookup("A"), types.Types[types.TUINT8]),
		types.NewField(src.NoXPos, types.LocalPkg.Lookup("S"), sub),
	})
	types.CalcSize(nested)
	blanked := types.NewStruct([]*types.Field{
		types.NewField(src.NoXPos, types.LocalPkg.Lookup("Tag"), types.Types[types.TUINT8]),
		types.NewField(src.NoXPos, types.LocalPkg.Lookup("_"), types.NewArray(types.Types[types.TUINT8], 7)),
		types.NewField(src.NoXPos, types.LocalPkg.Lookup("ID"), types.Types[types.TUINT64]),
	})
	types.CalcSize(blanked)
	arr := types.NewArray(types.Types[types.TUINT8], 4)
	types.CalcSize(arr)
	empty := types.NewStruct(nil)
	types.CalcSize(empty)
	for _, tc := range []struct {
		name string
		typ  *types.Type
		want [][2]int64
	}{
		{"Struct", sub, [][2]int64{{0, 1}, {8, 8}}},
		{"BlankSkipped", blanked, [][2]int64{{0, 1}, {8, 8}}},
		{"ArrayRange", arr, [][2]int64{{0, 4}}},
		{"Nested", nested, [][2]int64{{0, 1}, {8, 1}, {16, 8}}},
		{"Empty", empty, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			eqPinCheck(t, eqPinEmit(t, true, false, tc.typ), tc.want)
		})
	}
}

// MSan checks live in the generated equality, where they follow its
// early exits. The call site must emit nothing under MSan: an upfront
// check would read elements a short-circuited comparison never reaches.
func TestInstrumentEqMSanEmitsNothing(t *testing.T) {
	sub := eqPinStruct(t)
	arr := types.NewArray(sub, 2)
	types.CalcSize(arr)
	for _, tc := range []struct {
		name string
		typ  *types.Type
	}{
		{"Struct", sub},
		{"Array", arr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := eqPinEmit(t, false, true, tc.typ); len(got) != 0 {
				t.Fatalf("MSan call site emits %d checks, want none", len(got))
			}
		})
	}
}

// Without a sanitizer the call site stays silent.
func TestInstrumentEqNoSanitizerEmitsNothing(t *testing.T) {
	sub := eqPinStruct(t)
	if got := eqPinEmit(t, false, false, sub); len(got) != 0 {
		t.Fatalf("unsanitized call site emits %d checks, want none", len(got))
	}
}

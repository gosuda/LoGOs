// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package walk

import (
	"cmd/compile/internal/base"
	"cmd/compile/internal/ir"
	"cmd/compile/internal/typecheck"
	"cmd/compile/internal/types"
)

// instrumentEqOperands adds the ASan or MSan reads of both operands of an
// equality call. Neither memequal nor a generated equality function is
// instrumented (see reflectdata.markSharedAlg), so the call site checks the
// memory they compare. Like ssagen's instrumentFields, a struct is checked
// field by field, so its padding is never read. Under MSan, an array whose
// elements skip bytes (padding or blank fields) is checked element by
// element in a loop, for the same reason; other arrays keep one range read.
func instrumentEqOperands(call *ir.CallExpr, init *ir.Nodes, t *types.Type, operands ...ir.Node) {
	if !base.Flag.ASan && !base.Flag.MSan || ir.CurFunc.Pragma&ir.Norace != 0 {
		return
	}
	read := typecheck.LookupRuntime("asanread")
	if base.Flag.MSan {
		read = typecheck.LookupRuntime("msanread")
	}
	for _, addr := range operands {
		instrumentEqRead(call, init, read, addr, t, 0)
	}
}

func instrumentEqRead(call *ir.CallExpr, init *ir.Nodes, read, addr ir.Node, t *types.Type, off int64) {
	if t.IsStruct() && !t.IsSIMD() {
		for _, f := range t.Fields() {
			if !f.Sym.IsBlank() {
				instrumentEqRead(call, init, read, addr, f.Type, off+f.Offset)
			}
		}
		return
	}
	if t.IsArray() && base.Flag.MSan && t.NumElem() > 0 && eqSkipsBytes(t.Elem()) {
		// The generated equality skips these bytes, so one range read
		// would check memory the comparison never reads. Emit nothing
		// when no check applies at all, and an element loop otherwise.
		if eqEmitsCheck(t.Elem()) {
			instrumentEqArray(call, read, addr, t, off)
		}
		return
	}
	if t.Size() == 0 {
		return
	}
	p := typecheck.Conv(typecheck.Conv(addr, types.Types[types.TUNSAFEPTR]), types.Types[types.TUINTPTR])
	if off != 0 {
		p = typecheck.Expr(ir.NewBinaryExpr(base.Pos, ir.OADD, p, ir.NewUintptr(base.Pos, off)))
	}
	call.PtrInit().Append(mkcall1(read, nil, init, p, ir.NewUintptr(base.Pos, t.Size())))
}

// eqSkipsBytes reports whether the generated equality for t skips any bytes:
// blank struct fields, or padding between or after fields, at any depth. It
// mirrors the skips eqSigBuilder.build emits, so a type with no skipped bytes
// is checked exactly by one range read.
func eqSkipsBytes(t *types.Type) bool {
	if t.IsStruct() && !t.IsSIMD() {
		var off int64
		for _, f := range t.Fields() {
			if f.Sym.IsBlank() {
				return true
			}
			if off < f.Offset {
				return true
			}
			if eqSkipsBytes(f.Type) {
				return true
			}
			off = f.Offset + f.Type.Size()
		}
		return off < t.Size()
	}
	if t.IsArray() {
		return eqSkipsBytes(t.Elem())
	}
	return false
}

// eqEmitsCheck reports whether the field-wise check of a value of type t
// emits any read: a struct emits one exactly when some non-blank field does,
// and any other type emits one exactly when it is not zero-sized.
func eqEmitsCheck(t *types.Type) bool {
	if t.IsStruct() && !t.IsSIMD() {
		for _, f := range t.Fields() {
			if !f.Sym.IsBlank() && eqEmitsCheck(f.Type) {
				return true
			}
		}
		return false
	}
	if t.IsArray() {
		return t.NumElem() > 0 && eqEmitsCheck(t.Elem())
	}
	return t.Size() > 0
}

// instrumentEqArray emits a loop that checks each element of the array type t
// at addr+off with the field-wise check, so padding inside elements is never
// read. MSan only: ASan checks only bounds, for which one range read of the
// whole array is already precise.
func instrumentEqArray(call *ir.CallExpr, read, addr ir.Node, t *types.Type, off int64) {
	elem := t.Elem()
	esz := elem.Size()
	// Base address, evaluated once.
	baseTmp := typecheck.TempAt(base.Pos, ir.CurFunc, types.Types[types.TUINTPTR])
	p0 := typecheck.Conv(typecheck.Conv(addr, types.Types[types.TUNSAFEPTR]), types.Types[types.TUINTPTR])
	if off != 0 {
		p0 = typecheck.Expr(ir.NewBinaryExpr(base.Pos, ir.OADD, p0, ir.NewUintptr(base.Pos, off)))
	}
	idx := typecheck.TempAt(base.Pos, ir.CurFunc, types.Types[types.TUINTPTR])
	var body ir.Nodes
	instrumentEqLoopElem(&body, read, baseTmp, idx, elem, 0)
	if len(body) == 0 {
		return
	}
	appendWalkStmt(call.PtrInit(), ir.NewAssignStmt(base.Pos, baseTmp, p0))
	loop := ir.NewForStmt(base.Pos, nil,
		ir.NewBinaryExpr(base.Pos, ir.OLT, idx, ir.NewUintptr(base.Pos, t.NumElem()*esz)),
		ir.NewAssignStmt(base.Pos, idx, ir.NewBinaryExpr(base.Pos, ir.OADD, idx, ir.NewUintptr(base.Pos, esz))),
		nil, false)
	loop.SetInit([]ir.Node{ir.NewAssignStmt(base.Pos, idx, ir.NewUintptr(base.Pos, 0))})
	loop.Body = body
	appendWalkStmt(call.PtrInit(), loop)
}

// instrumentEqLoopElem appends one unwalked read call per memory range the
// generated equality compares in a value of type t at addrBase+idx+off, where
// addrBase and idx are uintptr temporaries. Nested arrays unroll against the
// dynamic base; only the outermost array needs the loop.
func instrumentEqLoopElem(body *ir.Nodes, read, addrBase, idx ir.Node, t *types.Type, off int64) {
	if t.IsStruct() && !t.IsSIMD() {
		for _, f := range t.Fields() {
			if !f.Sym.IsBlank() {
				instrumentEqLoopElem(body, read, addrBase, idx, f.Type, off+f.Offset)
			}
		}
		return
	}
	if t.IsArray() {
		for i := int64(0); i < t.NumElem(); i++ {
			instrumentEqLoopElem(body, read, addrBase, idx, t.Elem(), off+i*t.Elem().Size())
		}
		return
	}
	if t.Size() == 0 {
		return
	}
	p := ir.NewBinaryExpr(base.Pos, ir.OADD, addrBase, idx)
	if off != 0 {
		p = ir.NewBinaryExpr(base.Pos, ir.OADD, p, ir.NewUintptr(base.Pos, off))
	}
	stmt := ir.NewCallExpr(base.Pos, ir.OCALL, read, []ir.Node{p, ir.NewUintptr(base.Pos, t.Size())})
	body.Append(orderStmtInPlace(typecheck.Stmt(stmt), map[string][]*ir.Name{}))
}

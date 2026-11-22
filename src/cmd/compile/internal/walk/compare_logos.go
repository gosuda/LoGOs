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
// field by field, so its padding is never read.
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
	if t.Size() == 0 {
		return
	}
	p := typecheck.Conv(typecheck.Conv(addr, types.Types[types.TUNSAFEPTR]), types.Types[types.TUINTPTR])
	if off != 0 {
		p = typecheck.Expr(ir.NewBinaryExpr(base.Pos, ir.OADD, p, ir.NewUintptr(base.Pos, off)))
	}
	call.PtrInit().Append(mkcall1(read, nil, init, p, ir.NewUintptr(base.Pos, t.Size())))
}

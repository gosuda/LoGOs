// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package noder

import (
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

func logosWriterNormalizeType(typ types2.Type, st *logosExportState) types2.Type {
normalize:
	for {
		switch t := typ.(type) {
		case *types2.Option:
			// Options survive in inferred positions (locals, init
			// statements) when lowering makes no edit and no recheck
			// runs. Erase them to the instantiated logos/rt.Option[E]
			// so the reader sees the same named type the import
			// overlay restores for the referenced declaration; the
			// anonymous struct stays the layout shape only. When
			// logos/rt is unreachable, keep the struct erasure.
			if st != nil {
				if et := types2.LogosOptionExportType(st.rt, t, st.ctxt); et != nil {
					typ = et
					break normalize
				}
			}
			typ = types2.LogosOptionRepr(t)
		case *types2.ConstView:
			typ = t.Elem
		case *types2.ReadonlyDescendants:
			typ = t.Elem
		case *types2.Alias:
			if isGlobal(t.Obj()) {
				break normalize
			}
			typ = t.Rhs()
		default:
			break normalize
		}
	}
	return typ
}

// logosExportState carries the state for writing option types at the
// export boundary: the reachable logos/rt package for instantiating
// logos/rt.Option[E], and one shared instantiation context so repeated
// writes of the same option reuse one instance.
type logosExportState struct {
	rt   *types2.Package
	ctxt *types2.Context
}

func (pw *pkgWriter) logosExportState() *logosExportState {
	if pw.logosExport == nil {
		pw.logosExport = &logosExportState{rt: types2.LogosRTPackage(pw.curpkg)}
	}
	if pw.logosExport.ctxt == nil {
		pw.logosExport.ctxt = types2.NewContext()
	}
	return pw.logosExport
}

func logosWriterIsNil(obj types2.Object, tv syntax.TypeAndValue) bool {
	_, isNil := obj.(*types2.Nil)
	return isNil || tv.IsNil()
}

func unconstView(t types2.Type) types2.Type {
	for {
		switch typ := types2.Unalias(t).(type) {
		case *types2.ConstView:
			t = typ.Elem
			continue
		case *types2.ReadonlyDescendants:
			t = typ.Elem
			continue
		}
		return t
	}
}

func (w *writer) logosWriterConvertExpr(src, dst types2.Type, expr syntax.Expr, implicit bool) (identical, done bool) {
	usrc := unconstView(src)
	udst := unconstView(dst)
	identical = dst == nil || types2.Identical(src, dst) || (implicit && types2.Identical(usrc, udst))
	if implicit && identical {
		w.expr(expr)
		return identical, true
	}
	if implicit && !types2.AssignableTo(src, dst) && !types2.AssignableTo(usrc, udst) {
		w.p.fatalf(expr, "%v is not assignable to %v", src, dst)
	}
	return identical, false
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import "cmd/compile/internal/syntax"

func (check *Checker) logosWriteBase(e syntax.Expr) (syntax.Expr, bool) {
	viaReference := false
	for {
		e = syntax.Unparen(e)
		switch n := e.(type) {
		case *syntax.SelectorExpr:
			if check.logosQualified(n) {
				return n, viaReference
			}
			viaReference = viaReference || check.logosPointerTyped(n.X)
			e = n.X
		case *syntax.IndexExpr:
			viaReference = viaReference || check.logosPointerTyped(n.X) || check.logosSliceTyped(n.X)
			e = n.X
		case *syntax.SliceExpr:
			viaReference = true
			e = n.X
		case *syntax.Operation:
			if n.Op != syntax.Mul || n.Y != nil {
				return n, viaReference
			}
			viaReference = true
			e = n.X
		default:
			return n, viaReference
		}
	}
}

func (check *Checker) logosUnderlying(e syntax.Expr) Type {
	if check.Info == nil {
		return nil
	}
	t := check.Info.TypeOf(e)
	if t == nil {
		return nil
	}
	return Unalias(t).Underlying()
}

func (check *Checker) logosPointerTyped(e syntax.Expr) bool {
	_, ok := check.logosUnderlying(e).(*Pointer)
	return ok
}

func (check *Checker) logosSliceTyped(e syntax.Expr) bool {
	_, ok := check.logosUnderlying(e).(*Slice)
	return ok
}

func (check *Checker) logosQualified(sel *syntax.SelectorExpr) bool {
	pkg, ok := sel.X.(*syntax.Name)
	if !ok || check.Info == nil {
		return false
	}
	_, ok = check.Info.Uses[pkg].(*PkgName)
	return ok
}

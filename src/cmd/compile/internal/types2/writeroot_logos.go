// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import "cmd/compile/internal/syntax"

func (check *Checker) logosWriteBase(e syntax.Expr) syntax.Expr {
	for {
		e = syntax.Unparen(e)
		switch n := e.(type) {
		case *syntax.SelectorExpr:
			if check.logosQualified(n) {
				return n
			}
			e = n.X
		case *syntax.IndexExpr:
			e = n.X
		case *syntax.SliceExpr:
			e = n.X
		case *syntax.Operation:
			if n.Op != syntax.Mul || n.Y != nil {
				return n
			}
			e = n.X
		default:
			return n
		}
	}
}

func (check *Checker) logosQualified(sel *syntax.SelectorExpr) bool {
	pkg, ok := sel.X.(*syntax.Name)
	if !ok || check.Info == nil {
		return false
	}
	_, ok = check.Info.Uses[pkg].(*PkgName)
	return ok
}

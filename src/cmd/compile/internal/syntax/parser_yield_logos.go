// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package syntax

func (p *parser) logosYield(lhs Expr) Stmt {
	if p.mode&LogosMode == 0 {
		return nil
	}
	name, ok := lhs.(*Name)
	if !ok || name.Value != "yield" {
		return nil
	}
	switch p.tok {
	case _Literal, _Func, _Struct, _Map, _Chan, _Interface, _Name:
		return p.yieldStmt(name.Pos())
	default:
		return nil
	}
}

func (p *parser) yieldStmt(pos Pos) Stmt {
	s := new(YieldStmt)
	s.pos = pos
	s.Value = p.exprList()
	return s
}

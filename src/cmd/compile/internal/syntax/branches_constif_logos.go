// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package syntax

func logosConstBranches(stmt Stmt, ctxt targets, innerBlock func(targets, Pos, []Stmt)) {
	inner := targets{ctxt.breaks, ctxt.continues, -1}
	switch s := stmt.(type) {
	case *ConstIfStmt:
		innerBlock(inner, s.Then.Pos(), s.Then.List)
		if s.Else != nil {
			innerBlock(inner, s.Else.Pos(), []Stmt{s.Else})
		}
	case *ConstSwitchStmt:
		for _, c := range s.Cases {
			innerBlock(inner, c.Case, c.Body)
		}
	}
}

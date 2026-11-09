// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
)

func (check *Checker) logosAddrFresh(x *operand, e *syntax.Operation) bool {
	if !check.conf.Logos {
		return false
	}
	if _, ok := syntax.Unparen(e.X).(*syntax.CompositeLit); ok {
		return false
	}
	if !x.isValid() || x.isNil() || x.mode() == variable {
		return false
	}
	x.mode_ = value
	x.typ_ = &Pointer{base: Default(x.typ())}
	if check.Info != nil {
		if check.Info.LogosAddrFresh == nil {
			check.Info.LogosAddrFresh = make(map[syntax.Expr]bool)
		}
		check.Info.LogosAddrFresh[e] = true
	}
	return true
}

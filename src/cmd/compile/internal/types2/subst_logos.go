// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
)

func logosSubstOne(check *Checker, pos syntax.Pos, typ Type, tpar *TypeParam, repl Type) Type {
	return check.subst(pos, typ, makeSubstMap([]*TypeParam{tpar}, []Type{repl}), nil, check.context())
}

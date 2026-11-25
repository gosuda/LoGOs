// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"go/token"

	"cmd/compile/internal/syntax"
)

func cfCallSpan(ctx cfCtx, call *syntax.CallExpr) (int, int) {
	start := offAt(ctx.starts, syntax.StartPos(call))
	end := cfExactEnd(ctx, call.Fun)
	depth := 0
	scanSegment(ctx.buf.b, end, len(ctx.buf.b), func(t tok) bool {
		switch t.kind {
		case token.LPAREN:
			depth++
		case token.RPAREN:
			depth--
			if depth < 0 {
				return false
			}
			if depth == 0 {
				end = t.end
				return false
			}
		case token.COMMENT:
		default:
			return depth > 0
		}
		return true
	})
	return start, end
}

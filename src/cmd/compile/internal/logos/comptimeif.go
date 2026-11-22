// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
	"go/constant"
	"strconv"
)

func cfLiveBranch(s *syntax.ConstIfStmt, info *types2.Info) (live *syntax.BlockStmt, ok bool) {
	keepThen, ok := info.LogosConstIf[s]
	if !ok {
		return nil, false
	}
	if keepThen {
		return s.Then, true
	}
	switch els := s.Else.(type) {
	case *syntax.BlockStmt:
		return els, true
	case *syntax.ConstIfStmt:
		return cfLiveBranch(els, info)
	}
	return nil, true
}

func (fl *fileLower) cfPruneConstIf(s *syntax.ConstIfStmt, run *cfRun, add func(int, int, string)) *syntax.BlockStmt {
	live, ok := cfLiveBranch(s, run.info)
	if !ok {
		fl.cfError(run.sites.at(s.Cond.Pos()), "[comptime::not_constant] const if condition cannot be evaluated for this instantiation")
	}
	starts := run.ctx.starts
	start := offAt(starts, s.Pos())
	end := cfExactEnd(run.ctx, s)
	liveStart, liveEnd := end, end
	if live != nil {
		liveStart = offAt(starts, live.Pos())
		liveEnd = offAt(starts, syntax.EndPos(live)) + 1
	}
	add(start, liveStart, "")
	add(liveEnd, end, "")
	return live
}

func cfInnerConstFors(body *syntax.BlockStmt, info *types2.Info) []*syntax.ConstForStmt {
	var out []*syntax.ConstForStmt
	var visit func(syntax.Node) bool
	visit = func(n syntax.Node) bool {
		if st, ok := n.(syntax.Stmt); ok && info.LogosConstIfDead[st] {
			return false
		}
		switch n := n.(type) {
		case *syntax.ConstForStmt:
			out = append(out, n)
			return false
		case *syntax.ConstIfStmt:
			if live, _ := cfLiveBranch(n, info); live != nil {
				syntax.Inspect(live, visit)
			}
			return false
		}
		return true
	}
	syntax.Inspect(body, visit)
	return out
}

func (b *cfRecordBuilder) foldCond(e syntax.Expr) {
	if val, ok := typeConst(b.fl.pkg.info, e); ok && val.Kind() == constant.Bool {
		ctx := cfCtx{buf: b.fl.srcMap, starts: b.fl.start}
		s := offAt(ctx.starts, syntax.StartPos(e))
		end := cfFoldEnd(ctx, e)
		b.subs = append(b.subs, splice{start: s, end: end, text: keepComments(ctx.buf.b, s, end, strconv.FormatBool(constant.BoolVal(val)))})
		return
	}
	switch e := e.(type) {
	case *syntax.ParenExpr:
		b.foldCond(e.X)
	case *syntax.Operation:
		b.foldCond(e.X)
		if e.Y != nil {
			b.foldCond(e.Y)
		}
	default:
		syntax.Inspect(e, b.visit)
	}
}

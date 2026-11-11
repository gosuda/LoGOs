// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package noder

import (
	"bytes"
	"slices"
	"strconv"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/logos/lint"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

const logosMisplacedTryTag = "[correctness::misplaced_try] "

func logosReportMisplacedTry(m *posMap, files []*syntax.File, info *types2.Info, terr types2.Error) bool {
	if !strings.HasPrefix(terr.Msg, logosMisplacedTryTag) {
		return false
	}
	f := logosFileAt(files, terr.Pos)
	if f == nil {
		return false
	}
	stashed, _ := nextInputs([]*syntax.File{f})
	src, ok := stashed[f]
	if !ok {
		return false
	}
	if r, ok := misplacedTryFix(info, f, src, terr.Pos); ok {
		base.LogosReport(logosDiag(m, r))
		return true
	}
	if r, ok := misplacedTryBare(info, f, src, terr.Pos); ok {
		base.LogosReport(logosDiag(m, r))
		return true
	}
	return false
}

func misplacedTryBare(info *types2.Info, f *syntax.File, src []byte, pos syntax.Pos) (lint.Result, bool) {
	t, _ := tryAt(f, pos)
	if t == nil {
		return lint.Result{}, false
	}
	var origin, typ string
	var line int
	if call, ok := syntax.Unparen(t.X).(*syntax.CallExpr); ok {
		origin = syntax.String(call)
		line = int(call.Pos().Line())
		if tup, ok := checkedCallResult(info, call); ok {
			typ = types2.TypeString(tup.At(0).Type(), func(p *types2.Package) string { return p.Name() })
		}
	}
	return lint.Result{
		Rule:     "correctness::misplaced_try",
		Severity: lint.Forbid,
		Pos:      syntax.StartPos(t),
		End:      syntax.EndPos(t),
		Message:  "? must end an assignment, expression statement, defer, or errdefer",
		Provenance: lint.Provenance{
			OriginExpr:   origin,
			OriginLine:   line,
			InferredType: typ,
		},
	}, true
}

func enclosingSig(info *types2.Info, fn syntax.Node) (*types2.Signature, bool) {
	switch n := fn.(type) {
	case *syntax.FuncDecl:
		obj := info.Defs[n.Name]
		fobj, ok := obj.(*types2.Func)
		if !ok || fobj == nil {
			return nil, false
		}
		return fobj.Signature(), true
	case *syntax.FuncLit:
		s, ok := info.TypeOf(n).(*types2.Signature)
		if !ok || s == nil {
			return nil, false
		}
		return s, true
	default:
		return nil, false
	}
}

func misplacedTryFix(info *types2.Info, f *syntax.File, src []byte, pos syntax.Pos) (lint.Result, bool) {
	t, stack := tryAt(f, pos)
	if t == nil {
		return lint.Result{}, false
	}
	call, ok := syntax.Unparen(t.X).(*syntax.CallExpr)
	if !ok || syntax.Unparen(t.X) != t.X {
		return lint.Result{}, false
	}
	tup, ok := checkedCallResult(info, call)
	if !ok {
		return lint.Result{}, false
	}
	stmt, fn, ok := enclosingSplitStmt(stack)
	if !ok {
		return lint.Result{}, false
	}
	if countTryExpr(stmt) > 1 {
		return lint.Result{}, false
	}
	if tup.Len() > 2 {
		if _, ok := stack[len(stack)-2].(*syntax.CallExpr); !ok {
			if _, ok := stmt.(*syntax.ReturnStmt); !ok {
				return lint.Result{}, false
			}
		}
	}
	if !leftmostTry(stmt, t, src) {
		return lint.Result{}, false
	}
	if !splitPreservesOrder(stmt, t, src) {
		return lint.Result{}, false
	}
	if !unconditionalAncestors(stack, stmt) {
		return lint.Result{}, false
	}
	if !enclosingReturnsError(info, fn) {
		return lint.Result{}, false
	}
	return splitResult(splitRequest{info: info, fn: fn, stmt: stmt, call: call, expr: t, src: src, tup: tup})
}

func tryAt(f *syntax.File, pos syntax.Pos) (*syntax.TryExpr, []syntax.Node) {
	var stack, found []syntax.Node
	var target *syntax.TryExpr
	syntax.Inspect(f, func(n syntax.Node) bool {
		switch {
		case n == nil:
			stack = stack[:len(stack)-1]
			return false
		case found != nil:
			return false
		}
		stack = append(stack, n)
		if t, ok := n.(*syntax.TryExpr); ok && t.Pos() == pos {
			found = slices.Clone(stack)
			target = t
		}
		return true
	})
	return target, found
}

func checkedCallResult(info *types2.Info, call *syntax.CallExpr) (*types2.Tuple, bool) {
	typ := info.TypeOf(call)
	if typ == nil {
		return nil, false
	}
	tup, ok := typ.(*types2.Tuple)
	if !ok || tup.Len() < 2 {
		return nil, false
	}
	errType := types2.Universe.Lookup("error").Type()
	if !types2.Identical(tup.At(tup.Len()-1).Type(), errType) {
		return nil, false
	}
	nvals := tup.Len() - 1
	for i := range nvals {
		if tup.At(i).Type() == nil {
			return nil, false
		}
	}
	return tup, true
}

func enclosingSplitStmt(stack []syntax.Node) (syntax.Stmt, syntax.Node, bool) {
	tryIdx := len(stack) - 1
	stmtIdx := -1
	var stmt syntax.Stmt
	for i := tryIdx - 1; i >= 0; i-- {
		switch s := stack[i].(type) {
		case *syntax.AssignStmt:
			if s.Rhs == nil || (s.Op != 0 && s.Op != syntax.Def) {
				return nil, nil, false
			}
			if len(syntax.UnpackListExpr(s.Rhs)) != 1 {
				return nil, nil, false
			}
			for _, l := range syntax.UnpackListExpr(s.Lhs) {
				if _, ok := syntax.Unparen(l).(*syntax.Name); !ok {
					return nil, nil, false
				}
			}
			stmt, stmtIdx = s, i
		case *syntax.ExprStmt:
			stmt, stmtIdx = s, i
		case *syntax.IfStmt:
			if s.Cond == nil || stack[i+1] != syntax.Node(s.Cond) {
				return nil, nil, false
			}
			stmt, stmtIdx = s, i
		case *syntax.ReturnStmt:
			stmt, stmtIdx = s, i
		case *syntax.FuncLit, *syntax.LambdaExpr:
			return nil, nil, false
		case syntax.Stmt:
			return nil, nil, false
		default:
			continue
		}
		break
	}
	if stmtIdx < 0 {
		return nil, nil, false
	}
	parent := stack[stmtIdx-1]
	switch p := parent.(type) {
	case *syntax.BlockStmt, *syntax.CaseClause:
	case *syntax.CommClause:
		if p.Comm == stmt {
			return nil, nil, false
		}
	default:
		return nil, nil, false
	}
	for i := stmtIdx - 1; i >= 0; i-- {
		switch n := stack[i].(type) {
		case *syntax.FuncDecl:
			return stmt, n, true
		case *syntax.FuncLit:
			return stmt, n, true
		case *syntax.LambdaExpr:
			return nil, nil, false
		}
	}
	return nil, nil, false
}

func countTryExpr(stmt syntax.Stmt) int {
	n := 0
	syntax.Inspect(stmt, func(nd syntax.Node) bool {
		if _, ok := nd.(*syntax.TryExpr); ok {
			n++
		}
		return true
	})
	return n
}

func leftmostTry(stmt syntax.Stmt, t *syntax.TryExpr, src []byte) bool {
	want, ok := tryOffset(src, syntax.StartPos(t))
	if !ok {
		return false
	}
	best := -1
	syntax.Inspect(stmt, func(n syntax.Node) bool {
		if u, ok := n.(*syntax.TryExpr); ok {
			if off, ok := tryOffset(src, syntax.StartPos(u)); ok {
				if best < 0 || off < best {
					best = off
				}
			}
		}
		return true
	})
	return best == want
}

func unconditionalAncestors(stack []syntax.Node, stmt syntax.Stmt) bool {
	for i := len(stack) - 2; i >= 0; i-- {
		if stack[i] == stmt {
			return true
		}
		child := stack[i+1]
		switch n := stack[i].(type) {
		case *syntax.Operation:
			if child == syntax.Node(n.Y) && (n.Op == syntax.AndAnd || n.Op == syntax.OrOr) {
				return false
			}
		case *syntax.CoalesceExpr:
			if child == syntax.Node(n.Y) {
				return false
			}
		case *syntax.CallExpr:
			if _, ok := syntax.Unparen(n.Fun).(*syntax.OptSelectExpr); ok && child != syntax.Node(n.Fun) {
				return false
			}
		case *syntax.OptSelectExpr:
			if child != syntax.Node(n.X) {
				return false
			}
		case *syntax.SwitchExpr, *syntax.ArmClause,
			*syntax.CaseClause, *syntax.CommClause, *syntax.ErrdeferStmt:
			return false
		case *syntax.SelectorExpr, *syntax.IndexExpr,
			*syntax.SliceExpr, *syntax.AssertExpr, *syntax.ParenExpr,
			*syntax.CompositeLit, *syntax.KeyValueExpr, *syntax.ListExpr,
			*syntax.TryExpr:
		default:
			return false
		}
	}
	return false
}

func splitPreservesOrder(stmt syntax.Stmt, t *syntax.TryExpr, src []byte) bool {
	start, ok1 := tryOffset(src, syntax.StartPos(t))
	end, ok2 := tryOffset(src, syntax.EndPos(t))
	if !ok1 || !ok2 || end <= start {
		return false
	}
	ok := true
	syntax.Inspect(stmt, func(n syntax.Node) bool {
		if !ok {
			return false
		}
		switch n := n.(type) {
		case *syntax.CallExpr:
			s, ok1 := tryOffset(src, syntax.StartPos(n))
			e, ok2 := callEnd(src, n)
			if !ok1 || !ok2 {
				ok = false
				return false
			}
			if s >= start && e <= end {
				return true
			}
			if e <= start {
				ok = false
				return false
			}
		case *syntax.Operation:
			if n.Op != syntax.Recv {
				return true
			}
			s, ok1 := tryOffset(src, syntax.StartPos(n))
			e, ok2 := tryOffset(src, syntax.EndPos(n))
			if !ok1 || !ok2 {
				ok = false
				return false
			}
			if s >= start && e <= end {
				return true
			}
			if e <= start {
				ok = false
				return false
			}
		}
		return true
	})
	return ok
}

func enclosingReturnsError(info *types2.Info, fn syntax.Node) bool {
	sig, ok := enclosingSig(info, fn)
	if !ok || sig == nil || sig.Results().Len() == 0 {
		return false
	}
	errType := types2.Universe.Lookup("error").Type()
	return types2.Identical(sig.Results().At(sig.Results().Len()-1).Type(), errType)
}

type splitRequest struct {
	info *types2.Info
	fn   syntax.Node
	stmt syntax.Stmt
	call *syntax.CallExpr
	expr *syntax.TryExpr
	src  []byte
	tup  *types2.Tuple
}

func returnSplitSuffix(r splitRequest) (string, bool) {
	rs, ok := r.stmt.(*syntax.ReturnStmt)
	if !ok {
		return "", true
	}
	if rs.Results == nil {
		return "", false
	}
	exprs := syntax.UnpackListExpr(rs.Results)
	direct := -1
	for i, e := range exprs {
		if syntax.Unparen(e) == syntax.Node(r.expr) {
			direct = i
			break
		}
	}
	if direct < 0 {
		return "", true
	}
	sig, ok := enclosingSig(r.info, r.fn)
	if !ok || sig == nil || sig.Results().Len() == 0 {
		return "", false
	}
	nvals := r.tup.Len() - 1
	arity := len(exprs) - 1 + nvals
	total := sig.Results().Len()
	if arity != total && arity != total-1 {
		return "", false
	}
	for i := range nvals {
		if direct+i >= total {
			return "", false
		}
		if !types2.AssignableTo(r.tup.At(i).Type(), sig.Results().At(direct+i).Type()) {
			return "", false
		}
	}
	if arity == total-1 {
		return ", nil", true
	}
	return "", true
}

func splitResult(r splitRequest) (lint.Result, bool) {
	startPos := splitStmtStart(r.stmt)
	start, ok1 := tryOffset(r.src, startPos)
	tryStart, ok2 := tryOffset(r.src, syntax.StartPos(r.expr))
	tryEnd, ok3 := tryOffset(r.src, syntax.EndPos(r.expr))
	callStart, ok4 := tryOffset(r.src, syntax.StartPos(r.call))
	callEnd, ok5 := callEnd(r.src, r.call)
	stmtEnd, ok6 := splitStmtEnd(r.src, r.stmt)
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
		return lint.Result{}, false
	}
	if !(start <= tryStart && tryStart < tryEnd && tryEnd <= stmtEnd) {
		return lint.Result{}, false
	}
	if !(start <= callStart && callStart < callEnd && callEnd <= tryEnd) {
		return lint.Result{}, false
	}
	suffix, ok := returnSplitSuffix(r)
	if !ok {
		return lint.Result{}, false
	}
	names := splitTempNames(r.fn, r.call, r.tup.Len()-1)
	picked := strings.Join(names, ", ")
	indentStart := start
	for indentStart > 0 && (r.src[indentStart-1] == ' ' || r.src[indentStart-1] == '\t') {
		indentStart--
	}
	indent := string(r.src[indentStart:start])
	stmtSrc := string(r.src[start:stmtEnd])
	callSrc := string(r.src[callStart:callEnd])
	rest := stmtSrc[:tryStart-start] + picked + stmtSrc[tryEnd-start:] + suffix
	gapSrc := string(r.src[callEnd:tryEnd])
	if len(gapSrc) == 0 || gapSrc[len(gapSrc)-1] != '?' {
		return lint.Result{}, false
	}
	replacement := picked + " := " + callSrc + gapSrc + "\n" + indent + rest
	endLine, endCol := offsetLineCol(r.src, stmtEnd)
	return lint.Result{
		Rule:     "correctness::misplaced_try",
		Severity: lint.Forbid,
		Pos:      startPos,
		End:      syntax.MakePos(startPos.Base(), endLine, endCol),
		Message:  "? must end an assignment, expression statement, defer, or errdefer",
		Provenance: lint.Provenance{
			OriginExpr:   syntax.String(r.call),
			OriginLine:   int(r.call.Pos().Line()),
			InferredType: types2.TypeString(r.tup.At(0).Type(), func(p *types2.Package) string { return p.Name() }),
		},
		Fixes: []lint.Fix{
			{Description: "Split the expression so `?` ends its own statement.", Replacement: replacement},
		},
	}, true
}

func splitStmtStart(s syntax.Stmt) syntax.Pos {
	if es, ok := s.(*syntax.ExprStmt); ok {
		return syntax.StartPos(es.X)
	}
	return syntax.StartPos(s)
}

func splitStmtEnd(src []byte, s syntax.Stmt) (int, bool) {
	end, ok := tryOffset(src, syntax.EndPos(s))
	if !ok {
		return 0, false
	}
	for end < len(src) && (src[end] == ')' || src[end] == ']' || src[end] == '}') {
		end++
	}
	if end < 0 || end > len(src) {
		return 0, false
	}
	return end, true
}

func splitTempNames(fn syntax.Node, call *syntax.CallExpr, n int) []string {
	base := "tmp"
	switch fun := syntax.Unparen(call.Fun).(type) {
	case *syntax.Name:
		if fun.Value != "" && fun.Value != "_" {
			base = fun.Value
		}
	case *syntax.SelectorExpr:
		if fun.Sel != nil && fun.Sel.Value != "" && fun.Sel.Value != "_" {
			base = fun.Sel.Value
		}
	}
	used := map[string]bool{}
	syntax.Inspect(fn, func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Name); ok {
			used[id.Value] = true
		}
		return true
	})
	names := make([]string, 0, n)
	for i := 0; len(names) < n; i++ {
		suffix := ""
		if i > 0 {
			suffix = strconv.Itoa(i)
		}
		cand := "_" + base + suffix
		if isReservedIdent(cand) {
			cand = "_fix" + cand
		}
		if cand != "_" && !used[cand] {
			used[cand] = true
			names = append(names, cand)
		}
	}
	return names
}

func isReservedIdent(name string) bool {
	return strings.HasPrefix(name, "_logos_") || strings.HasPrefix(name, "Logos_")
}

func tryOffset(src []byte, pos syntax.Pos) (int, bool) {
	off := 0
	for line := uint(1); line < pos.Line(); line++ {
		i := bytes.IndexByte(src[off:], '\n')
		if i < 0 {
			return 0, false
		}
		off += i + 1
	}
	off += int(pos.Col()) - 1
	if off < 0 || off > len(src) {
		return 0, false
	}
	return off, true
}

func callEnd(src []byte, call *syntax.CallExpr) (int, bool) {
	if end, ok := syntax.ParenEnd(src, call.Pos(), syntax.LogosMode); ok {
		return tryOffset(src, end)
	}
	return 0, false
}

func offsetLineCol(src []byte, off int) (uint, uint) {
	line := uint(1)
	start := 0
	for i := 0; i < off && i < len(src); i++ {
		if src[i] == '\n' {
			line++
			start = i + 1
		}
	}
	return line, uint(off-start) + 1
}

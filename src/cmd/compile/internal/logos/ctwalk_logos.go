// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

type ctWalker struct {
	*funcLower
}

func (fl *fileLower) lowerCtBody(fn *syntax.FuncDecl, sig *types2.Signature) {
	fl.ct.byteConversions = ctByteConversions(fn, fl.pkg.info)
	fl.ctFunction(fn.Type, sig, fn.Body)
	fl.ct.byteConversions = nil
	fl.ct.makeLenProducts = nil
	fl.ct.constMaps = nil
	fl.ct.retain(fn, fl.constDecls)
}

func (fl *fileLower) ctFunction(ft *syntax.FuncType, sig *types2.Signature, body *syntax.BlockStmt) {
	w := ctWalker{newFuncLower(fl, ft, sig, body)}
	w.functionType(ft)
	if w.ctYieldFunction(sig) {
		return
	}
	w.prepareCleanupSyntax()
	body.List = w.scopeSyntaxList(body, body.List, w.stmts)
	w.finishCleanupSyntax()
}

func (w ctWalker) functionType(ft *syntax.FuncType) {
	for _, fields := range [][]*syntax.Field{ft.ParamList, ft.ResultList} {
		for _, f := range fields {
			f.Type = w.typeSyntax(f.Type)
		}
	}
}

func (w ctWalker) typeSyntax(e syntax.Expr) syntax.Expr {
	if e == nil {
		return nil
	}
	out, pre := w.expr(e)
	if len(pre) != 0 {
		w.reject(e, "a type expression cannot propagate a runtime error")
	}
	return out
}

func (w ctWalker) reject(n syntax.Node, detail string) {
	if w.fl.ct.err == nil {
		w.fl.ct.err = fmt.Errorf("%s: comptime instance: %s", n.Pos(), detail)
	}
}

func (w ctWalker) chain() chainSyntaxLower {
	return chainSyntaxLower{
		expr: w.expr,
		lambda: func(lam *syntax.LambdaExpr, sig *types2.Signature) *syntax.FuncLit {
			lit := w.fl.chainLambdaSyntax(lam, sig)
			w.fl.ctFunction(lit.Type, sig, lit.Body)
			return lit
		},
		workerLambda: w.ctChainWorkerLambda,
		fail: func(err syntax.Expr) []syntax.Stmt {
			if id, ok := err.(*syntax.Name); ok {
				return w.tryExitSyntax(id.Pos(), id.Value)
			}
			name := w.nextErr()
			pos := err.Pos()
			def := defineOf(pos, []syntax.Expr{nameOf(pos, name)}, []syntax.Expr{err})
			return append([]syntax.Stmt{def}, w.tryExitSyntax(pos, name)...)
		},
		typeExpr: w.fl.ctTypeExpr,
	}
}

func (w ctWalker) stmts(list []syntax.Stmt) []syntax.Stmt {
	var out []syntax.Stmt
	for i, stmt := range list {
		pre, next := w.stmt(stmt)
		switch s := stmt.(type) {
		case *syntax.IfStmt, *syntax.ForStmt, *syntax.SwitchStmt, *syntax.SelectStmt,
			*syntax.ReturnStmt, *syntax.ExprStmt, *syntax.CallStmt, *syntax.SendStmt:
			if len(pre) != 0 {
				next, pre = w.scoped(stmt.Pos(), pre, next), nil
			}
		case *syntax.AssignStmt:
			if len(pre) != 0 && s.Op != syntax.Def {
				next, pre = w.scoped(stmt.Pos(), pre, next), nil
			}
		case *syntax.BranchStmt:
			if len(pre) != 0 && s.Tok != syntax.Fallthrough {
				next, pre = w.scoped(stmt.Pos(), pre, next), nil
			}
		}
		if out == nil && len(pre) == 0 && next != nil {
			list[i] = next
			continue
		}
		if out == nil {
			out = make([]syntax.Stmt, i, len(list)+len(pre))
			copy(out, list[:i])
		}
		out = append(out, pre...)
		if next != nil {
			out = append(out, next)
		}
	}
	if out != nil {
		return out
	}
	return list
}

func (w ctWalker) stmt(s syntax.Stmt) ([]syntax.Stmt, syntax.Stmt) {
	if pre, next, handled := w.groupStatement(s); handled {
		return pre, next
	}
	if pre, next, handled := w.ctOptionStmt(s); handled {
		return pre, next
	}
	if as, ok := s.(*syntax.AssignStmt); ok {
		if list := w.tryAssignment(as); len(list) != 0 {
			return list, nil
		}
		if list, ok := w.assignment(as); ok {
			return list, nil
		}
	}
	switch s := s.(type) {
	case syntax.SimpleStmt:
		pre := w.simple(s)
		if list, ok := w.trySimpleSyntax(s); ok {
			return append(pre, list...), nil
		}
		return pre, s
	case *syntax.BlockStmt:
		s.List = w.scopeSyntaxList(s, s.List, w.stmts)
	case *syntax.ReturnStmt:
		if w.ctYield != nil {
			s.Results = nil
		}
		if t, ok := syntax.Unparen(s.Results).(*syntax.TryExpr); ok {
			return w.tryResult(s, t)
		}
		if pre, next, handled := w.ctTupleStmt(s); handled {
			return pre, next
		}
		pre := w.optionValues(s, &s.Results)
		cleanup, next := w.cleanupReturn(s)
		return append(pre, cleanup...), next
	case *syntax.CallStmt:
		if _, try := syntax.Unparen(s.Call).(*syntax.TryExpr); try {
			return w.lowerCleanupCallSyntax(s, w.expr), nil
		}
		if pre, handled := w.ctPropertyCallStmt(s); handled {
			return pre, s
		}
		return w.expressions(&s.Call, &s.DeferAt), s
	case *syntax.DeclStmt:
		return w.decls(s)
	case *syntax.IfStmt:
		return w.ifStmt(s), s
	case *syntax.ForStmt:
		return w.forStmt(s), s
	case *syntax.SwitchStmt:
		if pre, handled := w.ctOptionSwitch(s); handled {
			return pre, s
		}
		return w.switchStmt(s), s
	case *syntax.SelectStmt:
		return w.ctChainSelect(s), s
	case *syntax.LabeledStmt:
		return w.labeled(s)
	case *syntax.ErrdeferStmt:
		return w.lowerCleanupCallSyntax(s, w.expr), nil
	case *syntax.ScopeDeferStmt:
		return w.lowerScopeDeferSyntax(s, w.expr), nil
	case *syntax.YieldStmt:
		return w.ctYieldStmt(s)
	case *syntax.BranchStmt:
		return w.scopeBranchSyntax(s), s
	default:
		w.reject(s, fmt.Sprintf("unsupported statement %T in native lowering", s))
	}
	return nil, s
}

func (w ctWalker) simple(s syntax.SimpleStmt) []syntax.Stmt {
	switch s := s.(type) {
	case nil, *syntax.EmptyStmt:
		return nil
	case *syntax.AssignStmt:
		if pre, handled := w.ctTupleAssign(s); handled {
			return pre
		}
		if pre, handled := w.groupAssign(s); handled {
			return pre
		}
		return w.assignValues(s)
	case *syntax.ExprStmt:
		return w.expressions(&s.X)
	case *syntax.SendStmt:
		return w.watchExprs(2, func(i int) *syntax.Expr {
			if i == 0 {
				return &s.Chan
			}
			return &s.Value
		}, func(i int, old syntax.Expr) (syntax.Expr, []syntax.Stmt) {
			if i == 0 {
				return w.expr(old)
			}
			return w.checkedOperand(old, w.ctOptionTarget(s, old, 0))
		})
	}
	w.reject(s, fmt.Sprintf("unsupported simple statement %T in native lowering", s))
	return nil
}

func (w ctWalker) assignment(s *syntax.AssignStmt) ([]syntax.Stmt, bool) {
	if list, ok := w.ctPropertyAssign(s); ok {
		return list, true
	}
	return w.ctStructSpreadSelfUpdate(s)
}

func (w ctWalker) assignValues(s *syntax.AssignStmt) []syntax.Stmt {
	lhs := syntax.UnpackListExpr(s.Lhs)
	var smallTargets [4]types2.Type
	var targets []types2.Type
	if s.Op == 0 || s.Op == syntax.Def {
		if len(lhs) <= len(smallTargets) {
			targets = smallTargets[:len(lhs)]
		} else {
			targets = make([]types2.Type, len(lhs))
		}
		for i := range targets {
			targets[i] = w.ctOptionTarget(s, s.Rhs, i)
		}
	}
	target := func(i int) types2.Type {
		if i < len(targets) {
			return targets[i]
		}
		return nil
	}
	var lhsPre []syntax.Stmt
	if s.Op != 0 && s.Op != syntax.Def {
		if id, ok := syntax.Unparen(s.Lhs).(*syntax.Name); ok {
			if e, pre, handled := w.ctOptionExpr(id); handled {
				s.Lhs, lhsPre = e, pre
			}
		}
	}
	var slots []*syntax.Expr
	var keys map[*syntax.Expr]types2.Type
	if s.Op != syntax.Def {
		for _, dest := range lhs {
			slots = append(slots, w.groupLvalueSlots(dest)...)
			keys = w.optionKeys(keys, dest)
		}
	}
	values := syntax.UnpackListExpr(s.Rhs)
	rhs := func(i int) *syntax.Expr {
		if list, ok := s.Rhs.(*syntax.ListExpr); ok {
			return &list.ElemList[i]
		}
		return &s.Rhs
	}
	pre := w.watchExprs(len(slots)+len(values), func(i int) *syntax.Expr {
		if i < len(slots) {
			return slots[i]
		}
		return rhs(i - len(slots))
	}, func(i int, old syntax.Expr) (syntax.Expr, []syntax.Stmt) {
		if i < len(slots) {
			return w.optionOperand(old, keys[slots[i]])
		}
		e, steps := w.checkedOperand(old, target(i-len(slots)))
		if len(values) == 1 {
			return w.optionMultiSink(old, e, steps, target)
		}
		return e, steps
	})
	return append(lhsPre, pre...)
}

func (w ctWalker) optionKeys(keys map[*syntax.Expr]types2.Type, target syntax.Expr) map[*syntax.Expr]types2.Type {
	for target != nil {
		switch n := syntax.Unparen(target).(type) {
		case *syntax.IndexExpr:
			if typ := w.ctOptionIndexKey(n); w.fl.asOption(typ) != nil {
				if keys == nil {
					keys = make(map[*syntax.Expr]types2.Type)
				}
				keys[&n.Index] = typ
			}
			target = n.X
		case *syntax.SelectorExpr:
			target = n.X
		case *syntax.Operation:
			if n.Op != syntax.Mul || n.Y != nil {
				return keys
			}
			target = n.X
		default:
			return keys
		}
	}
	return keys
}

func (w ctWalker) decls(s *syntax.DeclStmt) ([]syntax.Stmt, syntax.Stmt) {
	var out []syntax.Stmt
	for i, decl := range s.DeclList {
		var pre []syntax.Stmt
		var replaced bool
		switch d := decl.(type) {
		case *syntax.VarDecl:
			var handled bool
			if len(d.NameList) == 2 && d.Values != nil {
				if e, setup, ok := w.groupReceive(d.Values, 2); ok {
					d.Values, pre = e, setup
					d.Type = w.typeSyntax(d.Type)
					break
				}
			}
			if pre, handled = w.ctTupleVar(d); !handled {
				pre = w.optionValues(d, &d.Values)
			}
			d.Type = w.typeSyntax(d.Type)
		case *syntax.ConstDecl:
			if pre, replaced = w.ctConstDecl(d); replaced {
				break
			}
			d.Type = w.typeSyntax(d.Type)
			d.Values = w.typeSyntax(d.Values)
		case *syntax.TypeDecl:
			d.Type = w.typeSyntax(d.Type)
		}
		if len(pre) == 0 && !replaced && out == nil {
			continue
		}
		if out == nil {
			out = make([]syntax.Stmt, 0, len(s.DeclList)-i+len(pre)+1)
			if i != 0 {
				out = append(out, at(&syntax.DeclStmt{DeclList: s.DeclList[:i]}, s.Pos()))
			}
		}
		out = append(out, pre...)
		if !replaced {
			out = append(out, at(&syntax.DeclStmt{DeclList: s.DeclList[i : i+1]}, decl.Pos()))
		}
	}
	if out != nil {
		return out, nil
	}
	return nil, s
}

func (w ctWalker) header(host syntax.Stmt, init syntax.SimpleStmt) []syntax.Stmt {
	if list, handled := w.tryHeaderSyntax(host, init); handled {
		return list
	}
	if list, handled := w.simpleReplacement(init); handled {
		clearInit(host)
		return list
	}
	return w.simple(init)
}

func (w ctWalker) simpleReplacement(s syntax.SimpleStmt) ([]syntax.Stmt, bool) {
	if as, ok := s.(*syntax.AssignStmt); ok {
		return w.assignment(as)
	}
	if pre, next, handled := w.ctOptionStmt(s); handled {
		if next != nil {
			pre = append(pre, next)
		}
		return pre, true
	}
	return nil, false
}

func (w ctWalker) ifStmt(s *syntax.IfStmt) []syntax.Stmt {
	pre := w.header(s, s.Init)
	cond := w.expressions(&s.Cond)
	if len(cond) != 0 && s.Init != nil {
		pre = append(pre, s.Init)
		s.Init = nil
	}
	pre = append(pre, cond...)
	s.Then.List = w.scopeSyntaxList(s.Then, s.Then.List, w.stmts)
	if s.Else != nil {
		pre, next := w.stmt(s.Else)
		s.Else = w.scoped(s.Else.Pos(), pre, next)
	}
	return pre
}

func (w ctWalker) forStmt(s *syntax.ForStmt) []syntax.Stmt {
	if rng, ok := s.Init.(*syntax.RangeClause); ok {
		s.Body.List = w.scopeSyntaxList(s.Body, s.Body.List, func(original []syntax.Stmt) []syntax.Stmt {
			s.Body.List = nil
			w.ctPropertyRange(rng, s.Body)
			setup := s.Body.List
			s.Body.List = original
			return append(setup, w.stmts(original)...)
		})
		loop := w.scopeLoopSyntax(s)
		if pre, lowered := w.fl.lowerCtChainRange(s, w.chain()); lowered {
			return append(loop, pre...)
		}
		if pre, lowered := w.ctConstMapRange(s); lowered {
			return append(loop, pre...)
		}
		return append(loop, w.expressions(&rng.X)...)
	}
	s.Body.List = w.scopeSyntaxList(s.Body, s.Body.List, w.stmts)
	loop := w.scopeLoopSyntax(s)
	pre := append(loop, w.header(s, s.Init)...)
	cond := w.expressions(&s.Cond)
	if len(cond) != 0 {
		stop := at(&syntax.Operation{Op: syntax.Not, X: s.Cond}, s.Cond.Pos())
		brk := new(syntax.BranchStmt)
		brk.Tok = syntax.Break
		brk.Target = s
		brk.SetPos(s.Cond.Pos())
		s.Body.List = append(append(cond, ifOf(stop.Pos(), nil, stop, []syntax.Stmt{brk})), s.Body.List...)
		s.Cond = nil
	}
	if post, handled := w.tryPostAssignment(s); handled {
		return append(pre, post...)
	}
	if list, handled := w.simpleReplacement(s.Post); handled {
		s.Post = at(&syntax.EmptyStmt{}, s.Post.Pos())
		w.ctChainPost(s, list)
	}
	if post := w.simple(s.Post); len(post) != 0 {
		w.ctChainPost(s, post)
	}
	return pre
}

func (w ctWalker) switchStmt(s *syntax.SwitchStmt) []syntax.Stmt {
	pre := w.header(s, s.Init)
	cases := w.ctChainCases(s, w.expressions(&s.Tag))
	for _, clause := range s.Body {
		clause.Body = w.scopeSyntaxList(clause, clause.Body, w.stmts)
	}
	return append(pre, cases...)
}

func (w ctWalker) labeled(s *syntax.LabeledStmt) ([]syntax.Stmt, syntax.Stmt) {
	original := s.Stmt
	for {
		nested, ok := original.(*syntax.LabeledStmt)
		if !ok {
			break
		}
		original = nested.Stmt
	}
	defines := false
	switch n := original.(type) {
	case *syntax.DeclStmt:
		defines = true
	case *syntax.AssignStmt:
		defines = n.Op == syntax.Def
	}
	pre, next := w.stmt(s.Stmt)
	if defines && len(pre) != 0 {
		if next != nil {
			pre = append(pre, next)
		}
		s.Stmt = pre[0]
		pre[0] = s
		return pre, nil
	}
	if len(pre) != 0 {
		switch next.(type) {
		case *syntax.ForStmt, *syntax.SwitchStmt, *syntax.SelectStmt:
			old := s.Label.Value
			inner := w.fl.cswName("ctlabel")
			usedInner := false
			syntax.Inspect(next, func(node syntax.Node) bool {
				switch node.(type) {
				case *syntax.FuncLit, *syntax.LambdaExpr:
					return false
				}
				br, ok := node.(*syntax.BranchStmt)
				if ok && (br.Tok == syntax.Break || br.Tok == syntax.Continue) && br.Label != nil && br.Label.Value == old {
					br.Label = nameOf(br.Label.Pos(), inner)
					usedInner = true
				}
				return true
			})
			if usedInner {
				next = at(&syntax.LabeledStmt{Label: nameOf(s.Label.Pos(), inner), Stmt: next}, next.Pos())
			}
		}
	}
	s.Stmt = w.scoped(s.Stmt.Pos(), pre, next)
	if len(pre) != 0 && !w.labelInUse(s.Label.Value) {
		return nil, s.Stmt
	}
	return nil, s
}

func (w ctWalker) labelInUse(label string) bool {
	found := false
	syntax.Inspect(w.body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit, *syntax.LambdaExpr:
			return false
		case *syntax.BranchStmt:
			found = found || (n.Label != nil && n.Label.Value == label)
		}
		return !found
	})
	return found
}

func (w ctWalker) scoped(pos syntax.Pos, pre []syntax.Stmt, next syntax.Stmt) syntax.Stmt {
	if next != nil {
		if len(pre) == 0 {
			return next
		}
		pre = append(pre, next)
	}
	end := pos
	if len(pre) != 0 {
		end = syntax.EndPos(pre[len(pre)-1])
	}
	return blockEndingAt(pos, end, pre)
}

func (w ctWalker) expressions(fields ...*syntax.Expr) []syntax.Stmt {
	return w.walkExprs(len(fields), func(i int) *syntax.Expr { return fields[i] }, nil)
}

func (w ctWalker) exprList(list []syntax.Expr) []syntax.Stmt {
	return w.walkExprs(len(list), func(i int) *syntax.Expr { return &list[i] }, nil)
}

func (w ctWalker) optionValues(parent syntax.Node, slot *syntax.Expr) []syntax.Stmt {
	if *slot == nil {
		return nil
	}
	field, n := func(int) *syntax.Expr { return slot }, 1
	if list, ok := (*slot).(*syntax.ListExpr); ok {
		field, n = func(i int) *syntax.Expr { return &list.ElemList[i] }, len(list.ElemList)
	}
	return w.watchExprs(n, field, func(i int, old syntax.Expr) (syntax.Expr, []syntax.Stmt) {
		return w.optionSink(parent, old, i, n == 1)
	})
}

func (w ctWalker) walkExprs(n int, field func(int) *syntax.Expr, target func(int, syntax.Expr) types2.Type) []syntax.Stmt {
	return w.watchExprs(n, field, func(i int, old syntax.Expr) (syntax.Expr, []syntax.Stmt) {
		var typ types2.Type
		if target != nil && old != nil {
			typ = target(i, old)
		}
		return w.optionOperand(old, typ)
	})
}

func (w ctWalker) watchExprs(n int, field func(int) *syntax.Expr, lower func(int, syntax.Expr) (syntax.Expr, []syntax.Stmt)) []syntax.Stmt {
	var out []syntax.Stmt
	var replaced map[int]syntax.Expr
	lo := 0
	for i := range n {
		dst := field(i)
		old := *dst
		e, pre := lower(i, old)
		if len(pre) != 0 {
			for j := lo; j < i; j++ {
				prev := field(j)
				original := replaced[j]
				if original == nil {
					original = *prev
				}
				out = w.holdPrior(out, original, prev)
			}
			lo = i
			clear(replaced)
			out = append(out, pre...)
		}
		*dst = e
		if old != e && i+1 < n {
			if replaced == nil {
				replaced = make(map[int]syntax.Expr)
			}
			replaced[i] = old
		}
	}
	return out
}

func (w ctWalker) optionOperand(old syntax.Expr, typ types2.Type) (syntax.Expr, []syntax.Stmt) {
	e, pre := w.expr(old)
	return w.optionConversion(old, e, typ), pre
}

func (w ctWalker) checkedOperand(old syntax.Expr, typ types2.Type) (syntax.Expr, []syntax.Stmt) {
	e, pre := w.expr(old)
	return w.optionConversion(old, w.ctPlatformOperand(old, e), typ), pre
}

func (w ctWalker) optionConversion(old, e syntax.Expr, typ types2.Type) syntax.Expr {
	if _, isTry := syntax.Unparen(old).(*syntax.TryExpr); isTry {
		return e
	}
	if typ != nil {
		e = w.ctOptionValue(old, e, typ)
	}
	return w.ctConstCov(old, e)
}

func (w ctWalker) optionSink(parent syntax.Node, old syntax.Expr, index int, split bool) (syntax.Expr, []syntax.Stmt) {
	if old == nil {
		return nil, nil
	}
	e, pre := w.checkedOperand(old, w.ctOptionTarget(parent, old, index))
	if !split {
		return e, pre
	}
	return w.optionMultiSink(old, e, pre, func(i int) types2.Type {
		return w.ctOptionTarget(parent, old, i)
	})
}

func (w ctWalker) optionMultiSink(old, e syntax.Expr, pre []syntax.Stmt, target func(int) types2.Type) (syntax.Expr, []syntax.Stmt) {
	if _, try := syntax.Unparen(old).(*syntax.TryExpr); try {
		return e, pre
	}
	if values, steps, ok := w.ctOptionMultiTargets(old, e, target); ok {
		return listOf(old.Pos(), values), append(pre, steps...)
	}
	return e, pre
}

func (w ctWalker) holdPrior(out []syntax.Stmt, old syntax.Expr, dst *syntax.Expr) []syntax.Stmt {
	if pair, ok := old.(*syntax.KeyValueExpr); ok {
		if next, ok := (*dst).(*syntax.KeyValueExpr); ok {
			out = w.holdPrior(out, pair.Key, &next.Key)
			return w.holdPrior(out, pair.Value, &next.Value)
		}
	}
	if hold := w.hold(old, dst); hold != nil {
		return append(out, hold)
	}
	return out
}

func (w ctWalker) hold(old syntax.Expr, dst *syntax.Expr) syntax.Stmt {
	if old == nil {
		return nil
	}
	switch n := syntax.Unparen(old).(type) {
	case *syntax.Name:
		if _, isFunc := w.fl.pkg.info.Uses[n].(*types2.Func); isFunc {
			return nil
		}
	case *syntax.SelectorExpr:
		_, isFunc := w.fl.pkg.info.Uses[n.Sel].(*types2.Func)
		if isFunc && w.fl.pkg.info.Selections[n] == nil {
			return nil
		}
	}
	tv := w.fl.pkg.info.Types[old]
	if !tv.IsValue() || tv.Value != nil || tv.IsNil() {
		return nil
	}
	pos := old.Pos()
	name := w.nextArg()
	e := *dst
	*dst = nameOf(pos, name)
	if old == e {
		if typ := w.groupDeclaredType(old); typ != nil {
			decl := at(&syntax.VarDecl{
				NameList: []*syntax.Name{nameOf(pos, name)},
				Type:     w.fl.ctTypeExpr(pos, typ),
				Values:   e,
			}, pos)
			return at(&syntax.DeclStmt{DeclList: []syntax.Decl{decl}}, pos)
		}
	}
	return defineOf(pos, []syntax.Expr{nameOf(pos, name)}, []syntax.Expr{e})
}

func (w ctWalker) expr(e syntax.Expr) (syntax.Expr, []syntax.Stmt) {
	if e != nil {
		if next, pre, handled := w.groupReceive(e, 1); handled {
			return next, pre
		}
		if call, ok := e.(*syntax.CallExpr); ok && w.fl.ctChainCall(call) {
			return w.fl.lowerCtChain(call, w.chain())
		}
		if next, pre, handled := w.ctConstMapExpr(e); handled {
			return next, pre
		}
		if next, pre, handled := w.ctOptionExpr(e); handled {
			return next, pre
		}
		if next, pre, handled := w.ctTupleExpr(e); handled {
			return next, pre
		}
		if next, pre, handled := w.ctSIMDExpr(e); handled {
			return next, pre
		}
	}
	switch n := e.(type) {
	case nil, *syntax.Name, *syntax.BasicLit:
	case *syntax.ParenExpr:
		return n, w.expressions(&n.X)
	case *syntax.SelectorExpr:
		if next, pre, handled := w.ctPropertyExpr(n); handled {
			return next, pre
		}
		return n, w.expressions(&n.X)
	case *syntax.CallExpr:
		if next, pre, handled := w.ctPropertyCall(n); handled {
			return next, pre
		}
		if next, pre, handled := w.ctZeroCopy(n); handled {
			return next, pre
		}
		if next, pre, handled := w.ctFreezeCall(n); handled {
			return next, pre
		}
		if next, pre, handled := w.ctMakeLen(n); handled {
			return next, pre
		}
		fun := n.Fun
		pre := w.watchExprs(1+len(n.ArgList), func(i int) *syntax.Expr {
			if i == 0 {
				return &fun
			}
			return &n.ArgList[i-1]
		}, func(i int, old syntax.Expr) (syntax.Expr, []syntax.Stmt) {
			if i == 0 {
				return w.expr(old)
			}
			return w.optionSink(n, old, i-1, len(n.ArgList) == 1)
		})
		n.Fun = fun
		if len(n.ArgList) == 1 {
			if list, ok := n.ArgList[0].(*syntax.ListExpr); ok {
				n.ArgList = list.ElemList
			}
		}
		return n, pre
	case *syntax.TryExpr:
		return n, w.expressions(&n.X)
	case *syntax.IndexExpr:
		return n, w.expressions(&n.X, &n.Index)
	case *syntax.SliceExpr:
		if next, pre, handled := w.ctConstSlice(n); handled {
			return next, pre
		}
		return n, w.expressions(&n.X, &n.Index[0], &n.Index[1], &n.Index[2])
	case *syntax.AssertExpr:
		n.Type = w.typeSyntax(n.Type)
		return n, w.expressions(&n.X)
	case *syntax.TypeSwitchGuard:
		return n, w.expressions(&n.X)
	case *syntax.Operation:
		if next, pre, handled := w.ctStructSpreadAddr(n); handled {
			return next, pre
		}
		if next, pre, handled := w.ctAddrFresh(n); handled {
			return next, pre
		}
		if next, pre, handled := w.ctMakeLenProduct(n); handled {
			return next, pre
		}
		if n.Op == syntax.AndAnd || n.Op == syntax.OrOr {
			return w.shortCircuit(n)
		}
		return n, w.expressions(&n.X, &n.Y)
	case *syntax.ListExpr:
		return n, w.exprList(n.ElemList)
	case *syntax.CompositeLit:
		if next, pre, handled := w.ctStructDefault(n); handled {
			return next, pre
		}
		pre := w.literalValues(n)
		n.Type = w.typeSyntax(n.Type)
		return n, pre
	case *syntax.KeyValueExpr:
		return n, w.expressions(&n.Key, &n.Value)
	case *syntax.FuncLit:
		sig, _ := w.fl.pkg.info.TypeOf(n).(*types2.Signature)
		w.fl.ctFunction(n.Type, sig, n.Body)
	case *syntax.LambdaExpr:
		sig, _ := w.fl.pkg.info.TypeOf(n).(*types2.Signature)
		if sig == nil {
			w.reject(n, "missing checked lambda signature")
			break
		}
		lit := w.fl.lambdaSyntax(n, sig)
		w.fl.ctFunction(lit.Type, sig, lit.Body)
		return lit, nil
	case *syntax.InterpExpr:
		return w.interpolation(n)
	case *syntax.ArrayType:
		n.Len, n.Elem = w.typeSyntax(n.Len), w.typeSyntax(n.Elem)
	case *syntax.SliceType:
		n.Elem = w.typeSyntax(n.Elem)
	case *syntax.DotsType:
		n.Elem = w.typeSyntax(n.Elem)
	case *syntax.MapType:
		n.Key, n.Value = w.typeSyntax(n.Key), w.typeSyntax(n.Value)
	case *syntax.ChanType:
		n.Elem = w.typeSyntax(n.Elem)
	case *syntax.AssertedStruct:
		return w.typeSyntax(n.Struct), nil
	case *syntax.StructType:
		w.ctStructDefaultType(n)
		for _, f := range n.FieldList {
			f.Type = w.typeSyntax(f.Type)
		}
	case *syntax.InterfaceType:
		if next, handled := w.ctPropertyInterface(n); handled {
			return next, nil
		}
		for _, f := range n.MethodList {
			f.Const = false
			f.Type = w.typeSyntax(f.Type)
		}
	case *syntax.FuncType:
		w.functionType(n)
	case *syntax.ConstType:
		return w.expr(n.Elem)
	case *syntax.SwitchExpr:
		return w.trySwitchExprSyntax(n)
	default:
		w.reject(e, fmt.Sprintf("unsupported expression %T in native lowering", e))
	}
	return e, nil
}

func (w ctWalker) interpolation(e *syntax.InterpExpr) (syntax.Expr, []syntax.Stmt) {
	ops := w.fl.checkedInterpOperands(e)
	var pending [][]syntax.Stmt
	for i, operand := range e.Exprs {
		repl, pre := w.expr(operand)
		if len(pre) != 0 {
			if pending == nil {
				pending = make([][]syntax.Stmt, len(e.Exprs))
			}
			pending[i] = pre
		}
		e.Exprs[i] = repl
	}
	if pending == nil {
		return w.fl.interpSyntax(e, ops), nil
	}

	pos := e.Pos()
	stmts := interpBody(e, e.Exprs, ops, interpNames{
		builtin:  w.fl.ctBuiltin,
		selector: w.fl.ctPkgSelector,
	})
	var scoped []syntax.Stmt
	for i, op := range ops {
		width := 1
		if op.helper != "" {
			width++
		}
		scoped = append(scoped, pending[i]...)
		scoped = append(scoped, stmts[:width]...)
		stmts = stmts[width:]
	}
	last := len(stmts) - 1
	result := stmts[last].(*syntax.ReturnStmt).Results
	name := w.nextArg()
	scoped = append(scoped, stmts[:last]...)
	scoped = append(scoped, assignOf(pos, []syntax.Expr{nameOf(pos, name)}, []syntax.Expr{result}))
	return nameOf(pos, name), []syntax.Stmt{
		typedVarDecl(pos, nameOf(pos, name), w.fl.ctBuiltin(pos, "string")),
		blockEndingAt(pos, syntax.EndPos(e), scoped),
	}
}

func (w ctWalker) tryResult(s *syntax.ReturnStmt, t *syntax.TryExpr) ([]syntax.Stmt, syntax.Stmt) {
	pos := t.Pos()
	n := len(w.specs) - 1
	if n < 0 {
		w.reject(t, "try in a lambda needs an error result")
		return nil, s
	}
	var checked syntax.Stmt = exprStmtOf(pos, t)
	results := make([]syntax.Expr, n+1)
	if n != 0 {
		names := make([]syntax.Expr, n)
		typ := w.fl.pkg.info.TypeOf(t)
		for i := range names {
			name := w.nextArg()
			names[i] = nameOf(pos, name)
			source := typ
			if tuple, ok := typ.(*types2.Tuple); ok {
				source = tuple.At(i).Type()
			}
			results[i] = w.ctOptionValueFrom(nameOf(pos, name), source, w.specs[i].typ)
		}
		checked = defineOf(pos, names, []syntax.Expr{t})
	}
	pre, check := w.stmt(checked)
	if check != nil {
		pre = append(pre, check)
	}
	results[n] = w.fl.ctBuiltin(pos, "nil")
	s.Results = listOf(pos, results)
	cleanup, next := w.cleanupReturn(s)
	return append(pre, cleanup...), next
}

func (w ctWalker) literalValues(lit *syntax.CompositeLit) []syntax.Stmt {
	return w.watchExprs(len(lit.ElemList), func(i int) *syntax.Expr { return &lit.ElemList[i] },
		func(i int, old syntax.Expr) (syntax.Expr, []syntax.Stmt) {
			target := w.ctOptionTarget(lit, old, i)
			pair, ok := old.(*syntax.KeyValueExpr)
			if !ok {
				return w.checkedOperand(old, target)
			}
			kv := *pair
			key := w.ctOptionLiteralKey(lit)
			pre := w.watchExprs(2, func(i int) *syntax.Expr {
				if i == 0 {
					return &kv.Key
				}
				return &kv.Value
			}, func(i int, old syntax.Expr) (syntax.Expr, []syntax.Stmt) {
				if i == 0 {
					return w.optionOperand(old, key)
				}
				return w.checkedOperand(old, target)
			})
			if kv.Key == pair.Key && kv.Value == pair.Value {
				return pair, pre
			}
			return &kv, pre
		})
}

func (w ctWalker) shortCircuit(n *syntax.Operation) (syntax.Expr, []syntax.Stmt) {
	left, pre := w.expr(n.X)
	right, guarded := w.expr(n.Y)
	if len(guarded) == 0 {
		n.X, n.Y = left, right
		return n, pre
	}
	pos := n.Pos()
	name := w.nextArg()
	typ := w.fl.pkg.info.TypeOf(n)
	if typ == nil || typ == types2.Typ[types2.UntypedBool] {
		typ = types2.Typ[types2.Bool]
	}
	decl := at(&syntax.VarDecl{
		NameList: []*syntax.Name{nameOf(pos, name)},
		Type:     w.fl.ctTypeExpr(pos, typ),
		Values:   left,
	}, pos)
	pre = append(pre, at(&syntax.DeclStmt{DeclList: []syntax.Decl{decl}}, pos))
	var test syntax.Expr = nameOf(pos, name)
	if n.Op == syntax.OrOr {
		test = at(&syntax.Operation{Op: syntax.Not, X: test}, pos)
	}
	guarded = append(guarded, assignOf(pos, []syntax.Expr{nameOf(pos, name)}, []syntax.Expr{right}))
	return nameOf(pos, name), append(pre, ifOf(pos, nil, test, guarded))
}

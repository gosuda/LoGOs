// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

const yieldParam = "_logos_yield"

func (fc *funcLower) generatorTarget() *types2.TypeList {
	if len(fc.specs) != 1 {
		return nil
	}
	// The spec result type resolves through the function's signature, so a
	// lambda whose FuncType was rebuilt synthetically still detects as a
	// generator.
	typ := fc.specs[0].typ
	if typ == nil {
		typ = fc.fl.pkg.info.TypeOf(fc.ft.ResultList[0].Type)
	}
	return yieldTypeArgs(typ)
}

func yieldTypeArgs(typ types2.Type) *types2.TypeList {
	named, ok := types2.Unalias(typ).(*types2.Named)
	if !ok {
		return nil
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != "iter" {
		return nil
	}
	args := named.TypeArgs()
	if obj.Name() == "Seq" && args.Len() == 1 || obj.Name() == "Seq2" && args.Len() == 2 {
		return args
	}
	return nil
}

type ctYieldFrame struct {
	callback string
	args     *types2.TypeList
}

func (w ctWalker) ctYieldFunction(sig *types2.Signature) bool {
	if sig == nil || sig.Results().Len() != 1 {
		return false
	}
	args := yieldTypeArgs(sig.Results().At(0).Type())
	if args.Len() == 0 {
		return false
	}
	fl, pos := w.fl, w.body.Pos()
	params := make([]*syntax.Field, args.Len())
	for i := range params {
		params[i] = at(&syntax.Field{Type: fl.ctTypeExpr(pos, args.At(i))}, pos)
	}
	callback := w.ctYieldName()
	yield := at(&syntax.Field{
		Name: nameOf(pos, callback),
		Type: at(&syntax.FuncType{
			ParamList:  params,
			ResultList: []*syntax.Field{at(&syntax.Field{Type: fl.ctBuiltin(pos, "bool")}, pos)},
		}, pos),
	}, pos)
	body := *w.body
	ft := at(&syntax.FuncType{ParamList: []*syntax.Field{yield}}, pos)
	frame := newFuncLower(fl, ft, nil, &body)
	frame.ctYield = &ctYieldFrame{callback: callback, args: args}
	fl.ctFunction(ft, nil, &body)
	lit := at(&syntax.FuncLit{Type: ft, Body: &body}, pos)
	w.body.List = []syntax.Stmt{returnOf(pos, []syntax.Expr{lit})}
	return true
}

func (w ctWalker) ctYieldName() string {
	for {
		name := w.fl.cswName("ctyield")
		used := false
		syntax.Inspect(w.body, func(n syntax.Node) bool {
			if id, ok := n.(*syntax.Name); ok && id.Value == name {
				used = true
			}
			return !used
		})
		if !used {
			return name
		}
	}
}

func (w ctWalker) ctYieldStmt(s *syntax.YieldStmt) ([]syntax.Stmt, syntax.Stmt) {
	frame := w.ctYield
	if frame == nil {
		w.reject(s, "missing generator callback for checked yield")
		return nil, s
	}
	pos, end := s.Pos(), syntax.EndPos(s)
	args := syntax.UnpackListExpr(s.Value)
	pre := w.walkExprs(len(args), func(i int) *syntax.Expr { return &args[i] }, func(i int, _ syntax.Expr) types2.Type {
		return frame.args.At(i)
	})
	call := callOf(pos, nameOf(pos, frame.callback), args)
	cond := at(&syntax.Operation{Op: syntax.Not, X: call}, pos)
	exits, ret := w.cleanupReturn(returnOf(pos, nil))
	branch := ifOf(pos, nil, cond, append(exits, ret))
	branch.Then.Rbrace = end
	if len(pre) == 0 {
		return nil, branch
	}
	return nil, blockEndingAt(pos, end, append(pre, branch))
}

func (fc *funcLower) yieldQualifier(p *types2.Package) string {
	if p == fc.fl.pkg.tpkg {
		return ""
	}
	if name, ok := fc.fl.importNames()[p]; ok {
		return name
	}
	return p.Name()
}

func (fc *funcLower) lowerYield() bool {
	targs := fc.generatorTarget()
	if targs.Len() == 0 {
		return false
	}
	fl := fc.fl
	pos := fc.body.Pos()
	spellings := make([]string, targs.Len())
	natives := make([]syntax.Expr, targs.Len())
	for i := range targs.Len() {
		targ := targs.At(i)
		if fl.asOption(targ) != nil {
			// An optional element (`int?`) has no plain-Go spelling: the
			// yield callback parameter must spell the lowered option.
			s, ok := fl.spellLowered(targ)
			if !ok {
				base.Fatalf("logos: cannot respell yield type %s", targ)
			}
			spellings[i] = s
		} else {
			spellings[i] = fl.sourceTypeString(targ, fc.yieldQualifier)
		}
		decl, isType := reparseType(spellings[i])
		if !isType {
			base.Fatalf("logos: cannot respell yield type %q", spellings[i])
		}
		syntax.Inspect(decl, func(n syntax.Node) bool {
			if n != nil {
				n.SetPos(pos)
			}
			return true
		})
		natives[i] = decl
	}
	fc.body.List = fc.rewriteYieldList(fc.body.List)
	params := make([]*syntax.Field, len(natives))
	for i, typ := range natives {
		params[i] = at(&syntax.Field{Type: typ}, pos)
	}
	yieldField := at(&syntax.Field{
		Name: nameOf(pos, yieldParam),
		Type: at(&syntax.FuncType{
			ParamList:  params,
			ResultList: []*syntax.Field{at(&syntax.Field{Type: nameOf(pos, "bool")}, pos)},
		}, pos),
	}, pos)
	lit := at(&syntax.FuncLit{
		Type: at(&syntax.FuncType{
			ParamList: []*syntax.Field{yieldField},
		}, pos),
		Body: blockOf(pos, fc.body.List),
	}, pos)
	fc.body.List = []syntax.Stmt{returnOf(pos, []syntax.Expr{lit})}
	fc.yieldSpellings = spellings
	braceOff := fl.off(pos)
	fl.add(braceOff+1, braceOff+1, " return func("+yieldParam+" func("+strings.Join(spellings, ", ")+") bool) {")
	rbraceOff := fl.off(fc.body.Rbrace)
	fl.add(rbraceOff, rbraceOff, " }")
	return true
}

func (fc *funcLower) rewriteYieldList(list []syntax.Stmt) []syntax.Stmt {
	for i, s := range list {
		list[i] = fc.rewriteYieldStmt(s)
	}
	return list
}

func (fc *funcLower) rewriteYieldStmt(s syntax.Stmt) syntax.Stmt {
	switch s := s.(type) {
	case *syntax.YieldStmt:
		return fc.lowerYieldStmt(s)
	case *syntax.ReturnStmt:
		if len(syntax.UnpackListExpr(s.Results)) > 0 {
			fc.dropReturnValues(s)
			s.Results = nil
		}
		return s
	case *syntax.BlockStmt:
		s.List = fc.rewriteYieldList(s.List)
		return s
	case *syntax.IfStmt:
		s.Then.List = fc.rewriteYieldList(s.Then.List)
		switch els := s.Else.(type) {
		case *syntax.BlockStmt:
			els.List = fc.rewriteYieldList(els.List)
		case *syntax.IfStmt:
			s.Else = fc.rewriteYieldStmt(els)
		}
		return s
	case *syntax.ForStmt:
		s.Body.List = fc.rewriteYieldList(s.Body.List)
		return s
	case *syntax.SwitchStmt:
		for _, clause := range s.Body {
			clause.Body = fc.rewriteYieldList(clause.Body)
		}
		return s
	case *syntax.SelectStmt:
		for _, clause := range s.Body {
			clause.Body = fc.rewriteYieldList(clause.Body)
		}
		return s
	case *syntax.LabeledStmt:
		s.Stmt = fc.rewriteYieldStmt(s.Stmt)
		return s
	case *syntax.GroupStmt:
		return fc.yieldGroupStmt(s)
	default:
		return s
	}
}

func (fc *funcLower) lowerYieldStmt(s *syntax.YieldStmt) syntax.Stmt {
	fl := fc.fl
	pos := s.Pos()
	vals := syntax.UnpackListExpr(s.Value)
	targs := fc.generatorTarget()
	args := make([]syntax.Expr, len(vals))
	var texts []otext
	texts = append(texts, gen("if !"+yieldParam+"("))
	for i, v := range vals {
		var val syntax.Expr
		head, tail := "", ""
		if i < targs.Len() {
			t := targs.At(i)
			if fl.asOption(t) != nil {
				// An optional element yield (`iter.Seq[int?]`) must wrap the
				// member value into the option the callback accepts.
				val, head, tail = fl.wrapParts(v, fl.optType(v), t, pos)
			}
		}
		if val == nil {
			val = v
		}
		args[i] = val
		if i > 0 {
			texts = append(texts, gen(", "))
		}
		texts = append(texts, gen(head))
		// A wrapper's opening insertion sits at the operand span's
		// boundary, which the span copy excludes: drain it here so a
		// coalescing function keeps its `func() T {` head.
		texts = append(texts, fl.wrapperHead(v))
		texts = append(texts, fl.lazyText(fl.exprSpan(v)))
		texts = append(texts, gen(tail))
	}
	call := callOf(pos, nameOf(pos, yieldParam), args)
	cond := at(&syntax.Operation{Op: syntax.Not, X: call}, pos)
	native := ifOf(pos, nil, cond, []syntax.Stmt{returnOf(pos, nil)})
	valEnd := fl.exprSpan(vals[len(vals)-1]).end
	tail := fl.wrapperTail(vals[len(vals)-1])
	texts = append(texts, tail, gen(") { return }"))
	fl.addT(fl.off(pos), valEnd, cat(texts...))
	return native
}

func (fc *funcLower) dropReturnValues(s *syntax.ReturnStmt) {
	fl := fc.fl
	vals := syntax.UnpackListExpr(s.Results)
	end := fl.exprSpan(vals[len(vals)-1]).end
	_ = fl.wrapperTail(vals[len(vals)-1])
	fl.moveRegion(span{start: fl.off(s.Pos()) + len("return"), end: end}, "")
}

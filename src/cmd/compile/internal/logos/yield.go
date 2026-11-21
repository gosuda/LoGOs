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

func (fc *funcLower) generatorTarget() ([]types2.Type, bool) {
	if len(fc.specs) != 1 {
		return nil, false
	}
	// The spec result type resolves through the function's signature, so a
	// lambda whose FuncType was rebuilt synthetically still detects as a
	// generator.
	typ := fc.specs[0].typ
	if typ == nil {
		typ = fc.fl.pkg.info.TypeOf(fc.ft.ResultList[0].Type)
	}
	named, ok := types2.Unalias(typ).(*types2.Named)
	if !ok {
		return nil, false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil || obj.Pkg().Path() != "iter" {
		return nil, false
	}
	args := named.TypeArgs()
	switch obj.Name() {
	case "Seq":
		if args.Len() != 1 {
			return nil, false
		}
		return []types2.Type{args.At(0)}, true
	case "Seq2":
		if args.Len() != 2 {
			return nil, false
		}
		return []types2.Type{args.At(0), args.At(1)}, true
	}
	return nil, false
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
	targs, ok := fc.generatorTarget()
	if !ok {
		return false
	}
	fl := fc.fl
	pos := fc.body.Pos()
	spellings := make([]string, len(targs))
	natives := make([]syntax.Expr, len(targs))
	for i, targ := range targs {
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
	targs, _ := fc.generatorTarget()
	args := make([]syntax.Expr, len(vals))
	var texts []otext
	texts = append(texts, gen("if !"+yieldParam+"("))
	for i, v := range vals {
		var val syntax.Expr
		head, tail := "", ""
		if i < len(targs) {
			var t types2.Type = targs[i]
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

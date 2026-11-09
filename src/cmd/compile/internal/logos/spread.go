// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

func (fl *fileLower) lowerSpreads() {
	for _, decl := range fl.file.DeclList {
		switch d := decl.(type) {
		case *syntax.FuncDecl:
			if d.Body != nil {
				d.Body.List = fl.lowerSpreadList(d.Body.List)
			}
		case *syntax.VarDecl:
			if d.Values != nil {
				d.Values = fl.spreadValue(d.Values)
			}
		case *syntax.ConstDecl:
			if d.Values != nil {
				d.Values = fl.spreadValue(d.Values)
			}
		}
	}
}

func (fl *fileLower) hasSpread(c *syntax.CompositeLit) bool {
	return len(fl.pkg.info.LogosSpreads[c]) > 0
}

func (fl *fileLower) lowerSpreadList(list []syntax.Stmt) []syntax.Stmt {
	var out []syntax.Stmt
	for _, s := range list {
		out = append(out, fl.lowerSpreadStmt(s)...)
	}
	return out
}

func (fl *fileLower) lowerSpreadStmt(s syntax.Stmt) []syntax.Stmt {
	if as, ok := s.(*syntax.AssignStmt); ok {
		if tup, ok := fl.spreadSelfUpdate(as); ok {
			return []syntax.Stmt{tup}
		}
		as.Lhs = fl.spreadValue(as.Lhs)
		as.Rhs = fl.spreadValue(as.Rhs)
		return []syntax.Stmt{s}
	}
	switch s := s.(type) {
	case *syntax.BlockStmt:
		s.List = fl.lowerSpreadList(s.List)
	case *syntax.IfStmt:
		s.Init = fl.spreadSimple(s.Init)
		s.Cond = fl.spreadValue(s.Cond)
		s.Then.List = fl.lowerSpreadList(s.Then.List)
		fl.lowerSpreadElse(s)
	case *syntax.ForStmt:
		s.Init = fl.spreadSimple(s.Init)
		s.Cond = fl.spreadValue(s.Cond)
		s.Post = fl.spreadSimple(s.Post)
		s.Body.List = fl.lowerSpreadList(s.Body.List)
	case *syntax.SwitchStmt:
		s.Init = fl.spreadSimple(s.Init)
		s.Tag = fl.spreadValue(s.Tag)
		for _, cc := range s.Body {
			cc.Cases = fl.spreadValue(cc.Cases)
			cc.Body = fl.lowerSpreadList(cc.Body)
		}
	case *syntax.SelectStmt:
		for _, cc := range s.Body {
			cc.Comm = fl.spreadSimple(cc.Comm)
			cc.Body = fl.lowerSpreadList(cc.Body)
		}
	case *syntax.LabeledStmt:
		inner := fl.lowerSpreadStmt(s.Stmt)
		s.Stmt = inner[0]
		return append([]syntax.Stmt{s}, inner[1:]...)
	case *syntax.ExprStmt:
		s.X = fl.spreadValue(s.X)
	case *syntax.SendStmt:
		s.Chan = fl.spreadValue(s.Chan)
		s.Value = fl.spreadValue(s.Value)
	case *syntax.CallStmt:
		s.Call = fl.spreadValue(s.Call)
	case *syntax.ReturnStmt:
		s.Results = fl.spreadValue(s.Results)
	case *syntax.DeclStmt:
		for _, decl := range s.DeclList {
			switch d := decl.(type) {
			case *syntax.VarDecl:
				if d.Values != nil {
					d.Values = fl.spreadValue(d.Values)
				}
			case *syntax.ConstDecl:
				if d.Values != nil {
					d.Values = fl.spreadValue(d.Values)
				}
			}
		}
	case *syntax.RangeClause:
		s.X = fl.spreadValue(s.X)
	}
	return []syntax.Stmt{s}
}

func (fl *fileLower) lowerSpreadElse(s *syntax.IfStmt) {
	switch els := s.Else.(type) {
	case nil:
	case *syntax.BlockStmt:
		els.List = fl.lowerSpreadList(els.List)
	case *syntax.IfStmt:
		s.Else = fl.lowerSpreadStmt(els)[0]
	}
}

func (fl *fileLower) spreadSimple(s syntax.SimpleStmt) syntax.SimpleStmt {
	if s == nil {
		return nil
	}
	switch s := s.(type) {
	case *syntax.AssignStmt:
		if tup, ok := fl.spreadSelfUpdate(s); ok {
			return tup
		}
		s.Lhs = fl.spreadValue(s.Lhs)
		s.Rhs = fl.spreadValue(s.Rhs)
	case *syntax.RangeClause:
		s.X = fl.spreadValue(s.X)
	case *syntax.SendStmt:
		s.Chan = fl.spreadValue(s.Chan)
		s.Value = fl.spreadValue(s.Value)
	case *syntax.ExprStmt:
		s.X = fl.spreadValue(s.X)
	}
	return s
}

func (fl *fileLower) spreadValue(e syntax.Expr) syntax.Expr {
	if e == nil {
		return nil
	}
	if lit, ok := e.(*syntax.FuncLit); ok {
		lit.Body.List = fl.lowerSpreadList(lit.Body.List)
		return e
	}
	if op, ok := e.(*syntax.Operation); ok && op.Op == syntax.And && op.Y == nil {
		if c, ok := syntax.Unparen(op.X).(*syntax.CompositeLit); ok && fl.hasSpread(c) {
			replaceFieldExprs(c, exprTypeFields, fl.spreadValue)
			return fl.spreadAddrOp(op, c)
		}
		op.X = fl.spreadValue(op.X)
		return e
	}
	replaceFieldExprs(e, exprTypeFields, fl.spreadValue)
	if c, ok := e.(*syntax.CompositeLit); ok && fl.hasSpread(c) {
		return fl.spreadValueComposite(c)
	}
	return e
}

type spreadBuilder struct {
	fl        *fileLower
	c         *syntax.CompositeLit
	pos       syntax.Pos
	vSpelling string
	infos     []types2.LogosSpread
	tw        *twin
	native    []syntax.Stmt
	text      []syntax.Stmt
}

func (fl *fileLower) spreadValueComposite(c *syntax.CompositeLit) syntax.Expr {
	return fl.spreadComposite(c, false)
}

func (fl *fileLower) spreadAddrComposite(c *syntax.CompositeLit) syntax.Expr {
	return fl.spreadComposite(c, true)
}

func (fl *fileLower) spreadComposite(c *syntax.CompositeLit, addr bool) syntax.Expr {
	b := fl.spreadOpen(c)
	b.body()
	ret := func() syntax.Expr {
		v := nameOf(b.pos, b.vSpelling)
		if addr {
			return at(&syntax.Operation{Op: syntax.And, X: v}, b.pos)
		}
		return v
	}
	b.native = append(b.native, returnOf(b.pos, []syntax.Expr{ret()}))
	b.text = append(b.text, returnOf(b.pos, []syntax.Expr{ret()}))
	nat := b.nativeType()
	if addr {
		nat = at(&syntax.Operation{Op: syntax.Mul, X: nat}, b.pos)
	}
	native := callOf(b.pos, at(&syntax.FuncLit{
		Type: at(&syntax.FuncType{
			ResultList: []*syntax.Field{at(&syntax.Field{Type: nat}, b.pos)},
		}, b.pos),
		Body: blockOf(b.pos, b.native),
	}, b.pos), nil)
	ptr := ""
	if addr {
		ptr = "*"
	}
	text := cat(gen("func() "+ptr), b.typeText(), gen(" {\n"), renderText(b.text, b.tw.subs), gen("}()"))
	fl.addT(fl.off(syntax.StartPos(c)), fl.off(c.Rbrace)+1, text)
	return native
}

func (fl *fileLower) spreadAddrOp(op *syntax.Operation, c *syntax.CompositeLit) syntax.Expr {
	start := fl.off(op.Pos())
	native := fl.spreadAddrComposite(c)
	inner := fl.textOf(span{start: start + len("&"), end: fl.off(c.Rbrace) + 1})
	fl.addT(start, skipCloseParens(fl.src, fl.off(c.Rbrace)+1), inner)
	return native
}

func (fl *fileLower) spreadOpen(c *syntax.CompositeLit) *spreadBuilder {
	vSpelling := fmt.Sprintf("_logos_v%d", fl.spreadSeq)
	fl.spreadSeq++
	return &spreadBuilder{
		fl:        fl,
		c:         c,
		pos:       c.Pos(),
		vSpelling: vSpelling,
		infos:     fl.pkg.info.LogosSpreads[c],
		tw:        &twin{},
	}
}

func (b *spreadBuilder) typeText() otext {
	c := b.c
	if c.Type == nil {
		text, ok := b.fl.spellType(b.fl.pkg.info.TypeOf(c))
		if !ok {
			base.Fatalf("logos: cannot spell spread literal type %s", b.fl.pkg.info.TypeOf(c))
		}
		return gen(text)
	}
	return b.fl.textOf(span{start: b.fl.off(syntax.StartPos(c.Type)), end: b.fl.exprEndOf(c.Type)})
}

func (b *spreadBuilder) nativeType() syntax.Expr {
	c := b.c
	if c.Type == nil {
		text, ok := b.fl.spellType(b.fl.pkg.info.TypeOf(c))
		if !ok {
			base.Fatalf("logos: cannot spell spread literal type %s", b.fl.pkg.info.TypeOf(c))
		}
		reparsed, ok := reparseType(text)
		if !ok {
			base.Fatalf("logos: cannot reparse spread literal type %s", text)
		}
		syntax.Inspect(reparsed, func(n syntax.Node) bool {
			if n != nil {
				n.SetPos(b.pos)
			}
			return true
		})
		return reparsed
	}
	return cloneType(c.Type, b.pos)
}

func (b *spreadBuilder) body() {
	c := b.c
	pos := b.pos
	v := func() *syntax.Name { return nameOf(pos, b.vSpelling) }
	b.native = append(b.native, defineOf(pos,
		[]syntax.Expr{v()},
		[]syntax.Expr{at(&syntax.CompositeLit{Type: b.nativeType(), Rbrace: c.Rbrace}, pos)}))
	b.text = append(b.text, defineOf(pos,
		[]syntax.Expr{v()},
		[]syntax.Expr{at(&syntax.CompositeLit{Type: b.tw.mark(pos, b.typeText()), Rbrace: c.Rbrace}, pos)}))
	for _, el := range c.ElemList {
		if se, ok := el.(*syntax.SpreadExpr); ok {
			b.copy(se)
			continue
		}
		kv, ok := el.(*syntax.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*syntax.Name)
		if !ok {
			continue
		}
		valText := b.fl.textOf(span{start: b.fl.spreadStartOf(kv.Value), end: b.fl.exprEndOf(kv.Value)})
		b.native = append(b.native, assignOf(pos,
			[]syntax.Expr{selOf(pos, v(), key.Value)},
			[]syntax.Expr{kv.Value}))
		b.text = append(b.text, assignOf(pos,
			[]syntax.Expr{selOf(pos, v(), key.Value)},
			[]syntax.Expr{b.tw.mark(pos, valText)}))
	}
}

func (b *spreadBuilder) copy(se *syntax.SpreadExpr) {
	var fields []string
	for _, info := range b.infos {
		if info.Src == se.X {
			fields = info.Fields
			break
		}
	}
	pos := b.pos
	srcText := b.fl.textOf(span{start: b.fl.spreadStartOf(se.X), end: b.fl.exprEndOf(se.X)})
	if len(fields) == 0 {
		b.native = append(b.native, assignOf(pos,
			[]syntax.Expr{nameOf(pos, "_")},
			[]syntax.Expr{se.X}))
		b.text = append(b.text, assignOf(pos,
			[]syntax.Expr{nameOf(pos, "_")},
			[]syntax.Expr{b.tw.mark(pos, srcText)}))
		return
	}
	tmpSpelling := fmt.Sprintf("_logos_spread%d", b.fl.spreadSeq)
	b.fl.spreadSeq++
	b.native = append(b.native, defineOf(pos,
		[]syntax.Expr{nameOf(pos, tmpSpelling)},
		[]syntax.Expr{se.X}))
	b.text = append(b.text, defineOf(pos,
		[]syntax.Expr{nameOf(pos, tmpSpelling)},
		[]syntax.Expr{b.tw.mark(pos, srcText)}))
	for _, name := range fields {
		b.native = append(b.native, assignOf(pos,
			[]syntax.Expr{selOf(pos, nameOf(pos, b.vSpelling), name)},
			[]syntax.Expr{selOf(pos, nameOf(pos, tmpSpelling), name)}))
		b.text = append(b.text, assignOf(pos,
			[]syntax.Expr{selOf(pos, nameOf(pos, b.vSpelling), name)},
			[]syntax.Expr{selOf(pos, nameOf(pos, tmpSpelling), name)}))
	}
}

func (fl *fileLower) spreadSelfUpdate(s *syntax.AssignStmt) (*syntax.AssignStmt, bool) {
	if s.Op != 0 {
		return nil, false
	}
	lhs := syntax.UnpackListExpr(s.Lhs)
	rhs := syntax.UnpackListExpr(s.Rhs)
	if len(lhs) != 1 || len(rhs) != 1 {
		return nil, false
	}
	target, ok := lhs[0].(*syntax.Name)
	if !ok || target.Value == "_" {
		return nil, false
	}
	c, ok := syntax.Unparen(rhs[0]).(*syntax.CompositeLit)
	if !ok || !fl.hasSpread(c) {
		return nil, false
	}
	obj := fl.pkg.info.Uses[target]
	if obj == nil {
		return nil, false
	}
	for _, info := range fl.pkg.info.LogosSpreads[c] {
		src, ok := syntax.Unparen(info.Src).(*syntax.Name)
		if !ok || fl.pkg.info.Uses[src] != obj {
			return nil, false
		}
	}
	pos := c.Pos()
	var fields []string
	var values []syntax.Expr
	tw := &twin{}
	var ltexts []string
	var vtexts []otext
	for _, el := range c.ElemList {
		kv, ok := el.(*syntax.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*syntax.Name)
		if !ok {
			continue
		}
		val := fl.spreadValue(kv.Value)
		fields = append(fields, key.Value)
		values = append(values, val)
		ltexts = append(ltexts, target.Value+"."+key.Value)
		vtexts = append(vtexts, fl.textOf(span{start: fl.spreadStartOf(val), end: fl.exprEndOf(val)}))
	}
	if len(fields) == 0 {
		native := assignOf(pos, []syntax.Expr{nameOf(pos, target.Value)}, []syntax.Expr{nameOf(pos, target.Value)})
		fl.add(fl.stmtSpanStart(s, lhs[0]), fl.spanEndOf(rhs[0]), target.Value+" = "+target.Value)
		return native, true
	}
	nativeLhs := make([]syntax.Expr, len(fields))
	textLhs := make([]syntax.Expr, len(fields))
	textVals := make([]syntax.Expr, len(values))
	for i, name := range fields {
		nativeLhs[i] = selOf(pos, nameOf(pos, target.Value), name)
		textLhs[i] = tw.mark(pos, gen(ltexts[i]))
		textVals[i] = tw.mark(pos, vtexts[i])
	}
	native := assignOf(pos, nativeLhs, values)
	text := renderText([]syntax.Stmt{assignOf(pos, textLhs, textVals)}, tw.subs)
	fl.addT(fl.stmtSpanStart(s, lhs[0]), fl.spanEndOf(rhs[0]), text)
	return native, true
}

func (fl *fileLower) stmtSpanStart(s *syntax.AssignStmt, lhs syntax.Expr) int {
	return spanStartBack(fl.src, fl.off(leftmostOf(lhs)))
}

func (fl *fileLower) spreadStartOf(e syntax.Expr) int {
	if c, ok := syntax.Unparen(e).(*syntax.CompositeLit); ok && c.Type != nil {
		return spanStartBack(fl.src, fl.off(syntax.StartPos(c)))
	}
	return spanStartBack(fl.src, fl.off(leftmostOf(e)))
}

func (fl *fileLower) spanEndOf(e syntax.Expr) int {
	if c, ok := syntax.Unparen(e).(*syntax.CompositeLit); ok {
		return skipCloseParens(fl.src, fl.off(c.Rbrace)+1)
	}
	return fl.exprEndOf(e)
}

func (fl *fileLower) exprEndOf(e syntax.Expr) int {
	if c, ok := syntax.Unparen(e).(*syntax.CompositeLit); ok {
		return skipCloseParens(fl.src, fl.off(c.Rbrace)+1)
	}
	return fl.exprEnd(fl.off(leftmostOf(e)))
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package syntax

import "strconv"

const iterImportPath = "iter"

func (p *parser) logosYield() (Expr, Stmt) {
	if p.mode&LogosMode == 0 || p.lit != "yield" {
		return p.exprList(), nil
	}
	name := p.name()
	if !p.yieldOperandStart() {
		return p.exprListTail(p.logosLambdaOrExpr(p.binaryExpr(p.pexpr(name, true), 0))), nil
	}
	s := new(YieldStmt)
	s.pos = name.Pos()
	s.Value = p.exprList()
	return nil, s
}

func (p *parser) yieldOperandStart() bool {
	if !p.yieldGen {
		switch p.tok {
		case _Literal, _Func, _Struct, _Map, _Chan, _Interface, _Name:
			return true
		}
		return false
	}
	switch p.tok {
	case _Name, _Literal, _Lparen, _Lbrack, _Func, _Struct, _Map, _Chan, _Interface, _Const, _Switch, _Star, _Arrow:
		return true
	case _Operator:
		switch p.op {
		case Add, Sub, Not, Xor, And:
			return true
		}
	}
	return false
}

func (p *parser) logosNoteImport(d *ImportDecl) {
	if p.mode&LogosMode == 0 || d.Path == nil || d.Path.Bad {
		return
	}
	if path, err := strconv.Unquote(d.Path.Value); err != nil || path != iterImportPath {
		return
	}
	name := iterImportPath
	if d.LocalPkgName != nil {
		name = d.LocalPkgName.Value
	}
	p.yieldIter = append(p.yieldIter, name)
}

func (p *parser) logosEnterBody(ftyp *FuncType) bool {
	outer := p.yieldGen
	p.yieldGen = p.mode&LogosMode != 0 && p.generatorResult(ftyp)
	return outer
}

func (p *parser) generatorResult(ftyp *FuncType) bool {
	if len(ftyp.ResultList) != 1 {
		return false
	}
	inst, ok := ftyp.ResultList[0].Type.(*IndexExpr)
	if !ok {
		return false
	}
	switch seq := inst.X.(type) {
	case *SelectorExpr:
		pkg, ok := seq.X.(*Name)
		return ok && p.iterName(pkg.Value) && isSeqName(seq.Sel.Value)
	case *Name:
		return p.iterName(".") && isSeqName(seq.Value)
	}
	return false
}

func (p *parser) iterName(name string) bool {
	for _, imported := range p.yieldIter {
		if imported == name {
			return true
		}
	}
	return false
}

func isSeqName(name string) bool {
	return name == "Seq" || name == "Seq2"
}

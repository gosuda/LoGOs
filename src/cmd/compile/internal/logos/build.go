// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"bytes"
	"fmt"
	"go/constant"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

const rtImportPath = "logos/rt"

const rtAlias = "_logos_rt"

const markerPrefix = "\x00logosP"

type textSub struct {
	marker string
	text   otext
}

func markerText(subs *[]textSub, seq *int, pos syntax.Pos, text otext) *syntax.Name {
	m := fmt.Sprintf("%s%d\x00", markerPrefix, *seq)
	*seq++
	*subs = append(*subs, textSub{marker: m, text: text})
	return nameOf(pos, m)
}

func renderText(stmts []syntax.Stmt, subs []textSub) otext {
	var buf bytes.Buffer
	for _, s := range stmts {
		_, _ = syntax.Fprint(&buf, s, 0)
		buf.WriteByte('\n')
	}
	out := gen(buf.String())
	if len(subs) == 0 {
		return out
	}
	byMark := make(map[string]otext, len(subs))
	for _, sub := range subs {
		byMark[sub.marker] = sub.text
	}
	return substNul(out, func(mark string) (otext, bool) {
		t, ok := byMark[mark]
		return t, ok
	})
}

func leftmostOf(e syntax.Expr) syntax.Pos {
	for {
		switch n := syntax.Unparen(e).(type) {
		case *syntax.SelectorExpr:
			e = n.X
		case *syntax.IndexExpr:
			e = n.X
		case *syntax.SliceExpr:
			e = n.X
		case *syntax.AssertExpr:
			e = n.X
		case *syntax.CallExpr:
			e = n.Fun
		case *syntax.Operation:
			if n.Y == nil {
				return e.Pos()
			}
			e = n.X
		default:
			return e.Pos()
		}
	}
}

func renderExpr(e syntax.Expr) string {
	var buf bytes.Buffer
	_, _ = syntax.Fprint(&buf, e, syntax.LineForm)
	return buf.String()
}

func at[T syntax.Node](n T, pos syntax.Pos) T {
	n.SetPos(pos)
	return n
}

func nameOf(pos syntax.Pos, value string) *syntax.Name {
	return at(syntax.NewName(pos, value), pos)
}

func litOf(pos syntax.Pos, value string, kind syntax.LitKind) *syntax.BasicLit {
	return at(&syntax.BasicLit{Value: value, Kind: kind}, pos)
}

func nilOf(pos syntax.Pos) *syntax.Name {
	return nameOf(pos, "nil")
}

func callOf(pos syntax.Pos, fun syntax.Expr, args []syntax.Expr) *syntax.CallExpr {
	return at(&syntax.CallExpr{Fun: fun, ArgList: args}, pos)
}

func selOf(pos syntax.Pos, x syntax.Expr, sel string) *syntax.SelectorExpr {
	return at(&syntax.SelectorExpr{X: x, Sel: nameOf(pos, sel)}, pos)
}

func notNil(pos syntax.Pos, x syntax.Expr) *syntax.Operation {
	return at(&syntax.Operation{Op: syntax.Neq, X: x, Y: nilOf(pos)}, pos)
}

func assignOf(pos syntax.Pos, lhs, rhs []syntax.Expr) *syntax.AssignStmt {
	return at(&syntax.AssignStmt{Lhs: listOf(pos, lhs), Rhs: listOf(pos, rhs)}, pos)
}

func defineOf(pos syntax.Pos, lhs, rhs []syntax.Expr) *syntax.AssignStmt {
	return at(&syntax.AssignStmt{Op: syntax.Def, Lhs: listOf(pos, lhs), Rhs: listOf(pos, rhs)}, pos)
}

func listOf(pos syntax.Pos, exprs []syntax.Expr) syntax.Expr {
	if len(exprs) == 1 {
		return exprs[0]
	}
	return at(&syntax.ListExpr{ElemList: exprs}, pos)
}

func exprStmtOf(pos syntax.Pos, x syntax.Expr) *syntax.ExprStmt {
	return at(&syntax.ExprStmt{X: x}, pos)
}

func blockOf(pos syntax.Pos, list []syntax.Stmt) *syntax.BlockStmt {
	return at(&syntax.BlockStmt{List: list, Rbrace: pos}, pos)
}

func ifOf(pos syntax.Pos, init syntax.SimpleStmt, cond syntax.Expr, then []syntax.Stmt) *syntax.IfStmt {
	return at(&syntax.IfStmt{Init: init, Cond: cond, Then: blockOf(pos, then)}, pos)
}

func ifElseOf(pos syntax.Pos, init syntax.SimpleStmt, cond syntax.Expr, then []syntax.Stmt, els syntax.Stmt) *syntax.IfStmt {
	return at(&syntax.IfStmt{Init: init, Cond: cond, Then: blockOf(pos, then), Else: els}, pos)
}

func returnOf(pos syntax.Pos, results []syntax.Expr) *syntax.ReturnStmt {
	var r syntax.Expr
	if len(results) > 0 {
		r = listOf(pos, results)
	}
	return at(&syntax.ReturnStmt{Results: r}, pos)
}

func deferOf(pos syntax.Pos, call syntax.Expr) *syntax.CallStmt {
	return at(&syntax.CallStmt{Tok: syntax.Defer, Call: call}, pos)
}

func rtSel(pos syntax.Pos, sel string) *syntax.SelectorExpr {
	return selOf(pos, nameOf(pos, rtAlias), sel)
}

func zeroForType(typ types2.Type, typeExpr syntax.Expr, pos syntax.Pos) syntax.Expr {
	typ = types2.Unalias(typ)
	if _, ok := typ.(*types2.TypeParam); ok {
		return at(&syntax.Operation{
			Op: syntax.Mul,
			X:  callOf(pos, nameOf(pos, "new"), []syntax.Expr{cloneType(typeExpr, pos)}),
		}, pos)
	}
	if b, ok := typ.(*types2.Basic); ok {
		return zeroBasic(b, pos)
	}
	switch typ.Underlying().(type) {
	case *types2.Struct, *types2.Array:
		return at(&syntax.CompositeLit{Type: cloneType(typeExpr, pos), Rbrace: pos}, pos)
	default:
		return nilOf(pos)
	}
}

func zeroBasic(b *types2.Basic, pos syntax.Pos) syntax.Expr {
	switch {
	case b.Kind() == types2.Bool:
		return nameOf(pos, "false")
	case b.Kind() == types2.String:
		return litOf(pos, `""`, syntax.StringLit)
	case b.Kind() == types2.UnsafePointer:
		return nilOf(pos)
	default:
		return litOf(pos, "0", syntax.IntLit)
	}
}

func cloneType(e syntax.Expr, pos syntax.Pos) syntax.Expr {
	if decl, ok := respellType(e, pos); ok {
		return decl
	}
	return nameOf(pos, "any")
}

func mustCloneType(e syntax.Expr, pos syntax.Pos) syntax.Expr {
	decl, ok := respellType(e, pos)
	if !ok {
		base.Fatalf("logos: cannot respell type %q", renderExpr(e))
	}
	return decl
}

func respellType(e syntax.Expr, pos syntax.Pos) (syntax.Expr, bool) {
	decl, ok := reparseType(renderExpr(e))
	if !ok {
		return nil, false
	}
	syntax.Inspect(decl, func(n syntax.Node) bool {
		if n != nil {
			n.SetPos(pos)
		}
		return true
	})
	return decl, true
}

func reparseType(text string) (syntax.Expr, bool) {
	file, err := syntax.Parse(syntax.NewFileBase("logos-clone"), strings.NewReader("package logosclone\nvar _ "+text), nil, nil, 0)
	if err != nil || file == nil || len(file.DeclList) != 1 {
		return nil, false
	}
	decl, ok := file.DeclList[0].(*syntax.VarDecl)
	if !ok || decl.Type == nil {
		return nil, false
	}
	return decl.Type, true
}

func typeConst(info *types2.Info, e syntax.Expr) (constant.Value, bool) {
	if info.Types != nil {
		if tv, ok := info.Types[e]; ok && tv.Value != nil {
			return tv.Value, true
		}
		return nil, false
	}
	if tv := e.GetTypeInfo(); tv.Value != nil {
		return tv.Value, true
	}
	return nil, false
}

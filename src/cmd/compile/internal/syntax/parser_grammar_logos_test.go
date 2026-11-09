// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package syntax

import "testing"

type grammarProbe struct {
	prod  string
	src   string
	found func(*File) bool
}

func hasNode[N Node](f *File) bool {
	found := false
	Inspect(f, func(n Node) bool {
		if _, ok := n.(N); ok {
			found = true
			return false
		}
		return true
	})
	return found
}

func hasDeferTry(f *File) bool {
	found := false
	Inspect(f, func(n Node) bool {
		if c, ok := n.(*CallStmt); ok && c.Tok == Defer {
			Inspect(c.Call, func(m Node) bool {
				if _, ok := m.(*TryExpr); ok {
					found = true
					return false
				}
				return true
			})
		}
		return true
	})
	return found
}

func hasChainCall(f *File) bool {
	found := false
	Inspect(f, func(n Node) bool {
		if c, ok := n.(*CallExpr); ok {
			if _, ok := Unparen(c.Fun).(*SelectorExpr); ok {
				found = true
				return false
			}
		}
		return true
	})
	return found
}
func hasUnionDecl(f *File) bool {
	found := false
	Inspect(f, func(n Node) bool {
		if it, ok := n.(*InterfaceType); ok && len(it.MethodList) == 1 {
			if fld := it.MethodList[0]; fld.Name == nil {
				if _, ok := Unparen(fld.Type).(*Operation); ok {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found
}

func hasUnionTerms(f *File) bool {
	found := false
	Inspect(f, func(n Node) bool {
		if op, ok := n.(*Operation); ok && op.Op == Or {
			found = true
			return false
		}
		return true
	})
	return found
}
func hasReadonlyMethod(f *File) bool {
	found := false
	Inspect(f, func(n Node) bool {
		if fld, ok := n.(*Field); ok && fld.Const {
			found = true
			return false
		}
		return true
	})
	return found
}

var grammarProbes = []grammarProbe{
	{"PostfixTry", "package p\nfunc f() { g()? }\n", hasNode[*TryExpr]},
	{"OptionType", "package p\nfunc f(x int?) {}\n", hasNode[*OptionType]},
	{"Coalesce", "package p\nfunc f() { _ = a ?? b }\n", hasNode[*CoalesceExpr]},
	{"OptChain", "package p\nfunc f() { _ = a?.b }\n", hasNode[*OptSelectExpr]},
	{"Interp", "package p\nvar s = $\"n={1}\"\n", hasNode[*InterpExpr]},
	{"Lambda", "package p\nfunc f() { g := (a, b) => a }\n", hasNode[*LambdaExpr]},
	{"ChainCall", "package p\nfunc f() {\n_ = xs\n.filter(1)\n.map(2)\n}\n", hasChainCall},
	{"TupleType", "package p\nvar v (int, string)\n", hasNode[*TupleType]},
	{"TupleLit", "package p\nfunc f() { _ = (a, b) }\n", hasNode[*TupleLit]},
	{"SwitchExpr", "package p\nfunc f(v int) { _ = switch v { case 1 => 1; default => 2 } }\n", hasNode[*SwitchExpr]},
	{"Arm", "package p\nfunc f(v int) { _ = switch v { case 1 => 1; default => 2 } }\n", hasNode[*ArmClause]},
	{"UnionDecl", "package p\ntype U interface { int | string }\n", hasUnionDecl},
	{"UnionTerms", "package p\ntype U interface { int | string }\n", hasUnionTerms},
	{"AssertDecl", "package p\ntype I interface { M() }\ntype S struct : I { X int }\n", hasNode[*AssertedStruct]},
	{"PropertyElem", "package p\ntype C interface { const Name string\nHP int }\n", hasNode[*PropertyType]},
	{"ReadonlyMethod", "package p\ntype I interface { const M() string }\n", hasReadonlyMethod},
	{"ConstView", "package p\nfunc f(x const []int) {}\n", hasNode[*ConstType]},
	{"ConstDecl", "package p\ntype S struct{ A int }\nconst K = S{A: 1}\n", hasNode[*ConstDecl]},
	{"ConstIf", "package p\nfunc f() { const if true {} }\n", hasNode[*ConstIfStmt]},
	{"ConstFor", "package p\nfunc f() { const for i := range x {} }\n", hasNode[*ConstForStmt]},
	{"FieldProj", "package p\nfunc f() { _ = v.[f] }\n", hasNode[*FieldProjExpr]},
	{"ConstSwitch", "package p\nfunc f[T any](v T) int {\n_ = v\nconst switch T {\ncase int:\nreturn 1\n}\nreturn 0\n}\n", hasNode[*ConstSwitchStmt]},
	{"DeferTry", "package p\nfunc f() { defer g()? }\n", hasDeferTry},
	{"ErrDefer", "package p\nfunc f() { errdefer g() }\n", hasNode[*ErrdeferStmt]},
	{"ScopeDefer", "package p\nfunc f() { scope defer g() }\n", hasNode[*ScopeDeferStmt]},
	{"GoGroup", "package p\nfunc f(ctx int) { go group (ctx) { go { g() } } }\n", hasNode[*GroupStmt]},
	{"GroupChild", "package p\nfunc f(ctx int) { go group (ctx) { go { g() } } }\n", hasNode[*GroupChild]},
	{"YieldStmt", "package p\nfunc f() { yield 1 }\n", hasNode[*YieldStmt]},
	{"SpreadElem", "package p\ntype S struct{ A, B int }\nvar _ = S{A: 1, ..s}\n", hasNode[*SpreadExpr]},
	{"DefaultField", "package p\ntype S struct { Port int = 8080 }\n", hasNode[*FieldDefault]},
}

func TestGrammarProductionsHaveFixtures(t *testing.T) {
	seen := make(map[string]bool, len(grammarProbes))
	for _, g := range grammarProbes {
		if seen[g.prod] {
			t.Fatalf("duplicate probe for %s", g.prod)
		}
		seen[g.prod] = true
		f := parseLogos(t, g.src)
		if !g.found(f) {
			t.Errorf("%s: no witness node in %q", g.prod, g.src)
		}
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

// Consumer behavior proof that the const for copy check carries the
// check's import capability: a copied block that calls an imported
// recursive const func records its value, and the same block without a
// threaded importer records none (no global cache). Plain Go checking is
// unaffected. The step limit of an imported const func inside a comptime
// instance is pinned by cmd/go script logos_constfunc_import_limits.

import (
	"fmt"
	"strings"
	"testing"

	"cmd/compile/internal/syntax"
)

const logosCopyImportPath = "example.com/copyimportrec"

const logosCopyImportRecSrc = "package reclib\n\nconst func Rec(n int) int {\n\tif n <= 0 {\n\t\treturn 0\n\t}\n\treturn 1 + Rec(n-1)\n}\n"

// logosCopyImportStub is the configured importer a copy checker opens
// packages through. It serves only packages the test installed.
type logosCopyImportStub struct {
	pkgs map[string]*Package
}

func (s *logosCopyImportStub) Import(path string) (*Package, error) {
	if p, ok := s.pkgs[path]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("unknown import %s", path)
}

// logosCopyImportFixture registers the constfn record for the reclib
// Rec helper and serves its defining package through a stub importer.
func logosCopyImportFixture(t *testing.T) (*Package, Importer) {
	t.Helper()
	defPkg := NewPackage(logosCopyImportPath, "reclib")
	sig := NewSignatureType(nil, nil, nil,
		NewTuple(NewVar(nopos, defPkg, "n", Typ[Int])),
		NewTuple(NewVar(nopos, defPkg, "", Typ[Int])), false)
	defPkg.Scope().Insert(NewFunc(nopos, defPkg, "Rec", sig))
	LogosImportedConstFuncSource = func(path, object string) []byte {
		if path == logosCopyImportPath && object == "Rec" {
			return []byte(logosCopyImportRecSrc)
		}
		return nil
	}
	return defPkg, &logosCopyImportStub{pkgs: map[string]*Package{logosCopyImportPath: defPkg}}
}

// logosCopyCaller checks a trivial caller package under the selected
// step budget and returns its package, file, and info, mirroring the
// main check that records importer and context for copies.
func logosCopyCaller(t *testing.T, imp Importer) (*Package, *syntax.File, *Info) {
	t.Helper()
	file, err := syntax.Parse(syntax.NewFileBase("caller"), strings.NewReader("package caller\nfunc Use() int { return 1 }\n"), nil, nil, 0)
	if err != nil {
		t.Fatalf("parse caller: %v", err)
	}
	conf := &Config{
		Logos:             true,
		Importer:          imp,
		Context:           NewContext(),
		LogosCTFEMaxSteps: 100,
		Sizes:             SizesFor("gc", "amd64"),
	}
	info := &Info{
		Types: map[syntax.Expr]TypeAndValue{}, Defs: map[*syntax.Name]Object{},
		Uses: map[*syntax.Name]Object{}, Selections: map[*syntax.SelectorExpr]*Selection{},
		Scopes: map[syntax.Node]*Scope{}, Instances: map[*syntax.Name]Instance{},
		// The driver records the selected budget on both conf and
		// info; the copy keeps the selected limit through ctx.
		LogosCTFEMaxSteps: 100,
	}
	pkg, err := conf.Check("example.com/copycaller", []*syntax.File{file}, info)
	if err != nil {
		t.Fatalf("caller check: %v", err)
	}
	return pkg, file, info
}

// logosCopyParseBlock parses one copied const for iteration block.
func logosCopyParseBlock(t *testing.T, stmts string) *syntax.BlockStmt {
	t.Helper()
	file, err := syntax.Parse(syntax.NewFileBase("cfcopy"), strings.NewReader("package cfcopy\nfunc cfcopy() {\n{\n"+stmts+"\n}\n}\n"), nil, nil, 0)
	if err != nil {
		t.Fatalf("parse copy: %v", err)
	}
	for _, decl := range file.DeclList {
		if fn, ok := decl.(*syntax.FuncDecl); ok && len(fn.Body.List) == 1 {
			if block, ok := fn.Body.List[0].(*syntax.BlockStmt); ok {
				return block
			}
		}
	}
	t.Fatal("copy source holds no block")
	return nil
}

// logosCopyLimitValue returns the value the copy check recorded for the
// constant Limit, if any.
func logosCopyLimitValue(t *testing.T, info *Info) (int64, bool) {
	t.Helper()
	for _, o := range info.Defs {
		if c, ok := o.(*Const); ok && c.Name() == "Limit" {
			v, ok := info.LogosCTFE[c]
			if !ok || !v.IsValid() {
				return 0, false
			}
			return v.Int64(), true
		}
	}
	t.Fatal("copy check recorded no Limit constant")
	return 0, false
}

// TestLogosCopyImportedUnderLimit proves a below-limit call in a copied
// block to the imported const func evaluates through the threaded
// importer and records its value.
func TestLogosCopyImportedUnderLimit(t *testing.T) {
	defPkg, imp := logosCopyImportFixture(t)
	pkg, file, info := logosCopyCaller(t, imp)
	ctx := LogosCopyContext{
		Scope:    info.Scopes[file],
		Sig:      NewSignatureType(nil, nil, nil, nil, nil, false),
		Aliases:  map[*Package]string{defPkg: "_logos_t0"},
		MaxSteps: info.LogosCTFEMaxSteps,
		Importer: info.LogosImporter,
		Context:  info.LogosContext,
	}
	if ctx.Scope == nil {
		t.Fatal("caller file scope missing")
	}
	LogosCheckCopy(pkg, info, ctx, []*syntax.BlockStmt{logosCopyParseBlock(t, "const Limit = _logos_t0.Rec(5)\n_ = Limit")})
	v, ok := logosCopyLimitValue(t, info)
	if !ok {
		t.Fatal("copied Rec(5) recorded no value for Limit")
	}
	if v != 5 {
		t.Fatalf("copied Rec(5) Limit = %d, want 5", v)
	}
}

// TestLogosCopyWithoutImporterFailsClosed proves the capability is
// threaded, not ambient: the same copied block without an importer in
// context or info cannot open the defining package and records no value.
func TestLogosCopyWithoutImporterFailsClosed(t *testing.T) {
	defPkg, _ := logosCopyImportFixture(t)
	pkg := NewPackage("example.com/copycallerplain", "caller")
	info := &Info{
		Types: map[syntax.Expr]TypeAndValue{}, Defs: map[*syntax.Name]Object{},
		Uses: map[*syntax.Name]Object{}, Selections: map[*syntax.SelectorExpr]*Selection{},
		Scopes: map[syntax.Node]*Scope{}, Instances: map[*syntax.Name]Instance{},
	}
	ctx := LogosCopyContext{
		Scope:    pkg.Scope(),
		Sig:      NewSignatureType(nil, nil, nil, nil, nil, false),
		Aliases:  map[*Package]string{defPkg: "_logos_t0"},
		MaxSteps: 100,
	}
	LogosCheckCopy(pkg, info, ctx, []*syntax.BlockStmt{logosCopyParseBlock(t, "const Limit = _logos_t0.Rec(5)\n_ = Limit")})
	if v, ok := logosCopyLimitValue(t, info); ok {
		t.Fatalf("importer-less copy recorded Limit = %d, want no evaluation", v)
	}
}

// TestLogosPlainCheckUnaffected proves ordinary Go checking still
// passes with no Logos importer or context recorded.
func TestLogosPlainCheckUnaffected(t *testing.T) {
	file, err := syntax.Parse(syntax.NewFileBase("plain"), strings.NewReader("package p\nconst C = 40 + 2\nfunc F() int { return C }\n"), nil, nil, 0)
	if err != nil {
		t.Fatalf("parse plain: %v", err)
	}
	info := &Info{
		Types: map[syntax.Expr]TypeAndValue{}, Defs: map[*syntax.Name]Object{},
		Uses: map[*syntax.Name]Object{},
	}
	if _, err := (&Config{}).Check("example.com/plain", []*syntax.File{file}, info); err != nil {
		t.Fatalf("plain Go check: %v", err)
	}
}

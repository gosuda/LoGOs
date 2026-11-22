// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"strings"
	"testing"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

func TestCfCopyNameShape(t *testing.T) {
	pkg := types2.NewPackage("example.com/p", "p")
	obj := types2.NewFunc(syntax.Pos{}, pkg, "Autowire", nil)
	if got, want := cfCopyName(obj, 7), "Autowire_logosct7"; got != want {
		t.Fatalf("cfCopyName = %q, want %q", got, want)
	}
	if cfCopyInfix != "_logosct" {
		t.Fatalf("cfCopyInfix = %q, want %q (logosCtASCIIInfix in cmd/link/internal/ld must match)", cfCopyInfix, "_logosct")
	}

	parse := func(name string) error {
		var errs []error
		_, _ = syntax.Parse(syntax.NewFileBase("cfcopy.lgo"),
			strings.NewReader("package p\nfunc "+name+"() {}\n"),
			func(err error) { errs = append(errs, err) },
			nil, syntax.CheckBranches|syntax.LogosMode)
		if len(errs) == 0 {
			return nil
		}
		return errs[0]
	}
	if err := parse(cfCopyName(obj, 0)); err != nil {
		t.Fatalf("copy name does not parse: %v", err)
	}
	if err := parse("Autowire·logosct0"); err == nil {
		t.Fatalf("middle-dot copy name parses, want a parse error")
	}
}
func TestCfCalleeObjResolvesImportMarker(t *testing.T) {
	leaf := types2.NewPackage("example.com/fwdtest/leaf", "leaf")
	desc := types2.NewFunc(syntax.Pos{}, leaf, "Describe", nil)
	leaf.Scope().Insert(desc)
	leaf.MarkComplete()
	mid := types2.NewPackage("example.com/fwdtest/mid", "mid")
	mid.SetImports([]*types2.Package{leaf})
	mid.MarkComplete()
	top := types2.NewPackage("example.com/fwdtest/top", "top")
	top.MarkComplete()
	fl := &fileLower{pkg: &packageLower{tpkg: top, ctExp: &cfExpander{roots: []*types2.Package{mid}}}}
	sites := &cfSites{scope: mid.Scope(), imports: []cfImport{{name: "leaf", path: "example.com/fwdtest/leaf"}}}
	call := &syntax.SelectorExpr{X: &syntax.Name{Value: cfImportMarker + "0"}, Sel: &syntax.Name{Value: "Describe"}}
	if got := fl.cfCalleeObj(call, sites); got != desc {
		t.Fatalf("cfCalleeObj import marker = %v, want Describe", got)
	}
}

// fakeRuntimeImporter serves logos/rt with a Site struct for copy-check
// tests. It stands in for the toolchain importer without a full build.
type fakeRuntimeImporter struct{}

func (fakeRuntimeImporter) Import(path string) (*types2.Package, error) {
	if path != rtImportPath {
		return nil, fmt.Errorf("unknown import %s", path)
	}
	pkg := types2.NewPackage(rtImportPath, "rt")
	name := types2.NewTypeName(syntax.Pos{}, pkg, "Site", nil)
	types2.NewNamed(name, types2.NewStruct(nil, nil), nil)
	pkg.Scope().Insert(name)
	pkg.MarkComplete()
	return pkg, nil
}

// testCopyFileLower builds the smallest fileLower that can check and emit
// a copy: one parsed file, a universe-parented scope, and the fake
// runtime importer.
func testCopyFileLower(t *testing.T, src string) *fileLower {
	t.Helper()
	file, errs := cfParseSource(src)
	if len(errs) > 0 {
		t.Fatalf("parse test file: %v", errs)
	}
	pkg := types2.NewPackage("example.com/copytest", "p")
	pkg.MarkComplete()
	info := &types2.Info{
		Scopes: map[syntax.Node]*types2.Scope{
			file: types2.NewScope(types2.Universe, syntax.Pos{}, syntax.Pos{}, ""),
		},
		LogosImporter: fakeRuntimeImporter{},
	}
	exp := &cfExpander{
		byObj:   make(map[*types2.Func]*cfGenericDef),
		byDecl:  make(map[*syntax.FuncDecl]*cfGenericDef),
		foreign: make(map[string]*cfRecord),
		memo:    make(map[string]string),
		pkgs:    make(map[string]*types2.Package),
	}
	fl := &fileLower{
		pkg:  &packageLower{info: info, tpkg: pkg, ctExp: exp},
		file: file,
		src:  []byte(src),
		base: file.Pos().FileBase(),
	}
	fl.srcMap = origBuf(fl.src)
	return fl
}

func countCopyDecls(fl *fileLower, name string) int {
	n := 0
	for _, decl := range fl.file.DeclList {
		if fn, ok := decl.(*syntax.FuncDecl); ok && fn.Name.Value == name {
			n++
		}
	}
	return n
}

func TestCfEmitCopyResolvesRuntimeAlias(t *testing.T) {
	fl := testCopyFileLower(t, "package p\n")
	plain := gen("func Plain_logosct0() string {\nreturn \"q\"\n}")
	fl.cfEmitCopy(plain, &cfSite{pos: syntax.Pos{}})
	if got := countCopyDecls(fl, "Plain_logosct0"); got != 1 {
		t.Fatalf("plain copy emitted %d times, want 1", got)
	}
	needsRT := gen("func NeedRT_logosct1() string {\nvar _ _logos_rt.Site\nreturn \"q\"\n}")
	fl.cfEmitCopy(needsRT, &cfSite{pos: syntax.Pos{}})
	if got := countCopyDecls(fl, "NeedRT_logosct1"); got != 1 {
		t.Fatalf("runtime-alias copy emitted %d times, want 1 (check must resolve %s)", got, rtAlias)
	}
}

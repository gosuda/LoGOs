// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"bytes"
	"internal/testenv"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cmd/compile/internal/logos/facts"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
	"cmd/internal/goobj"
)

func TestStructDefaultHelperSignature(t *testing.T) {
	const head = `package producer
type Config struct { Value int }
type Other struct { Value int }
type Alias = Config
`
	for _, tc := range []struct {
		name string
		decl string
		want string
	}{
		{"missing", "", "not a function"},
		{"constant", "const Logos_Default_Config = 1", "not a function"},
		{"function variable", "var Logos_Default_Config func(Config, [1]bool) Config", "not a function"},
		{"missing parameter", "func Logos_Default_Config(v Config) Config { return v }", "incompatible signature"},
		{"extra parameter", "func Logos_Default_Config(v Config, m [1]bool, extra int) Config { return v }", "incompatible signature"},
		{"different named input", "func Logos_Default_Config(v Other, m [1]bool) Config { return Config(v) }", "incompatible signature"},
		{"short mask", "func Logos_Default_Config(v Config, m [0]bool) Config { return v }", "incompatible signature"},
		{"long mask", "func Logos_Default_Config(v Config, m [2]bool) Config { return v }", "incompatible signature"},
		{"wrong mask element", "func Logos_Default_Config(v Config, m [1]int) Config { return v }", "incompatible signature"},
		{"slice mask", "func Logos_Default_Config(v Config, m []bool) Config { return v }", "incompatible signature"},
		{"missing result", "func Logos_Default_Config(v Config, m [1]bool) {}", "incompatible signature"},
		{"extra result", "func Logos_Default_Config(v Config, m [1]bool) (Config, Config) { return v, v }", "incompatible signature"},
		{"different named result", "func Logos_Default_Config(v Config, m [1]bool) Other { return Other(v) }", "incompatible signature"},
		{"variadic", "func Logos_Default_Config(v Config, m ...bool) Config { return v }", "incompatible signature"},
		{"unexpected generic parameter", "func Logos_Default_Config[T any](v Config, m [1]bool) Config { return v }", "incompatible type parameters"},
		{"exact signature", "func Logos_Default_Config(v Config, m [1]bool) Config { return v }", ""},
		{"identical aliases", "func Logos_Default_Config(v Alias, m [1]bool) Alias { return v }", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkg := checkDefaultHelperPackage(t, "example.com/helper-signature", head+tc.decl)
			typ := pkg.Scope().Lookup("Config").Type()
			err := checkDefaultHelper(pkg, typ, "Logos_Default_Config", nil)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("valid producer helper rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("helper validation = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestStructDefaultHelperGenericSignature(t *testing.T) {
	const head = `package producer
type Config[T any] struct { Value T }
var Target Config[int]
`
	for _, tc := range []struct {
		name string
		decl string
		want string
	}{
		{"missing type parameter", "func Logos_Default_Config(v Config[int], m [1]bool) Config[int] { return v }", "incompatible type parameters"},
		{"extra type parameter", "func Logos_Default_Config[T, U any](v Config[T], m [1]bool) Config[T] { return v }", "incompatible type parameters"},
		{"incompatible constraint", "func Logos_Default_Config[T ~string](v Config[T], m [1]bool) Config[T] { return v }", "does not satisfy"},
		{"exact generic signature", "func Logos_Default_Config[T any](v Config[T], m [1]bool) Config[T] { return v }", ""},
		{"compatible constraint", "func Logos_Default_Config[T ~int](v Config[T], m [1]bool) Config[T] { return v }", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pkg := checkDefaultHelperPackage(t, "example.com/helper-generic", head+tc.decl)
			typ := pkg.Scope().Lookup("Target").Type().(*types2.Named)
			err := checkDefaultHelper(pkg, typ, "Logos_Default_Config", typ.TypeArgs())
			if tc.want == "" {
				if err != nil {
					t.Fatalf("valid generic producer helper rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("generic helper validation = %v, want %q", err, tc.want)
			}
		})
	}
}

// Matching-fingerprint metadata may describe a default, but its source text
// is never executable consumer code. Only the checked producer helper runs.
func TestStructDefaultImportedTextIsOpaque(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	const module = "example.com/defaultopaque"
	const path = module + "/producer"
	const producer = `package producer
type Config struct {
	Value int
	First int
	Second int
	hidden int
}
func Logos_Default_Config(v Config, present [4]bool) Config {
	if !present[0] { v.Value = 17 }
	if !present[3] { v.hidden = 29 }
	return v
}
func Valid(v Config) bool { return v.hidden == 29 }
`
	pkg := checkDefaultHelperPackage(t, path, producer)
	const hostile = `func() int { panic("untrusted default expression executed") }()`
	fp := goobj.FingerprintType{1, 3, 5, 7, 2, 4, 6, 8}
	metadata := &facts.Facts{
		Defaults: []facts.DefaultFact{
			{Type: "Config", Field: "Value", Text: hostile},
			{Type: "Config", Field: "hidden", Text: hostile},
		},
		DefaultHelpers: []facts.DefaultHelperFact{{Type: "Config", Helper: "Logos_Default_Config"}},
	}
	if _, err := facts.Import(path, string(fp[:]), facts.Encode(fp, metadata)); err != nil {
		t.Fatalf("import structurally valid helper metadata: %v", err)
	}
	const consumer = `package main
import source "example.com/defaultopaque/producer"
type Alias = source.Config
var valid = source.Valid
var order int
func next(v int) int { order = order*10 + v; return v }
func main() {
	const source = 91
	_ = source
	a := Alias{First: next(1), Second: next(2)}
	b := Alias{Value: next(3), First: next(4), Second: next(5)}
	if order != 12345 || a.Value != 17 || b.Value != 3 ||
		a.First != 1 || a.Second != 2 || b.First != 4 || b.Second != 5 ||
		!valid(a) || !valid(b) {
		panic("producer helper lost defaults, explicit values, or evaluation order")
	}
	println("ok opaque default metadata")
}
`
	data := []byte(consumer)
	base := syntax.NewFileBase("defaultopaque.lgo")
	file := ejectParse(t, base, data, syntax.LogosMode|syntax.CheckBranches)
	imp := newTestImporter(t)
	imp.pkgs[path] = pkg
	info := &types2.Info{
		Types:      make(map[syntax.Expr]types2.TypeAndValue),
		Instances:  make(map[*syntax.Name]types2.Instance),
		Defs:       make(map[*syntax.Name]types2.Object),
		Uses:       make(map[*syntax.Name]types2.Object),
		Implicits:  make(map[syntax.Node]types2.Object),
		Selections: make(map[*syntax.SelectorExpr]*types2.Selection),
		Scopes:     make(map[syntax.Node]*types2.Scope),
	}
	conf := types2.Config{Importer: imp, Logos: true}
	consumerPkg, err := conf.Check(module, []*syntax.File{file}, info)
	if err != nil {
		t.Fatalf("check consumer: %v", err)
	}
	edits := ejectLower(t, consumerPkg, info, file, data)
	ejected, err := ApplyEdits(data, base, edits)
	if err != nil {
		t.Fatalf("eject consumer: %v", err)
	}
	var native bytes.Buffer
	if _, err := syntax.Fprint(&native, file, 0); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		src  []byte
	}{
		{"native tree", native.Bytes()},
		{"ejected text", ejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if bytes.Contains(tc.src, []byte("untrusted default expression executed")) {
				t.Fatal("imported default text entered generated consumer code")
			}
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, "producer"), 0o755); err != nil {
				t.Fatal(err)
			}
			for name, text := range map[string][]byte{
				"go.mod":               []byte("module " + module + "\n\ngo 1.27\n"),
				"main.go":              tc.src,
				"producer/producer.go": []byte(producer),
			} {
				if err := os.WriteFile(filepath.Join(dir, name), text, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(testenv.GoToolPath(t), "run", ".")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local", "GOWORK=off")
			out, err := cmd.CombinedOutput()
			if err != nil || string(out) != "ok opaque default metadata\n" {
				t.Fatalf("producer helper execution: %v\n%s", err, out)
			}
		})
	}
}

func checkDefaultHelperPackage(t *testing.T, path, src string) *types2.Package {
	t.Helper()
	file := ejectParse(t, syntax.NewFileBase("helper.go"), []byte(src), 0)
	conf := types2.Config{}
	pkg, err := conf.Check(path, []*syntax.File{file}, nil)
	if err != nil {
		t.Fatalf("check producer helper: %v", err)
	}
	return pkg
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

func lowerSnippetSrc(t *testing.T, name, src string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(src), 0666); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var perrs []error
	f, err := syntax.ParseFile(path, func(err error) { perrs = append(perrs, err) }, nil, syntax.CheckBranches|syntax.LogosMode)
	if err != nil {
		t.Fatal(errors.Join(append(perrs, err)...))
	}
	imp := newTestImporter(t)
	info := &types2.Info{
		Defs:       make(map[*syntax.Name]types2.Object),
		Uses:       make(map[*syntax.Name]types2.Object),
		Types:      make(map[syntax.Expr]types2.TypeAndValue),
		Selections: make(map[*syntax.SelectorExpr]*types2.Selection),
		Scopes:     make(map[syntax.Node]*types2.Scope),
		Instances:  make(map[*syntax.Name]types2.Instance),
	}
	var cerrs []error
	conf := types2.Config{
		Importer: imp,
		Error:    func(err error) { cerrs = append(cerrs, err) },
		Logos:    true,
	}
	files := []*syntax.File{f}
	pkg, _ := conf.Check("main", files, info)
	if err := errors.Join(cerrs...); err != nil {
		t.Fatalf("type check: %v", err)
	}
	changed, edits := Lower(pkg, info, files, map[*syntax.File][]byte{f: data})
	if !changed {
		t.Fatalf("Lower reported no change for %s source", name)
	}
	out, err := ApplyEdits(data, f.Pos().FileBase(), edits[f])
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestScopeDeferOpenCoded(t *testing.T) {
	src := "package main\n\nimport \"fmt\"\n\nfunc note(s string) { fmt.Println(s) }\n\nfunc f(n int) int {\n\t{\n\t\tnote(\"lock\")\n\t\tscope defer note(\"unlock\")\n\t\tnote(\"body\")\n\t\tif n > 0 {\n\t\t\treturn 1\n\t\t}\n\t}\n\treturn 0\n}\n\nfunc g() {\n\tfor range 3 {\n\t\tscope defer note(\"iter\")\n\t\tnote(\"work\")\n\t}\n}\n"
	out := lowerSnippetSrc(t, "scopesnip.lgo", src)
	if n := strings.Count(out, "defer func() {"); n != 2 {
		t.Errorf("got %d guarding defers, want 2 (one per scope defer site)", n)
	}
	if n := strings.Count(out, "= func() {"); n != 1 {
		t.Errorf("got %d holder closures, want 1 (the loop body rebinds one holder per iteration)", n)
	}
	if !strings.Contains(out, "var _logos_sd0") {
		t.Errorf("missing hoisted holder declaration:\n%s", out)
	}
	if n := strings.Count(out, "if _logos_sd0 {"); n != 3 {
		t.Errorf("got %d armed-flag stanzas, want 3", n)
	}
	if n := strings.Count(out, "if _logos_sd0 != nil {"); n != 2 {
		t.Errorf("got %d holder stanzas, want 2 (panic guard and iteration end)", n)
	}
	if !strings.Contains(out, "_logos_f0 := note") {
		t.Errorf("callee capture missing:\n%s", out)
	}
	if strings.Contains(strings.ToLower(out), "unwind") {
		t.Errorf("unwind table reference in open-coded lowering:\n%s", out)
	}
	if strings.LastIndex(out, "defer func() {") > strings.Index(out, "for range") {
		t.Errorf("loop guard sits inside the loop, disabling open-coded defers:\n%s", out)
	}
}

func TestScopeReturnMultiResultEject(t *testing.T) {
	src := "package main\n\nimport \"log/slog\"\n\nfunc note(s string) { slog.Info(s) }\n\nfunc pair() (int, int) { return 1, 2 }\n\nfunc f() (a, b int) {\n\t{\n\t\tscope defer note(\"cleanup\")\n\t\treturn pair()\n\t}\n\treturn\n}\n"
	out := lowerSnippetSrc(t, "scopemultiret.lgo", src)
	if !strings.Contains(out, "a, b = pair()") {
		t.Errorf("ejected text lost the call parentheses:\n%s", out)
	}
}

func TestChainSumPairwise(t *testing.T) {
	scalarSliceTarget(t)
	src := "package main\n\nfunc f(xs []float64) float64 {\n\treturn xs.sum()\n}\n"
	out := lowerSnippetSrc(t, "sumsnip.lgo", src)
	if !strings.Contains(out, "/ 2") {
		t.Errorf("pairwise halving split missing:\n%s", out)
	}
	if n := strings.Count(out, "_sum("); n != 3 {
		t.Errorf("got %d helper calls, want 3 (top-level plus left and right halves)", n)
	}
	left := strings.Index(out, "_s[:")
	right := strings.Index(out, "_m:]")
	if left < 0 || right < 0 || left >= right {
		t.Errorf("left-half slice not before right-half slice:\n%s", out)
	}
	if !strings.Contains(out, ") + ") {
		t.Errorf("halves not joined by plus:\n%s", out)
	}
	if strings.Contains(out, "*") {
		t.Errorf("multiply operator in sum lowering (FMA surface):\n%s", out)
	}
	if strings.Contains(out, "FMA") {
		t.Errorf("FMA reference in sum lowering:\n%s", out)
	}
}

func TestChainFuseOneLoop(t *testing.T) {
	src := "package main\n\nfunc bump(x int) int { return 2 * x }\n\nfunc f(xs []int) []int {\n\treturn xs.filter(x => x > 2).map(x => bump(x)).take(5)\n}\n"
	out := lowerSnippetSrc(t, "fusesnip.lgo", src)
	if n := len(regexp.MustCompile(`\bfor\b`).FindAllString(out, -1)); n != 1 {
		t.Errorf("got %d for loops, want exactly 1 fused loop:\n%s", n, out)
	}
	if n := len(regexp.MustCompile(`_rcv := xs`).FindAllString(out, -1)); n != 1 {
		t.Errorf("got %d receiver bindings, want 1:\n%s", n, out)
	}
	if n := strings.Count(out, "xs"); n != 2 {
		t.Errorf("got %d xs mentions (param + one binding), want 2:\n%s", n, out)
	}
}

func TestChainDelimSpans(t *testing.T) {
	bodySrc := "package main\n\ntype Point struct{ X int }\n\nfunc f(xs []int) []Point {\n\treturn xs.map(x => Point{X: x})\n}\n"
	out := lowerSnippetSrc(t, "bodysnip.lgo", bodySrc)
	if !strings.Contains(out, "Point{X: x}") {
		t.Errorf("composite body lost its closing brace:\n%s", out)
	}
	argSrc := "package main\n\nfunc f(xs []int, ys []int) []int {\n\treturn xs.take(ys[0])\n}\n"
	out = lowerSnippetSrc(t, "argsnip.lgo", argSrc)
	if !strings.Contains(out, "ys[0]") {
		t.Errorf("index argument lost its closing bracket:\n%s", out)
	}
}

func TestInterpBindBeforeSizing(t *testing.T) {
	src := "package main\n\nimport \"fmt\"\n\nfunc f(name string, n int) {\n\tfmt.Println($\"User {name} has {n} items\")\n}\n"
	out := lowerSnippetSrc(t, "interpsnip.lgo", src)
	v0 := strings.Index(out, "_logos_v0 :=")
	v1 := strings.Index(out, "_logos_v1 :=")
	mk := strings.Index(out, "make([]byte, 0,")
	if v0 < 0 || v1 < 0 || mk < 0 {
		t.Fatalf("bind-before-size shape missing:\n%s", out)
	}
	if v0 >= v1 || v1 >= mk {
		t.Errorf("wrong order: v0 at %d, v1 at %d, make at %d (want v0 < v1 < make)", v0, v1, mk)
	}
	if n := strings.Count(out, "_logos_v0 :="); n != 1 {
		t.Errorf("operand 0 bound %d times, want once", n)
	}
	if n := strings.Count(out, "_logos_v1 :="); n != 1 {
		t.Errorf("operand 1 bound %d times, want once", n)
	}
	if !strings.Contains(out, "len(_logos_v0)") {
		t.Errorf("bound does not count the string operand:\n%s", out)
	}
}

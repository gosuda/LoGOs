// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"bytes"
	"strings"
	"testing"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

func scanSnippet(t *testing.T, imp *testImporter, src string) ([]NextEdit, []NextTask) {
	t.Helper()
	var perrs []error
	f, err := syntax.Parse(syntax.NewFileBase("next.go"), bytes.NewReader([]byte(src)), func(err error) { perrs = append(perrs, err) }, nil, 0)
	if err != nil {
		t.Fatal(perrs, err)
	}
	info := &types2.Info{
		StoreTypesInSyntax: true,
		Defs:               make(map[*syntax.Name]types2.Object),
		Uses:               make(map[*syntax.Name]types2.Object),
		Selections:         make(map[*syntax.SelectorExpr]*types2.Selection),
		Scopes:             make(map[syntax.Node]*types2.Scope),
	}
	var cerrs []error
	conf := types2.Config{
		Importer: imp,
		Error:    func(err error) { cerrs = append(cerrs, err) },
	}
	if _, err := conf.Check("p", []*syntax.File{f}, info); err != nil {
		t.Fatal(err)
	}
	if len(cerrs) > 0 {
		t.Fatal(cerrs)
	}
	data := []byte(src)
	reps := ScanNext(info, []*syntax.File{f}, map[*syntax.File][]byte{f: data}, map[*syntax.File]string{f: "next.go"})
	if len(reps) == 0 {
		return nil, nil
	}
	if len(reps) != 1 {
		t.Fatalf("got %d reports", len(reps))
	}
	return reps[0].Edits, reps[0].Tasks
}

func TestNextScan(t *testing.T) {
	imp := newTestImporter(t)
	call := `
package p

func f(s string) (string, error) {
	if s == "" {
		return "", errNew()
	}
	return s, nil
}

func errNew() error { return nil }
`
	cases := []struct {
		name  string
		body  string
		tasks int
		rule  string
		instr string
	}{
		{
			name:  "assign",
			body:  "func g(s string) (string, error) {\n\tv, err := f(s)\n\tif err != nil {\n\t\treturn \"\", err\n\t}\n\treturn v, nil\n}",
			tasks: 1,
			rule:  "err-return",
			instr: "keep explicit error propagation",
		},
		{
			name:  "assignEq",
			body:  "func g(s string) (string, error) {\n\tvar v string\n\tvar err error\n\tv, err = f(s)\n\tif err != nil {\n\t\treturn \"\", err\n\t}\n\treturn v, nil\n}",
			tasks: 1,
			rule:  "err-return",
			instr: "keep explicit error propagation",
		},
		{
			name:  "reused",
			body:  "func g(s string) (string, error) {\n\tv := \"\"\n\tv, err := f(s)\n\tif err != nil {\n\t\treturn \"\", err\n\t}\n\treturn v, nil\n}",
			tasks: 1,
			rule:  "err-return",
			instr: "keep explicit error propagation",
		},
		{
			name:  "single",
			body:  "func g() error {\n\terr := errNew()\n\tif err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}",
			tasks: 1,
			rule:  "err-return",
			instr: "keep explicit error propagation",
		},
		{
			name:  "ifInit",
			body:  "func g() error {\n\tif err := errNew(); err != nil {\n\t\treturn err\n\t}\n\treturn nil\n}",
			tasks: 1,
			rule:  "err-return",
			instr: "keep explicit error propagation",
		},
		{
			name:  "ifInitZeros",
			body:  "func g(s string) (string, error) {\n\tif err := errNew(); err != nil {\n\t\treturn \"\", err\n\t}\n\treturn s, nil\n}",
			tasks: 1,
			rule:  "err-return",
			instr: "keep explicit error propagation",
		},
		{
			name:  "usedLater",
			body:  "func g(s string) (string, error) {\n\tv, err := f(s)\n\tif err != nil {\n\t\treturn \"\", err\n\t}\n\treturn v, err\n}",
			tasks: 1,
			rule:  "err-return",
			instr: "read after the check",
		},
		{
			name:  "nonzero",
			body:  "func g(s string) (int, error) {\n\tv, err := f(s)\n\tif err != nil {\n\t\treturn -1, err\n\t}\n\treturn len(v), nil\n}",
			tasks: 1,
			rule:  "err-return",
			instr: "non-zero values",
		},
		{
			name:  "bodyMulti",
			body:  "func g(s string) (string, error) {\n\tv, err := f(s)\n\tif err != nil {\n\t\tprintln(s)\n\t\treturn \"\", err\n\t}\n\treturn v, nil\n}",
			tasks: 1,
			rule:  "err-return",
		},
		{
			name: "noErrorResult",
			body: "func g(s string) {\n\tv, err := f(s)\n\tif err != nil {\n\t\tprintln(err)\n\t\treturn\n\t}\n\tprintln(v)\n}",
		},
		{
			name:  "loop",
			body:  "func d(x int) int { return x * 2 }\nfunc g(xs []int) []int {\n\tvar ys []int\n\tfor i := 0; i < len(xs); i++ {\n\t\tys = append(ys, d(xs[i]))\n\t}\n\treturn ys\n}",
			tasks: 1,
			rule:  "loop-chain",
			instr: "keep the append loop unless",
		},
		{
			name:  "loopBreak",
			body:  "func g(xs []int, n int) []int {\n\tvar out []int\n\tfor i := 0; i < len(xs); i++ {\n\t\tif xs[i] > n {\n\t\t\tbreak\n\t\t}\n\t\tout = append(out, xs[i])\n\t}\n\treturn out\n}",
			tasks: 1,
			rule:  "loop-chain",
		},
		{
			name:  "loopSide",
			body:  "func g(xs []int) (int, []int) {\n\tvar out []int\n\tn := 0\n\tfor i := 0; i < len(xs); i++ {\n\t\tn++\n\t\tout = append(out, xs[i])\n\t}\n\treturn n, out\n}",
			tasks: 1,
			rule:  "loop-chain",
		},
		{
			name:  "loopGap",
			body:  "func d(x int) int { return x * 2 }\nfunc g(xs []int) ([]int, int) {\n\tvar ys []int\n\tsize := len(xs)\n\tfor i := 0; i < len(xs); i++ {\n\t\tys = append(ys, d(xs[i]))\n\t}\n\treturn ys, size\n}",
			tasks: 1,
			rule:  "loop-chain",
		},
		{
			name:  "loopContinue",
			body:  "func g(xs []int, n int) []int {\n\tvar out []int\n\tfor i := 0; i < len(xs); i++ {\n\t\tif xs[i] > n {\n\t\t\tout = append(out, xs[i])\n\t\t\tcontinue\n\t\t}\n\t}\n\treturn out\n}",
			tasks: 1,
			rule:  "loop-chain",
		},
		{
			name: "loopNoAppend",
			body: "func g(xs []int) int {\n\tn := 0\n\tfor i := 0; i < len(xs); i++ {\n\t\tn += xs[i]\n\t}\n\treturn n\n}",
		},
		{
			name:  "loopSeeded",
			body:  "func d(x int) int { return x * 2 }\nfunc g(xs []int) []int {\n\tys := []int{}\n\tys = append(ys, 0)\n\tfor i := 0; i < len(xs); i++ {\n\t\tys = append(ys, d(xs[i]))\n\t}\n\treturn ys\n}",
			tasks: 1,
			rule:  "loop-chain",
		},
		{
			name:  "loopIndex",
			body:  "func d2(x, i int) int { return x + i }\nfunc g(xs []int) []int {\n\tvar out []int\n\tfor i := 0; i < len(xs); i++ {\n\t\tout = append(out, d2(xs[i], i))\n\t}\n\treturn out\n}",
			tasks: 1,
			rule:  "loop-chain",
		},
		{
			name:  "loopParamCollision",
			body:  "func g(xs []int, x, v, e, elem int) []int {\n\tvar ys []int\n\tfor i := 0; i < len(xs); i++ {\n\t\tys = append(ys, xs[i]+x+v+e+elem)\n\t}\n\treturn ys\n}",
			tasks: 1,
			rule:  "loop-chain",
			instr: "keep the append loop unless",
		},
		{
			name:  "rangeIdx",
			body:  "func d(x int) int { return x * 2 }\nfunc g(xs []int) []int {\n\tvar ys []int\n\tfor i := range xs {\n\t\tys = append(ys, d(xs[i]))\n\t}\n\treturn ys\n}",
			tasks: 1,
			rule:  "loop-chain",
			instr: "keep the append loop unless",
		},
		{
			name:  "rangeVal",
			body:  "func d(x int) int { return x * 2 }\nfunc g(xs []int) []int {\n\tvar ys []int\n\tfor _, x := range xs {\n\t\tys = append(ys, d(x))\n\t}\n\treturn ys\n}",
			tasks: 1,
			rule:  "loop-chain",
			instr: "keep the append loop unless",
		},
		{
			name:  "rangeBreak",
			body:  "func g(xs []int, n int) []int {\n\tvar out []int\n\tfor i := range xs {\n\t\tif xs[i] > n {\n\t\t\tbreak\n\t\t}\n\t\tout = append(out, xs[i])\n\t}\n\treturn out\n}",
			tasks: 1,
			rule:  "loop-chain",
		},
		{
			name:  "rangeSide",
			body:  "func g(xs []int) (int, []int) {\n\tvar out []int\n\tn := 0\n\tfor _, x := range xs {\n\t\tn++\n\t\tout = append(out, x)\n\t}\n\treturn n, out\n}",
			tasks: 1,
			rule:  "loop-chain",
		},
		{
			name:  "rangeIndex",
			body:  "func d2(x, i int) int { return x + i }\nfunc g(xs []int) []int {\n\tvar out []int\n\tfor i := range xs {\n\t\tout = append(out, d2(xs[i], i))\n\t}\n\treturn out\n}",
			tasks: 1,
			rule:  "loop-chain",
		},
		{
			name:  "rangeTwoSlices",
			body:  "func add(a, b int) int { return a + b }\nfunc g(xs, zs []int) []int {\n\tvar out []int\n\tfor i := range xs {\n\t\tout = append(out, add(xs[i], zs[i]))\n\t}\n\treturn out\n}",
			tasks: 1,
			rule:  "loop-chain",
		},
		{
			name:  "rangeInt",
			body:  "func twice(i int) int { return 2 * i }\nfunc g(n int) []int {\n\tvar out []int\n\tfor i := range n {\n\t\tout = append(out, twice(i))\n\t}\n\treturn out\n}",
			tasks: 1,
			rule:  "loop-chain",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			edits, tasks := scanSnippet(t, imp, call+tc.body+"\n")
			if len(edits) != 0 {
				t.Fatalf("got edits %v, want none: ? and .map are review tasks, never edits", edits)
			}
			if len(tasks) != tc.tasks {
				t.Fatalf("got %d tasks %v", len(tasks), tasks)
			}
			if tc.rule != "" {
				if len(tasks) == 0 {
					t.Fatalf("want a %s task, got none", tc.rule)
				}
				if tasks[0].Rule != tc.rule {
					t.Fatalf("got rule %q want %q", tasks[0].Rule, tc.rule)
				}
				if tc.instr != "" && !strings.Contains(tasks[0].Instruction, tc.instr) {
					t.Fatalf("got instruction %q want it to contain %q", tasks[0].Instruction, tc.instr)
				}
			}
		})
	}
}

func TestNextSprintf(t *testing.T) {
	imp := newTestImporter(t)
	cases := []struct {
		name string
		body string
		edit string
	}{
		{
			name: "basic",
			body: "func g(name string, n int) string {\n\treturn fmt.Sprintf(\"hello %s %d\", name, n)\n}\n\nfunc h() {\n\tfmt.Println(\"x\")\n}",
			edit: `$"hello {name} {n}"`,
		},
		{
			name: "boolVerb",
			body: "func g(ok bool) string {\n\treturn fmt.Sprintf(\"ok=%v\", ok)\n}\n\nfunc h() {\n\tfmt.Println(\"x\")\n}",
			edit: `$"ok={ok}"`,
		},
		{
			name: "hex",
			body: "func g(n int) string {\n\treturn fmt.Sprintf(\"hex %x\", n)\n}",
		},
		{
			name: "floatVerb",
			body: "func g(f float64) string {\n\treturn fmt.Sprintf(\"v=%v\", f)\n}",
		},
		{
			name: "bytesVerb",
			body: "func g(b []byte) string {\n\treturn fmt.Sprintf(\"v=%v\", b)\n}",
		},
		{
			name: "bytesS",
			body: "func g(b []byte) string {\n\treturn fmt.Sprintf(\"v=%s\", b)\n}",
		},
		{
			name: "compositeIndex",
			body: "func g() string {\n\treturn fmt.Sprintf(\"%d\", []int{1}[0])\n}\n\nfunc h() {\n\tfmt.Println(\"x\")\n}",
			edit: `$"{[]int{1}[0]}"`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "package p\n\nimport \"fmt\"\n\n" + tc.body + "\n"
			edits, _ := scanSnippet(t, imp, src)
			if tc.edit == "" {
				if len(edits) != 0 {
					t.Fatalf("got edits %v", edits)
				}
				return
			}
			if len(edits) != 1 {
				t.Fatalf("got %d edits %v", len(edits), edits)
			}
			if edits[0].Replacement != tc.edit {
				t.Fatalf("got %q want %q", edits[0].Replacement, tc.edit)
			}
		})
	}
}

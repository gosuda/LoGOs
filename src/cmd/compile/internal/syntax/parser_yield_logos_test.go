// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package syntax

import "testing"

func TestLogosYieldStartsInGenerator(t *testing.T) {
	starts := []string{
		"yield 1",
		"yield x",
		"yield func() {}",
		"yield (1)",
		"yield []int{1}[0]",
		"yield [1]int{1}[0]",
		"yield -x",
		"yield +x",
		"yield *p",
		"yield &v",
		"yield ^x",
		"yield !ok",
		"yield <-ch",
		"yield const []byte(s)",
		"yield switch x { default => 1 }",
		"yield map[int]int{}[0]",
		"yield struct{}{}",
		"yield x, y",
	}
	for _, stmt := range starts {
		t.Run(stmt, func(t *testing.T) {
			src := "package p\nimport \"iter\"\nfunc f() iter.Seq[int] {\n" + stmt + "\n}\n"
			if got := countNodes[*YieldStmt](parseLogos(t, src)); got != 1 {
				t.Fatalf("%s: found %d yield statements, want 1", stmt, got)
			}
		})
	}
}

func TestLogosYieldIdentifierFormsInGenerator(t *testing.T) {
	forms := []string{
		"yield := 1",
		"yield = 1",
		"yield.f()",
		"yield++",
		"yield--",
		"yield += 1",
		"yield, x = 1, 2",
		"yield, x := 1, 2",
	}
	for _, stmt := range forms {
		t.Run(stmt, func(t *testing.T) {
			src := "package p\nimport \"iter\"\nfunc f() iter.Seq[int] {\n" + stmt + "\n}\n"
			if got := countNodes[*YieldStmt](parseLogos(t, src)); got != 0 {
				t.Fatalf("%s: found %d yield statements, want 0", stmt, got)
			}
		})
	}
}

func TestLogosYieldOutsideGeneratorKeepsCalls(t *testing.T) {
	tests := []struct {
		name string
		src  string
		want int
	}{
		{"no result", "package p\nfunc f(yield func(int) bool) {\nyield (1)\nyield(2)\n}\n", 0},
		{"method parameter", "package p\ntype T struct{}\nfunc (T) f(yield func(int) bool) bool {\nyield (1)\nreturn yield(2)\n}\n", 0},
		{"plain result", "package p\nfunc f(yield func(int) bool) bool {\nreturn yield (1)\n}\n", 0},
		{"unqualified Seq", "package p\nfunc f(yield func(int) bool) Seq[int] {\nyield (1)\n}\n", 0},
		{"other package Seq", "package p\nimport \"other\"\nfunc f(yield func(int) bool) other.Seq[int] {\nyield (1)\n}\n", 0},
		{"two results", "package p\nimport \"iter\"\nfunc f(yield func(int) bool) (iter.Seq[int], error) {\nyield (1)\n}\n", 0},
		{"value operand stays a statement", "package p\nfunc f() {\nyield 1\n}\n", 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := countNodes[*YieldStmt](parseLogos(t, test.src)); got != test.want {
				t.Fatalf("found %d yield statements, want %d", got, test.want)
			}
		})
	}
}

func TestLogosYieldFollowsTheFunctionLiteral(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{"literal with a yield parameter", "yield 1\nfunc(yield func(int) bool) {\nyield (2)\nyield(3)\n}(nil)", 1},
		{"literal that is itself a generator", "func() iter.Seq[int] {\nyield (4)\n}()", 1},
		{"statement after the literal", "func(yield func(int) bool) {\nyield(1)\n}(nil)\nyield (2)", 1},
		{"blocks stay in the generator", "for {\nif true {\nyield (1)\n}\nswitch {\ncase true:\nyield -1\n}\n}", 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			src := "package p\nimport \"iter\"\nfunc f() iter.Seq[int] {\n" + test.body + "\n}\n"
			if got := countNodes[*YieldStmt](parseLogos(t, src)); got != test.want {
				t.Fatalf("found %d yield statements, want %d", got, test.want)
			}
		})
	}
}

func TestLogosYieldGeneratorThroughImportName(t *testing.T) {
	tests := []struct {
		name   string
		header string
		result string
		want   int
	}{
		{"renamed import", "import it \"iter\"", "it.Seq[int]", 1},
		{"renamed import for Seq2", "import it \"iter\"", "it.Seq2[int, int]", 1},
		{"dot import", "import . \"iter\"", "Seq[int]", 1},
		{"grouped import", "import (\n\"fmt\"\n\"iter\"\n)", "iter.Seq[int]", 1},
		{"name of another import", "import (\n\"iter\"\nit \"other\"\n)", "it.Seq[int]", 0},
		{"no import", "", "iter.Seq[int]", 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			src := "package p\n" + test.header + "\nfunc f(x int) " + test.result + " {\nyield -x\n}\n"
			got := countNodes[*YieldStmt](parseLogos(t, src))
			if got != test.want {
				t.Fatalf("found %d yield statements, want %d", got, test.want)
			}
		})
	}
}

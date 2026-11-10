// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
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

func TestCfCopiesMemoizedPerTargs(t *testing.T) {
	src := "package main\n\nimport \"comptime\"\n\nfunc F[T any]() string {\n\tconst if comptime.IsStruct[T]() {\n\t\treturn \"struct\"\n\t}\n\treturn \"other\"\n}\n\nfunc main() {\n\t_ = F[int]()\n\t_ = F[int]()\n\t_ = F[string]()\n}\n"
	out := lowerSnippetSrc(t, "cfmemo.lgo", src)
	if !strings.Contains(out, "F_logosct0") {
		t.Fatalf("missing first copy F_logosct0:\n%s", out)
	}
	if !strings.Contains(out, "F_logosct1") {
		t.Fatalf("missing second copy F_logosct1 (want two copies, not one):\n%s", out)
	}
	if strings.Contains(out, "F_logosct2") {
		t.Fatalf("extra copy F_logosct2 (want two copies, not three):\n%s", out)
	}
	if n := strings.Count(out, "func F_logosct"); n != 2 {
		t.Fatalf("func F_logosct defs = %d, want 2 (one per type-argument list):\n%s", n, out)
	}
}

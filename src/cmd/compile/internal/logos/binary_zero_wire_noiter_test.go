// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestBinaryZeroWireNoIteration(t *testing.T) {
	src := "package main\n\ntype ZPad struct {\n\tPad int64 `json:\"-\"`\n}\n\ntype ZHold struct {\n\tItems []ZPad `json:\"items\"`\n}\n"
	out := lowerSnippetSrc(t, "zeronoiter.lgo", src)
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "zeronoiter.go", out, 0)
	if err != nil {
		t.Fatal(err)
	}
	var found int
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || !strings.HasPrefix(fd.Name.Name, "_logos_bin_") {
			continue
		}
		found++
		loops := 0
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			switch n.(type) {
			case *ast.ForStmt, *ast.RangeStmt:
				loops++
			}
			return true
		})
		if loops != 0 {
			t.Fatalf("%s holds %d loops, want none (zero-wire constant time)", fd.Name.Name, loops)
		}
	}
	if found == 0 {
		t.Fatal("no _logos_bin_ funcs in lowered zero-wire output")
	}
}

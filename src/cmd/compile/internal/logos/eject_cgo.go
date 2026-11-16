// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"

	"cmd/compile/internal/syntax"
)

// ejectCgoImportLines marks the complete C import declaration and its
// comments. A //line comment inserted anywhere in that range can become
// part of the C preamble, or replace the comment group cgo selects.
// Source positions resume at the next ordinary declaration.
func ejectCgoImportLines(file *syntax.File, data []byte) (map[int]bool, error) {
	hasC := false
	for _, decl := range file.DeclList {
		if imp, ok := decl.(*syntax.ImportDecl); ok && imp.Path.Value == `"C"` {
			hasC = true
			break
		}
	}
	if !hasC {
		return nil, nil
	}
	fs := token.NewFileSet()
	parsed, err := parser.ParseFile(fs, file.Pos().FileBase().Filename(), data, parser.ImportsOnly|parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse cgo import declarations: %w", err)
	}
	lines := make(map[int]bool)
	for _, decl := range parsed.Decls {
		gen := decl.(*ast.GenDecl) // ImportsOnly returns only imports.
		for _, spec := range gen.Specs {
			imp := spec.(*ast.ImportSpec)
			if imp.Path.Value != `"C"` {
				continue
			}
			start := gen.Pos()
			if gen.Doc != nil {
				start = gen.Doc.Pos()
			}
			if imp.Doc != nil && imp.Doc.Pos() < start {
				start = imp.Doc.Pos()
			}
			first := fs.PositionFor(start, false).Line
			last := fs.PositionFor(gen.End(), false).Line
			for line := first; line <= last; line++ {
				lines[line] = true
			}
		}
	}
	return lines, nil
}

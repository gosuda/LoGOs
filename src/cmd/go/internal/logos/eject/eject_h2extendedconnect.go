// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"go/ast"
	"go/token"
	"strconv"
	"strings"
)

// h2extendedConnectMethod is the fork-only per-server opt-in for HTTP/2
// extended CONNECT. Stock Go has no per-server surface for the setting:
// net/http Protocols selects protocols only, and the only stock knob is
// the process-global GODEBUG=http2xconnect=1, which no go.mod //go:debug
// line may set (the key is unregistered, so the build rejects it).
const h2extendedConnectMethod = "EnableHTTP2ExtendedConnect"

// h2ExtendedConnectEdits rewrites direct EnableHTTP2ExtendedConnect calls to
// a self-contained stock form: an immediately-invoked check that requires
// GODEBUG=http2xconnect=1 and panics otherwise. It fires only in files
// importing net/http; the method name is fork-specific, so a user type with
// the same zero-argument method name in such a file would also be rewritten
// (no such type exists in the wild). A call whose receiver needs another
// rewrite (a compiler-temporary _logos_site name) is left alone, so this
// edit never overlaps another rewrite. The rewrite keeps the receiver text
// verbatim, so go and defer statements wrapping the call keep working, and
// the receiver still counts as used. Needed os and strings imports are added
// when missing; the gofmt pass normalizes layout.
func h2ExtendedConnectEdits(fset *token.FileSet, data []byte, f *ast.File) []textEdit {
	hasNetHTTP := false
	haveOS := false
	haveStrings := false
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		switch path {
		case "net/http":
			hasNetHTTP = true
		case "os":
			haveOS = true
		case "strings":
			haveStrings = true
		}
	}
	if !hasNetHTTP {
		return nil
	}
	var edits []textEdit
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 0 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != h2extendedConnectMethod || h2HasSiteIdent(sel.X) {
			return true
		}
		recvStart := fset.Position(sel.X.Pos()).Offset
		recvEnd := fset.Position(sel.X.End()).Offset
		start := fset.Position(call.Pos()).Offset
		end := fset.Position(call.End()).Offset
		if recvStart < 0 || recvEnd < recvStart || recvEnd > len(data) ||
			start < 0 || end < start || end > len(data) {
			return true
		}
		receiver := string(data[recvStart:recvEnd])
		repl := "func() {\n_ = " + receiver + "\nif !strings.Contains(os.Getenv(\"GODEBUG\"), \"http2xconnect=1\") {\npanic(\"net/http: extended CONNECT needs GODEBUG=http2xconnect=1 in the ejected process environment\")\n}\n}()"
		edits = append(edits, textEdit{start, end, repl})
		return true
	})
	if len(edits) == 0 {
		return nil
	}
	var missing []string
	if !haveOS {
		missing = append(missing, "\"os\"")
	}
	if !haveStrings {
		missing = append(missing, "\"strings\"")
	}
	if len(missing) > 0 {
		edits = append(edits, h2ImportEdit(fset, f, missing))
	}
	return edits
}

// h2HasSiteIdent reports whether the receiver subtree holds a
// compiler-temporary _logos_site name that another rewrite renames.
func h2HasSiteIdent(n ast.Node) bool {
	found := false
	ast.Inspect(n, func(x ast.Node) bool {
		if id, ok := x.(*ast.Ident); ok && isSiteIdent(id.Name) {
			found = true
			return false
		}
		return true
	})
	return found
}

// h2ImportEdit appends a fresh import declaration holding the missing paths
// after the last import declaration. A fresh declaration cannot collide with
// the existing import block, and the gofmt pass normalizes its layout.
func h2ImportEdit(fset *token.FileSet, f *ast.File, missing []string) textEdit {
	end := 0
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.IMPORT {
			continue
		}
		if off := fset.Position(gen.End()).Offset; off > end {
			end = off
		}
	}
	var b strings.Builder
	b.WriteString("\nimport (\n")
	for _, m := range missing {
		b.WriteString(m + "\n")
	}
	b.WriteString(")\n")
	return textEdit{end, end, b.String()}
}

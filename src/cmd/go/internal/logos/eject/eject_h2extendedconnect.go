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
// GODEBUG=http2xconnect=1 and panics otherwise, naming the server and the
// setting. It fires only in files importing net/http; the method name is
// fork-specific, so a user type with the same zero-argument method name in
// such a file would also be rewritten (no such type exists in the wild).
// A call whose receiver needs another rewrite (a compiler-temporary
// _logos_site name) is left alone. The rewrite keeps the receiver text
// verbatim, so go and defer statements wrapping the call keep working, and
// the receiver still counts as used. Needed os and strings imports are added
// when missing; the gofmt pass normalizes layout.
//
// Widening: the fork enables extended CONNECT per server, while stock Go
// offers only the process-global GODEBUG=http2xconnect=1, so after eject
// the per-server intent becomes a process-global requirement. Every
// rewritten call in the process checks the same setting, and a process
// without it fails fast at the first call instead of serving without the
// feature.
func h2ExtendedConnectEdits(fset *token.FileSet, data []byte, f *ast.File) []textEdit {
	hasNetHTTP := false
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if path == "net/http" {
			hasNetHTTP = true
			break
		}
	}
	if !hasNetHTTP {
		return nil
	}
	// The text rewrite cannot resolve selector identity; a local
	// interface or method with the same name would be rewritten
	// incorrectly. Skip files that define the name so only the
	// type-checked stdlib exemption (which matches by type) is trusted.
	if h2DefinesExtendedConnect(f) {
		return nil
	}
	used := h2UsedNames(f)
	osName := h2FreshName(used, "logosH2Os")
	used[osName] = true
	stringsName := h2FreshName(used, "logosH2Strings")
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
		msg := "net/http: extended CONNECT on server " + strconv.Quote(receiver) + " needs GODEBUG=http2xconnect=1 in the ejected process environment"
		// Evaluate the receiver as an argument so defer/go preserve
		// registration-time side effects; only the GODEBUG check runs later.
		repl := "func(_ any) {\nif !" + stringsName + ".Contains(" + osName + ".Getenv(\"GODEBUG\"), \"http2xconnect=1\") {\npanic(" + strconv.Quote(msg) + ")\n}\n}(" + receiver + ")"
		edits = append(edits, textEdit{start, end, repl})
		return true
	})
	if len(edits) == 0 {
		return nil
	}
	missing := []string{osName + " \"os\"", stringsName + " \"strings\""}
	edits = append(edits, h2ImportEdit(fset, f, missing))
	return edits
}

// h2UsedNames collects local import names and all identifiers in f so
// generated bindings cannot collide with an alias, a local variable, or
// an existing import, generally for any file shape.
func h2UsedNames(f *ast.File) map[string]bool {
	used := make(map[string]bool)
	for _, imp := range f.Imports {
		if imp.Name != nil {
			used[imp.Name.Name] = true
			continue
		}
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if i := strings.LastIndex(path, "/"); i >= 0 {
			used[path[i+1:]] = true
		} else {
			used[path] = true
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			used[id.Name] = true
		}
		return true
	})
	return used
}

// h2FreshName returns base or the first base+N unused in used.
func h2FreshName(used map[string]bool, base string) string {
	if !used[base] {
		return base
	}
	for i := 2; ; i++ {
		cand := base + strconv.Itoa(i)
		if !used[cand] {
			return cand
		}
	}
}

// h2DefinesExtendedConnect reports whether f declares the method name
// itself (interface member or func/method). Such files are skipped so
// a same-named user interface call is never rewritten by spelling.
func h2DefinesExtendedConnect(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		switch x := n.(type) {
		case *ast.Field:
			for _, name := range x.Names {
				if name.Name == h2extendedConnectMethod {
					found = true
					return false
				}
			}
		case *ast.FuncDecl:
			if x.Name.Name == h2extendedConnectMethod {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// h2OverlapOffset returns the start offset of the first h2 edit that
// overlaps an edit outside the h2 set, if any. The import-insertion edit
// is zero-width, so it overlaps only when strictly inside another edit
// (for example a whole-line comptime removal spanning the last import
// declaration); boundary-touching inserts apply cleanly in offset order.
// Call spans avoid the other rewrites by construction (see h2HasSiteIdent
// and the import-only span of ejectedImportEdits), but they are checked
// the same way so a future overlap fails safe.
func h2OverlapOffset(others, h2edits []textEdit) (int, bool) {
	for _, h := range h2edits {
		if h.start == h.end {
			for _, o := range others {
				if o.start < h.start && h.start < o.end {
					return h.start, true
				}
			}
			continue
		}
		for _, o := range others {
			if h.start < o.end && o.start < h.end {
				return h.start, true
			}
		}
	}

	return 0, false
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

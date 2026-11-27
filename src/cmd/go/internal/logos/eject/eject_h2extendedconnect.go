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

// h2SupportImport is the local name and path the rewrite uses for the
// ejected support module. The support package owns the os, strings and
// GODEBUG parsing privately, so rewritten call sites stay correct when
// the file aliases os or strings or declares those names itself.
const h2SupportImport = `ejectedhttp "github.com/gosuda/loGos/rt/logos/net/http"`

// h2ExtendedConnectEdits rewrites direct EnableHTTP2ExtendedConnect calls to
// a call into the ejected support module, which fails fast unless the stock
// process-global GODEBUG=http2xconnect=1 was in effect at process startup.
// It fires only in files importing net/http. A method value with the same
// name is rewritten only when it is marked lowering output: generic
// lowering binds every callee capture through a reserved _logos_f
// temporary (rewrite.go nextFun), which user code cannot declare
// ([correctness::reserved_identifier]), so only a marked assignment is
// trusted and a same-named user method value is left alone.
// A call whose receiver needs another rewrite (a compiler-temporary
// _logos_site name) is left alone. The receiver passes as a call argument,
// so go and defer statements wrapping the call evaluate it in the calling
// goroutine at the call site, exactly like the native method call. When the
// file already imports the support package and no other file binding
// shadows its name, the rewrite uses that binding; otherwise it adds the
// import under a fresh file-wide alias. The gofmt pass
// normalizes layout.
//
// Widening: the fork enables extended CONNECT per server, while stock Go
// offers only the process-global GODEBUG=http2xconnect=1, so after eject
// the per-server intent becomes a process-global requirement. Every
// rewritten call in the process checks the same startup setting, and a
// process without it fails fast at the first call instead of serving
// without the feature. Last-wins GODEBUG parsing lives in the shared
// support helper, so the emitted code carries no parsing of its own.
func h2ExtendedConnectEdits(fset *token.FileSet, data []byte, f *ast.File) ([]textEdit, bool) {
	supportPath := rtModulePath + "/logos/net/http"
	hasNetHTTP := false
	supportLocal := ""
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		switch path {
		case "net/http":
			hasNetHTTP = true
		case "logos/net/http", supportPath:
			// A blank or dot import binds no usable call prefix; fall
			// through to the fresh ejectedhttp import below.
			if imp.Name == nil {
				supportLocal = "http"
			} else if imp.Name.Name != "_" && imp.Name.Name != "." {
				supportLocal = imp.Name.Name
			}
		}
	}
	if !hasNetHTTP {
		return nil, false
	}
	// The text rewrite cannot resolve selector identity; a local
	// interface or method with the same name would be rewritten
	// incorrectly. Skip files that define the name so only the
	// type-checked stdlib exemption (which matches by type) is trusted.
	if h2DefinesExtendedConnect(f) {
		return nil, false
	}
	local, needImport := h2SupportLocal(f, supportPath, supportLocal)
	var edits []textEdit
	ast.Inspect(f, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
			// Group lowering binds the opt-in as a method value before
			// spawning its child. Only a marked assignment is trusted:
			// the left-hand side must be a lowering-generated callee
			// temporary, so a same-named user method value in another
			// file keeps its own binding and is never rewritten into
			// the guard. Rebind the marked capture through the
			// deferred guard so the receiver still evaluates exactly
			// once, eagerly, and the spawned child runs the same check.
			lhsIdent, ok := as.Lhs[0].(*ast.Ident)
			if !ok || !h2IsLoweredCalleeTemp(lhsIdent.Name) {
				return true
			}
			sel, ok := as.Rhs[0].(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != h2extendedConnectMethod || h2HasSiteIdent(sel.X) {
				return true
			}
			recvStart := fset.Position(sel.X.Pos()).Offset
			recvEnd := fset.Position(sel.X.End()).Offset
			lhsStart := fset.Position(as.Lhs[0].Pos()).Offset
			lhsEnd := fset.Position(as.Lhs[0].End()).Offset
			if recvStart < 0 || recvEnd < recvStart || recvEnd > len(data) ||
				lhsStart < 0 || lhsEnd < lhsStart || lhsEnd > len(data) {
				return true
			}
			receiver := string(data[recvStart:recvEnd])
			lhs := string(data[lhsStart:lhsEnd])
			repl := lhs + " " + as.Tok.String() + " " + local + ".RequireExtendedConnectFunc(" + strconv.Quote(receiver) + ", " + receiver + ")"
			edits = append(edits, textEdit{lhsStart, fset.Position(as.End()).Offset, repl})
			return true
		}
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
		repl := local + ".RequireExtendedConnect(" + strconv.Quote(receiver) + ", " + receiver + ")"
		edits = append(edits, textEdit{start, end, repl})
		return true
	})
	if len(edits) == 0 {
		return nil, false
	}
	if needImport {
		edits = append(edits, h2ImportEdit(fset, f, []string{h2SupportImportFor(local)}))
	}
	return edits, true
}

// h2SupportImportFor renders the support import with the chosen local
// name. The default alias keeps the historical spelling; a fresh alias
// from h2SupportLocal carries the same path.
func h2SupportImportFor(local string) string {
	if local == "ejectedhttp" {
		return h2SupportImport
	}
	return local + " " + strconv.Quote(rtModulePath+"/logos/net/http")
}

// h2SupportLocal picks the qualifier for generated support calls. An
// existing support import binding is reused only where no other file
// binding shadows it: a local, parameter, or same-file declaration with
// the same name would capture the generated call. Otherwise a binding
// with a fresh file-wide name is used. The second result reports whether
// the import still needs adding.
func h2SupportLocal(f *ast.File, supportPath, supportLocal string) (string, bool) {
	bound := map[string]bool{}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		if p == "logos/net/http" || p == supportPath {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name != "_" && imp.Name.Name != "." {
				bound[imp.Name.Name] = true
			}
		} else if i := strings.LastIndex(p, "/"); i >= 0 {
			bound[p[i+1:]] = true
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if x.Recv == nil {
				bound[x.Name.Name] = true
			}
		case *ast.TypeSpec:
			bound[x.Name.Name] = true
		case *ast.ValueSpec:
			for _, name := range x.Names {
				bound[name.Name] = true
			}
		case *ast.Field:
			for _, name := range x.Names {
				bound[name.Name] = true
			}
		case *ast.AssignStmt:
			if x.Tok == token.DEFINE {
				for _, lhs := range x.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						bound[id.Name] = true
					}
				}
			}
		case *ast.RangeStmt:
			if x.Tok == token.DEFINE {
				for _, e := range []ast.Expr{x.Key, x.Value} {
					if id, ok := e.(*ast.Ident); ok {
						bound[id.Name] = true
					}
				}
			}
		}
		return true
	})
	if supportLocal != "" && !bound[supportLocal] {
		return supportLocal, false
	}
	local := "ejectedhttp"
	for i := 2; bound[local]; i++ {
		local = "ejectedhttp" + strconv.Itoa(i)
	}
	return local, true
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

// h2IsLoweredCalleeTemp reports whether name is a lowering-generated callee
// temporary (_logos_f with a numeric suffix, from rewrite.go nextFun).
// Generic lowering binds every group and defer callee capture through one
// of these reserved temporaries, which user code cannot declare, so the
// name marks exactly the assignments lowering created. The eject method
// value rewrite trusts only marked assignments.
func h2IsLoweredCalleeTemp(name string) bool {
	rest, ok := strings.CutPrefix(name, "_logos_f")
	return ok && rest != "" && strings.Trim(rest, "0123456789") == ""
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

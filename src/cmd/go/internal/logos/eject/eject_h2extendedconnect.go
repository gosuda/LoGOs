// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"go/ast"
	"go/token"
	"slices"
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
// a call into the ejected support module, which requires
// GODEBUG=http2xconnect=1 and panics naming the server otherwise. The
// receiver passes as a call argument, so go and defer statements wrapping
// the call evaluate it in the calling goroutine at the call site, exactly
// like the native method call. It fires only in files importing net/http;
// the method name is fork-specific, so a user type with the same
// zero-argument method name in such a file would also be rewritten (no such
// type exists in the wild). A call whose receiver needs another rewrite (a
// compiler-temporary _logos_site name) is left alone. When the file already
// imports the support package, the rewrite uses that binding; otherwise the
// support import is added. The gofmt pass normalizes layout.
//
// A saved value of the public logos/net/http helper is not a capture: its
// receiver names the support package itself, so the rewrite leaves it
// alone. Rewriting it would pass a package as the server and change the
// signature from func(*Server) error to func(). The logos/net/http import
// still remaps to the ejected support copy, so the saved value keeps
// working.
//
// A capture whose receiver holds another opt-in call composes the inner
// guard into the capture's replacement, so the receiver still evaluates
// once and both guards run. Emitting both edits would overlap and stop the
// export.
//
// Widening: the fork enables extended CONNECT per server, while stock Go
// offers only the process-global GODEBUG=http2xconnect=1, so after eject
// the per-server intent becomes a process-global requirement. Every
// rewritten call in the process checks the same startup setting, and a
// process without it fails fast at the first call instead of serving
// without the feature.
func h2ExtendedConnectEdits(fset *token.FileSet, data []byte, f *ast.File) []textEdit {
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
		case "logos/net/http":
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
		return nil
	}
	// The text rewrite cannot resolve selector identity; a local
	// interface or method with the same name would be rewritten
	// incorrectly. Skip files that define the name so only the
	// type-checked stdlib exemption (which matches by type) is trusted.
	if h2DefinesExtendedConnect(f) {
		return nil
	}
	local := supportLocal
	if local == "" {
		local = "ejectedhttp"
	}
	var assigns []h2assignSite
	var calls []textEdit
	ast.Inspect(f, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
			// Group lowering binds the opt-in as a method value before
			// spawning its child. The verifier exempts the source call
			// by type, so a file that reaches the rewrite can only hold
			// this shape as lowering output, except for a saved value
			// of the public logos/net/http helper (handled below): a
			// same-named user method value never passes verification.
			// Rebind it through the deferred guard so the receiver
			// still evaluates exactly once, eagerly, and the spawned
			// child runs the same check.
			sel, ok := as.Rhs[0].(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != h2extendedConnectMethod || h2HasSiteIdent(sel.X) {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); ok && supportLocal != "" && id.Name == supportLocal {
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
			assigns = append(assigns, h2assignSite{
				start: lhsStart, end: fset.Position(as.End()).Offset,
				recvStart: recvStart, recvEnd: recvEnd,
				lhs: string(data[lhsStart:lhsEnd]), tok: as.Tok.String(),
			})
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
		calls = append(calls, textEdit{start, end, repl})
		return true
	})
	// A capture whose receiver holds another opt-in call takes the inner
	// guard into its own replacement: the composed receiver still
	// evaluates once, eagerly, and both guards run. The name argument
	// keeps the receiver as written; only the value carries the composed
	// form. An inner overlap that cannot be composed (which no lowering
	// shape produces) leaves the capture alone while the inner guards
	// still apply, so the export names the fork-only value instead of
	// stopping.
	used := make([]bool, len(calls))
	var edits []textEdit
	for _, a := range assigns {
		var inner []int
		for i := range calls {
			if !used[i] && calls[i].start >= a.recvStart && calls[i].end <= a.recvEnd {
				inner = append(inner, i)
			}
		}
		slices.SortFunc(inner, func(x, y int) int { return calls[x].start - calls[y].start })
		composable := true
		for j := 1; j < len(inner); j++ {
			if calls[inner[j]].start < calls[inner[j-1]].end {
				composable = false
				break
			}
		}
		if !composable {
			continue
		}
		name := string(data[a.recvStart:a.recvEnd])
		receiver := name
		if len(inner) > 0 {
			var b strings.Builder
			prev := a.recvStart
			for _, i := range inner {
				b.WriteString(string(data[prev:calls[i].start]))
				b.WriteString(calls[i].text)
				prev = calls[i].end
				used[i] = true
			}
			b.WriteString(string(data[prev:a.recvEnd]))
			receiver = b.String()
		}
		repl := a.lhs + " " + a.tok + " " + local + ".RequireExtendedConnectFunc(" + strconv.Quote(name) + ", " + receiver + ")"
		edits = append(edits, textEdit{a.start, a.end, repl})
	}
	for i, c := range calls {
		if !used[i] {
			edits = append(edits, c)
		}
	}
	if len(edits) == 0 {
		return nil
	}
	if supportLocal == "" {
		edits = append(edits, h2ImportEdit(fset, f, []string{h2SupportImport}))
	}
	return edits
}

// h2assignSite is a lowered method-value capture of the opt-in:
// _fN := <receiver>.EnableHTTP2ExtendedConnect.
type h2assignSite struct {
	start, end         int
	recvStart, recvEnd int
	lhs, tok           string
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

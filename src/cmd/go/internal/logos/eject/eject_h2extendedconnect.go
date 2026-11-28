// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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
// A saved value of the public logos/net/http helper is not a capture,
// and a lowered capture of the helper keeps its binding: the helper
// takes the server as an argument, so the rewrite rebinds only captures
// used with no arguments, which must be the zero-argument server method.
// Arity, not the receiver spelling, decides, so a server capture through
// a local that shadows the support import alias still rewrites. The
// logos/net/http import still remaps to the ejected support copy, so the
// saved value keeps working.
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
// without the feature. Last-wins GODEBUG parsing lives in the shared
// support helper, so the emitted code carries no parsing of its own.
//
// extraUsed holds package-level names from sibling files in the ejected
// package; the chosen qualifier avoids those too, because no identifier
// may be declared in both the file and package block. A nil map keeps
// the file-local decision for generated sources with no package context.
func h2ExtendedConnectEdits(fset *token.FileSet, data []byte, f *ast.File, extraUsed map[string]bool) ([]textEdit, bool) {
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
	local, needImport := h2SupportLocal(f, supportPath, supportLocal, extraUsed)
	// Uses are attributed by lexical scope: lowering restarts its
	// callee-temporary numbering in every function body, literal and
	// chain frame, so one spelling can name several bindings in one
	// file and only the innermost enclosing declaration tells which
	// binding a call site uses.
	captureUses := h2CaptureUses(f)
	var assigns []h2assignSite
	var calls []textEdit
	ast.Inspect(f, func(n ast.Node) bool {
		if as, ok := n.(*ast.AssignStmt); ok && len(as.Lhs) == 1 && len(as.Rhs) == 1 {
			// Group lowering binds the opt-in as a method value before
			// spawning its child. The verifier exempts the source call
			// by type, so a file that reaches the rewrite can only hold
			// this shape as lowering output or a saved value of the
			// public logos/net/http helper: a same-named user method
			// value never passes verification. Rebind the server
			// capture through the deferred guard so the receiver
			// still evaluates exactly once, eagerly, and the spawned
			// child runs the same check.
			sel, ok := as.Rhs[0].(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != h2extendedConnectMethod || h2HasSiteIdent(sel.X) {
				return true
			}
			// A capture used with arguments is the public helper
			// value, whose receiver names its import: the helper takes
			// the server as an argument, so rebinding it would change
			// its signature and break the deferred call. Arity, not
			// spelling, decides, so a server capture through a
			// shadowing local still rewrites while a helper capture
			// stays put. A capture with no recorded call site stays
			// put as well; lowering always uses the temporary it
			// binds.
			lhsIdent, ok := as.Lhs[0].(*ast.Ident)
			if !ok || !h2IsLoweredCalleeTemp(lhsIdent.Name) {
				return true
			}
			// Only the call sites bound to this assignment count: an
			// unrelated binding elsewhere in the file may share the
			// spelling, and its arguments must not spare a server
			// capture (or condemn a helper capture).
			if args, ok := captureUses[as]; !ok || !h2AllZeroArgs(args) {
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
// the same name would capture the generated call. A fresh alias avoids
// the file bindings and the package-level names in extraUsed. It also
// avoids every identifier the file mentions: an unaliased import's
// package name can differ from its path basename, and the file's own
// uses are the only signal for that binding. The existing-import reuse
// decision stays before that scan so references to the support import
// itself do not count as shadowing. The second result reports whether
// the import still needs adding.
func h2SupportLocal(f *ast.File, supportPath, supportLocal string, extraUsed map[string]bool) (string, bool) {
	bound := map[string]bool{}
	// extraUsed holds package-level names from sibling files in the
	// ejected package; the support import of this file must not reuse
	// one of those either.
	for name := range extraUsed {
		bound[name] = true
	}
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
	// Reserve every identifier the file mentions before choosing a
	// fresh alias. The import scan above only knows an unaliased
	// import by its path basename, while Go binds it by its package
	// clause name; a use such as ejectedhttp.Probe is the only
	// file-local trace of that binding, and reusing it would emit a
	// duplicate file-scope binding.
	ast.Inspect(f, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			bound[id.Name] = true
		}
		return true
	})
	local := "ejectedhttp"
	for i := 2; bound[local]; i++ {
		local = "ejectedhttp" + strconv.Itoa(i)
	}
	return local, true
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

// h2Overlaps reports whether any h2 edit overlaps an edit outside the h2
// set. The import-insertion edit is zero-width, so it overlaps only when
// strictly inside another edit (for example a whole-line comptime removal
// spanning the last import declaration); boundary-touching inserts apply
// cleanly in offset order.
// Call spans avoid the other rewrites by construction (see h2HasSiteIdent
// and the import-only span of ejectedImportEdits), but they are checked
// the same way so a future overlap fails safe.
func h2Overlaps(others, h2edits []textEdit) bool {
	for _, h := range h2edits {
		if h.start == h.end {
			for _, o := range others {
				if o.start < h.start && h.start < o.end {
					return true
				}
			}
			continue
		}
		for _, o := range others {
			if h.start < o.end && o.start < h.end {
				return true
			}
		}
	}

	return false
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

// h2AllZeroArgs reports whether every recorded call site passes no
// arguments. The verifier already refused every same-named user method,
// so a marked capture the rewrite trusts holds the zero-argument server
// method exactly then; any argument means the one-argument helper value.
func h2AllZeroArgs(args []int) bool {
	for _, n := range args {
		if n != 0 {
			return false
		}
	}
	return true
}

// h2CaptureUses attributes each lowered callee-temporary call site to the
// assignment that binds it. The verifier already refused every same-named
// user method, so a marked capture the rewrite trusts holds either the
// zero-argument server method or the one-argument helper value: the
// argument counts at its own uses tell which one, even when the receiver
// spells a shadowed import alias. The walk tracks one visible binding per
// temporary per scope; a use resolves to the innermost enclosing
// declaration, so an unrelated binding that reuses the spelling in
// another function or literal cannot lend its arguments here. A use with
// no visible binding records nothing: lowering always uses the temporary
// it binds, so an orphan use is not a shape the rewrite trusts.
func h2CaptureUses(f *ast.File) map[*ast.AssignStmt][]int {
	uses := map[*ast.AssignStmt][]int{}
	var scopes []map[string]*ast.AssignStmt
	resolve := func(name string) (*ast.AssignStmt, bool) {
		for i := len(scopes) - 1; i >= 0; i-- {
			if as, ok := scopes[i][name]; ok {
				return as, true
			}
		}
		return nil, false
	}
	// Walk calls the visitor with nil after every node's children, so
	// each visit pushes a marker telling the matching nil visit whether
	// a scope ends there.
	var endsScope []bool
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			if endsScope[len(endsScope)-1] {
				scopes = scopes[:len(scopes)-1]
			}
			endsScope = endsScope[:len(endsScope)-1]
			return true
		}
		scope := false
		switch n.(type) {
		case *ast.File, *ast.BlockStmt, *ast.CaseClause, *ast.CommClause,
			*ast.IfStmt, *ast.ForStmt, *ast.RangeStmt,
			*ast.SwitchStmt, *ast.TypeSwitchStmt, *ast.SelectStmt:
			scopes = append(scopes, map[string]*ast.AssignStmt{})
			scope = true
		}
		endsScope = append(endsScope, scope)
		switch n := n.(type) {
		case *ast.AssignStmt:
			// Any declared temporary shadows the outer spelling from
			// here on, even in a multi-target assignment the capture
			// matcher itself skips.
			for _, lhs := range n.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && h2IsLoweredCalleeTemp(id.Name) && (n.Tok == token.DEFINE || n.Tok == token.ASSIGN) {
					scopes[len(scopes)-1][id.Name] = n
				}
			}
		case *ast.GenDecl:
			if n.Tok == token.VAR {
				for _, spec := range n.Specs {
					if vs, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range vs.Names {
							if h2IsLoweredCalleeTemp(name.Name) {
								scopes[len(scopes)-1][name.Name] = nil
							}
						}
					}
				}
			}
		case *ast.CallExpr:
			if id, ok := n.Fun.(*ast.Ident); ok && h2IsLoweredCalleeTemp(id.Name) {
				if as, ok := resolve(id.Name); ok && as != nil {
					uses[as] = append(uses[as], len(n.Args))
				}
			}
		}
		return true
	})
	return uses
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

// h2PackageReserved collects top-level declared names from every Go
// file that will land in one ejected package directory: lowered
// staging files plus the plain Go sources copied verbatim. A generated
// import alias in any file of the package must avoid all of them,
// because no identifier may be declared in both the file and package
// block. Over-reservation is safe: it only lengthens a fresh alias.
// Non-regular entries are skipped unread, as in copyModuleTree: parsing
// a .go-named pipe would block waiting for a writer the loader ignores.
// Files that fail to parse are skipped; their names stay reserved
// through the importing file's own identifiers.
func h2PackageReserved(dirs ...string) map[string]bool {
	used := map[string]bool{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			name := e.Name()
			if !e.Type().IsRegular() || !strings.HasSuffix(name, ".go") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				continue
			}
			h2ReserveGoNames(used, filepath.Join(dir, name), data)
		}
	}
	return used
}

// h2ReserveGoNames records the top-level declared names and local import
// names of one Go source. Files that fail to parse record nothing; the
// importing file's own identifiers still reserve its name through the
// rewrite's file-local decision.
func h2ReserveGoNames(used map[string]bool, filename string, data []byte) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, data, 0)
	if err != nil {
		return
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			used[d.Name.Name] = true
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					used[s.Name.Name] = true
				case *ast.ValueSpec:
					for _, id := range s.Names {
						used[id.Name] = true
					}
				}
			}
		}
	}
	for _, imp := range f.Imports {
		if imp.Name != nil {
			used[imp.Name.Name] = true
		}
	}
}

// h2PackageReservedFor returns the package-wide reserved names for the
// h2 rewrite of one package, or nil when no emitted file in the package
// can carry an h2 call. The gate is a substring scan of the staging
// files: only a file mentioning the method name can produce h2 edits,
// and only h2 edits add fresh imports.
func h2PackageReservedFor(names []string, stagingPkgDir, srcDir string) map[string]bool {
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(stagingPkgDir, logosStem(name)+".go"))
		if err != nil {
			continue
		}
		if bytes.Contains(raw, []byte(h2extendedConnectMethod)) {
			return h2PackageReserved(stagingPkgDir, srcDir)
		}
	}
	return nil
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// VerifyEjectStdlib reports standard-library selectors with no upstream
// spelling while ejecting. Ejected code must build with an upstream Go
// 1.27 toolchain (SPEC T-0.3), but the fork toolchain also accepts
// fork-only standard-library APIs, so without this check logos eject
// would report success for output that upstream Go rejects. Each miss is
// reported at its source location; the staging build failure makes logos
// eject exit nonzero before it writes any output.
// VerifyEjectStdlib reports fork-only standard-library selectors in the main
// files. Call before lowering: the unlowered tree still holds every call
// site, including go-group children that lowering desugars away. After
// lowering, VerifyEjectStdlibPrepared walks the main files again for call
// sites lowering created.
func VerifyEjectStdlib(pkg *types2.Package, info *types2.Info, files []*syntax.File) {
	verifyEjectStdlib(pkg, info, files, false)
}

// VerifyEjectStdlibPrepared verifies comptime-prepared instances, which only
// exist after LogosCtFinish runs, and re-verifies the main files after
// lowering. Lowering creates call sites the source check cannot see (for
// example the ordinary method call an optional `?.` call desugars into),
// so verification after lowering must cover every call site the export
// will contain, main files included. Exemptions the source check granted
// are re-derived from the same rule, never replayed, so a lowered shape
// that the text rewrite cannot handle is still refused.
func VerifyEjectStdlibPrepared(pkg *types2.Package, info *types2.Info, files []*syntax.File) {
	verifyEjectStdlib(pkg, info, files, true)
}

func verifyEjectStdlib(pkg *types2.Package, info *types2.Info, files []*syntax.File, preparedOnly bool) {
	if base.LogosEject == "" && !base.LogosEjectCheck {
		return
	}
	if preparedOnly {
		if _, dup := verifyEjectStdlibDone.LoadOrStore(pkg.Path(), true); dup {
			return
		}
	}
	t := loadUpstreamAPI()
	if t == nil {
		return
	}
	// pkgRewritable reports whether any package file can reach the eject
	// text rewrite: it imports net/http without declaring the method name.
	// A prepared instance may land in any package file, so a user-method
	// call inside one is only provably safe when no file can rewrite it.
	pkgRewritable := false
	for _, f := range files {
		if fileImportsNetHTTP(f) && !h2DefinedInFile(f) {
			pkgRewritable = true
			break
		}
	}
	check := func(live map[*syntax.Name]bool, lowered map[*syntax.Name]bool, name *syntax.Name, obj types2.Object) {
		if obj == nil {
			return
		}
		if live != nil && !live[name] {
			return
		}
		if lowered[name] {
			return
		}
		if !isExportedName(obj.Name()) {
			return
		}
		p := obj.Pkg()
		if p == nil || p.Path() == pkg.Path() {
			return
		}
		if !t.hasPackage(p.Path()) {
			return
		}
		if t.hasName(p.Path(), obj.Name()) {
			return
		}
		base.Errorf("%s: [portability::fork_stdlib_api] logos eject: %s.%s has no upstream spelling (absent from the Go 1.27 API files); ejected output must build with upstream Go (SPEC T-0.3)", name.Pos(), p.Path(), obj.Name())
	}
	verify := func(info *types2.Info, roots []syntax.Node, prepSafe bool) {
		mapped, ambiguous := mappedEjectStdlibCalls(pkg, info, roots, prepSafe)
		lowered := loweredH2CalleeSels(roots)
		// After lowering, judge only the call sites the export will
		// contain: lowering replaces nodes, and type-checker entries
		// for replaced nodes must not be reported at stale positions.
		var live map[*syntax.Name]bool
		if preparedOnly {
			live = syntaxNames(roots)
		}
		for name, obj := range info.Uses {
			if mapped[name] {
				continue
			}
			if reason, ok := ambiguous[name]; ok {
				base.Errorf("%s: [portability::fork_stdlib_api] logos eject: ambiguous EnableHTTP2ExtendedConnect call (%s); eject rewrites the name by spelling, so only (*net/http.Server) and logos/net/http calls are safe to eject (SPEC T-0.3)", name.Pos(), reason)
				continue
			}
			check(live, lowered, name, obj)
		}
	}
	if len(files) != 0 {
		roots := make([]syntax.Node, len(files))
		for i, file := range files {
			roots[i] = file
		}
		verify(info, roots, false)
	}
	for _, rec := range info.LogosCtPrepared {
		if rec == nil || rec.Info == nil {
			continue
		}
		roots := make([]syntax.Node, 0, len(rec.Helpers)+1)
		if rec.Decl != nil {
			roots = append(roots, rec.Decl)
		}
		for _, helper := range rec.Helpers {
			roots = append(roots, helper)
		}
		verify(rec.Info, roots, !pkgRewritable && !recRefsNetHTTP(rec.Info))
	}
}

// mappedEjectStdlibCalls identifies exact call sites with an existing
// upstream rewrite, and ambiguous same-named calls the text rewrite would
// match by spelling. The exemption belongs to the selector occurrence,
// never the method name: escaping method values and method expressions
// still have no upstream spelling. A zero-argument call the exemption does
// not cover is ambiguous with the stdlib opt-in: eject cannot tell it
// apart from the genuine call, so it is reported for refusal instead of
// passing the wrong behavior through. A call with arguments can never be
// the genuine opt-in, and the rewrite never touches it (direct calls need
// zero arguments, and a lowered capture used with arguments keeps its
// binding), so argument-bearing calls are left to the ordinary upstream
// check; only the package-qualified helper, which takes the server as an
// argument, stays exempt.
//
// The decision resolves the callee's receiver type. Only the stdlib or
// fork (*net/http.Server) method is mapped or refused here; a method
// declared on a user type is left to the ordinary upstream check, which
// passes it when no rewrite can steal it and still rejects a genuine
// fork-only call. Files the text rewrite skips (no net/http import, or a
// local declaration of the name) never mark a call: the rewrite cannot
// touch them, so even a nested receiver that returns the user type needs
// no spelling guard there.
func mappedEjectStdlibCalls(pkg *types2.Package, info *types2.Info, roots []syntax.Node, prepSafe bool) (mapped map[*syntax.Name]bool, ambiguous map[*syntax.Name]string) {
	markMapped := func(sel *syntax.Name) {
		if mapped == nil {
			mapped = make(map[*syntax.Name]bool)
		}
		mapped[sel] = true
	}
	markAmbiguous := func(selector *syntax.SelectorExpr) {
		if ambiguous == nil {
			ambiguous = make(map[*syntax.Name]string)
		}
		ambiguous[selector.Sel] = h2CallDescription(info, selector)
	}
	var mayRewrite bool
	var definesName bool
	inspect := func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*syntax.SelectorExpr)
		if !ok || selector.Sel.Value != h2extendedConnectMethod {
			return true
		}
		if len(call.ArgList) != 0 {
			// A call with arguments can never be the genuine opt-in: the
			// stdlib method takes none, the text rewrite rebinds only
			// zero-argument direct calls, and a lowered capture used with
			// arguments keeps its binding (the rewrite decides by arity,
			// not spelling). Leave every argument-bearing call to the
			// ordinary upstream check below: a user method passes it, the
			// package-qualified helper passes it, and a genuine fork-only
			// call still fails it by type.
			return true
		}
		// The emitter leaves this file alone: either it never imports
		// net/http, or it declares the method name and the rewrite
		// skips it. A same-named user method call needs no rewrite,
		// and a genuine fork-only Server call still fails the
		// ordinary upstream check. Leave both unmarked.
		if !mayRewrite {
			return true
		}
		selection := info.Selections[selector]
		if selection == nil {
			// A package-qualified call names no method. Only the
			// logos/net/http helper is safe to eject; anything else
			// with this name would take the stdlib guard by spelling.
			if fn, ok := info.ObjectOf(selector.Sel).(*types2.Func); ok && fn.Pkg() != nil && isH2HelperPackage(fn.Pkg().Path()) {
				markMapped(selector.Sel)
				return true
			}
			markAmbiguous(selector)
			return true
		}
		if selection.Kind() != types2.MethodVal {
			markAmbiguous(selector)
			return true
		}
		// Decide from the method object, not the receiver type: a call
		// through an embedded *http.Server records the outer struct as
		// selection.Recv, but the method object is still the Server method
		// the textual eject rewrite accepts. Embedding paths stay allowed.
		fn, ok := selection.Obj().(*types2.Func)
		if !ok || fn.Name() != h2extendedConnectMethod {
			markAmbiguous(selector)
			return true
		}
		if isH2ServerMethod(fn) {
			// The text rewrite declines receivers containing generated
			// site names because their independent edits would overlap
			// the call. Keep exactly that boundary here instead of
			// approving output which the later rewrite cannot
			// materialize. Before lowering the site names do not exist
			// yet, so refuse the shapes lowering lifts into a site
			// temporary inside the receiver span: a `?` (for example
			// inside an inline callback) lowers into a TraceErr
			// carrying a fresh site temporary, and a call nested in
			// another call's arguments feeds a platform argument check
			// that parks the result in one the same way. A receiver
			// that is itself one flat call over plain arguments needs
			// no temporary and stays exempt. The nested-call rule is
			// conservative: value-parameter nesting needs no check and
			// the rewrite would accept it, but no fixture shapes it
			// that way and the error names the mechanism, so a future
			// hit stays diagnosable instead of slipping a fork-only
			// call into output upstream Go rejects.
			if h2ReceiverHasSite(selector.X) {
				markAmbiguous(selector)
				return true
			}
			markMapped(selector.Sel)
			return true
		}
		// A method on a user type. In a file the rewrite processes,
		// the text rewrite would steal it by spelling, so refuse it.
		// A prepared instance whose destination provably cannot reach
		// the rewrite (prepSafe) keeps the call: the ordinary check
		// passes a same-package method, and no spelling guard applies.
		if prepSafe && fn.Pkg() != nil && fn.Pkg().Path() == pkg.Path() {
			return true
		}
		markAmbiguous(selector)
		return true
	}
	for _, root := range roots {
		if f, ok := root.(*syntax.File); ok {
			definesName = h2DefinedInFile(f)
			mayRewrite = fileImportsNetHTTP(f) && !definesName
		} else {
			definesName = false
			mayRewrite = true
		}
		syntax.Inspect(root, inspect)
	}
	return mapped, ambiguous
}

// isH2ServerMethod reports whether fn is the fork-only opt-in on
// (*net/http.Server): the stdlib method the fork adds, whatever the
// receiver expression spells (a user struct embedding *http.Server still
// carries this method object). Any other method object with this name is
// a user method the text rewrite must not steal.
func isH2ServerMethod(fn *types2.Func) bool {
	if fn.Name() != h2extendedConnectMethod {
		return false
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != "net/http" {
		return false
	}
	sig, ok := fn.Type().(*types2.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	recv := types2.Unalias(sig.Recv().Type())
	if pointer, ok := recv.(*types2.Pointer); ok {
		recv = types2.Unalias(pointer.Elem())
	}
	named, ok := recv.(*types2.Named)
	if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "net/http" || named.Obj().Name() != "Server" {
		return false
	}
	return true
}

// h2ReceiverHasSite reports whether lowering may lift a site temporary
// out of the receiver span, which the eject text rewrite cannot overlap.
func h2ReceiverHasSite(x syntax.Node) bool {
	hasSite := false
	syntax.Inspect(x, func(node syntax.Node) bool {
		if _, ok := node.(*syntax.TryExpr); ok {
			hasSite = true
			return false
		}
		if _, ok := node.(*syntax.CallExpr); ok && node != x {
			hasSite = true
			return false
		}
		if name, ok := node.(*syntax.Name); ok {
			rest, site := strings.CutPrefix(name.Value, "_logos_site")
			hasSite = hasSite || site && rest != "" && strings.Trim(rest, "0123456789") == ""
		}
		return !hasSite
	})
	return hasSite
}

// loweredH2CalleeSels collects the method-value selectors that group and
// defer lowering binds through a reserved callee temporary: `_logos_fN :=
// <receiver>.EnableHTTP2ExtendedConnect`. The eject text rewrite rebinds
// exactly these captures through its deferred guard, so the ordinary
// upstream check must not report them: the staging build sees the
// rewritten guard, never the fork-only value. A capture whose receiver
// holds a compiler-temporary site name is left alone by the emitter and
// stays reportable, mirroring the rewrite's own boundary, which matches
// only site names and not other receiver shapes. User code cannot declare
// these temporaries, so the name marks exactly the shapes lowering
// created.
func loweredH2CalleeSels(roots []syntax.Node) map[*syntax.Name]bool {
	var out map[*syntax.Name]bool
	for _, root := range roots {
		syntax.Inspect(root, func(node syntax.Node) bool {
			as, ok := node.(*syntax.AssignStmt)
			if !ok || as.Op == 0 || as.Rhs == nil {
				return true
			}
			lhs, ok := as.Lhs.(*syntax.Name)
			if !ok || !isLoweredH2CalleeTemp(lhs.Value) {
				return true
			}
			sel, ok := as.Rhs.(*syntax.SelectorExpr)
			if !ok || sel.Sel.Value != h2extendedConnectMethod {
				return true
			}
			if h2SubtreeHasSiteIdent(sel.X) {
				return true
			}
			if out == nil {
				out = make(map[*syntax.Name]bool)
			}
			out[sel.Sel] = true
			return true
		})
	}
	return out
}

// h2SubtreeHasSiteIdent reports whether the subtree holds a
// compiler-temporary _logos_site name that the eject text rewrite renames.
// It mirrors the rewrite's own boundary, which matches only these names.
func h2SubtreeHasSiteIdent(x syntax.Node) bool {
	found := false
	syntax.Inspect(x, func(node syntax.Node) bool {
		if name, ok := node.(*syntax.Name); ok {
			rest, site := strings.CutPrefix(name.Value, "_logos_site")
			found = found || site && rest != "" && strings.Trim(rest, "0123456789") == ""
		}
		return !found
	})
	return found
}

// isLoweredH2CalleeTemp reports whether name is a lowering-generated callee
// temporary (_logos_f with a numeric suffix, from group lowering). User
// code cannot declare these names, so the name marks exactly the
// assignments lowering created.
func isLoweredH2CalleeTemp(name string) bool {
	rest, ok := strings.CutPrefix(name, "_logos_f")
	return ok && rest != "" && strings.Trim(rest, "0123456789") == ""
}

// syntaxNames collects every name under roots. Verification after lowering
// must only judge the call sites the export will contain: lowering
// replaces nodes, and type-checker entries for replaced nodes must not be
// reported at stale positions.
func syntaxNames(roots []syntax.Node) map[*syntax.Name]bool {
	out := make(map[*syntax.Name]bool)
	for _, root := range roots {
		syntax.Inspect(root, func(node syntax.Node) bool {
			if name, ok := node.(*syntax.Name); ok {
				out[name] = true
			}
			return true
		})
	}
	return out
}

// recRefsNetHTTP reports whether a prepared instance references the
// net/http package: through a value, a type, or a package qualifier. When
// it does, emitting the instance may add a net/http import to its
// destination file, so the eject text rewrite could reach a same-named
// user-method call inside it.
func recRefsNetHTTP(info *types2.Info) bool {
	if info == nil {
		return true
	}
	for _, obj := range info.Uses {
		if obj == nil {
			continue
		}
		if p := obj.Pkg(); p != nil && p.Path() == "net/http" {
			return true
		}
		if pn, ok := obj.(*types2.PkgName); ok {
			if imp := pn.Imported(); imp != nil && imp.Path() == "net/http" {
				return true
			}
		}
	}
	return false
}

// h2CallDescription names what a same-named call actually resolves to, so
// the ambiguity diagnostic says which declaration the rewrite would steal.
func h2CallDescription(info *types2.Info, selector *syntax.SelectorExpr) string {
	if selection := info.Selections[selector]; selection != nil {
		if recv := selection.Recv(); recv != nil {
			return "method on " + recv.String()
		}
		if obj := selection.Obj(); obj != nil {
			if pkg := obj.Pkg(); pkg != nil {
				return "member " + pkg.Path() + "." + obj.Name()
			}
			return "member " + obj.Name()
		}
	}
	if obj := info.ObjectOf(selector.Sel); obj != nil {
		if pkg := obj.Pkg(); pkg != nil {
			return "function " + pkg.Path() + "." + obj.Name()
		}
		return "function " + obj.Name()
	}
	return "unresolved call"
}

// isH2HelperPackage reports whether path is the logos/net/http package that
// owns the ejected spelling's source helper.
func isH2HelperPackage(path string) bool {
	return path == "logos/net/http" || strings.HasSuffix(path, "/logos/net/http")
}

// h2extendedConnectMethod mirrors eject.h2extendedConnectMethod: the two
// must name the same fork-only method or verification and rewriting drift
// apart and a same-named declaration lets a genuine call through.
const h2extendedConnectMethod = "EnableHTTP2ExtendedConnect"

// h2DefinedInFile reports whether f declares the extended-CONNECT method
// name itself, mirroring eject.h2DefinesExtendedConnect on the eject side.
func h2DefinedInFile(f *syntax.File) bool {
	found := false
	syntax.Inspect(f, func(n syntax.Node) bool {
		if found {
			return false
		}
		switch x := n.(type) {
		case *syntax.Field:
			if x.Name != nil && x.Name.Value == h2extendedConnectMethod {
				found = true
				return false
			}
		case *syntax.FuncDecl:
			if x.Name != nil && x.Name.Value == h2extendedConnectMethod {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// fileImportsNetHTTP reports whether f directly imports net/http.
// The eject rewrite only fires in files with a local net/http import,
// so verification must not exempt alias calls from other files that
// the emitter will leave behind for upstream Go to reject.
func fileImportsNetHTTP(f *syntax.File) bool {
	for _, decl := range f.DeclList {
		imp, ok := decl.(*syntax.ImportDecl)
		if !ok || imp.Path == nil {
			continue
		}
		if path, err := strconv.Unquote(imp.Path.Value); err == nil && path == "net/http" {
			return true
		}
	}
	return false
}

// verifyEjectStdlibDone keeps VerifyEjectStdlib to one report per package:
// the finish hook can run again after a midway rebuild.
var verifyEjectStdlibDone sync.Map

// upstreamAPI holds the package and member names the Go 1.27 API files
// describe. A nil table means the data is unavailable and verification
// is skipped.
type upstreamAPI struct {
	pkgs  map[string]struct{}
	names map[string]struct{}
}

var upstreamAPILoad sync.Once
var upstreamAPITable *upstreamAPI

func loadUpstreamAPI() *upstreamAPI {
	upstreamAPILoad.Do(func() {
		dir := filepath.Join(runtime.GOROOT(), "api")
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		t := &upstreamAPI{
			pkgs:  make(map[string]struct{}),
			names: make(map[string]struct{}),
		}
		files := 0
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() {
				continue
			}
			if name != "except.txt" && !(strings.HasPrefix(name, "go") && strings.HasSuffix(name, ".txt")) {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				return
			}
			parseUpstreamAPIFile(data, t)
			files++
		}
		if files == 0 || len(t.pkgs) == 0 {
			return
		}
		upstreamAPITable = t
	})
	return upstreamAPITable
}

func (t *upstreamAPI) hasPackage(path string) bool {
	_, ok := t.pkgs[path]
	return ok
}

func (t *upstreamAPI) hasName(path, name string) bool {
	_, ok := t.names[path+"\x00"+name]
	return ok
}

// parseUpstreamAPIFile records every package and member name an api/*.txt
// file describes. The format is "pkg <path>[ (<qual>)]?, <feature>" where a
// feature is a func, method, type (with optional struct-field or
// interface-method member), const, or var declaration. Only names are
// recorded: verification asks whether a selected name exists upstream, so
// a name present in any qualifier or on any receiver counts as present.
// That errs toward missing a violation, never toward refusing valid code.
func parseUpstreamAPIFile(data []byte, t *upstreamAPI) {
	add := func(path, name string) {
		if path == "" || name == "" {
			return
		}
		t.pkgs[path] = struct{}{}
		t.names[path+"\x00"+name] = struct{}{}
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "pkg ") {
			continue
		}
		rest := line[len("pkg "):]
		comma := strings.IndexByte(rest, ',')
		if comma < 0 {
			continue
		}
		fields := strings.Fields(rest[:comma])
		if len(fields) == 0 {
			continue
		}
		path := fields[0]
		feature := strings.TrimSpace(rest[comma+1:])
		switch {
		case strings.HasPrefix(feature, "func "):
			add(path, apiIdent(feature[len("func "):]))
		case strings.HasPrefix(feature, "method "):
			rest := feature[len("method "):]
			if !strings.HasPrefix(rest, "(") {
				continue
			}
			close := strings.IndexByte(rest, ')')
			if close < 0 {
				continue
			}
			add(path, apiIdent(rest[close+1:]))
		case strings.HasPrefix(feature, "type "):
			rest := feature[len("type "):]
			name := apiIdent(rest)
			add(path, name)
			tail := rest[len(name):]
			// A struct field is separated at the declaration's top
			// level. Its type, and generic constraints before it, may
			// contain braces that do not describe interface members.
			if idx := apiMemberSeparator(tail); idx >= 0 {
				add(path, apiIdent(tail[idx+1:]))
				continue
			}
			if open := strings.IndexByte(tail, '{'); open >= 0 {
				end := strings.IndexByte(tail[open:], '}')
				body := tail[open+1:]
				if end >= 0 {
					body = tail[open+1 : open+end]
				}
				for _, m := range apiMembers(body) {
					add(path, m)
				}
				continue
			}
		case strings.HasPrefix(feature, "const "), strings.HasPrefix(feature, "var "):
			if idx := strings.IndexByte(feature, ' '); idx >= 0 {
				add(path, apiIdent(feature[idx+1:]))
			}
		}
	}
}

// apiMemberSeparator finds the comma introducing a struct field, outside
// type parameters, constraints, and interface member lists.
func apiMemberSeparator(s string) int {
	depth := 0
	for i, c := range s {
		switch c {
		case '[', '(', '{':
			depth++
		case ']', ')', '}':
			depth--
		case ',':
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// apiIdent returns the leading identifier of s, or "" if s starts with
// anything else.
func apiIdent(s string) string {
	s = strings.TrimLeft(s, " \t")
	i := 0
	for i < len(s) && (s[i] == '_' || s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z' || s[i] >= '0' && s[i] <= '9') {
		i++
	}
	return s[:i]
}

// apiMembers returns the exported identifiers in s: member names in an
// interface brace list or after a struct-field comma. Unexported words
// and type spellings are skipped; every returned name is a real member
// of the described type.
func apiMembers(s string) []string {
	var out []string
	for i := 0; i < len(s); {
		c := s[i]
		if c == '_' || c >= 'A' && c <= 'Z' {
			j := i + 1
			for j < len(s) && (s[j] == '_' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z' || s[j] >= '0' && s[j] <= '9') {
				j++
			}
			out = append(out, s[i:j])
			i = j
			continue
		}
		i++
	}
	return out
}

func isExportedName(name string) bool {
	return name != "" && name[0] >= 'A' && name[0] <= 'Z'
}

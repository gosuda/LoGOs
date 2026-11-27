// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"os"
	"path/filepath"
	"runtime"
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
// site, including go-group children that lowering desugars away.
func VerifyEjectStdlib(pkg *types2.Package, info *types2.Info, files []*syntax.File) {
	verifyEjectStdlib(pkg, info, files, false)
}

// VerifyEjectStdlibPrepared verifies comptime-prepared instances, which only
// exist after LogosCtFinish runs. Main files are covered separately by
// VerifyEjectStdlib before lowering.
func VerifyEjectStdlibPrepared(pkg *types2.Package, info *types2.Info) {
	verifyEjectStdlib(pkg, info, nil, true)
}

func verifyEjectStdlib(pkg *types2.Package, info *types2.Info, files []*syntax.File, preparedOnly bool) {
	if base.LogosEject == "" {
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
	check := func(name *syntax.Name, obj types2.Object) {
		if obj == nil {
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
	verify := func(info *types2.Info, roots []syntax.Node) {
		mapped, ambiguous := mappedEjectStdlibCalls(info, roots)
		for name, obj := range info.Uses {
			if mapped[name] {
				continue
			}
			if reason, ok := ambiguous[name]; ok {
				base.Errorf("%s: [portability::fork_stdlib_api] logos eject: ambiguous EnableHTTP2ExtendedConnect call (%s); eject rewrites the name by spelling, so only (*net/http.Server) and logos/net/http calls are safe to eject (SPEC T-0.3)", name.Pos(), reason)
				continue
			}
			check(name, obj)
		}
	}
	if !preparedOnly {
		roots := make([]syntax.Node, len(files))
		for i, file := range files {
			roots[i] = file
		}
		verify(info, roots)
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
		verify(rec.Info, roots)
	}
}

// mappedEjectStdlibCalls identifies exact call sites with an existing
// upstream rewrite, and ambiguous same-named calls the text rewrite would
// match by spelling. The exemption belongs to the selector occurrence,
// never the method name: escaping method values and method expressions
// still have no upstream spelling. A zero-argument call the exemption does
// not cover is ambiguous with the stdlib opt-in: eject cannot tell it
// apart from the genuine call, so it is reported for refusal instead of
// passing the wrong behavior through.
func mappedEjectStdlibCalls(info *types2.Info, roots []syntax.Node) (map[*syntax.Name]bool, map[*syntax.Name]string) {
	var mapped map[*syntax.Name]bool
	var ambiguous map[*syntax.Name]string
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
	var definesName bool
	inspect := func(node syntax.Node) bool {
		call, ok := node.(*syntax.CallExpr)
		if !ok || len(call.ArgList) != 0 {
			return true
		}
		selector, ok := call.Fun.(*syntax.SelectorExpr)
		if !ok || selector.Sel.Value != "EnableHTTP2ExtendedConnect" {
			return true
		}
		// Eject skips files that declare the method name by spelling,
		// so none of their calls is exempt: fail closed on every one.
		if definesName {
			markAmbiguous(selector)
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
		// Any other method object with this name is ambiguous with the
		// stdlib opt-in, so refuse it instead of rewriting the wrong
		// behavior by spelling.
		fn, ok := selection.Obj().(*types2.Func)
		if !ok || fn.Name() != h2extendedConnectMethod {
			markAmbiguous(selector)
			return true
		}
		if fn.Pkg() == nil || fn.Pkg().Path() != "net/http" {
			markAmbiguous(selector)
			return true
		}
		sig, ok := fn.Type().(*types2.Signature)
		if !ok || sig.Recv() == nil {
			markAmbiguous(selector)
			return true
		}
		recv := types2.Unalias(sig.Recv().Type())
		if pointer, ok := recv.(*types2.Pointer); ok {
			recv = types2.Unalias(pointer.Elem())
		}
		named, ok := recv.(*types2.Named)
		if !ok || named.Obj().Pkg() == nil || named.Obj().Pkg().Path() != "net/http" || named.Obj().Name() != "Server" {
			markAmbiguous(selector)
			return true
		}
		// The text rewrite declines receivers containing generated site
		// names because their independent edits would overlap the call.
		// Keep exactly that boundary here instead of approving output
		// which the later rewrite cannot materialize. Before lowering the
		// site names do not exist yet, so refuse the shapes lowering lifts
		// into a site temporary inside the receiver span: a `?` (for
		// example inside an inline callback) lowers into a TraceErr
		// carrying a fresh site temporary, and a call nested in another
		// call's arguments feeds a platform argument check that parks the
		// result in one the same way. A receiver that is itself one flat
		// call over plain arguments needs no temporary and stays exempt.
		// The nested-call rule is conservative: value-parameter nesting
		// needs no check and the rewrite would accept it, but no fixture
		// shapes it that way and the error names the mechanism, so a
		// future hit stays diagnosable instead of slipping a fork-only
		// call into output upstream Go rejects.
		hasSite := false
		syntax.Inspect(selector.X, func(node syntax.Node) bool {
			if _, ok := node.(*syntax.TryExpr); ok {
				hasSite = true
				return false
			}
			if _, ok := node.(*syntax.CallExpr); ok && node != selector.X {
				hasSite = true
				return false
			}
			if name, ok := node.(*syntax.Name); ok {
				rest, site := strings.CutPrefix(name.Value, "_logos_site")
				hasSite = hasSite || site && rest != "" && strings.Trim(rest, "0123456789") == ""
			}
			return !hasSite
		})
		if hasSite {
			markAmbiguous(selector)
			return true
		}
		markMapped(selector.Sel)
		return true
	}
	for _, root := range roots {
		if f, ok := root.(*syntax.File); ok {
			if !fileImportsNetHTTP(f) {
				continue
			}
			// Eject skips files that declare the method name by
			// spelling, so none of their calls is exempt: the
			// rewrite leaves every one of them behind, and a
			// genuine stdlib call there has no upstream spelling
			// either. Record the declaration and fail closed.
			definesName = h2DefinedInFile(f)
		} else {
			definesName = false
		}
		syntax.Inspect(root, inspect)
	}
	return mapped, ambiguous
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
		if imp.Path.Value == "\"net/http\"" {
			return true
		}
	}
	return false
}

// verifyEjectStdlibDone keeps VerifyEjectStdlib to one report per package:
// the finish hook can run again after a midway rebuild.

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

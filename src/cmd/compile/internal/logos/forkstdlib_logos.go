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
func VerifyEjectStdlib(pkg *types2.Package, info *types2.Info, files []*syntax.File) {
	if base.LogosEject == "" {
		return
	}
	if _, dup := verifyEjectStdlibDone.LoadOrStore(pkg.Path(), true); dup {
		return
	}
	t := loadUpstreamAPI()
	if t == nil {
		return
	}
	rewritten := rewrittenH2Calls(files)
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
		if p.Path() == "net/http" && obj.Name() == h2extendedConnectMethod && rewritten[name.Pos()] {
			return
		}
		base.Errorf("%s: [portability::fork_stdlib_api] logos eject: %s.%s has no upstream spelling (absent from the Go 1.27 API files); ejected output must build with upstream Go (SPEC T-0.3)", name.Pos(), p.Path(), obj.Name())
	}
	for name, obj := range info.Uses {
		check(name, obj)
	}
	for _, rec := range info.LogosCtPrepared {
		if rec == nil || rec.Info == nil {
			continue
		}
		for name, obj := range rec.Info.Uses {
			check(name, obj)
		}
	}
}

// h2extendedConnectMethod is the fork-only per-server opt-in for HTTP/2
// extended CONNECT. logos eject rewrites direct calls to the GODEBUG-guarded
// stock form (see cmd/go/internal/logos/eject/eject_h2extendedconnect.go),
// so the verifier must not refuse those calls: the rewritten output builds
// with upstream Go. Every other use (method values, calls with arguments,
// or calls eject leaves alone) still has no upstream spelling and fails.
const h2extendedConnectMethod = "EnableHTTP2ExtendedConnect"

// rewrittenH2Calls records the selector positions of direct zero-argument
// EnableHTTP2ExtendedConnect calls in files importing net/http. Those are
// exactly the calls eject rewrites, so the verifier exempts them.
func rewrittenH2Calls(files []*syntax.File) map[syntax.Pos]bool {
	out := make(map[syntax.Pos]bool)
	for _, f := range files {
		if f == nil {
			continue
		}
		hasNetHTTP := false
		for _, d := range f.DeclList {
			imp, ok := d.(*syntax.ImportDecl)
			if !ok || imp.Path == nil {
				continue
			}
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
			continue
		}
		syntax.Inspect(f, func(n syntax.Node) bool {
			call, ok := n.(*syntax.CallExpr)
			if !ok || len(call.ArgList) != 0 {
				return true
			}
			sel, ok := call.Fun.(*syntax.SelectorExpr)
			if !ok || sel.Sel == nil || sel.Sel.Value != h2extendedConnectMethod {
				return true
			}
			out[sel.Sel.Pos()] = true
			return true
		})
	}
	return out
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
			if idx := strings.IndexByte(tail, ','); idx >= 0 {
				add(path, apiIdent(tail[idx+1:]))
			}
		case strings.HasPrefix(feature, "const "), strings.HasPrefix(feature, "var "):
			if idx := strings.IndexByte(feature, ' '); idx >= 0 {
				add(path, apiIdent(feature[idx+1:]))
			}
		}
	}
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

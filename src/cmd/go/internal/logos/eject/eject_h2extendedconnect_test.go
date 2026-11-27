// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestH2ExtendedConnectEmitsSupportDelegation(t *testing.T) {
	const in = `package main

import "net/http"

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	out, uses, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	if !uses {
		t.Error("support import not reported as used")
	}
	got := string(out)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect()") {
		t.Errorf("direct call survives:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp.RequireExtendedConnect("s", s)`) {
		t.Errorf("no support delegation naming the server:\n%s", got)
	}
	if strings.Contains(got, "strings.Contains(os.Getenv") {
		t.Errorf("inline GODEBUG parsing survives:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp "github.com/gosuda/loGos/rt/logos/net/http"`) {
		t.Errorf("missing aliased support import:\n%s", got)
	}
}

func TestH2ExtendedConnectEmitsStockForm(t *testing.T) {
	const in = `package main

import "net/http"

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect()") {
		t.Errorf("direct call survives:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp.RequireExtendedConnect("s", s)`) {
		t.Errorf("no helper call with the receiver as argument:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp "github.com/gosuda/loGos/rt/logos/net/http"`) {
		t.Errorf("missing ejectedhttp support import:\n%s", got)
	}
	if strings.Contains(got, "os.Getenv") || strings.Contains(got, "strings.Contains") {
		t.Errorf("guard inlined with file bindings instead of the helper:\n%s", got)
	}
}

func TestH2ExtendedConnectReceiverEvaluatesAtCallSite(t *testing.T) {
	// Planted fault: the rewrite moved receiver evaluation into the
	// generated closure, so go and defer calls evaluated it late.
	const in = `package main

import "net/http"

func enable(s *http.Server) {
	go s.EnableHTTP2ExtendedConnect()
	defer s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if !strings.Contains(got, "go ejectedhttp.RequireExtendedConnect(\"s\", s)") {
		t.Errorf("go call keeps late evaluation:\n%s", got)
	}
	if !strings.Contains(got, "defer ejectedhttp.RequireExtendedConnect(\"s\", s)") {
		t.Errorf("defer call keeps late evaluation:\n%s", got)
	}
}

func TestH2ExtendedConnectEvaluatesReceiverEagerly(t *testing.T) {
	const in = `package main

import "net/http"

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
	defer s.EnableHTTP2ExtendedConnect()
	go s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "func() {") {
		t.Errorf("receiver moved inside a deferred closure:\n%s", got)
	}
	if !strings.Contains(got, `defer ejectedhttp.RequireExtendedConnect("s", s)`) {
		t.Errorf("deferred call does not evaluate the receiver as an argument:\n%s", got)
	}
	if !strings.Contains(got, `go ejectedhttp.RequireExtendedConnect("s", s)`) {
		t.Errorf("go call does not evaluate the receiver as an argument:\n%s", got)
	}
}

func TestH2ExtendedConnectRejectsOverlappingImportEdit(t *testing.T) {
	const in = `package main

import "net/http"
import "comptime"

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	out, uses, ok := remapEjectedFile([]byte(in), "", nil)
	if ok || uses || out != nil {
		t.Fatalf("overlapping edit reported partial success: ok=%v uses=%v output=%q", ok, uses, out)
	}

	saved := ejectGofmtFiles
	t.Cleanup(func() { ejectGofmtFiles = saved })
	ejectGofmtFiles = nil
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "input.go"), filepath.Join(dir, "out", "input.go")
	if err := os.WriteFile(src, []byte(in), 0666); err != nil {
		t.Fatal(err)
	}
	uses, err := copyDataFile(src, dst, map[string]map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if uses || string(got) != in || len(ejectGofmtFiles) != 0 {
		t.Errorf("copy changed rejected source: uses=%v format=%v output=%q", uses, ejectGofmtFiles, got)
	}
}

func TestH2ExtendedConnectKeepsExistingImports(t *testing.T) {
	const in = `package main

import (
	"net/http"
	"os"
	"strings"
)

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Count(got, `"os"`) != 1 || strings.Count(got, `"strings"`) != 1 {
		t.Errorf("existing imports disturbed:\n%s", got)
	}
	if strings.Count(got, `ejectedhttp "github.com/gosuda/loGos/rt/logos/net/http"`) != 1 {
		t.Errorf("support import missing or duplicated:\n%s", got)
	}
	if strings.Contains(got, "os.Getenv") || strings.Contains(got, "strings.Contains") {
		t.Errorf("guard inlined with file bindings instead of the helper:\n%s", got)
	}
}

func TestH2ExtendedConnectAvoidsFileBindings(t *testing.T) {
	const in = `package main

import (
	"net/http"
	sys "os"
)

var mode = sys.Getenv("H2XCONNECT_MODE")

func enable(s *http.Server) {
	strings := "shadow"
	_ = strings
	_ = mode
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "os.Getenv") || strings.Contains(got, "strings.Contains") {
		t.Errorf("rewrite uses the file's os/strings bindings:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp.RequireExtendedConnect("s", s)`) {
		t.Errorf("no hygienic helper call with the receiver as argument:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp "github.com/gosuda/loGos/rt/logos/net/http"`) {
		t.Errorf("missing ejectedhttp support import:\n%s", got)
	}
}

func TestH2ExtendedConnectRebindsLoweredMethodValue(t *testing.T) {
	const in = `package main

import "net/http"

func spawn(recv *http.Server) {
	_logos_f0 := recv.EnableHTTP2ExtendedConnect
	go _logos_f0()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect\"") || strings.Contains(got, ".EnableHTTP2ExtendedConnect") {
		t.Errorf("method value left unrewritten:\n%s", got)
	}
	if !strings.Contains(got, `_logos_f0 := ejectedhttp.RequireExtendedConnectFunc("recv", recv)`) {
		t.Errorf("method value not rebound through the deferred guard:\n%s", got)
	}
}

func TestH2ExtendedConnectLeavesUserMethodValue(t *testing.T) {
	// A user method value with the same selector name is not lowering
	// output: generic lowering binds its callee captures through reserved
	// _logos_f temporaries, so a user binding must survive the rewrite.
	const in = `package main

import "net/http"

func spawn(s *http.Server, m *Meter) {
	_ = s
	f := m.EnableHTTP2ExtendedConnect
	defer f()
}

type Meter struct{ hits int }
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "RequireExtendedConnect") {
		t.Errorf("user method value rewritten into the guard:\n%s", got)
	}
	if !strings.Contains(got, "f := m.EnableHTTP2ExtendedConnect") {
		t.Errorf("user method value binding lost:\n%s", got)
	}
}

func TestH2ExtendedConnectReusesExistingSupportImport(t *testing.T) {
	const in = `package main

import (
	"net/http"
	ejectedhttp "github.com/gosuda/loGos/rt/logos/net/http"
)

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Count(got, `"github.com/gosuda/loGos/rt/logos/net/http"`) != 1 {
		t.Errorf("support import duplicated:\n%s", got)
	}
}

func TestH2ExtendedConnectReusesLogosNetHTTPBinding(t *testing.T) {
	const aliased = `package main

import (
	"net/http"
	logoshttp "logos/net/http"
)

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(aliased), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if !strings.Contains(got, `logoshttp.RequireExtendedConnect("s", s)`) {
		t.Errorf("existing logos/net/http binding not reused:\n%s", got)
	}
	if strings.Contains(got, "ejectedhttp") {
		t.Errorf("fresh support import added despite existing binding:\n%s", got)
	}

	for _, src := range []string{
		"package main\n\nimport (\n\t\"net/http\"\n\t_ \"logos/net/http\"\n)\n\nfunc enable(s *http.Server) {\n\ts.EnableHTTP2ExtendedConnect()\n}\n",
		"package main\n\nimport (\n\t\"net/http\"\n\t. \"logos/net/http\"\n)\n\nfunc enable(s *http.Server) {\n\ts.EnableHTTP2ExtendedConnect()\n}\n",
	} {
		out, _, ok := remapEjectedFile([]byte(src), "", nil)
		if !ok {
			t.Fatal("remapEjectedFile failed")
		}
		got := string(out)
		if !strings.Contains(got, `ejectedhttp.RequireExtendedConnect("s", s)`) {
			t.Errorf("blank/dot binding not given a helper call:\n%s", got)
		}
		if !strings.Contains(got, `ejectedhttp "github.com/gosuda/loGos/rt/logos/net/http"`) {
			t.Errorf("fresh support import missing for blank/dot binding:\n%s", got)
		}
	}
}

func TestH2ExtendedConnectSkipsForeignFiles(t *testing.T) {
	const in = `package main

func enable(s *thing) {
	s.EnableHTTP2ExtendedConnect()
}

type thing struct{}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	if string(out) != in {
		t.Errorf("file without net/http changed:\n%s", out)
	}
}

func TestH2ExtendedConnectSkipsTempReceivers(t *testing.T) {
	for _, src := range []string{
		"package main\n\nimport \"net/http\"\n\nfunc enable() {\n\t_logos_site0.EnableHTTP2ExtendedConnect()\n}\n",
		"package main\n\nimport \"net/http\"\n\nfunc enable(s *http.Server) {\n\ts.EnableHTTP2ExtendedConnect(x)\n}\n",
	} {
		out, _, ok := remapEjectedFile([]byte(src), "", nil)
		if !ok {
			t.Fatal("remapEjectedFile failed")
		}
		if string(out) != src {
			t.Errorf("call wrongly rewritten:\n%s", out)
		}
	}
}

func TestH2ExtendedConnectSkipsDotBlankBindings(t *testing.T) {
	const in = `package main

import (
	"net/http"
	_ "os"
	. "strings"
)

func enable(s *http.Server) {
	_ = TrimSpace("x")
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect()") {
		t.Errorf("direct call survives:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp.RequireExtendedConnect("s", s)`) {
		t.Errorf("no delegation call with the receiver as argument:\n%s", got)
	}
	if strings.Contains(got, "..") || strings.Contains(got, "_.") {
		t.Errorf("dot or blank binding used as qualifier:\n%s", got)
	}
}

func TestH2ExtendedConnectIgnoresShadowableAny(t *testing.T) {
	const in = `package main

import "net/http"

type any = string

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect()") {
		t.Errorf("direct call survives:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp.RequireExtendedConnect("s", s)`) {
		t.Errorf("no delegation call with the receiver as argument:\n%s", got)
	}
	if strings.Contains(got, " any)") {
		t.Errorf("emitted code uses shadowable any:\n%s", got)
	}
}

func TestH2ExtendedConnectPanicReceiver(t *testing.T) {
	const in = `package main

import "net/http"

func enable(panic *http.Server) {
	panic.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect()") {
		t.Errorf("direct call survives:\n%s", got)
	}
	if strings.Contains(got, "func(panic ") {
		t.Errorf("parameter shadows builtin panic:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp.RequireExtendedConnect("panic", panic)`) {
		t.Errorf("receiver not passed verbatim to the delegation:\n%s", got)
	}
}

func TestH2ExtendedConnectReservesSiblingNames(t *testing.T) {
	const in = `package main

import "net/http"

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	extra := map[string]bool{"ejectedhttp": true}
	out, _, ok := remapEjectedFile([]byte(in), "", extra)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect()") {
		t.Errorf("direct call survives:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp2 "github.com/gosuda/loGos/rt/logos/net/http"`) {
		t.Errorf("support import does not avoid the sibling name:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp2.RequireExtendedConnect("s", s)`) {
		t.Errorf("delegation does not use the reserved alias:\n%s", got)
	}
}

func TestH2ExtendedConnectAvoidsShadowedSupportBinding(t *testing.T) {
	const in = `package main

import (
	"net/http"
	logoshttp "logos/net/http"
)

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}

func shadowed(srv *http.Server) {
	logoshttp := "shadow"
	_ = logoshttp
	srv.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "logoshttp.RequireExtendedConnect") {
		t.Errorf("shadowed binding reused:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp.RequireExtendedConnect("s", s)`) {
		t.Errorf("unshadowed call not on the fresh alias:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp.RequireExtendedConnect("srv", srv)`) {
		t.Errorf("shadowed call not on the fresh alias:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp "github.com/gosuda/loGos/rt/logos/net/http"`) {
		t.Errorf("fresh support import missing:\n%s", got)
	}
}

func TestH2ExtendedConnectAvoidsPackageLevelCollision(t *testing.T) {
	const in = `package main

import "net/http"

var ejectedhttp = 1

func enable(s *http.Server) {
	_ = ejectedhttp
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, `ejectedhttp.RequireExtendedConnect`) {
		t.Errorf("colliding package-level name reused:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp2.RequireExtendedConnect("s", s)`) {
		t.Errorf("call not on the fresh alias:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp2 "github.com/gosuda/loGos/rt/logos/net/http"`) {
		t.Errorf("fresh support import missing:\n%s", got)
	}
}

func TestH2ExtendedConnectKeepsSavedHelperValue(t *testing.T) {
	const in = `package main

import (
	"net/http"
	logohttp "logos/net/http"
)

func save() {
	enable := logohttp.EnableHTTP2ExtendedConnect
	_ = enable
}

func spawn(srv *http.Server) {
	_logos_f0 := srv.EnableHTTP2ExtendedConnect
	go _logos_f0()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if !strings.Contains(got, "enable := logohttp.EnableHTTP2ExtendedConnect") {
		t.Errorf("saved helper value rewritten:\n%s", got)
	}
	if !strings.Contains(got, `_logos_f0 := logohttp.RequireExtendedConnectFunc("srv", srv)`) {
		t.Errorf("server capture not rebound through the existing binding:\n%s", got)
	}
}

func TestH2ExtendedConnectComposesNestedOptIn(t *testing.T) {
	const in = `package main

import "net/http"

func spawn(backup *http.Server, make func(func()) *http.Server) {
	_logos_f0 := make(func() { backup.EnableHTTP2ExtendedConnect() }).EnableHTTP2ExtendedConnect
	go _logos_f0()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed: nested capture and inner call overlap")
	}
	got := string(out)
	// The original receiver text survives only as the guard's name
	// argument; no fork-only reference may remain in code position.
	if n := strings.Count(got, "EnableHTTP2ExtendedConnect"); n != 1 {
		t.Errorf("expected the opt-in name only in the guard label, found %d references:\n%s", n, got)
	}
	if !strings.Contains(got, `ejectedhttp.RequireExtendedConnect("backup", backup)`) {
		t.Errorf("inner guard missing from the composed capture:\n%s", got)
	}
	if !strings.Contains(got, "ejectedhttp.RequireExtendedConnectFunc(") {
		t.Errorf("capture not rebound through the deferred guard:\n%s", got)
	}
}

func TestH2ExtendedConnectAvoidsSiblingPackageNames(t *testing.T) {
	// A sibling file declaring ejectedhttp at package level forbids
	// that alias in the rewritten file: Go rejects an import name
	// that is also a package-block declaration, even across files.
	dir := t.TempDir()
	sibling := "package main\n\nvar ejectedhttp = 1\n"
	if err := os.WriteFile(filepath.Join(dir, "config.go"), []byte(sibling), 0666); err != nil {
		t.Fatal(err)
	}
	main := `package main

import "net/http"

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(main), 0666); err != nil {
		t.Fatal(err)
	}
	// The rewritten file never mentions the sibling name itself: only
	// the directory scan reserves it.
	reserved := h2PackageReserved(dir)
	if !reserved["ejectedhttp"] {
		t.Fatalf("sibling package-level name missing: %v", reserved)
	}
	out, _, ok := remapEjectedFile([]byte(main), "", reserved)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "ejectedhttp.RequireExtendedConnect") && !strings.Contains(got, "ejectedhttp2.RequireExtendedConnect") {
		t.Errorf("colliding sibling name reused:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp2.RequireExtendedConnect("s", s)`) {
		t.Errorf("call not on the reserved alias:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp2 "github.com/gosuda/loGos/rt/logos/net/http"`) {
		t.Errorf("support import does not avoid the sibling name:\n%s", got)
	}
}

func TestH2ExtendedConnectReuseYieldsToSiblingNames(t *testing.T) {
	// An existing support import cannot be reused when a sibling file
	// declares its name at package level: the import would collide
	// with that declaration in the ejected package.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.go"), []byte("package main\n\nvar ejectedhttp = 1\n"), 0666); err != nil {
		t.Fatal(err)
	}
	const in = `package main

import (
	"net/http"
	ejectedhttp "github.com/gosuda/loGos/rt/logos/net/http"
)

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(in), 0666); err != nil {
		t.Fatal(err)
	}
	out, _, ok := remapEjectedFile([]byte(in), "", h2PackageReserved(dir))
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "ejectedhttp.RequireExtendedConnect") && !strings.Contains(got, "ejectedhttp2.RequireExtendedConnect") {
		t.Errorf("shadowed-by-sibling binding reused:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp2 "github.com/gosuda/loGos/rt/logos/net/http"`) {
		t.Errorf("fresh support import missing:\n%s", got)
	}
}

func TestH2ExtendedConnectAvoidsImplicitPackageNames(t *testing.T) {
	// An unaliased import binds its package clause name, which can
	// differ from the path basename the import scan reserves. The
	// file's own uses are the only trace of that binding, so the
	// fresh alias must avoid every identifier the file mentions.
	const in = `package main

import (
	"net/http"
	"example.com/transport"
)

func probe(s *http.Server) {
	_ = ejectedhttp.Probe()
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, `"github.com/gosuda/loGos/rt/logos/net/http"`) && !strings.Contains(got, "ejectedhttp2") {
		t.Errorf("implicit package name reused for the support import:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp2.RequireExtendedConnect("s", s)`) {
		t.Errorf("call not on the fresh alias:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp2 "github.com/gosuda/loGos/rt/logos/net/http"`) {
		t.Errorf("fresh support import missing:\n%s", got)
	}
}

func TestH2PackageReservedSkipsNonregular(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs FIFOs")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n\nconst A = 1\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "_input.go"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan map[string]bool, 1)
	go func() { done <- h2PackageReserved(dir) }()
	select {
	case used := <-done:
		if !used["A"] {
			t.Errorf("regular file names missing: %v", used)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("h2PackageReserved hangs on a .go-named pipe")
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"encoding/json"
	"internal/testenv"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cmd/go/internal/fsys"
)

// The support copy has one form. A build tag of the toolchain that
// still selects files in it lets a tag of the user remove a function
// that the copy needs.
func TestSupportCopyDoesNotDependOnToolchainBuildTags(t *testing.T) {
	saved := ejectGofmtFiles
	t.Cleanup(func() { ejectGofmtFiles = saved })
	src := filepath.Join(testenv.GOROOT(t), "src")
	for _, pkg := range []string{"rt", "sync"} {
		dst := t.TempDir()
		if _, err := copyEjectedSupport(src, "", dst, []string{"logos/" + pkg}); err != nil {
			t.Fatalf("logos/%s: %v", pkg, err)
		}
		rel, _ := supportRelPath("logos/" + pkg)
		own, err := filepath.Glob(filepath.Join(dst, filepath.FromSlash(rel), "*.go"))
		if err != nil || len(own) == 0 {
			t.Fatalf("logos/%s: the copy holds %d files, error %v", pkg, len(own), err)
		}
		var copied []string
		err = filepath.WalkDir(dst, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(path, ".go") {
				copied = append(copied, path)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range copied {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for line := range strings.SplitSeq(string(data), "\n") {
				if !strings.HasPrefix(line, "//go:build ") {
					continue
				}
				if strings.Contains(line, "logosconc") || strings.Contains(line, "logosejected") {
					t.Errorf("logos/%s: %s keeps the build line %q", pkg, file, line)
				}
			}
		}
	}
}

// A source that already owns a companion stem must not collide with the
// JSONv2 companion the support copy emits: copyPackage reserves every
// output stem up front, so the companion takes the next free name.
func TestSupportCopyReservesCompanionStems(t *testing.T) {
	src := t.TempDir()
	staging := t.TempDir()
	out := t.TempDir()
	pkg := filepath.Join(src, "logos", "ejecttestcopy")
	if err := os.MkdirAll(pkg, 0777); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"item.lgo":        "package ejecttestcopy\n",
		"item_jsonv2.lgo": "package ejecttestcopy\n\nconst OwnedMarker = 1\n",
	} {
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(source), 0666); err != nil {
			t.Fatal(err)
		}
	}
	staged := filepath.Join(staging, "logos", "ejecttestcopy")
	if err := os.MkdirAll(staged, 0777); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"item.go": `package ejecttestcopy

import _logos_jsontext "encoding/json/jsontext"

type Item struct{}

func (_logos_v Item) MarshalJSONTo(_logos_enc *_logos_jsontext.Encoder) error { return nil }
`,
		"item_jsonv2.go": "package ejecttestcopy\n\nconst OwnedMarker = 1\n",
	} {
		if err := os.WriteFile(filepath.Join(staged, name), []byte(source), 0666); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Errorf("close support root: %v", err)
		}
	}()
	c := supportCopy{
		src:          root,
		srcDir:       src,
		staging:      staging,
		dst:          out,
		destinations: map[string]string{},
		written:      map[string]string{},
	}
	p := supportSourcePackage{src: "logos/ejecttestcopy", dst: "rt/logos/ejecttestcopy"}
	if err := c.copyPackage(p); err != nil {
		t.Fatalf("copyPackage: %v", err)
	}
	for file, declaration := range map[string]string{
		"item.go":               "type Item struct{}",
		"item_jsonv2.go":        "OwnedMarker",
		"item_jsonv2_jsonv2.go": "MarshalJSONTo",
	} {
		data, err := os.ReadFile(filepath.Join(out, "rt", "logos", "ejecttestcopy", file))
		if err != nil {
			t.Error(err)
			continue
		}
		if !strings.Contains(string(data), declaration) {
			t.Errorf("%s lost %s:\n%s", file, declaration, data)
		}
	}
}

func TestSupportCopyRewritesExtendedConnect(t *testing.T) {
	src := t.TempDir()
	staging := t.TempDir()
	out := t.TempDir()
	pkg := filepath.Join(src, "logos", "ejecttesth2")
	if err := os.MkdirAll(pkg, 0777); err != nil {
		t.Fatal(err)
	}
	const source = "package ejecttesth2\n\nimport \"net/http\"\n\nfunc Enable(s *http.Server) {\n\ts.EnableHTTP2ExtendedConnect()\n}\n"
	if err := os.WriteFile(filepath.Join(pkg, "h2.go"), []byte(source), 0666); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Errorf("close support root: %v", err)
		}
	}()
	c := supportCopy{
		src:          root,
		srcDir:       src,
		staging:      staging,
		dst:          out,
		destinations: map[string]string{},
		written:      map[string]string{},
	}
	p := supportSourcePackage{src: "logos/ejecttesth2", dst: "rt/logos/ejecttesth2"}
	if err := c.copyPackage(p); err != nil {
		t.Fatalf("copyPackage: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(out, "rt", "logos", "ejecttesth2", "h2.go"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect()") {
		t.Errorf("fork-only call survives support copy:\n%s", got)
	}
	if !strings.Contains(got, "RequireExtendedConnect") {
		t.Errorf("support copy missing guard delegation:\n%s", got)
	}
	if !strings.Contains(got, `"github.com/gosuda/loGos/rt/logos/net/http"`) {
		t.Errorf("support copy missing HTTP helper import:\n%s", got)
	}
	if _, ok := c.destinations["logos/net/http"]; !ok {
		t.Errorf("HTTP support package not enqueued, destinations=%v", c.destinations)
	}
}

// A lowered source keeps raw-string lines that match an ejected build
// line: the header rewrite already specialized the guard, so the
// whole-file legacy strip must not run on lowered bytes.
func TestSupportCopyKeepsBuildTagInLoweredString(t *testing.T) {
	src := t.TempDir()
	staging := t.TempDir()
	out := t.TempDir()
	pkg := filepath.Join(src, "logos", "ejectteststr")
	if err := os.MkdirAll(pkg, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "str.lgo"), []byte("package ejectteststr\n"), 0666); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(staging, "logos", "ejectteststr")
	if err := os.MkdirAll(staged, 0777); err != nil {
		t.Fatal(err)
	}
	const lowered = "package ejectteststr\n\nvar Tmpl = `a\n//go:build logosejected\nb`\n"
	if err := os.WriteFile(filepath.Join(staged, "str.go"), []byte(lowered), 0666); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Errorf("close support root: %v", err)
		}
	}()
	c := supportCopy{
		src:          root,
		srcDir:       src,
		staging:      staging,
		dst:          out,
		destinations: map[string]string{},
		written:      map[string]string{},
	}
	p := supportSourcePackage{src: "logos/ejectteststr", dst: "rt/logos/ejectteststr"}
	if _, _, err := c.copyPackageGo(p, "str.lgo", map[string]bool{}); err != nil {
		t.Fatalf("copyPackageGo: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(out, "rt", "logos", "ejectteststr", "str.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "//go:build logosejected\nb") {
		t.Errorf("support copy stripped a raw-string build line from lowered source:\n%s", data)
	}
}

// A sibling file declaring the fresh alias at package level does not
// break the support copy: the generated import alias avoids every
// package-level name in the ejected package, as the main-module copy does.
func TestSupportCopyAvoidsSiblingAlias(t *testing.T) {
	src := t.TempDir()
	out := t.TempDir()
	pkg := filepath.Join(src, "logos", "ejecttestsib")
	if err := os.MkdirAll(pkg, 0777); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"sibling.go": "package ejecttestsib\n\nvar ejectedhttp = 1\n",
		"server.go":  "package ejecttestsib\n\nimport \"net/http\"\n\nfunc Enable(s *http.Server) {\n\ts.EnableHTTP2ExtendedConnect()\n}\n",
	}
	for name, source := range files {
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(source), 0666); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Errorf("close support root: %v", err)
		}
	}()
	c := supportCopy{
		src:          root,
		srcDir:       src,
		staging:      t.TempDir(),
		dst:          out,
		destinations: map[string]string{},
		written:      map[string]string{},
	}
	p := supportSourcePackage{src: "logos/ejecttestsib", dst: "rt/logos/ejecttestsib"}
	if err := c.copyPackage(p); err != nil {
		t.Fatalf("copyPackage: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(out, "rt", "logos", "ejecttestsib", "server.go"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, `ejectedhttp2 "github.com/gosuda/loGos/rt/logos/net/http"`) {
		t.Errorf("copied file does not avoid the sibling name:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp2.RequireExtendedConnect("s", s)`) {
		t.Errorf("delegation does not use the reserved alias:\n%s", got)
	}
}

// A handwritten support file that looks like a server opt-in while a
// sibling file declares the same method name is ambiguous: the text
// rewrite cannot tell the fork method from the user method, so the
// support copy refuses instead of exporting the wrong behavior.
func TestSupportCopyRefusesAmbiguousExtendedConnect(t *testing.T) {
	src := t.TempDir()
	pkg := filepath.Join(src, "logos", "ejecttestambig")
	if err := os.MkdirAll(pkg, 0777); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"meter.go":  "package ejecttestambig\n\ntype Meter struct{}\n\nfunc (m *Meter) EnableHTTP2ExtendedConnect() {}\n",
		"server.go": "package ejecttestambig\n\nimport \"net/http\"\n\nfunc Enable(s *http.Server) {\n\ts.EnableHTTP2ExtendedConnect()\n}\n",
	}
	for name, source := range files {
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(source), 0666); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Errorf("close support root: %v", err)
		}
	}()
	c := supportCopy{
		src:          root,
		srcDir:       src,
		staging:      t.TempDir(),
		dst:          t.TempDir(),
		destinations: map[string]string{},
		written:      map[string]string{},
	}
	p := supportSourcePackage{src: "logos/ejecttestambig", dst: "rt/logos/ejecttestambig"}
	_, _, err = c.copyPackageGo(p, "server.go", map[string]bool{})
	if err == nil || !strings.Contains(err.Error(), "ambiguous extended-CONNECT") {
		t.Errorf("copyPackageGo = %v, want an ambiguous extended-CONNECT refusal", err)
	}
}

// A support package that selects constants by ejected mode and consumes
// them under const-if cannot ship host lowering: the ejected tree builds
// without the ejected tag, so the copy refuses instead of publishing a
// host specialization.
func TestSupportCopyRefusesModeDependentLowering(t *testing.T) {
	src := t.TempDir()
	staging := t.TempDir()
	out := t.TempDir()
	pkg := filepath.Join(src, "logos", "ejecttestmode")
	if err := os.MkdirAll(pkg, 0777); err != nil {
		t.Fatal(err)
	}
	originals := map[string]string{
		"native.lgo":  "//go:build !logosejected\n\npackage ejecttestmode\n\nconst Native = true\n",
		"ejected.lgo": "//go:build logosejected\n\npackage ejecttestmode\n\nconst Native = false\n",
		"use.lgo":     "package ejecttestmode\n\nconst if Native {\nconst Chosen = 1\n}\n",
	}
	for name, source := range originals {
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(source), 0666); err != nil {
			t.Fatal(err)
		}
	}
	staged := filepath.Join(staging, "logos", "ejecttestmode")
	if err := os.MkdirAll(staged, 0777); err != nil {
		t.Fatal(err)
	}
	lowered := map[string]string{
		"native.go":  "package ejecttestmode\n\nconst Native = true\n",
		"ejected.go": "package ejecttestmode\n\nconst Native = false\n",
		"use.go":     "package ejecttestmode\n\nconst Chosen = 1\n",
	}
	for name, source := range lowered {
		if err := os.WriteFile(filepath.Join(staged, name), []byte(source), 0666); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Errorf("close support root: %v", err)
		}
	}()
	c := supportCopy{
		src:          root,
		srcDir:       src,
		staging:      staging,
		dst:          out,
		destinations: map[string]string{},
		written:      map[string]string{},
	}
	p := supportSourcePackage{src: "logos/ejecttestmode", dst: "rt/logos/ejecttestmode"}
	if err := c.copyPackage(p); err == nil || !strings.Contains(err.Error(), "ejected-mode") {
		t.Errorf("copyPackage = %v, want an ejected-mode lowering refusal", err)
	}
}

// A comment naming the branch is not a branch: the mode check reads
// tokens, so incidental "const if" text in a comment never refuses a
// package with no compile-time branch.
func TestModeDependentSupportLoweringIgnoresComments(t *testing.T) {
	files := map[string][]byte{
		"ejected.lgo": []byte("//go:build logosejected\n\npackage ejecttestcomment\n\nconst Ejected = true\n"),
		"use.lgo":     []byte("package ejecttestcomment\n\n// This package does not use const if.\n\nconst Chosen = 1\n"),
	}
	read := func(name string) ([]byte, error) { return files[name], nil }
	if modeDependentSupportLowering(read, []string{"ejected.lgo", "use.lgo"}) {
		t.Fatal("modeDependentSupportLowering refused a package whose only const-if is in a comment")
	}
}

// A tab between const and if is still a branch: the compiler accepts any
// whitespace between the keywords, so the mode check must too.
func TestModeDependentSupportLoweringSeesTabBranch(t *testing.T) {
	files := map[string][]byte{
		"ejected.lgo": []byte("//go:build logosejected\n\npackage ejecttesttab\n\nconst Native = false\n"),
		"use.lgo":     []byte("package ejecttesttab\n\nconst\tif Native {\nconst Chosen = 1\n}\n"),
	}
	read := func(name string) ([]byte, error) { return files[name], nil }
	if !modeDependentSupportLowering(read, []string{"ejected.lgo", "use.lgo"}) {
		t.Fatal("modeDependentSupportLowering missed a const-if branch separated by a tab")
	}
}

// An overlay-inserted sibling declaring the fresh alias must move the
// generated import alias too: the reservation reads the support package
// through the same effective filesystem as the copy, not the physical
// directory.
func TestSupportCopyReservesOverlaySiblingAlias(t *testing.T) {
	if os.Getenv("LOGOS_TEST_OVERLAY_ALIAS") != "1" {
		cmd := testenv.Command(t, os.Args[0], "-test.run=^TestSupportCopyReservesOverlaySiblingAlias$")
		cmd.Env = append(os.Environ(), "LOGOS_TEST_OVERLAY_ALIAS=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("overlay alias subprocess: %v\n%s", err, output)
		}
		return
	}
	fake := t.TempDir()
	pkg := filepath.Join(fake, "src", "logos", "ejecttestoverlay")
	overlayDir := filepath.Join(fake, ".overlay")
	for _, dir := range []string{pkg, overlayDir} {
		if err := os.MkdirAll(dir, 0777); err != nil {
			t.Fatal(err)
		}
	}
	const server = "package ejecttestoverlay\n\nimport \"net/http\"\n\nfunc Enable(s *http.Server) {\n\ts.EnableHTTP2ExtendedConnect()\n}\n"
	if err := os.WriteFile(filepath.Join(pkg, "server.go"), []byte(server), 0666); err != nil {
		t.Fatal(err)
	}
	const alias = "package ejecttestoverlay\n\nvar ejectedhttp = 1\n"
	if err := os.WriteFile(filepath.Join(overlayDir, "alias.go"), []byte(alias), 0666); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(struct {
		Replace map[string]string
	}{Replace: map[string]string{
		filepath.Join(pkg, "alias.go"): filepath.Join(overlayDir, "alias.go"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	fsys.OverlayFile = filepath.Join(fake, "overlay.json")
	if err := os.WriteFile(fsys.OverlayFile, data, 0666); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Init(); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	c := supportCopy{
		srcDir:       filepath.Join(fake, "src"),
		staging:      t.TempDir(),
		dst:          out,
		destinations: map[string]string{},
		written:      map[string]string{},
	}
	p := supportSourcePackage{src: "logos/ejecttestoverlay", dst: "rt/logos/ejecttestoverlay"}
	if _, _, err := c.copyPackageGo(p, "server.go", map[string]bool{}); err != nil {
		t.Fatalf("copyPackageGo: %v", err)
	}
	copied, err := os.ReadFile(filepath.Join(out, "rt", "logos", "ejecttestoverlay", "server.go"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(copied)
	if !strings.Contains(got, `ejectedhttp2 "github.com/gosuda/loGos/rt/logos/net/http"`) {
		t.Errorf("copied file does not avoid the overlaid sibling name:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp2.RequireExtendedConnect("s", s)`) {
		t.Errorf("delegation does not use the reserved alias:\n%s", got)
	}
}

// A same-named method on an imported user type is not the fork opt-in:
// the support copy rewrites only calls whose receiver resolves to
// (*net/http.Server), so the user call stays untouched while the genuine
// call in the same file still delegates to the guard.
func TestSupportCopyKeepsUserMethodCall(t *testing.T) {
	src := t.TempDir()
	pkg := filepath.Join(src, "logos", "ejecttestusercall")
	if err := os.MkdirAll(pkg, 0777); err != nil {
		t.Fatal(err)
	}
	const source = "package ejecttestusercall\n\nimport (\n\t\"net/http\"\n\n\t\"example.com/othermod/meter\"\n)\n\nfunc Enable(s *http.Server, m *meter.Meter) {\n\ts.EnableHTTP2ExtendedConnect()\n\tm.EnableHTTP2ExtendedConnect()\n}\n"
	if err := os.WriteFile(filepath.Join(pkg, "user.go"), []byte(source), 0666); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Errorf("close support root: %v", err)
		}
	}()
	c := supportCopy{
		src:          root,
		srcDir:       src,
		staging:      t.TempDir(),
		dst:          t.TempDir(),
		destinations: map[string]string{},
		written:      map[string]string{},
	}
	p := supportSourcePackage{src: "logos/ejecttestusercall", dst: "rt/logos/ejecttestusercall"}
	if _, _, err := c.copyPackageGo(p, "user.go", map[string]bool{}); err != nil {
		t.Fatalf("copyPackageGo: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(c.dst, "rt", "logos", "ejecttestusercall", "user.go"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, `ejectedhttp.RequireExtendedConnect("s", s)`) {
		t.Errorf("genuine server call was not rewritten:\n%s", got)
	}
	if !strings.Contains(got, "m.EnableHTTP2ExtendedConnect()") {
		t.Errorf("user method call was rewritten by spelling:\n%s", got)
	}
}

// A receiver the support copy cannot resolve is refused instead of
// rewritten: a call result may return a user type with the same method
// name, and eject cannot tell it from the fork method.
func TestSupportCopyRefusesUnresolvedMethodCall(t *testing.T) {
	src := t.TempDir()
	pkg := filepath.Join(src, "logos", "ejecttestunknownrecv")
	if err := os.MkdirAll(pkg, 0777); err != nil {
		t.Fatal(err)
	}
	const source = "package ejecttestunknownrecv\n\nimport \"net/http\"\n\nfunc Enable() {\n\tnewServer().EnableHTTP2ExtendedConnect()\n}\n"
	if err := os.WriteFile(filepath.Join(pkg, "user.go"), []byte(source), 0666); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Errorf("close support root: %v", err)
		}
	}()
	c := supportCopy{
		src:          root,
		srcDir:       src,
		staging:      t.TempDir(),
		dst:          t.TempDir(),
		destinations: map[string]string{},
		written:      map[string]string{},
	}
	p := supportSourcePackage{src: "logos/ejecttestunknownrecv", dst: "rt/logos/ejecttestunknownrecv"}
	_, _, err = c.copyPackageGo(p, "user.go", map[string]bool{})
	if err == nil || !strings.Contains(err.Error(), "ambiguous extended-CONNECT") {
		t.Errorf("copyPackageGo = %v, want an ambiguous extended-CONNECT refusal", err)
	}
}

// A same-named parameter in a sibling file is scoped to its function and
// cannot be selected cross-file, so it never blocks the export: the
// genuine server call still delegates to the guard.
func TestSupportCopyIgnoresSiblingParameter(t *testing.T) {
	src := t.TempDir()
	pkg := filepath.Join(src, "logos", "ejecttestparam")
	if err := os.MkdirAll(pkg, 0777); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"setup.go":  "package ejecttestparam\n\nfunc setup(EnableHTTP2ExtendedConnect bool) {}\n",
		"server.go": "package ejecttestparam\n\nimport \"net/http\"\n\nfunc Enable(s *http.Server) {\n\ts.EnableHTTP2ExtendedConnect()\n}\n",
	}
	for name, source := range files {
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(source), 0666); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Errorf("close support root: %v", err)
		}
	}()
	c := supportCopy{
		src:          root,
		srcDir:       src,
		staging:      t.TempDir(),
		dst:          t.TempDir(),
		destinations: map[string]string{},
		written:      map[string]string{},
	}
	p := supportSourcePackage{src: "logos/ejecttestparam", dst: "rt/logos/ejecttestparam"}
	if _, _, err := c.copyPackageGo(p, "server.go", map[string]bool{}); err != nil {
		t.Fatalf("copyPackageGo: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(c.dst, "rt", "logos", "ejecttestparam", "server.go"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect()") {
		t.Errorf("fork-only call survives support copy:\n%s", got)
	}
	if !strings.Contains(got, "RequireExtendedConnect") {
		t.Errorf("support copy missing guard delegation:\n%s", got)
	}
}

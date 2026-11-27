// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"bytes"
	"internal/testenv"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"cmd/go/internal/cfg"
	"cmd/go/internal/load"
	"cmd/go/internal/modinfo"
)

func TestEjectIndependentTestGraphs(t *testing.T) {
	const childOut = "LOGOS_TEST_EJECT_INDEPENDENT_GRAPHS_OUT"
	if out := os.Getenv(childOut); out != "" {
		cfg.Logos = true
		cfg.BuildContext.ToolTags = append(cfg.BuildContext.ToolTags, "logos")
		ejectToDir(t.Context(), out)
		return
	}

	goTool := testenv.GoToolPath(t)
	src, dst := t.TempDir(), t.TempDir()
	for name, text := range map[string]string{
		"go.mod":                "module example.com/eject\n\ngo 1.27\n",
		"value/value.lgo":       "package value\n\nvar number = 1\n\nfunc Get() int { return number }\n",
		"value/export_test.lgo": "package value\n\nfunc SetForTest(n int) { number = n }\n",
		"value/value_test.lgo": `package value_test

import (
	"testing"

	"example.com/eject/reader"
	"example.com/eject/value"
)

func TestReaderUsesTestVariant(t *testing.T) {
	value.SetForTest(7)
	if got := reader.Read(); got != 7 {
		t.Fatalf("reader.Read() = %d, want 7", got)
	}
}
`,
		"reader/reader.lgo": `package reader

import "example.com/eject/value"

func Read() int { return value.Get() }
`,
		"reader/reader_test.lgo": `package reader_test

import (
	"testing"

	"example.com/eject/reader"
)

func TestReaderUsesOrdinaryPackage(t *testing.T) {
	if got := reader.Read(); got != 1 {
		t.Fatalf("reader.Read() = %d, want 1", got)
	}
}
`,
	} {
		path := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(path), 0777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0666); err != nil {
			t.Fatal(err)
		}
	}
	env := []string{"GO111MODULE=on", "GOWORK=off", "GOENV=off", "GOFLAGS=", "GOPROXY=off",
		"GOSUMDB=off", "GOTOOLCHAIN=local", "GOOS=" + runtime.GOOS, "GOARCH=" + runtime.GOARCH, "CGO_ENABLED=0"}
	cmd := testenv.Command(t, testenv.Executable(t), "-test.run=^TestEjectIndependentTestGraphs$")
	cmd.Dir = src
	cmd.Env = append(cmd.Environ(), env...)
	cmd.Env = append(cmd.Env, childOut+"="+dst)
	cmd.Env = append(cmd.Env, "LOGOSEJECT_IN_VARIANT=")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("eject: %v\n%s", err, output)
	}

	cmd = testenv.Command(t, goTool, "test", "-v", "-count=1", "./...")
	cmd.Dir = dst
	cmd.Env = append(cmd.Environ(), env...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("test ejected module: %v\n%s", err, output)
	}
	for _, name := range []string{"TestReaderUsesTestVariant", "TestReaderUsesOrdinaryPackage"} {
		if !bytes.Contains(output, []byte("--- PASS: "+name+" ")) {
			t.Errorf("ejected module did not run %s:\n%s", name, output)
		}
	}
}

func findVariantContextFromLinuxAMD64(pkgDir, name string, hostTags []string) (string, string, []string, bool) {
	vc, ok := selectVariantContext(pkgDir, name, "linux", "amd64", hostTags)
	return vc.goos, vc.goarch, vc.tags, ok
}

func TestEmitPackagePlatformFiles(t *testing.T) {
	modDir := t.TempDir()
	outDir := t.TempDir()
	staging := t.TempDir()

	pkgDir := filepath.Join(modDir, "plat")
	if err := os.MkdirAll(pkgDir, 0777); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(pkgDir, "main.lgo"), []byte("package main\n\nfunc main() {}\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "plat_linux.lgo"), []byte("package main\n\nfunc Platform() string { return \"linux\" }\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "plat_windows.lgo"), []byte("package main\n\nfunc Platform() string { return \"windows\" }\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "plat_notlinux.lgo"), []byte("//go:build !linux\n\npackage main\n\nfunc NotLinux() {}\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "plat_ignore.lgo"), []byte("//go:build ignore\n\npackage main\n\nfunc Ignored() {}\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "plus_ignore.lgo"), []byte("// +build ignore\n\npackage main\n\nfunc PlusIgnored() {}\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkgDir, "cgo_helper.lgo"), []byte("package main\n\nfunc CgoHelper() {}\n"), 0666); err != nil {
		t.Fatal(err)
	}

	stagingPkg := filepath.Join(staging, "example.com/plat")
	if err := os.MkdirAll(stagingPkg, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingPkg, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingPkg, "plat_linux.go"), []byte("package main\n\nfunc Platform() string { return \"linux\" }\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingPkg, "plat_windows.go"), []byte("package main\n\nfunc Platform() string { return \"windows\" }\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingPkg, "plat_notlinux.go"), []byte("//go:build !linux\n\npackage main\n\nfunc NotLinux() {}\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stagingPkg, "cgo_helper.go"), []byte("package main\n\nfunc CgoHelper() {}\n"), 0666); err != nil {
		t.Fatal(err)
	}

	p := &load.Package{
		PackagePublic: load.PackagePublic{
			ImportPath:     "example.com/plat",
			Name:           "main",
			Dir:            pkgDir,
			Module:         &modinfo.ModulePublic{Path: "example.com/plat", Main: true, Dir: modDir},
			GoFiles:        []string{"main.lgo", "plat_linux.lgo"},
			CgoFiles:       []string{"cgo_helper.lgo"},
			IgnoredGoFiles: []string{"plat_windows.lgo", "plat_notlinux.lgo", "plat_ignore.lgo", "plus_ignore.lgo"},
		},
	}

	plan := &ejectPlan{
		main:      &ejectModule{dir: modDir, pkgs: []*load.Package{p}},
		testBuilt: map[*load.Package]bool{},
	}

	emitPackageGoFiles(p, plan, staging, outDir, modDir)

	if _, err := os.Stat(filepath.Join(outDir, "plat", "main.go")); err != nil {
		t.Errorf("main.go missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "plat", "plat_linux.go")); err != nil {
		t.Errorf("plat_linux.go missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "plat", "plat_windows.go")); err != nil {
		t.Errorf("plat_windows.go missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "plat", "plat_notlinux.go")); err != nil {
		t.Errorf("plat_notlinux.go missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "plat", "cgo_helper.go")); err != nil {
		t.Errorf("cgo_helper.go missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "plat", "plat_ignore.go")); !os.IsNotExist(err) {
		t.Errorf("plat_ignore.go should not be emitted, stat err: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "plat", "plus_ignore.go")); !os.IsNotExist(err) {
		t.Errorf("plus_ignore.go should not be emitted, stat err: %v", err)
	}
}

func TestFindVariantContextPlatformLogosSyntax(t *testing.T) {
	modDir := t.TempDir()
	pkgDir := filepath.Join(modDir, "plat")
	if err := os.MkdirAll(pkgDir, 0777); err != nil {
		t.Fatal(err)
	}
	linuxSrc := "package main\n\nfunc Platform() string {\n\tvar u *User? = &User{Name: \"linux\"}\n\treturn (u?.Name ?? \"fallback\")\n}\n"
	windowsSrc := "package main\n\nfunc Platform() string {\n\tvar u *User? = &User{Name: \"windows\"}\n\treturn (u?.Name ?? \"fallback\")\n}\n"
	arm64Src := "package main\n\nfunc PlatformARM64() string {\n\tvar u *User? = &User{Name: \"windows-arm64\"}\n\treturn (u?.Name ?? \"fallback\")\n}\n"
	for name, src := range map[string]string{
		"plat_linux.lgo":         linuxSrc,
		"plat_windows.lgo":       windowsSrc,
		"plat_windows_arm64.lgo": arm64Src,
	} {
		if err := os.WriteFile(filepath.Join(pkgDir, name), []byte(src), 0666); err != nil {
			t.Fatal(err)
		}
	}
	if goos, goarch, _, ok := findVariantContextFromLinuxAMD64(pkgDir, "plat_windows.lgo", nil); !ok || goos != "windows" {
		t.Errorf("plat_windows.lgo context = %q/%q ok=%v, want windows", goos, goarch, ok)
	}
	if goos, goarch, _, ok := findVariantContextFromLinuxAMD64(pkgDir, "plat_windows_arm64.lgo", nil); !ok || goos != "windows" || goarch != "arm64" {
		t.Errorf("plat_windows_arm64.lgo context = %q/%q ok=%v, want windows/arm64", goos, goarch, ok)
	}
}

func TestSupportRelPathMapping(t *testing.T) {
	cases := map[string]string{
		"logos/rt":              "",
		"logos/rt/json":         "json",
		"logos/json":            "logos/json",
		"logos/arena":           "arena",
		"logos/sync":            "sync",
		"logos/ascii":           "logos/ascii",
		"logos/enum":            "logos/enum",
		"logos/proc":            "logos/proc",
		"logos/leak":            "logos/leak",
		"logos/io":              "logos/io",
		"logos/log":             "logos/log",
		"logos/strconv":         "logos/strconv",
		"logos/net/http":        "net/http",
		"logos/encoding/base64": "logos/encoding/base64",
	}
	for path, want := range cases {
		got, ok := supportRelPath(path)
		if !ok {
			t.Errorf("supportRelPath(%q) = not ok, want %q", path, want)
			continue
		}
		if got != want {
			t.Errorf("supportRelPath(%q) = %q, want %q", path, got, want)
		}
	}
	for _, path := range []string{"fmt", "example.com/logos/rt", "logosrt", ""} {
		if rel, ok := supportRelPath(path); ok {
			t.Errorf("supportRelPath(%q) = %q, ok, want not ok", path, rel)
		}
	}
}

func TestSupportDestinationsDistinct(t *testing.T) {
	dst, err := supportDestinations([]string{"logos/rt", "logos/rt/json", "logos/json", "logos/arena"})
	if err != nil {
		t.Fatalf("supportDestinations: %v", err)
	}
	want := map[string]string{"": "logos/rt", "json": "logos/rt/json", "logos/json": "logos/json", "arena": "logos/arena"}
	if len(dst) != len(want) {
		t.Fatalf("supportDestinations = %v, want %v", dst, want)
	}
	for rel, path := range want {
		if dst[rel] != path {
			t.Errorf("supportDestinations[%q] = %q, want %q", rel, dst[rel], path)
		}
	}
}

func TestSupportDestinationsCollision(t *testing.T) {
	want := "support packages logos/json and logos/rt/logos/json map to the same path logos/json"
	for _, paths := range [][]string{
		{"logos/json", "logos/rt/logos/json"},
		{"logos/rt/logos/json", "logos/json"},
		{"logos/rt", "logos/json", "logos/rt/logos/json"},
	} {
		_, err := supportDestinations(paths)
		if err == nil {
			t.Errorf("supportDestinations(%v) = nil error, want collision", paths)
			continue
		}
		if err.Error() != want {
			t.Errorf("supportDestinations(%v) error = %q, want %q", paths, err.Error(), want)
		}
	}
}

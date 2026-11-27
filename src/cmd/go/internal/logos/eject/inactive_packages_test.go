// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cmd/go/internal/load"
	"cmd/go/internal/modinfo"
)

func TestInactiveCensusIncludesUnloadedPackages(t *testing.T) {
	root, dependency, staging := t.TempDir(), t.TempDir(), t.TempDir()
	write := func(dir, name, source string) {
		t.Helper()
		file := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(file), 0777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(source), 0666); err != nil {
			t.Fatal(err)
		}
	}
	write(root, "go.mod", "module example.com/main\n\ngo 1.27\n")
	write(dependency, "go.mod", "module example.com/dependency\n\ngo 1.27\n")
	for _, name := range []string{"active.lgo", "winonly/feature_windows.lgo", "archonly/feature_arm64.lgo", "onlytests/feature_windows_test.lgo"} {
		write(root, name, "package p\n")
	}
	write(root, "tagonly/feature.lgo", "//go:build extra\n\npackage p\n")
	write(root, "tagonly/ignored.lgo", "//go:build ignore\n\npackage p\n")
	write(root, "_ignored_file.lgo", "package ignored\n")
	write(root, ".ignored_file.lgo", "package ignored\n")
	for _, dir := range []string{"testdata", "vendor", ".hidden", "_scratch", "nested"} {
		write(root, dir+"/never_windows.lgo", "package excluded\n")
	}
	write(root, "nested/go.mod", "module example.com/nested\n\ngo 1.27\n")
	write(dependency, "active.lgo", "package dep\n")
	write(dependency, "winonly/feature_windows.lgo", "package dep\n")
	write(staging, "example.com/main/active.go", "package p\n")
	write(staging, "example.com/dependency/active.go", "package dep\n")
	main := &load.Package{PackagePublic: load.PackagePublic{
		Dir: root, ImportPath: "example.com/main", Name: "p", GoFiles: []string{"active.lgo"},
		Module: &modinfo.ModulePublic{Path: "example.com/main", Main: true, Dir: root},
	}}
	dep := &load.Package{PackagePublic: load.PackagePublic{
		Dir: dependency, ImportPath: "example.com/dependency", Name: "dep", GoFiles: []string{"active.lgo"},
		Module: &modinfo.ModulePublic{Path: "example.com/dependency", Version: "v1.0.0", Dir: dependency},
	}}
	plan := &ejectPlan{
		main:  &ejectModule{path: "example.com/main", dir: root, pkgs: []*load.Package{main}},
		deps:  []*ejectModule{{path: "example.com/dependency", version: "v1.0.0", dir: dependency, pkgs: []*load.Package{dep}}},
		logos: []*load.Package{main, dep}, testBuilt: map[*load.Package]bool{},
	}
	var got []string
	for _, file := range collectInactiveLogosFiles(plan, staging) {
		if file.pkg.Module == nil {
			t.Fatalf("%s has no module ownership", file.name)
		}
		got = append(got, file.pkg.ImportPath+"/"+file.name)
	}
	slices.Sort(got)
	want := []string{
		"example.com/dependency/winonly/feature_windows.lgo",
		"example.com/main/archonly/feature_arm64.lgo",
		"example.com/main/onlytests/feature_windows_test.lgo",
		"example.com/main/tagonly/feature.lgo",
		"example.com/main/winonly/feature_windows.lgo",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("inactive census = %q, want every owned unloaded package %q", got, want)
	}
	if len(plan.main.pkgs) != 1 || len(plan.deps[0].pkgs) != 1 {
		t.Fatal("source census inserted unchecked packages into compilation roots")
	}
}

func TestInactiveCensusHandlesNoHostPackages(t *testing.T) {
	root := t.TempDir()
	for name, source := range map[string]string{
		"go.mod":           "module example.com/inactive\n\ngo 1.27\n",
		"only_windows.lgo": "package inactive\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(source), 0666); err != nil {
			t.Fatal(err)
		}
	}
	plan := &ejectPlan{main: &ejectModule{dir: root}, testBuilt: map[*load.Package]bool{}}
	got := collectInactiveLogosFiles(plan, t.TempDir())
	if len(got) != 1 || got[0].pkg.ImportPath != "example.com/inactive" || got[0].name != "only_windows.lgo" {
		t.Fatalf("module without host packages produced %v, want example.com/inactive/only_windows.lgo", got)
	}
}

func TestCgoOnlyGapSkipsPureGoClosure(t *testing.T) {
	src := t.TempDir()
	write := func(dir, name, source string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(src, dir), 0777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(src, dir, name), []byte(source), 0666); err != nil {
			t.Fatal(err)
		}
	}
	write("logos/pure", "a.go", "package pure\n")
	write("logos/mid", "m.go", "package mid\nimport _ \"logos/pure\"\n")
	write("logos/withlgo", "w.lgo", "package withlgo\n")
	write("logos/mixed", "m.go", "package mixed\nimport _ \"logos/withlgo\"\n")
	for imp, want := range map[string]bool{
		"logos/pure": false, "logos/mid": false,
		"logos/withlgo": true, "logos/mixed": true, "logos/missing": true,
	} {
		if got := logosNeedsLowering(src, imp, map[string]bool{}); got != want {
			t.Errorf("logosNeedsLowering(%s) = %v, want %v", imp, got, want)
		}
	}
}

func TestCgoOnlyGapPureGoHelper(t *testing.T) {
	dir := t.TempDir()
	cgo := filepath.Join(dir, "helper_linux.go")
	if err := os.WriteFile(cgo, []byte("package p\nimport \"C\"\nimport _ \"logos/ascii\"\n"), 0666); err != nil {
		t.Fatal(err)
	}
	plan := &ejectPlan{provenance: map[string][]*load.Package{}}
	if msg, ok := cgoOnlyLogosGap(cgo, plan); ok {
		t.Errorf("pure-Go helper reported as cgo gap: %s", msg)
	}
	withlgo := filepath.Join(dir, "needs_lowering.go")
	if err := os.WriteFile(withlgo, []byte("package p\nimport \"C\"\nimport _ \"logos/json\"\n"), 0666); err != nil {
		t.Fatal(err)
	}
	msg, ok := cgoOnlyLogosGap(withlgo, plan)
	if !ok || !strings.Contains(msg, "logos/json") {
		t.Errorf("LoGOs helper gap = %q, %v; want a gap naming logos/json", msg, ok)
	}
}

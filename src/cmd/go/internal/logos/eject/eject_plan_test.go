// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"cmd/go/internal/load"
	"cmd/go/internal/modinfo"
)

// The export copies the main module and selected dependency modules
// only. A plain dependency left behind keeps no rewrite-ambiguity check:
// the text rewrite never touches its files, so a same-named user call
// there is safe. Copied packages keep the full check, which is the only
// guard for the unfiltered rewrites their copies go through.
func TestDowngradeEjectCheckFlags(t *testing.T) {
	root := t.TempDir()
	writeGoMod := func(dir, content string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(content), 0666); err != nil {
			t.Fatal(err)
		}
	}
	mainDir := filepath.Join(root, "main")
	writeGoMod(mainDir, "module example.com/main\n\ngo 1.27\n\nrequire example.com/nesteddep v0.0.0\n\nreplace example.com/nesteddep => ./nested\n")
	nestedDir := filepath.Join(mainDir, "nested")
	writeGoMod(nestedDir, "module example.com/nesteddep\n\ngo 1.27\n")
	skippedDir := filepath.Join(mainDir, "skipped")
	writeGoMod(skippedDir, "module example.com/skippeddep\n\ngo 1.27\n")
	copiedDir := filepath.Join(root, "copieddep")
	writeGoMod(copiedDir, "module example.com/copieddep\n\ngo 1.27\n")
	plainDir := filepath.Join(root, "plaindep")
	writeGoMod(plainDir, "module example.com/plaindep\n\ngo 1.27\n")
	check := []string{"-logos.eject_check"}
	portability := []string{"-logos.eject_check_portability"}
	mainPkg := &load.Package{
		ImportPath: "example.com/main",
		Dir:        mainDir,
		Module:     &modinfo.ModulePublic{Path: "example.com/main", Main: true},
		Internal:   load.PackageInternal{Gcflags: slices.Clone(check)},
	}
	copiedPkg := &load.Package{
		ImportPath: "example.com/copieddep/dep",
		Dir:        copiedDir,
		Module:     &modinfo.ModulePublic{Path: "example.com/copieddep"},
		Internal:   load.PackageInternal{Gcflags: slices.Clone(check)},
	}
	keptPkg := &load.Package{
		ImportPath: "example.com/nesteddep/dep",
		Dir:        nestedDir,
		Module:     &modinfo.ModulePublic{Path: "example.com/nesteddep"},
		Internal:   load.PackageInternal{Gcflags: slices.Clone(check)},
	}
	skippedPkg := &load.Package{
		ImportPath: "example.com/skippeddep/dep",
		Dir:        skippedDir,
		Module:     &modinfo.ModulePublic{Path: "example.com/skippeddep"},
		Internal:   load.PackageInternal{Gcflags: slices.Clone(check)},
	}
	leftPkg := &load.Package{
		ImportPath: "example.com/plaindep/dep",
		Dir:        plainDir,
		Module:     &modinfo.ModulePublic{Path: "example.com/plaindep"},
		Internal:   load.PackageInternal{Gcflags: slices.Clone(check)},
	}
	plainPkg := &load.Package{
		ImportPath: "example.com/plaindep/other",
		Dir:        plainDir,
		Module:     &modinfo.ModulePublic{Path: "example.com/plaindep"},
	}
	stdPkg := &load.Package{ImportPath: "net/http"}
	plan := &ejectPlan{
		main:      &ejectModule{path: "example.com/main", dir: mainDir, pkgs: []*load.Package{mainPkg}},
		depByPath: map[string]*ejectModule{},
		provenance: map[string][]*load.Package{
			"example.com/main":           {mainPkg},
			"example.com/copieddep/dep":  {copiedPkg},
			"example.com/nesteddep/dep":  {keptPkg},
			"example.com/skippeddep/dep": {skippedPkg},
			"example.com/plaindep/dep":   {leftPkg},
			"example.com/plaindep/other": {plainPkg},
			"net/http":                   {stdPkg},
		},
	}
	plan.depByPath["example.com/copieddep"] = &ejectModule{path: "example.com/copieddep", dir: copiedDir, pkgs: []*load.Package{copiedPkg}}
	plan.depByPath["example.com/plaindep"] = &ejectModule{path: "example.com/plaindep", dir: plainDir, pkgs: []*load.Package{leftPkg, plainPkg}}
	plan.depByPath["example.com/nesteddep"] = &ejectModule{path: "example.com/nesteddep", dir: nestedDir, pkgs: []*load.Package{keptPkg}}
	plan.depByPath["example.com/skippeddep"] = &ejectModule{path: "example.com/skippeddep", dir: skippedDir, pkgs: []*load.Package{skippedPkg}}
	plan.deps = []*ejectModule{plan.depByPath["example.com/copieddep"]}

	downgradeEjectCheckFlags(plan, t.TempDir())

	if got := mainPkg.Internal.Gcflags; !slices.Equal(got, check) {
		t.Errorf("main package flags = %v, want %v", got, check)
	}
	if got := copiedPkg.Internal.Gcflags; !slices.Equal(got, check) {
		t.Errorf("copied dependency flags = %v, want %v", got, check)
	}
	if got := keptPkg.Internal.Gcflags; !slices.Equal(got, check) {
		t.Errorf("nested kept dependency flags = %v, want %v", got, check)
	}
	if got := skippedPkg.Internal.Gcflags; !slices.Equal(got, portability) {
		t.Errorf("nested skipped dependency flags = %v, want %v", got, portability)
	}
	if got := leftPkg.Internal.Gcflags; !slices.Equal(got, portability) {
		t.Errorf("left-behind dependency flags = %v, want %v", got, portability)
	}
	if got := plainPkg.Internal.Gcflags; len(got) != 0 {
		t.Errorf("flagless package flags = %v, want no flags", got)
	}
	if got := stdPkg.Internal.Gcflags; len(got) != 0 {
		t.Errorf("standard package flags = %v, want no flags", got)
	}
}

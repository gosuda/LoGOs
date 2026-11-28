// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
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
	check := []string{"-logos.eject_check"}
	portability := []string{"-logos.eject_check_portability"}
	mainPkg := &load.Package{
		ImportPath: "example.com/main",
		Module:     &modinfo.ModulePublic{Path: "example.com/main", Main: true},
		Internal:   load.PackageInternal{Gcflags: slices.Clone(check)},
	}
	copiedPkg := &load.Package{
		ImportPath: "example.com/copieddep/dep",
		Module:     &modinfo.ModulePublic{Path: "example.com/copieddep"},
		Internal:   load.PackageInternal{Gcflags: slices.Clone(check)},
	}
	leftPkg := &load.Package{
		ImportPath: "example.com/plaindep/dep",
		Module:     &modinfo.ModulePublic{Path: "example.com/plaindep"},
		Internal:   load.PackageInternal{Gcflags: slices.Clone(check)},
	}
	plainPkg := &load.Package{
		ImportPath: "example.com/plaindep/other",
		Module:     &modinfo.ModulePublic{Path: "example.com/plaindep"},
	}
	stdPkg := &load.Package{ImportPath: "net/http"}
	plan := &ejectPlan{
		main:      &ejectModule{path: "example.com/main", pkgs: []*load.Package{mainPkg}},
		depByPath: map[string]*ejectModule{},
		provenance: map[string][]*load.Package{
			"example.com/main":           {mainPkg},
			"example.com/copieddep/dep":  {copiedPkg},
			"example.com/plaindep/dep":   {leftPkg},
			"example.com/plaindep/other": {plainPkg},
			"net/http":                   {stdPkg},
		},
	}
	plan.depByPath["example.com/copieddep"] = &ejectModule{path: "example.com/copieddep", pkgs: []*load.Package{copiedPkg}}
	plan.depByPath["example.com/plaindep"] = &ejectModule{path: "example.com/plaindep", pkgs: []*load.Package{leftPkg, plainPkg}}
	plan.deps = []*ejectModule{plan.depByPath["example.com/copieddep"]}

	downgradeEjectCheckFlags(plan)

	if got := mainPkg.Internal.Gcflags; !slices.Equal(got, check) {
		t.Errorf("main package flags = %v, want %v", got, check)
	}
	if got := copiedPkg.Internal.Gcflags; !slices.Equal(got, check) {
		t.Errorf("copied dependency flags = %v, want %v", got, check)
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

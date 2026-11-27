// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"encoding/json"
	"internal/testenv"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"cmd/go/internal/cfg"
	"cmd/go/internal/fsys"
	"cmd/go/internal/load"
)

func TestInactiveCensusHonorsOverlay(t *testing.T) {
	// fsys initializes one immutable overlay per process, as cmd/go does.
	if os.Getenv("LOGOS_TEST_INACTIVE_OVERLAY") != "1" {
		cmd := testenv.Command(t, os.Args[0], "-test.run=^TestInactiveCensusHonorsOverlay$")
		cmd.Env = append(os.Environ(), "LOGOS_TEST_INACTIVE_OVERLAY=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("overlay census subprocess: %v\n%s", err, output)
		}
		return
	}
	root := t.TempDir()
	for name, source := range map[string]string{
		"go.mod":                "module example.com/overlay\n\ngo 1.27\n",
		"active.lgo":            "package overlay\n",
		"removed.lgo":           "package overlay\n",
		"replaced.lgo":          "//go:build physicalOnly\n\npackage overlay\n",
		"ignored.lgo":           "//go:build physicalOnly\n\npackage overlay\n",
		".overlay/replaced.lgo": "//go:build overlay_extra\n\npackage overlay\n",
		".overlay/ignored.lgo":  "//go:build ignore\n\npackage overlay\n",
		".overlay/inserted.lgo": "//go:build overlay_extra\n\npackage overlay\n",
		".overlay/virtual.lgo":  "//go:build overlay_extra\n\npackage virtual\n",
	} {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(source), 0666); err != nil {
			t.Fatal(err)
		}
	}
	replacements := map[string]string{
		filepath.Join(root, "removed.lgo"):     "",
		filepath.Join(root, "replaced.lgo"):    filepath.Join(root, ".overlay/replaced.lgo"),
		filepath.Join(root, "ignored.lgo"):     filepath.Join(root, ".overlay/ignored.lgo"),
		filepath.Join(root, "inserted.lgo"):    filepath.Join(root, ".overlay/inserted.lgo"),
		filepath.Join(root, "virtual/new.lgo"): filepath.Join(root, ".overlay/virtual.lgo"),
	}
	data, err := json.Marshal(struct{ Replace map[string]string }{replacements})
	if err != nil {
		t.Fatal(err)
	}
	fsys.OverlayFile = filepath.Join(root, "overlay.json")
	if err := os.WriteFile(fsys.OverlayFile, data, 0666); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Init(); err != nil {
		t.Fatal(err)
	}
	pkg := &load.Package{PackagePublic: load.PackagePublic{
		Dir: root, ImportPath: "example.com/overlay", GoFiles: []string{"active.lgo"},
	}}
	plan := &ejectPlan{
		main:  &ejectModule{path: "example.com/overlay", dir: root},
		logos: []*load.Package{pkg}, testBuilt: map[*load.Package]bool{},
	}
	files := collectInactiveLogosFiles(plan, t.TempDir())
	var got []string
	for _, file := range files {
		got = append(got, file.pkg.ImportPath+"/"+file.name)
		vc, ok := selectVariantContext(file.pkg.Dir, file.name, cfg.BuildContext.GOOS, cfg.BuildContext.GOARCH, nil)
		if !ok || !slices.Equal(vc.tags, []string{"overlay_extra"}) {
			t.Errorf("%s selects tags %q (ok=%v), want effective overlay constraint", file.name, vc.tags, ok)
		}
	}
	want := []string{
		"example.com/overlay/inserted.lgo",
		"example.com/overlay/replaced.lgo",
		"example.com/overlay/virtual/new.lgo",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("inactive census = %q, want effective sources %q", got, want)
	}
}

func TestEjectedPureGoPackageHonorsOverlay(t *testing.T) {
	if os.Getenv("LOGOS_TEST_PUREGO_OVERLAY") != "1" {
		cmd := testenv.Command(t, os.Args[0], "-test.run=^TestEjectedPureGoPackageHonorsOverlay$")
		cmd.Env = append(os.Environ(), "LOGOS_TEST_PUREGO_OVERLAY=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("overlay purego subprocess: %v\n%s", err, output)
		}
		return
	}
	fake := t.TempDir()
	helper := filepath.Join(fake, "src", "logos", "helper")
	helper2 := filepath.Join(fake, "src", "logos", "helper2")
	overlayDir := filepath.Join(fake, ".overlay")
	for _, dir := range []string{helper, helper2, overlayDir} {
		if err := os.MkdirAll(dir, 0777); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(helper, "helper.lgo"), []byte("package helper\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(helper, "helper.go"), []byte("package helper\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(helper2, "helper.go"), []byte("package helper2\n"), 0666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overlayDir, "inserted.lgo"), []byte("package helper2\n"), 0666); err != nil {
		t.Fatal(err)
	}
	replacements := map[string]string{
		filepath.Join(helper, "helper.lgo"):    "",
		filepath.Join(helper2, "inserted.lgo"): filepath.Join(overlayDir, "inserted.lgo"),
	}
	data, err := json.Marshal(struct{ Replace map[string]string }{replacements})
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
	src := filepath.Join(fake, "src")
	if got := logosNeedsLowering(src, "logos/helper", map[string]bool{}); got {
		t.Errorf("logosNeedsLowering(helper) with overlay deletion = true, want false")
	}
	if got := ejectedPureGoPackage(src, "logos/helper"); !got {
		t.Errorf("ejectedPureGoPackage(helper) with overlay deletion = false, want true")
	}
	if got := logosNeedsLowering(src, "logos/helper2", map[string]bool{}); !got {
		t.Errorf("logosNeedsLowering(helper2) with overlay insertion = false, want true")
	}
	if got := ejectedPureGoPackage(src, "logos/helper2"); got {
		t.Errorf("ejectedPureGoPackage(helper2) with overlay insertion = true, want false")
	}
}

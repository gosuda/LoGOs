// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lsp

import (
	"encoding/json"
	"internal/testenv"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const workspaceA = "// Package a reads files.\n" + aLgo

func realWorkspace(t *testing.T) (root string, tc *toolchain) {
	t.Helper()
	testenv.MustHaveGoBuild(t)
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOPROXY", "off")
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"go.work":           "go 1.27\n\nuse (\n\t./lmod\n\t./gomod\n)\n",
		"lmod/go.mod":       "module example.com/lmod\n\ngo 1.27\n",
		"lmod/a/a.lgo":      workspaceA,
		"lmod/app/main.lgo": "package main\n\nfunc main() {}\n",
		"gomod/go.mod":      "module example.com/gomod\n\ngo 1.27\n\nrequire example.com/lmod v0.0.0\n\nreplace example.com/lmod => ../lmod\n",
		"gomod/b/b.go":      "package b\n\nimport \"example.com/lmod/a\"\n\nvar _ = a.Read\n",
		"gomod/plain/p.go":  "package plain\n\nfunc F() int { return 1 }\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o666); err != nil {
			t.Fatal(err)
		}
	}
	tc = newToolchain(testenv.GoToolPath(t))
	return root, tc
}

func TestToolchainLowersImportedLogosPackage(t *testing.T) {
	root, tc := realWorkspace(t)
	ctx := t.Context()
	aPath, bDir := filepath.Join(root, "lmod", "a", "a.lgo"), filepath.Join(root, "gomod", "b")

	low, err := tc.Lower(ctx, bDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(low.Missing) != 0 || len(low.Files) != 1 || low.Files[0].Path != aPath {
		t.Fatalf("lowering = %+v, want the one file %s", low, aPath)
	}
	for _, dir := range []string{filepath.Dir(aPath), bDir} {
		if !slices.Contains(low.Dirs, dir) {
			t.Errorf("closure dirs %v lack %s", low.Dirs, dir)
		}
	}
	file := low.Files[0]
	if string(file.Src) != workspaceA {
		t.Errorf("source read by the compiler = %q, want the file on disk", file.Src)
	}
	pm, err := newPosMap(file.Src, file.Out, file.Map)
	if err != nil {
		t.Fatalf("the compiler's edit map does not fit its texts: %v", err)
	}
	out := string(file.Out)
	if !strings.Contains(out, "os.ReadFile(path)") || strings.Contains(out, "?\n") {
		t.Errorf("lowered text does not hold the lowered statement:\n%s", out)
	}

	orig := shift(findPos(t, workspaceA, "ReadFile(path)?", 0), len("ReadFile("))
	lowPos := pm.toLowered(orig)
	if got := lineOf(out, lowPos.Line)[lowPos.Character:]; !strings.HasPrefix(got, "path)") {
		t.Errorf("operand %v maps to lowered %v, which starts %q", orig, lowPos, got)
	}
	if back := pm.toOriginal(lowPos); back != orig {
		t.Errorf("operand round trip %v -> %v -> %v", orig, lowPos, back)
	}
	q := findPos(t, workspaceA, "?", 0)
	end := findPos(t, workspaceA, "return string(data)", 0)
	lowEnd := pm.toLowered(end)
	if lines := lowEnd.Line - pm.toLowered(q).Line; lines < 2 {
		t.Errorf("the ? statement expands to %d more lines in the lowered text, want at least 2", lines)
	}

	for _, stray := range []string{"lmod/a/a.go", "lmod/a/a.go.edits", "gomod/b/b.go.edits"} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(stray))); err == nil {
			t.Errorf("lowering wrote %s into the workspace", stray)
		}
	}

	edited := "// unsaved\n" + workspaceA
	low, err = tc.Lower(ctx, bDir, map[string][]byte{aPath: []byte(edited)})
	if err != nil {
		t.Fatal(err)
	}
	if len(low.Files) != 1 || string(low.Files[0].Src) != edited || !strings.Contains(string(low.Files[0].Out), "// unsaved") {
		t.Errorf("an unsaved buffer was not lowered: %+v", low)
	}
	onDisk, _ := os.ReadFile(aPath)
	if string(onDisk) != workspaceA {
		t.Errorf("the file on disk changed to %q", onDisk)
	}
}

func TestToolchainWritesNoBinaryForMainPackage(t *testing.T) {
	root, tc := realWorkspace(t)
	appDir := filepath.Join(root, "lmod", "app")
	before := listDir(t, appDir)
	low, err := tc.Lower(t.Context(), appDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(appDir, "main.lgo"); len(low.Files) != 1 || low.Files[0].Path != want || len(low.Missing) != 0 {
		t.Fatalf("lowering = %+v, want the one file %s", low, want)
	}
	if after := listDir(t, appDir); !slices.Equal(before, after) {
		t.Errorf("the package directory held %v and holds %v after lowering", before, after)
	}
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

func TestToolchainReportsBrokenSourceAsMissing(t *testing.T) {
	root, tc := realWorkspace(t)
	ctx := t.Context()
	aDir := filepath.Join(root, "lmod", "a")
	aPath := filepath.Join(aDir, "a.lgo")
	broken := strings.Replace(workspaceA, "return string(data), nil", "x := 1\n\treturn string(data), nil", 1)

	low, err := tc.Lower(ctx, aDir, map[string][]byte{aPath: []byte(broken)})
	if err != nil {
		t.Fatal(err)
	}
	if len(low.Files) != 0 || !slices.Equal(low.Missing, []string{aPath}) {
		t.Errorf("lowering of a broken package = %+v, want %s missing", low, aPath)
	}
	diags, err := tc.Diagnose(ctx, aDir, map[string][]byte{aPath: []byte(broken)})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range diags {
		if d.File == aPath && strings.Contains(d.Message, "declared and not used") && d.Span.StartLine == 8 {
			found = true
		}
	}
	if !found {
		t.Errorf("diagnostics = %+v, want declared and not used at line 8 of %s", diags, aPath)
	}
	diags, err = tc.Diagnose(ctx, aDir, nil)
	if err != nil || len(diags) != 0 {
		t.Errorf("diagnostics of the good package = %+v, %v, want none", diags, err)
	}
}

func TestToolchainDiagnosesGoPackages(t *testing.T) {
	root, tc := realWorkspace(t)
	pDir := filepath.Join(root, "gomod", "plain")
	pPath := filepath.Join(pDir, "p.go")
	diags, err := tc.Diagnose(t.Context(), pDir, map[string][]byte{
		pPath: []byte("package plain\n\nfunc F() int { var y int; return 1 }\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(diags) != 1 || diags[0].File != pPath || !strings.Contains(diags[0].Message, "declared and not used") {
		t.Errorf("diagnostics = %+v, want one declared and not used in %s", diags, pPath)
	}
}

func TestToolchainHints(t *testing.T) {
	root, tc := realWorkspace(t)
	pkgDir := filepath.Join(root, "lmod", "a")

	cases := []struct {
		name       string
		experiment string
		set        bool
	}{
		{name: "default", set: false},
		{name: "logosfastcgo", experiment: "logosfastcgo", set: true},
		{name: "nologosfastcgo", experiment: "nologosfastcgo", set: true},
		{name: "none", experiment: "none", set: true},
	}
	for _, tcCase := range cases {
		t.Run(tcCase.name, func(t *testing.T) {
			if tcCase.set {
				t.Setenv("GOEXPERIMENT", tcCase.experiment)
			}
			hints, err := tc.Hints(t.Context(), pkgDir, nil)
			if err != nil {
				t.Fatalf("Hints failed: %v", err)
			}
			var report struct {
				SchemaVersion string `json:"schemaVersion"`
				Package       string `json:"package"`
				Files         []struct {
					File     string `json:"file"`
					Narrowed []any  `json:"narrowed"`
					Unions   []any  `json:"unions"`
					Sites    []struct {
						Kind    string `json:"kind"`
						Lowered string `json:"lowered"`
					} `json:"sites"`
					Allocs []struct {
						Tier    string `json:"tier"`
						Message string `json:"message"`
					} `json:"allocs"`
				} `json:"files"`
			}
			if err := json.Unmarshal(hints, &report); err != nil {
				t.Fatalf("hints is not a valid HintsReport: %v\n%s", err, hints)
			}
			if report.SchemaVersion != "1.0" {
				t.Errorf("report schemaVersion = %q, want 1.0", report.SchemaVersion)
			}
			if report.Package != "example.com/lmod/a" {
				t.Errorf("report package = %q, want example.com/lmod/a", report.Package)
			}
			if len(report.Files) != 1 || filepath.Base(report.Files[0].File) != "a.lgo" {
				t.Fatalf("report files = %+v, want one a.lgo entry", report.Files)
			}
			if len(report.Files[0].Sites) != 1 || report.Files[0].Sites[0].Kind != "try" {
				t.Errorf("report sites = %+v, want one try site", report.Files[0].Sites)
			}
		})
	}
}

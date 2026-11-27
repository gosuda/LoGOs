// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"golang.org/x/mod/modfile"
)

func writeReplaceFixture(t *testing.T, dir, name, text string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0666); err != nil {
		t.Fatal(err)
	}
	return path
}

func replaceTargets(f *modfile.File) map[string]string {
	out := make(map[string]string, len(f.Replace))
	for _, r := range f.Replace {
		// The vendored DropReplace zeroes the entry in place; the ghost
		// stays in f.Replace until formatting drops its marked line.
		// Skip it here so assertions see the live directives.
		if r.Old.Path == "" {
			continue
		}
		key := r.Old.Path
		if r.Old.Version != "" {
			key += " " + r.Old.Version
		}
		out[key] = r.New.Path
	}
	return out
}

// Two versions of one module with distinct local targets keep two replace
// lines: rebasing edits each directive by its exact old path and version.
// The fixture directories quote spaces, and one absolute target outside the
// module stays absolute.
func TestRebaseLocalReplacesKeepsVersionTargets(t *testing.T) {
	orig := t.TempDir()
	dst := t.TempDir()
	outside := t.TempDir()
	// Only the origin holds the nested modules, so every relative
	// target must actually rebase onto the destination view.
	for _, dir := range []string{
		filepath.Join(orig, "with space", "lib v1"),
		filepath.Join(orig, "with space", "lib v2"),
	} {
		if err := os.MkdirAll(dir, 0777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/lib\n\ngo 1.27\n"), 0666); err != nil {
			t.Fatal(err)
		}
	}

	outsideAbs := filepath.Join(outside, "with space", "shared")
	if err := os.MkdirAll(outsideAbs, 0777); err != nil {
		t.Fatal(err)
	}

	gomod := "module example.com/app\n\ngo 1.27\n\n" +
		"replace example.com/lib v1.0.0 => " + quoteModPath("./with space/lib v1") + "\n" +
		"replace example.com/lib v1.1.0 => " + quoteModPath("./with space/lib v2") + "\n" +
		"replace example.com/wild => " + quoteModPath("./with space/lib v1") + "\n" +
		"replace example.com/pinned v1.0.0 => " + quoteModPath("./with space/lib v2") + "\n" +
		"replace example.com/outside => " + quoteModPath(outsideAbs) + "\n"
	path := writeReplaceFixture(t, orig, "go.mod", gomod)

	f := parseGoModFile(path)
	rebaseLocalReplaces(f, orig, dst, map[string]string{})

	got := replaceTargets(f)
	if len(got) != 5 {
		t.Fatalf("got %d replaces %v, want 5", len(got), got)
	}
	wantV1 := "./" + toSlashRel(t, dst, filepath.Join(orig, "with space", "lib v1"))
	if got["example.com/lib v1.0.0"] != wantV1 {
		t.Errorf("v1.0.0 target = %q, want %q", got["example.com/lib v1.0.0"], wantV1)
	}
	wantV2 := "./" + toSlashRel(t, dst, filepath.Join(orig, "with space", "lib v2"))
	if got["example.com/lib v1.1.0"] != wantV2 {
		t.Errorf("v1.1.0 target = %q, want %q", got["example.com/lib v1.1.0"], wantV2)
	}
	if wantV1 == wantV2 {
		t.Fatalf("version targets coincide: %q", wantV1)
	}
	if got["example.com/wild"] != wantV1 {
		t.Errorf("wildcard target = %q, want %q", got["example.com/wild"], wantV1)
	}
	if got["example.com/pinned v1.0.0"] != wantV2 {
		t.Errorf("pinned target = %q, want %q", got["example.com/pinned v1.0.0"], wantV2)
	}
	if got["example.com/outside"] != outsideAbs {
		t.Errorf("outside target = %q, want untouched %q", got["example.com/outside"], outsideAbs)
	}
	// The rewritten go.mod round-trips through the parser with the quoted
	// paths intact.
	formatted := modfile.Format(f.Syntax)
	if _, err := modfile.Parse(path, formatted, nil); err != nil {
		t.Fatalf("reparsing rewritten go.mod: %v\n%s", err, formatted)
	}
	writeGoModFile(path, f)
	if _, err := modfile.Parse(path, mustRead(t, path), nil); err != nil {
		t.Fatalf("reparsing written go.mod: %v", err)
	}
}

// Repointing one version of a module leaves the other versions and the
// wildcard replace alone.
func TestSetGoReplaceKeepsOtherVersions(t *testing.T) {
	dir := t.TempDir()
	path := writeReplaceFixture(t, dir, "go.mod", "module example.com/app\n\ngo 1.27\n\n"+
		"replace example.com/lib v1.0.0 => ./lib-v1\n"+
		"replace example.com/lib v1.1.0 => ./lib-v2\n"+
		"replace example.com/lib => ./lib-any\n")

	f := parseGoModFile(path)
	setGoReplace(f, "example.com/lib", "v1.0.0", "./lib-v1-new")

	got := replaceTargets(f)
	if len(got) != 3 {
		t.Fatalf("got replaces %v, want 3", got)
	}
	if got["example.com/lib v1.0.0"] != "./lib-v1-new" {
		t.Errorf("v1.0.0 target = %q, want ./lib-v1-new", got["example.com/lib v1.0.0"])
	}
	if got["example.com/lib v1.1.0"] != "./lib-v2" {
		t.Errorf("v1.1.0 target = %q, want ./lib-v2", got["example.com/lib v1.1.0"])
	}
	if got["example.com/lib"] != "./lib-any" {
		t.Errorf("wildcard target = %q, want ./lib-any", got["example.com/lib"])
	}

	setGoReplace(f, "example.com/lib", "", "./lib-any-new")
	got = replaceTargets(f)
	if got["example.com/lib"] != "./lib-any-new" {
		t.Errorf("wildcard target = %q, want ./lib-any-new", got["example.com/lib"])
	}
	if got["example.com/lib v1.0.0"] != "./lib-v1-new" {
		t.Errorf("v1.0.0 target = %q, want ./lib-v1-new", got["example.com/lib v1.0.0"])
	}
}

func quoteModPath(path string) string {
	return strconv.Quote(path)
}

func toSlashRel(t *testing.T, base, target string) string {
	t.Helper()
	rel, err := filepath.Rel(base, target)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.ToSlash(rel)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

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

// outsideModule decides the absolute-target skip before any rewrite:
// inside targets rebase at the ejected copy, the module root itself
// is not outside, and any other directory stays untouched.
func TestOutsideModuleDecision(t *testing.T) {
	orig := t.TempDir()
	if outsideModule(orig, filepath.Join(orig, "sub")) {
		t.Errorf("inside target reported outside: %q", filepath.Join(orig, "sub"))
	}
	if outsideModule(orig, orig) {
		t.Errorf("module root reported outside: %q", orig)
	}
	outside := filepath.Join(t.TempDir(), "other")
	if !outsideModule(orig, outside) {
		t.Errorf("outside target reported inside: %q", outside)
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

// A wildcard and a pinned replace of one module survive rebasing side by
// side: the wildcard edit must not match the pinned directive (the
// vendored AddReplace treats an empty old version as a match for every
// version of the path) and must keep the pin on its own target.
func TestRebaseWildcardKeepsPinnedReplace(t *testing.T) {
	orig := t.TempDir()
	dst := t.TempDir()
	for _, dir := range []string{
		filepath.Join(orig, "fallback"),
		filepath.Join(orig, "pinned"),
	} {
		if err := os.MkdirAll(dir, 0777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/nested\n\ngo 1.27\n"), 0666); err != nil {
			t.Fatal(err)
		}
	}
	path := writeGoMod(t, orig, "module example.com/app\n\ngo 1.27\n\n"+
		"replace example.com/lib => ./fallback\n"+
		"replace example.com/lib v1.0.0 => ./pinned\n")
	f := parseGoModFile(path)
	rebaseLocalReplaces(f, orig, dst, map[string]string{})
	wild := liveLibReplace(f, "")
	if wild == nil {
		t.Fatal("wildcard replace lost")
	}
	if want := "./" + toSlashRel(t, dst, filepath.Join(orig, "fallback")); wild.New.Path != want {
		t.Errorf("wildcard target = %q, want %q", wild.New.Path, want)
	}
	pin := liveLibReplace(f, "v1.0.0")
	if pin == nil {
		t.Fatal("pinned replace lost")
	}
	if want := "./" + toSlashRel(t, dst, filepath.Join(orig, "pinned")); pin.New.Path != want {
		t.Errorf("pinned target = %q, want %q", pin.New.Path, want)
	}
	formatted := modfile.Format(f.Syntax)
	if _, err := modfile.Parse(path, formatted, nil); err != nil {
		t.Fatalf("reparsing rewritten go.mod: %v\n%s", err, formatted)
	}
}

// Rebasing a wildcard keeps a versioned replacement target intact: a pin
// of the form `old vX => other vY` still names its target version after
// the wildcard moves, or the exported module fails go.mod parsing.
func TestSetGoReplaceWildcardKeepsTargetVersions(t *testing.T) {
	dir := t.TempDir()
	path := writeGoMod(t, dir, "module example.com/app\n\ngo 1.27\n\n"+
		"replace example.com/lib => ./fallback\n"+
		"replace example.com/lib v2.0.0+incompatible => example.com/other v2.1.0+incompatible\n")
	f := parseGoModFile(path)
	setGoReplace(f, "example.com/lib", "", "./fallback-new")
	wild := liveLibReplace(f, "")
	if wild == nil {
		t.Fatal("wildcard replace lost")
	}
	if wild.New.Path != "./fallback-new" {
		t.Errorf("wildcard target = %q, want ./fallback-new", wild.New.Path)
	}
	pin := liveLibReplace(f, "v2.0.0+incompatible")
	if pin == nil {
		t.Fatal("pinned replace lost")
	}
	if pin.New.Path != "example.com/other" || pin.New.Version != "v2.1.0+incompatible" {
		t.Errorf("pinned target = %q %q, want example.com/other v2.1.0+incompatible", pin.New.Path, pin.New.Version)
	}
	formatted := modfile.Format(f.Syntax)
	if _, err := modfile.Parse(path, formatted, nil); err != nil {
		t.Fatalf("reparsing rewritten go.mod: %v\n%s", err, formatted)
	}
}

// Rebasing a dependency's absolute wildcard replace into the ejected
// tree keeps a versioned pin of the same module intact: the pin still
// names its target version, or the exported module fails go.mod parsing
// because a replacement without a version must name a local directory.
func TestRebaseDepWildcardKeepsVersionedPin(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	depDir := filepath.Join(root, "dep")
	outDir := filepath.Join(root, "out")
	nested := filepath.Join(mainDir, "nested")
	outNested := filepath.Join(outDir, "nested")
	for _, dir := range []string{nested, outNested, depDir} {
		if err := os.MkdirAll(dir, 0777); err != nil {
			t.Fatal(err)
		}
	}
	// A kept nested module carries its go.mod into the export; the
	// rebase only follows targets that hold one.
	writeGoMod(t, outNested, "module example.com/nested\n\ngo 1.27\n")
	path := writeGoMod(t, depDir, "module example.com/deptestdep\n\ngo 1.27\n\n"+
		"replace example.com/lib => "+strconv.Quote(nested)+"\n"+
		"replace example.com/lib v2.0.0+incompatible => example.com/other v2.1.0+incompatible\n")
	g := parseGoModFile(path)
	plan := &ejectPlan{main: &ejectModule{dir: mainDir}}
	rebaseDepReplacesOnMain(g, map[string]string{}, plan, depDir, outDir)
	wild := liveLibReplace(g, "")
	if wild == nil {
		t.Fatal("wildcard replace lost")
	}
	if want := "./" + toSlashRel(t, depDir, outNested); wild.New.Path != want {
		t.Errorf("wildcard target = %q, want %q", wild.New.Path, want)
	}
	pin := liveLibReplace(g, "v2.0.0+incompatible")
	if pin == nil {
		t.Fatal("pinned replace lost")
	}
	if pin.New.Path != "example.com/other" || pin.New.Version != "v2.1.0+incompatible" {
		t.Errorf("pinned target = %q %q, want example.com/other v2.1.0+incompatible", pin.New.Path, pin.New.Version)
	}
	formatted := modfile.Format(g.Syntax)
	if _, err := modfile.Parse(path, formatted, nil); err != nil {
		t.Fatalf("reparsing rewritten go.mod: %v\n%s", err, formatted)
	}
}

// liveLibReplace returns the surviving replace directive for one version
// of example.com/lib, skipping the zeroed ghosts the vendored DropReplace
// leaves in f.Replace until formatting drops their marked lines. Every
// fixture below replaces one module, so the old path is fixed.
func liveLibReplace(f *modfile.File, oldVers string) *modfile.Replace {
	for _, r := range f.Replace {
		if r.Old.Path == "" {
			continue
		}
		if r.Old.Path == "example.com/lib" && r.Old.Version == oldVers {
			return r
		}
	}
	return nil
}

// writeGoMod writes dir/go.mod, creating dir first.
func writeGoMod(t *testing.T, dir, text string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0777); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "go.mod")
	if err := os.WriteFile(path, []byte(text), 0666); err != nil {
		t.Fatal(err)
	}
	return path
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

// A wildcard replace followed by two exact-version pins of one module,
// all with local targets needing relocation, rebases every directive:
// the wildcard edit drops and re-adds the file's replace lines, so the
// pass must snapshot directives by value instead of editing through the
// live pointers a previous edit already rewrote.
func TestRebaseDepWildcardFollowedByTwoLocalPins(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	depDir := filepath.Join(root, "dep")
	outDir := filepath.Join(root, "out")
	for _, dir := range []string{"fallback", "pinned1", "pinned2"} {
		for _, base := range []string{mainDir, outDir} {
			if err := os.MkdirAll(filepath.Join(base, dir), 0777); err != nil {
				t.Fatal(err)
			}
		}
		writeGoMod(t, filepath.Join(outDir, dir), "module example.com/"+dir+"\n\ngo 1.27\n")
	}
	path := writeGoMod(t, depDir, "module example.com/deptestdep\n\ngo 1.27\n\n"+
		"replace example.com/lib => "+strconv.Quote(filepath.Join(mainDir, "fallback"))+"\n"+
		"replace example.com/lib v1.0.0 => "+strconv.Quote(filepath.Join(mainDir, "pinned1"))+"\n"+
		"replace example.com/lib v1.1.0 => "+strconv.Quote(filepath.Join(mainDir, "pinned2"))+"\n")
	g := parseGoModFile(path)
	plan := &ejectPlan{main: &ejectModule{dir: mainDir}}
	rebaseDepReplacesOnMain(g, map[string]string{}, plan, depDir, outDir)
	for _, tc := range []struct{ vers, dir string }{
		{"", "fallback"},
		{"v1.0.0", "pinned1"},
		{"v1.1.0", "pinned2"},
	} {
		got := liveLibReplace(g, tc.vers)
		if got == nil {
			t.Fatalf("replace for version %q lost", tc.vers)
		}
		want := "./" + toSlashRel(t, depDir, filepath.Join(outDir, tc.dir))
		if got.New.Path != want || got.New.Version != "" {
			t.Errorf("replace for version %q = %q %q, want %q with no version", tc.vers, got.New.Path, got.New.Version, want)
		}
	}
	formatted := modfile.Format(g.Syntax)
	if _, err := modfile.Parse(path, formatted, nil); err != nil {
		t.Fatalf("reparsing rewritten go.mod: %v\n%s", err, formatted)
	}
}

// A replacement that names a parent directory is left alone when the
// export keeps a child module but omits its parent: the copy creates
// the parent directories on the way to the kept child, so a directory
// check alone mistakes the parent for the copied module. Only a
// target holding the module's go.mod is rebased.
func TestRebaseDepSkipsParentWithoutGoMod(t *testing.T) {
	root := t.TempDir()
	mainDir := filepath.Join(root, "main")
	depDir := filepath.Join(root, "dep")
	outDir := filepath.Join(root, "out")
	// Source tree: outer is its own module, inner a kept child module.
	writeGoMod(t, filepath.Join(mainDir, "outer"), "module example.com/outer\n\ngo 1.27\n")
	writeGoMod(t, filepath.Join(mainDir, "outer", "inner"), "module example.com/inner\n\ngo 1.27\n")
	// Export tree: the copy holds the child module at its main-relative
	// path; the parent directory exists only as its ancestor and has
	// no go.mod.
	writeGoMod(t, filepath.Join(outDir, "outer", "inner"), "module example.com/inner\n\ngo 1.27\n")
	outer := filepath.Join(mainDir, "outer")
	inner := filepath.Join(mainDir, "outer", "inner")
	path := writeGoMod(t, depDir, "module example.com/deptestdep\n\ngo 1.27\n\n"+
		"replace example.com/outer => "+strconv.Quote(outer)+"\n"+
		"replace example.com/inner => "+strconv.Quote(inner)+"\n")
	g := parseGoModFile(path)
	plan := &ejectPlan{main: &ejectModule{dir: mainDir}}
	rebaseDepReplacesOnMain(g, map[string]string{}, plan, depDir, outDir)
	outerGot := liveOuterReplace(g, "example.com/outer")
	if outerGot == nil {
		t.Fatal("outer replace lost")
	}
	if outerGot.New.Path != outer {
		t.Errorf("outer replace = %q, want untouched %q", outerGot.New.Path, outer)
	}
	innerGot := liveOuterReplace(g, "example.com/inner")
	if innerGot == nil {
		t.Fatal("inner replace lost")
	}
	if want := "./" + toSlashRel(t, depDir, filepath.Join(outDir, "outer", "inner")); innerGot.New.Path != want {
		t.Errorf("inner replace = %q, want %q", innerGot.New.Path, want)
	}
}

// liveOuterReplace returns the surviving replace directive for an
// arbitrary old path, skipping the zeroed ghosts the vendored
// DropReplace leaves in f.Replace.
func liveOuterReplace(f *modfile.File, oldPath string) *modfile.Replace {
	for _, r := range f.Replace {
		if r.Old.Path == "" {
			continue
		}
		if r.Old.Path == oldPath {
			return r
		}
	}
	return nil
}

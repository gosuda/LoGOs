// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package eject implements logos eject: it writes a complete pure-Go
// copy of the main module for upstream Go toolchains. Every .lgo file
// becomes a .go file through the native checker's shared lowering,
// every ejected loGo/s dependency module lands under
// DIR/_logos_deps, and the support sources become the shared module
// github.com/gosuda/loGos/rt.
package eject

import (
	"bytes"
	"context"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"internal/buildcfg"
	"internal/logosfile"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"

	"cmd/go/internal/base"
	"cmd/go/internal/cfg"
	"cmd/go/internal/fsys"
	"cmd/go/internal/load"
	"cmd/go/internal/modload"
	"cmd/go/internal/work"
	"cmd/internal/sys"
)

// rtModulePath is the shared support module for ejected trees. Every
// ejected loGo/s dependency keeps one type identity for Option and
// friends through it.
const rtModulePath = "github.com/gosuda/loGos/rt"

// rtRequireVersion pins the support module in ejected go.mod files.
// The replace line always points at the ejected copy, so the version
// never resolves over the network.
const rtRequireVersion = "v0.0.0"

// CmdEject implements the logos eject command.
var CmdEject = &base.Command{
	Run:       runEject,
	UsageLine: "logos eject [-o DIR]",
	Short:     "write a pure-Go copy of the main module",
	Long: `
Eject writes a complete pure-Go copy of the main module to DIR:
every .lgo file becomes a .go file with comments kept, every
ejected loGo/s dependency module lands under DIR/_logos_deps, and
the support sources become the shared module
github.com/gosuda/loGos/rt under DIR/_logos_deps/rt.

The copy holds every other regular file of the main module: each Go
file that still parses is rewritten to the ejected import paths and
formatted, and each remaining file is copied byte for byte.
Dot-prefixed, underscore-prefixed, vendor, testdata, and
version-control-ignored paths are copied because //go:embed can
reach them; only package discovery skips those paths. The copy
leaves out .git contents, logos.toml files, .lgo sources, the
destination subtree, symlinks and special files, and foreign
nested modules.

DIR must not exist or must be empty. DIR/go.mod keeps the go
directive of the main module, which logos mod init sets to the
version logos version reports, and the shared support module copies
it. Build the ejected tree with an upstream Go toolchain that
satisfies every go directive in it; the ejected code needs Go 1.27.2
or newer. Eject leaves the source tree unchanged apart from DIR,
which may lie inside it.
	`,
}

// ejectOut holds the required -o DIR output directory.
var ejectOut string

// EmitGo holds the logos build --emit-go directory. When set, logos
// build is an alias of logos eject into that directory.
var EmitGo string

func init() {
	CmdEject.Flag.StringVar(&ejectOut, "o", "", "")
	base.AddChdirFlag(&CmdEject.Flag)
}

// Register adds the eject subcommand and the logos-only --emit-go build
// flag to the logos root command.
func Register(root *base.Command) {
	root.Commands = append(root.Commands, CmdEject)
	build := root.Lookup("build")
	if build == nil {
		return
	}
	if build.Flag.Lookup("emit-go") == nil {
		build.Flag.StringVar(&EmitGo, "emit-go", "", "")
	}
	orig := build.Run
	build.Run = func(ctx context.Context, cmd *base.Command, args []string) {
		if EmitGo == "" {
			orig(ctx, cmd, args)
			return
		}
		ejectToDir(ctx, EmitGo)
	}
	const help = "\n\t-emit-go=DIR\n\t\twrite a pure-Go copy of the main module to DIR instead of building (logos builds only).\n"
	if !strings.Contains(build.Long, "-emit-go") {
		before, after, found := strings.Cut(build.Long, "\nSee also:")
		if found {
			build.Long = before + help + "\nSee also:" + after
			return
		}
		build.Long += help
	}
}

// runEject implements the logos eject command. Eject covers the whole
// main module, so package arguments are rejected.
func runEject(ctx context.Context, cmd *base.Command, args []string) {
	if len(args) > 0 {
		base.Fatalf("logos eject: unexpected package arguments")
	}
	ejectToDir(ctx, ejectOut)
}

// ejectModule groups the ejected packages of one module.
type ejectModule struct {
	path     string
	version  string
	dir      string
	pkgs     []*load.Package
	required bool // an actual helper load requires this module in the output
}

// ejectPlan splits the reachable packages into the main module, the
// ejected loGo/s dependency modules, and the emission targets.
// provenance retains every loaded package by import path for read-only
// declaration provenance, including standard packages (never writable)
// and test variants. Emission still uses main/deps/logos only.
type ejectPlan struct {
	main       *ejectModule
	deps       []*ejectModule
	depByPath  map[string]*ejectModule
	logos      []*load.Package
	known      map[*load.Package]bool
	testBuilt  map[*load.Package]bool
	testRoots  []*load.Package
	provenance map[string][]*load.Package
	// gorootLogos holds the reached GOROOT logos/* packages (for example
	// logos/slog) that the shared support module must materialize beyond
	// the fixed rt/arena/sync set. Standard packages are never owned, so
	// they stay out of logos and deps; they still need compiler lowering
	// into staging, which phaseOneRoots and makeEjectStagingDirs arrange.
	gorootLogos []*load.Package
}

// testBuild holds the test variant of one package for emission of its
// test files.
type testBuild struct {
	main   *load.Package
	ptest  *load.Package
	pxtest *load.Package
}

// ejectToDir writes the ejected tree to out. It loads the main module,
// compiles every loGo/s package with -logos.eject into staging, then
// assembles out from the staging output plus copied sources. The two
// builds run with ModeBuild only, so link outputs land under the build
// work directory and nothing is written into the source tree.
func ejectToDir(ctx context.Context, out string) {
	checkEjectOut(out)
	if isEjectVariant() {
		cfg.BuildContext.BuildTags = nil
		for tag := range strings.SplitSeq(os.Getenv("LOGOSEJECT_TAGS"), ",") {
			if tag != "" {
				cfg.BuildContext.BuildTags = append(cfg.BuildContext.BuildTags, tag)
			}
		}
	}
	moduleLoader := modload.NewLoader()
	moduleLoader.InitWorkfile()
	work.BuildInit(moduleLoader)
	var staging, stateDir string
	if isEjectVariant() && os.Getenv("LOGOSEJECT_STATE") == "1" {
		stateDir = variantStatePath(out)
		if err := os.Mkdir(stateDir, 0700); err != nil {
			base.Fatalf("logos eject: make variant state: %v", err)
		}
		staging = filepath.Join(stateDir, "staging")
		if err := os.Mkdir(staging, 0700); err != nil {
			base.Fatalf("logos eject: make variant staging: %v", err)
		}
	} else {
		var err error
		staging, err = os.MkdirTemp("", "logoseject")
		if err != nil {
			base.Fatalf("logos eject: %v", err)
		}
		defer func() {
			if err := os.RemoveAll(staging); err != nil {
				base.Errorf("logos eject: %v", err)
			}
		}()
	}
	load.ArmLogosEject(staging)
	defer load.DisarmLogosEject()
	// Eject builds test variants for every reachable package (see
	// collectTestBuilds), including the tests of regular dependencies,
	// so the module loader must resolve test dependencies of those
	// packages up front. Without this, a test-only import reached by
	// neither the roots nor their loaded imports is missing from the
	// loader's package set and the eject fails with
	// "cannot find package".
	opts := load.PackageOpts{AutoVCS: true, ModResolveTests: true}
	patterns := []string{"all"}
	if isEjectVariant() {
		if packages := os.Getenv("LOGOSEJECT_PACKAGES"); packages != "" {
			patterns = strings.Split(packages, "\n")
		}
	}
	pkgs := load.PackagesAndErrors(moduleLoader, ctx, opts, patterns)
	load.CheckPackageErrors(pkgs)
	if isEjectVariant() {
		// The helper loads only the packages owning inactive files, so
		// only those are roots whose test dependencies the first load
		// resolves. Reload with the full non-test closure as roots so
		// the loader also resolves the test dependencies of regular
		// dependencies; otherwise their test-only imports stay missing
		// from the loader's package set (see above).
		if roots := ejectClosureRoots(pkgs); len(roots) > 0 {
			pkgs = load.PackagesAndErrors(moduleLoader, ctx, opts, roots)
			load.CheckPackageErrors(pkgs)
		}
	}
	plan := arrangeEjectPlan(pkgs)
	builds := collectTestBuilds(moduleLoader, ctx, opts, plan)
	makeEjectStagingDirs(plan, staging, builds)
	buildEjectRoots(moduleLoader, ctx, phaseOneRoots(plan))
	if len(builds) > 0 {
		stripEjectCopies(builds)
		buildEjectRoots(moduleLoader, ctx, testBuildMains(builds))
	}
	if stateDir != "" {
		writeVariantState(plan, stateDir)
		return
	}
	var variantOuts []string
	if !isEjectVariant() {
		variantOuts = stageInactiveVariantsIntoStaging(ctx, plan, staging)
	}
	writeEjectedTree(ctx, plan, staging, out)
	for _, dir := range variantOuts {
		_ = os.RemoveAll(dir)
		_ = os.RemoveAll(variantStatePath(dir))
	}
	runEjectGofmt(ctx)
}

var ejectGofmtFiles []string

var ejectToolExeSuffix = cfg.ToolExeSuffix()

var ejectArgLengthLimit = sys.ExecArgLengthLimit

func queueEjectGofmt(path string) {
	ejectGofmtFiles = append(ejectGofmtFiles, path)
}

const ejectGofmtPasses = 3

func runEjectGofmt(ctx context.Context) {
	files := ejectGofmtFiles
	slices.Sort(files)
	files = slices.Compact(files)
	ejectGofmtFiles = nil
	gofmtFiles(ctx, "-w", files)
	for range ejectGofmtPasses {
		files = unformattedFiles(ctx, files)
		if len(files) == 0 {
			return
		}
		gofmtFiles(ctx, "-w", files)
	}
}

func unformattedFiles(ctx context.Context, files []string) []string {
	var out []string
	for line := range strings.SplitSeq(string(gofmtFiles(ctx, "-l", files)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func gofmtFiles(ctx context.Context, flag string, files []string) []byte {
	gofmt := gofmtBinary()
	args := []string{gofmt, flag}
	argLen := len(gofmt) + 1 + len(flag)
	base, baseLen := len(args), argLen
	var out []byte
	flush := func() {
		if len(args) > base {
			out = append(out, gofmtRun(ctx, args, nil)...)
			args = args[:base]
			argLen = baseLen
		}
	}
	for _, file := range files {
		if len(args) > base && argLen+1+len(file) >= ejectArgLengthLimit {
			flush()
		}
		args = append(args, file)
		argLen += 1 + len(file)
	}
	flush()
	return out
}

func gofmtRun(ctx context.Context, args []string, stdin []byte) []byte {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		base.Fatalf("logos eject: gofmt: %v: %s", err, out)
	}
	return out
}

func gofmtBinary() string {
	gofmt := "gofmt" + ejectToolExeSuffix
	p := filepath.Join(cfg.GOBIN, gofmt)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	p = filepath.Join(cfg.GOROOT, "bin", gofmt)
	if _, err := os.Stat(p); err == nil {
		return p
	}
	if p, err := exec.LookPath("gofmt"); err == nil {
		return p
	}
	base.Fatalf("logos eject: gofmt not found")
	return ""
}

// checkEjectOut rejects an output directory that exists and is not
// empty. It runs before any loading or building, so a rejected eject
// costs nothing and changes nothing.
func checkEjectOut(out string) {
	if out == "" {
		base.Fatalf("logos eject: -o DIR is required")
	}
	st, err := os.Stat(out)
	if err != nil {
		if os.IsNotExist(err) {
			return
		}
		base.Fatalf("logos eject: %s: %v", out, err)
	}
	if !st.IsDir() {
		base.Fatalf("logos eject: %s: not a directory", out)
	}
	entries, err := os.ReadDir(out)
	if err != nil {
		base.Fatalf("logos eject: %s: %v", out, err)
	}
	if len(entries) > 0 {
		base.Fatalf("logos eject: %s: directory is not empty", out)
	}
}

// arrangeEjectPlan groups the packages reachable from the main module
// into the main module entry, the loGo/s dependency modules, and the
// emission targets.
func arrangeEjectPlan(pkgs []*load.Package) *ejectPlan {
	gomod := modload.FindGoMod(base.Cwd())
	if gomod == "" {
		base.Fatalf("logos eject: no main module (run inside a module)")
	}
	plan := &ejectPlan{
		main:       &ejectModule{dir: filepath.Dir(gomod)},
		depByPath:  map[string]*ejectModule{},
		known:      map[*load.Package]bool{},
		testBuilt:  map[*load.Package]bool{},
		provenance: map[string][]*load.Package{},
	}
	for _, p := range load.PackageList(pkgs) {
		plan.provenance[p.ImportPath] = append(plan.provenance[p.ImportPath], p)
		plan.addPackage(p)
	}
	plan.gorootLogos = collectGorootLogos(plan.provenance)
	resortEjectPlan(plan)
	return plan
}

// isGorootLogos reports whether p is a GOROOT package under src/logos
// (for example logos/slog): a standard package whose import path names
// the fork's own tree. Dotless user modules cannot exist, so the prefix
// test cannot misfire on a dependency module.
func isGorootLogos(p *load.Package) bool {
	if !p.Standard && !p.Goroot {
		return false
	}
	rest, ok := strings.CutPrefix(p.ImportPath, "logos/")
	return ok && rest != ""
}

// collectGorootLogos returns one record per reached GOROOT logos/*
// package, sorted by import path for determinism. The first non-test
// record wins; test variants share the same sources for emission.
func collectGorootLogos(provenance map[string][]*load.Package) []*load.Package {
	var out []*load.Package
	for _, path := range slices.Sorted(maps.Keys(provenance)) {
		recs := provenance[path]
		if len(recs) == 0 || !isGorootLogos(recs[0]) {
			continue
		}
		pick := recs[0]
		for _, p := range recs {
			if p.ForTest == "" {
				pick = p
				break
			}
		}
		out = append(out, pick)
	}
	return out
}

// addPackage files one reachable package into the plan. Test artifacts
// stay out: test mains and test variants build in the second phase,
// never as plan roots.
func (plan *ejectPlan) addPackage(p *load.Package) {
	if plan.known[p] || p.Standard || p.Module == nil || isTestArtifact(p) {
		return
	}
	plan.known[p] = true
	if p.Module.Main {
		if p.Module.Dir != "" && p.Module.Dir != plan.main.dir {
			base.Fatalf("logos eject: workspaces with several main modules are not supported")
		}
		plan.main.pkgs = append(plan.main.pkgs, p)
	} else {
		m := plan.depByPath[p.Module.Path]
		if m == nil {
			if p.Module.Path == "" || p.Module.Version == "" {
				base.Fatalf("logos eject: dependency module has no path or version")
			}
			if p.Module.Dir == "" {
				base.Fatalf("logos eject: module %s has no directory", p.Module.Path)
			}
			m = &ejectModule{path: p.Module.Path, version: p.Module.Version, dir: p.Module.Dir}
			plan.depByPath[p.Module.Path] = m
		}
		if m.version != p.Module.Version || m.dir != p.Module.Dir {
			base.Fatalf("logos eject: module %s has several versions", p.Module.Path)
		}
		m.pkgs = append(m.pkgs, p)
	}
	if ejectOwnsLogos(p) {
		plan.logos = append(plan.logos, p)
	}
}

// resortEjectPlan rebuilds the sorted dependency and target lists after
// test-only dependencies joined the plan.
func resortEjectPlan(plan *ejectPlan) {
	plan.deps = nil
	for _, m := range plan.depByPath {
		if m.required || moduleHasLogos(m) || moduleImportsLogos(m) {
			plan.deps = append(plan.deps, m)
		}
	}
	slices.SortFunc(plan.deps, func(a, b *ejectModule) int { return strings.Compare(a.path, b.path) })
	slices.SortFunc(plan.logos, func(a, b *load.Package) int { return strings.Compare(a.ImportPath, b.ImportPath) })
	slices.SortFunc(plan.main.pkgs, func(a, b *load.Package) int { return strings.Compare(a.ImportPath, b.ImportPath) })
}

// ejectOwnsLogos reports whether p owns any loGo/s source, including
// host-inactive files. Unlike load.Package.IsLogos, which classifies by
// the first active file, eject must also lower alternate-platform files
// listed under IgnoredGoFiles: they never reach the host build, so
// without this the variant staging would never see them and the ejected
// tree would silently omit them instead of emitting their lowered Go.
func ejectOwnsLogos(p *load.Package) bool {
	if p.IsLogos() {
		return true
	}
	for _, name := range p.IgnoredGoFiles {
		if logosfile.Is(name) {
			return true
		}
	}
	return false
}

// moduleHasLogos reports whether any package of m is a loGo/s package.
func moduleHasLogos(m *ejectModule) bool {
	for _, p := range m.pkgs {
		if ejectOwnsLogos(p) {
			return true
		}
	}
	return false
}

// moduleImportsLogos reports whether any package of the module imports a
// logos support path: the module must vendor beside the support module or
// the ejected tree does not build under plain go.
func moduleImportsLogos(m *ejectModule) bool {
	for _, p := range m.pkgs {
		for _, q := range p.Internal.Imports {
			if q != nil && strings.HasPrefix(q.ImportPath, "logos/") {
				return true
			}
		}
	}
	return false
}

// isTestArtifact reports whether p is a synthesized test main or a test
// variant produced for one. Such packages build in the second phase.
func isTestArtifact(p *load.Package) bool {
	return p.ForTest != "" || p.Internal.TestmainGo != nil
}

// hasLogosTestFiles reports whether p holds loGo/s test files needing
// test-variant compilation for emission.
func hasLogosTestFiles(p *load.Package) bool {
	for _, name := range p.TestGoFiles {
		if logosfile.Is(name) {
			return true
		}
	}
	for _, name := range p.XTestGoFiles {
		if logosfile.Is(name) {
			return true
		}
	}
	return false
}

// ejectClosureRoots returns the sorted import paths of every loaded
// non-test, non-standard package with a module, so a follow-up load can
// treat the whole closure as roots. Only roots get their test
// dependencies resolved (see ModResolveTests), so without this the test
// imports of regular dependencies stay out of the loader's package set.
func ejectClosureRoots(pkgs []*load.Package) []string {
	seen := map[string]bool{}
	var roots []string
	for _, p := range load.PackageList(pkgs) {
		if isTestArtifact(p) || p.Standard || p.Module == nil || p.ImportPath == "" {
			continue
		}
		if !seen[p.ImportPath] {
			seen[p.ImportPath] = true
			roots = append(roots, p.ImportPath)
		}
	}
	slices.Sort(roots)
	return roots
}

// collectTestBuilds discovers test dependencies of packages being copied
// and prepares compilation only for loGo/s test files that need lowering.
func collectTestBuilds(moduleLoader *modload.Loader, ctx context.Context, opts load.PackageOpts, plan *ejectPlan) []testBuild {
	var builds []testBuild
	var bad []*load.Package
	pkgs := slices.Clone(plan.main.pkgs)
	for _, m := range plan.deps {
		pkgs = append(pkgs, m.pkgs...)
	}
	discoveryOpts := opts
	discoveryOpts.SuppressBuildInfo = true
	for _, p := range pkgs {
		if len(p.TestGoFiles)+len(p.XTestGoFiles) == 0 {
			continue
		}
		if !hasLogosTestFiles(p) {
			_, ptest, pxtest := load.TestPackagesAndErrors(moduleLoader, ctx, nil, discoveryOpts, p, nil)
			first := len(plan.testRoots)
			plan.testRoots = append(plan.testRoots, ptest)
			if pxtest != nil {
				plan.testRoots = append(plan.testRoots, pxtest)
			}
			load.CheckPackageErrors(load.PackageList(plan.testRoots[first:]))
			continue
		}
		pmain, ptest, pxtest, perr := load.TestPackagesFor(moduleLoader, ctx, opts, p, nil)
		if perr != nil {
			bad = append(bad, perr)
			continue
		}
		if pmain == nil {
			base.Fatalf("logos eject: no test main for %s", p.ImportPath)
		}
		load.CheckPackageErrors(load.PackageList([]*load.Package{pmain}))
		plan.testRoots = append(plan.testRoots, pmain)
		builds = append(builds, testBuild{main: pmain, ptest: ptest, pxtest: pxtest})
		plan.testBuilt[p] = true
	}
	load.CheckPackageErrors(bad)
	testPackages := load.PackageList(plan.testRoots)
	// Test variants from separate test binaries can share an import path.
	// Validate their closures separately and check the shared packages together.
	ordinary := testPackages[:0]
	for _, p := range testPackages {
		if p.Incomplete {
			base.Fatalf("logos eject: incomplete test dependency graph for %s", p.ImportPath)
		}
		if !isTestArtifact(p) {
			ordinary = append(ordinary, p)
		}
		found := false
		for _, q := range plan.provenance[p.ImportPath] {
			if q == p {
				found = true
				break
			}
		}
		if !found {
			plan.provenance[p.ImportPath] = append(plan.provenance[p.ImportPath], p)
		}
		plan.addPackage(p)
	}
	load.CheckPackageErrors(ordinary)
	// Test-only dependencies may reach new GOROOT logos/* packages; pick
	// them up before the first build so they lower into staging too.
	plan.gorootLogos = collectGorootLogos(plan.provenance)
	resortEjectPlan(plan)
	return builds
}

// makeEjectStagingDirs creates one staging directory per emission
// target, including test variants, so the compiler always finds its
// -logos.eject directory.
func makeEjectStagingDirs(plan *ejectPlan, staging string, builds []testBuild) {
	mkdir := func(importPath string) {
		if err := os.MkdirAll(filepath.Join(staging, filepath.FromSlash(importPath)), 0777); err != nil {
			base.Fatalf("logos eject: %v", err)
		}
	}
	for _, p := range plan.logos {
		mkdir(p.ImportPath)
	}
	for _, p := range plan.gorootLogos {
		mkdir(p.ImportPath)
	}
	for _, t := range builds {
		for _, p := range []*load.Package{t.ptest, t.pxtest} {
			if p != nil {
				mkdir(p.ImportPath)
			}
		}
	}
}

// testBuildMains returns the test main roots of the second phase.
func testBuildMains(builds []testBuild) []*load.Package {
	mains := make([]*load.Package, 0, len(builds))
	for _, t := range builds {
		mains = append(mains, t.main)
	}
	return mains
}

// phaseOneRoots returns the first-phase build roots: every main-module
// package, every marked loGo/s package, and every reached GOROOT logos/*
// package, sorted for determinism. GOROOT packages compile as roots so
// the shared support module can emit their lowered output.
func phaseOneRoots(plan *ejectPlan) []*load.Package {
	set := map[*load.Package]bool{}
	var roots []*load.Package
	for _, p := range plan.main.pkgs {
		if p.Standard || p.IsTestOnly() || set[p] {
			continue
		}
		set[p] = true
		roots = append(roots, p)
	}
	for _, p := range plan.logos {
		if p.IsTestOnly() || set[p] {
			continue
		}
		set[p] = true
		roots = append(roots, p)
	}
	for _, p := range plan.gorootLogos {
		if p.IsTestOnly() || set[p] {
			continue
		}
		set[p] = true
		roots = append(roots, p)
	}
	return roots
}

// buildEjectRoots compiles roots and their dependencies with ModeBuild.
// Link outputs land under the build work directory, never the source
// tree. A failed build exits before any output is written.
func buildEjectRoots(moduleLoader *modload.Loader, ctx context.Context, roots []*load.Package) {
	b := work.NewBuilder("", moduleLoader.VendorDirOrEmpty)
	defer func() {
		if err := b.Close(); err != nil {
			base.Fatalf("logos eject: %v", err)
		}
	}()
	root := &work.Action{Mode: "go build"}
	for _, p := range roots {
		if p.Internal.TestmainGo != nil {
			if err := b.StageTestmain(p); err != nil {
				base.Fatalf("logos eject: %v", err)
			}
		}
		root.Deps = append(root.Deps, b.AutoAction(moduleLoader, work.ModeBuild, work.ModeBuild, p))
	}
	b.Do(ctx, root)
	if root.Failed != nil {
		base.SetExitStatus(1)
	}
	if base.GetExitStatus() != 0 {
		base.Exit()
	}
}

// stripEjectCopies keeps emission on exactly one compile per package in
// the second phase: the test variants. Test copies of dependencies lose
// the flag, so no staging file gets two concurrent writers.
func stripEjectCopies(builds []testBuild) {
	keep := map[*load.Package]bool{}
	var mains []*load.Package
	for _, t := range builds {
		mains = append(mains, t.main)
		if t.ptest != nil {
			keep[t.ptest] = true
			t.ptest.Internal.LogosEject = true
		}
		if t.pxtest != nil {
			keep[t.pxtest] = true
			t.pxtest.Internal.LogosEject = true
		}
	}
	for _, p := range load.PackageList(mains) {
		if p.Internal.LogosEject && !keep[p] {
			p.Internal.LogosEject = false
			p.Internal.Gcflags = withoutEjectFlags(p.Internal.Gcflags)
		}
	}
}

// withoutEjectFlags returns flags without the -logos.eject entries in a
// fresh slice, leaving shared backing arrays untouched.
func withoutEjectFlags(flags []string) []string {
	out := make([]string, 0, len(flags))
	for _, f := range flags {
		if !strings.HasPrefix(f, "-logos.eject=") {
			out = append(out, f)
		}
	}
	return out
}

// writeEjectedTree assembles the output directory from copied sources,
// compiler-emitted files, the support module, and rewritten go.mod files.
// Test roots reach the support closure through plan.testRoots, which
// ejectSupportRoots seeds from the arranged plan.
func writeEjectedTree(ctx context.Context, plan *ejectPlan, staging, out string) {
	abs, err := filepath.Abs(out)
	if err != nil {
		base.Fatalf("logos eject: %s: %v", out, err)
	}
	emitOne := emitPackageGoFiles
	if isEjectVariant() {
		emitOne = emitPackageGoFilesVariant
	}
	usedRT := copyModuleTree(plan.main.dir, out, ejectSkipDir(plan.main.dir, abs, out))
	for _, p := range plan.main.pkgs {
		if emitOne(p, plan, staging, out, plan.main.dir) {
			usedRT = true
		}
	}
	ejected := map[string]string{}
	for _, m := range plan.deps {
		dst := filepath.Join(out, "_logos_deps", m.path+"@"+m.version)
		ejected[m.path] = dst
		if copyModuleTree(m.dir, dst, ejectSkipDir(m.dir, abs, out)) {
			usedRT = true
		}
		for _, p := range m.pkgs {
			if emitOne(p, plan, staging, dst, m.dir) {
				usedRT = true
			}
		}
	}
	// pulled in only by an owned helper still get their require lines.
	if usesOwned, err := emitOwnedHelpers(ctx, plan, staging, out, ejected); err != nil {
		base.Fatalf("logos eject: %v", err)
	} else if usesOwned {
		usedRT = true
	}
	var rtDir string
	if usedRT {
		rtDir = filepath.Join(out, "_logos_deps", "rt")
		writeSupportModule(out, goVersionOf(parseGoModFile(filepath.Join(out, "go.mod"))), plan)
		addSupportRequire(filepath.Join(out, "go.mod"), "./_logos_deps/rt")
		materializeGorootLogos(plan, staging, out, rtDir)
	}
	rewriteMainGoMod(out, plan)
	for _, m := range plan.deps {
		rewriteDepGoMod(plan, m, ejected)
	}
	if usedRT {
		for _, m := range plan.deps {
			addSupportRequire(filepath.Join(ejected[m.path], "go.mod"), relEjectTarget(ejected[m.path], rtDir))
		}
	}
}

// gorootCovered reports whether a GOROOT logos/* import path needs no
// lowering from staging: the support closure copies the Go sources of
// logos/rt and everything below it, and of the arena and sync subtrees,
// when reached, and none of those trees holds LoGOs sources.
func gorootCovered(path string) bool {
	if path == "logos/rt" {
		return true
	}
	for _, prefix := range []string{"logos/rt/", "logos/arena/", "logos/sync/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// supportCandidates maps one ejected import path back to the GOROOT
// logos/* packages that forward-map to it. Only candidates present in
// the loaded import graph can be materialized; anything else is a
// hand-written support import that the plain build will judge.
func supportCandidates(imp string) []string {
	if imp == rtModulePath {
		return []string{"logos/rt"}
	}
	suffix, ok := strings.CutPrefix(imp, rtModulePath+"/")
	if !ok || suffix == "" || strings.HasPrefix(suffix, "internal/") {
		return nil
	}
	var out []string
	for _, cand := range []string{"logos/" + suffix, "logos/rt/" + suffix} {
		rel, ok := supportRelPath(cand)
		if !ok {
			continue
		}
		image := rtModulePath
		if rel != "" {
			image += "/" + rel
		}
		if image == imp {
			out = append(out, cand)
		}
	}
	return out
}

// ejectedSupportRefs scans written .go files for imports of the shared
// support module and maps each back to the GOROOT logos/* packages the
// tree needs. Files that fail to parse contribute nothing: an unrewritten
// import either rode along as data or already failed loudly.
func ejectedSupportRefs(root string) []string {
	var refs []string
	seen := map[string]bool{}
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, data, parser.ImportsOnly)
		if err != nil {
			return nil
		}
		for _, imp := range f.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			for _, cand := range supportCandidates(p) {
				if !seen[cand] {
					seen[cand] = true
					refs = append(refs, cand)
				}
			}
		}
		return nil
	})
	slices.Sort(refs)
	return refs
}

// materializeGorootLogos emits every reached GOROOT logos/* package
// outside the fixed set into the shared support module, following the
// import graph transitively. Packages owning .lgo files are emitted from
// compiler staging (built as plan roots, or staged by variant helpers);
// pure-Go packages are copied from GOROOT. A referenced package with no
// lowered output is a loud failure, never a dangling import. The support
// module ships no tests, so test files stay out on both paths.
func materializeGorootLogos(plan *ejectPlan, staging, out, rtDir string) {
	done := map[string]bool{}
	queue := ejectedSupportRefs(out)
	for len(queue) > 0 {
		path := queue[0]
		queue = queue[1:]
		if done[path] || gorootCovered(path) {
			continue
		}
		done[path] = true
		recs := plan.provenance[path]
		if len(recs) == 0 {
			continue
		}
		var rec *load.Package
		var names []string
		seen := map[string]bool{}
		for _, p := range recs {
			if rec == nil || (rec.ForTest != "" && p.ForTest == "") {
				rec = p
			}
			for _, files := range [][]string{p.GoFiles, p.CgoFiles, p.IgnoredGoFiles} {
				for _, name := range files {
					if !logosfile.Is(name) || seen[name] {
						continue
					}
					seen[name] = true
					if strings.HasSuffix(logosStem(name), "_test") {
						continue
					}
					names = append(names, name)
				}
			}
		}
		slices.Sort(names)
		rel, ok := supportRelPath(path)
		if !ok || rec == nil {
			continue
		}
		dst := rtDir
		if rel != "" {
			dst = filepath.Join(rtDir, filepath.FromSlash(rel))
		}
		if len(names) == 0 {
			// Pure-Go packages ride the support closure, which copies
			// their sources with import rewrites; only packages holding
			// LoGOs sources need lowering from staging here.
			continue
		}
		for _, name := range names {
			stem := logosStem(name)
			raw, err := os.ReadFile(filepath.Join(staging, filepath.FromSlash(path), stem+".go"))
			if err != nil {
				base.Fatalf("logos eject: missing ejected output for %s: the ejected tree references it but no build lowered it", filepath.Join(rec.Dir, name))
			}
			writeLoweredFile(dst, ".", stem, name, raw)
		}
		for _, cand := range ejectedSupportRefs(dst) {
			if !done[cand] {
				queue = append(queue, cand)
			}
		}
		slices.Sort(queue)
		queue = slices.Compact(queue)
	}
}

func ejectPackageNames(p *load.Package, plan *ejectPlan) ([]string, map[string]bool) {
	names := slices.Clone(p.GoFiles)
	names = append(names, p.CgoFiles...)
	for _, name := range p.IgnoredGoFiles {
		if strings.HasSuffix(logosStem(name), "_test") && !plan.testBuilt[p] {
			continue
		}
		names = append(names, name)
	}
	if plan.testBuilt[p] {
		names = append(names, p.TestGoFiles...)
		names = append(names, p.XTestGoFiles...)
	}
	ignoredSet := make(map[string]bool, len(p.IgnoredGoFiles))
	for _, name := range p.IgnoredGoFiles {
		ignoredSet[name] = true
	}
	return names, ignoredSet
}

func writeLoweredFile(dstRoot, rel, stem, name string, raw []byte) bool {
	fixed, uses := rewriteEjectedGo(raw, name)
	mainBytes, extraBytes := splitJSONv2File(fixed)
	dst := filepath.Join(dstRoot, rel, stem+".go")
	if err := os.MkdirAll(filepath.Dir(dst), 0777); err != nil {
		base.Fatalf("logos eject: %v", err)
	}
	if err := os.WriteFile(dst, mainBytes, 0666); err != nil {
		base.Fatalf("logos eject: %v", err)
	}
	queueEjectGofmt(dst)
	if extraBytes != nil {
		extraDst := filepath.Join(dstRoot, rel, jsonv2ExtraStem(stem)+".go")
		if err := os.WriteFile(extraDst, extraBytes, 0666); err != nil {
			base.Fatalf("logos eject: %v", err)
		}
		queueEjectGofmt(extraDst)
	}
	return uses
}

// jsonv2ExtraStem inserts _jsonv2 before any platform or test suffix so
// the Go tool still reads the last suffix elements. Go matches only the
// trailing elements: foo_linux_jsonv2.go has no platform rule, while
// foo_jsonv2_linux.go stays linux-only and foo_jsonv2_test.go stays a
// test file.
func jsonv2ExtraStem(stem string) string {
	test := false
	base := stem
	if strings.HasSuffix(base, "_test") {
		test = true
		base = strings.TrimSuffix(base, "_test")
	}
	platform := ""
	for _, goarch := range ownedGoarchTags {
		if !strings.HasSuffix(base, "_"+goarch) {
			continue
		}
		matched := "_" + goarch
		for _, goos := range ownedGoosTags {
			if strings.HasSuffix(base, "_"+goos+"_"+goarch) {
				matched = "_" + goos + "_" + goarch
				break
			}
		}
		platform = matched
		base = strings.TrimSuffix(base, matched)
		break
	}
	if platform == "" {
		for _, goos := range ownedGoosTags {
			if strings.HasSuffix(base, "_"+goos) {
				platform = "_" + goos
				base = strings.TrimSuffix(base, platform)
				break
			}
		}
	}
	out := base + "_jsonv2" + platform
	if test {
		out += "_test"
	}
	return out
}

// splitJSONv2File moves experiment-dependent JSONv2 methods into a separate
// file constrained to goexperiment.jsonv2. The compiler lowers a .lgo file
// once under the helper context, so an ejected file that keeps the source
// original guard (for example goexperiment.jsonv2 || goexperiment.loopvar)
// would otherwise carry MarshalJSONTo/UnmarshalJSONFrom and their
// encoding/json/jsontext import into a context where jsontext has no
// buildable files. The main file keeps the type and the v1 methods with the
// original guard; the extra file holds only the v2 methods with the
// original guard narrowed by jsonv2.
func splitJSONv2File(fixed []byte) (mainBytes []byte, extraBytes []byte) {
	if !bytes.Contains(fixed, []byte("encoding/json/jsontext")) {
		return fixed, nil
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", fixed, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return fixed, nil
	}
	type span struct{ s, e int }
	var moved []span
	var movedText [][]byte
	offset := func(p token.Pos) int {
		return fset.Position(p).Offset
	}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil {
			continue
		}
		if fn.Name.Name != "MarshalJSONTo" && fn.Name.Name != "UnmarshalJSONFrom" {
			continue
		}
		s, e := offset(fn.Pos()), offset(fn.End())
		if s < 0 || e <= s || e > len(fixed) {
			return fixed, nil
		}
		moved = append(moved, span{s, e})
		movedText = append(movedText, bytes.TrimRight(fixed[s:e], "\n"))
	}
	if len(moved) == 0 {
		return fixed, nil
	}
	// Original go:build expression from the leading comments.
	origExpr := ""
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			if expr, ok := strings.CutPrefix(c.Text, "//go:build "); ok {
				// Only the header constraint before the package clause
				// selects this file; comments inside the body are not it.
				if fset.Position(c.Pos()).Line < fset.Position(f.Package).Line {
					origExpr = strings.TrimSpace(expr)
					break
				}
			}
		}
		if origExpr != "" {
			break
		}
	}
	// Remove the moved declarations from the main file, from the end so
	// earlier offsets stay valid.
	main := bytes.Clone(fixed)
	for i := len(moved) - 1; i >= 0; i-- {
		s, e := moved[i].s, moved[i].e
		// Extend to the end of the line so no half-line remains.
		for e < len(main) && main[e] != '\n' {
			e++
		}
		if e < len(main) {
			e++
		}
		main = append(main[:s], main[e:]...)
	}
	// Drop the jsontext import from the main file when nothing left uses it.
	if !bytes.Contains(main, []byte("_logos_jsontext.")) {
		lines := bytes.SplitAfter(main, []byte("\n"))
		var kept [][]byte
		for _, ln := range lines {
			if bytes.Contains(ln, []byte("encoding/json/jsontext")) {
				continue
			}
			kept = append(kept, ln)
		}
		main = bytes.Join(kept, nil)
	}
	// Imports for the extra file: every import whose alias is used by the
	// moved declarations.
	joinedMoved := bytes.Join(movedText, []byte("\n"))
	var extraImports []string
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.IMPORT {
			continue
		}
		for _, spec := range gen.Specs {
			imp, ok := spec.(*ast.ImportSpec)
			if !ok {
				continue
			}
			alias := ""
			if imp.Name != nil {
				alias = imp.Name.Name
			} else {
				path := strings.Trim(imp.Path.Value, "\"")
				alias = path[strings.LastIndex(path, "/")+1:]
			}
			if alias == "_" || alias == "." {
				continue
			}
			if bytes.Contains(joinedMoved, []byte(alias+".")) {
				impPath := strings.TrimSpace(imp.Path.Value)
				extraImports = append(extraImports, "import "+alias+" "+impPath)
			}
		}
	}
	var extra bytes.Buffer
	if origExpr == "" {
		extra.WriteString("//go:build goexperiment.jsonv2\n")
	} else {
		extra.WriteString("//go:build (" + origExpr + ") && goexperiment.jsonv2\n")
	}
	extra.WriteString("\npackage " + f.Name.Name + "\n\n")
	for _, ln := range extraImports {
		extra.WriteString(ln + "\n")
	}
	if len(extraImports) > 0 {
		extra.WriteString("\n")
	}
	for i, txt := range movedText {
		extra.Write(txt)
		extra.WriteString("\n")
		if i+1 < len(movedText) {
			extra.WriteString("\n")
		}
	}
	return main, extra.Bytes()
}

// emitPackageGoFiles writes the compiler-emitted Go files of one loGo/s
// package under dstRoot, rewritten to the ejected import paths and
// formatted. It reports whether any file uses the support module.
// Missing non-ignore files fail loudly: after variant staging every
// owned file is present, so a miss is a build failure, never a silent
// omission or raw fallback.
func emitPackageGoFiles(p *load.Package, plan *ejectPlan, staging, dstRoot, modRoot string) bool {
	return emitLoweredGoFiles(p, plan, emitDest{staging: staging, dstRoot: dstRoot, modRoot: modRoot}, emitStrictMissing)
}

func emitPackageGoFilesVariant(p *load.Package, plan *ejectPlan, staging, dstRoot, modRoot string) bool {
	return emitLoweredGoFiles(p, plan, emitDest{staging: staging, dstRoot: dstRoot, modRoot: modRoot}, emitSkipMissing)
}

type emitDest struct {
	staging string
	dstRoot string
	modRoot string
}

type emitMissingFunc func(p *load.Package, name string, ignoredSet map[string]bool, err error) bool

func emitStrictMissing(p *load.Package, name string, ignoredSet map[string]bool, err error) bool {
	if os.IsNotExist(err) && ignoredSet[name] {
		srcData, readErr := fsys.ReadFile(filepath.Join(p.Dir, name))
		if readErr != nil {
			base.Fatalf("logos eject: missing ejected output for %s: %v", filepath.Join(p.Dir, name), readErr)
		}
		if isBuildIgnored(srcData) {
			return true
		}
	}
	base.Fatalf("logos eject: missing ejected output for %s: %v", filepath.Join(p.Dir, name), err)
	return true
}

func emitSkipMissing(p *load.Package, name string, ignoredSet map[string]bool, err error) bool {
	return true
}

func emitLoweredGoFiles(p *load.Package, plan *ejectPlan, dest emitDest, onMissing emitMissingFunc) bool {
	if !ejectOwnsLogos(p) {
		return false
	}
	rel, err := filepath.Rel(dest.modRoot, p.Dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		base.Fatalf("logos eject: package %s outside module", p.ImportPath)
	}
	names, ignoredSet := ejectPackageNames(p, plan)
	used := false
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if seen[name] || !logosfile.Is(name) {
			continue
		}
		seen[name] = true
		stem := logosStem(name)
		raw, err := os.ReadFile(filepath.Join(dest.staging, filepath.FromSlash(p.ImportPath), stem+".go"))
		if err != nil {
			if onMissing(p, name, ignoredSet, err) {
				continue
			}
		}
		if writeLoweredFile(dest.dstRoot, rel, stem, name, raw) {
			used = true
		}
	}
	return used
}

// isEjectVariant reports whether this eject runs as an inactive-variant
// helper. Helpers do plain host-context emission only, so they never
// spawn further variants and recursion terminates.
func isEjectVariant() bool {
	return os.Getenv("LOGOSEJECT_IN_VARIANT") != ""
}

// inactiveFile is one owned .lgo file excluded from the host build by
// platform or tag constraints that still needs compiler lowering.
type inactiveFile struct {
	pkg  *load.Package
	name string
}

// collectConstraintTags appends every tag named by expr.
func collectConstraintTags(expr constraint.Expr, tags *[]string) {
	switch e := expr.(type) {
	case *constraint.TagExpr:
		*tags = append(*tags, e.Tag)
	case *constraint.NotExpr:
		collectConstraintTags(e.X, tags)
	case *constraint.AndExpr:
		collectConstraintTags(e.X, tags)
		collectConstraintTags(e.Y, tags)
	case *constraint.OrExpr:
		collectConstraintTags(e.X, tags)
		collectConstraintTags(e.Y, tags)
	}
}

// selectVariantContext finds one host-close build context that
// selects the inactive file, using go/build MatchFile so filename
// suffixes and //go:build lines evaluate exactly as the go tool does.
// It varies GOOS, GOARCH, and tags from the file; CGO stays host
// to keep the helper count to real platform/tag variants.
func selectVariantContext(pkgDir, name, hostGOOS, hostGOARCH string, hostTags []string) (variantCtx, bool) {
	data, err := fsys.ReadFile(filepath.Join(pkgDir, name))
	if err != nil {
		base.Fatalf("logos eject: read inactive file %s: %v", filepath.Join(pkgDir, name), err)
	}
	pf, err := parser.ParseFile(token.NewFileSet(), name, data, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		base.Fatalf("logos eject: parse build header of %s: %v", filepath.Join(pkgDir, name), err)
	}
	expr, err := ownedHeaderExpr(pf)
	if err != nil {
		base.Fatalf("logos eject: parse build constraint of %s: %v", filepath.Join(pkgDir, name), err)
	}
	var mentioned []string
	if expr != nil {
		collectConstraintTags(expr, &mentioned)
	}
	mentioned = append(mentioned, ownedFilenameTags(name)...)
	seenMention := map[string]bool{}
	var uniqMention []string
	for _, t := range mentioned {
		if !seenMention[t] {
			seenMention[t] = true
			uniqMention = append(uniqMention, t)
		}
	}
	var goosMentioned []string
	var goarchMentioned []string
	var customMentioned []string
	var expMentioned []string
	for _, t := range uniqMention {
		if t == "wasip1" || slices.Contains(ownedGoosTags, t) {
			goosMentioned = append(goosMentioned, t)
			continue
		}
		if t == "riscv" {
			continue
		}
		if slices.Contains(ownedGoarchTags, t) {
			goarchMentioned = append(goarchMentioned, t)
			continue
		}
		// Experiment tags vary with the helper's real GOEXPERIMENT
		// value, solved as their own dimension below; they must not
		// be mistaken for custom -tags or frozen as host facts.
		if rest, ok := strings.CutPrefix(t, "goexperiment."); ok && rest != "" {
			expMentioned = append(expMentioned, rest)
			continue
		}
		if t == "cgo" || t == "ignore" || t == "unix" || t == "boringcrypto" || t == "logos" || t == "gc" || t == "gccgo" || architectureFeatureTag(t) || slices.Contains(cfg.BuildContext.ToolTags, t) || strings.HasPrefix(t, "go1.") {
			continue
		}
		customMentioned = append(customMentioned, t)
	}
	goosCands := []string{hostGOOS}
	for _, g := range goosMentioned {
		if g != hostGOOS {
			goosCands = append(goosCands, g)
		}
	}
	goarchCands := []string{hostGOARCH}
	for _, a := range goarchMentioned {
		if a != hostGOARCH {
			goarchCands = append(goarchCands, a)
		}
	}
	var customUniq []string
	seenCustom := map[string]bool{}
	for _, t := range customMentioned {
		if !seenCustom[t] {
			seenCustom[t] = true
			customUniq = append(customUniq, t)
		}
	}
	slices.Sort(customUniq)
	slices.Sort(expMentioned)
	// Synthesized GOEXPERIMENT values must parse. ParseGOEXPERIMENT rejects an
	// unknown experiment in either polarity, so an unknown tag next to a real
	// one would poison every candidate. Unknown tags stay fixed facts for the
	// matcher, which reads an absent tag as false.
	var expKnown []string
	for _, name := range expMentioned {
		if buildcfg.KnownExperiment(name) {
			expKnown = append(expKnown, name)
		}
	}
	search := func(goosCands, goarchCands []string) (variantCtx, bool) {
		combos := experimentCombinations(expKnown)
		// Prefer a jsonv2-enabled helper when the guard allows one. The
		// helper lowering is frozen into the ejected tree; a nojsonv2
		// lowering carries no MarshalJSONTo/UnmarshalJSONFrom, so the
		// jsonv2 branches of a disjunctive guard such as !jsonv2 ||
		// loopvar would silently lose the v2 methods. A jsonv2 lowering
		// splits into v1 plus a jsonv2-gated extra file and covers all
		// branches. Raw stays first; only the synthesized entries reorder.
		if len(combos) > 1 && slices.Contains(expKnown, "jsonv2") {
			rest := slices.Clone(combos[1:])
			slices.SortStableFunc(rest, func(a, b string) int {
				ha, hb := comboEnablesJSONv2(a), comboEnablesJSONv2(b)
				switch {
				case ha == hb:
					return 0
				case ha && !hb:
					return -1
				default:
					return 1
				}
			})
			combos = append([]string{combos[0]}, rest...)
		}
		for _, experiment := range combos {
			if vc, ok := searchVariantContext(pkgDir, name, expr, goosCands, goarchCands, hostTags, experiment, customUniq, uniqMention); ok {
				return vc, true
			}
		}
		return variantCtx{}, false
	}
	if vc, ok := search(goosCands, goarchCands); ok {
		return vc, true
	}
	for _, g := range []string{"linux", "darwin", "windows", "freebsd"} {
		if !slices.Contains(goosCands, g) {
			goosCands = append(goosCands, g)
		}
	}
	for _, g := range ownedGoosTags {
		if !slices.Contains(goosCands, g) {
			goosCands = append(goosCands, g)
		}
	}
	if !slices.Contains(goosCands, "wasip1") {
		goosCands = append(goosCands, "wasip1")
	}
	for _, a := range ownedGoarchTags {
		if a == "riscv" {
			continue
		}
		if !slices.Contains(goarchCands, a) {
			goarchCands = append(goarchCands, a)
		}
	}
	return search(goosCands, goarchCands)
}

// comboEnablesJSONv2 reports whether a synthesized GOEXPERIMENT value
// enables jsonv2. An absent jsonv2 means the toolchain default, which
// enables it.
func comboEnablesJSONv2(combo string) bool {
	if combo == "" {
		return true
	}
	for item := range strings.SplitSeq(combo, ",") {
		if item == "jsonv2" {
			return true
		}
		if item == "nojsonv2" {
			return false
		}
	}
	return true
}

// experimentCombinations lists the GOEXPERIMENT values to try for files
// mentioning experiment tags, host value first. Later entries set every
// mentioned experiment explicitly, one combination at a time, so a helper
// runs with a real GOEXPERIMENT value instead of a synthesized user tag.
func experimentCombinations(mentioned []string) []string {
	raw := cfg.RawGOEXPERIMENT
	if len(mentioned) == 0 {
		return []string{raw}
	}
	out := []string{raw}
	for mask := range 1 << len(mentioned) {
		var keep []string
		if raw != "" {
			for item := range strings.SplitSeq(raw, ",") {
				drop := false
				for _, name := range mentioned {
					if item == name || item == "no"+name {
						drop = true
						break
					}
				}
				if !drop {
					keep = append(keep, item)
				}
			}
		}
		for i, name := range mentioned {
			if mask&(1<<i) != 0 {
				keep = append(keep, name)
			} else {
				keep = append(keep, "no"+name)
			}
		}
		combo := strings.Join(keep, ",")
		if combo != raw {
			out = append(out, combo)
		}
	}
	return out
}

func searchVariantContext(pkgDir, name string, expr constraint.Expr, goosCands, goarchCands, hostTags []string, experiment string, customUniq, mentioned []string) (variantCtx, bool) {
	for _, goos := range goosCands {
		for _, goarch := range goarchCands {
			if work.CheckGOOSARCHPair(goos, goarch) != nil {
				continue
			}
			archName, levels := variantArchLevels(goarch, mentioned)
			for _, level := range levels {
				vc := variantCtx{goos: goos, goarch: goarch, archName: archName, archValue: level, experiment: experiment}
				ctxt, ok := variantBuildContext(vc)
				if !ok {
					continue
				}
				if tags, ok := matchVariantTags(pkgDir, name, expr, ctxt, hostTags, customUniq); ok {
					vc.tags = tags
					return vc, true
				}
			}
		}
	}
	return variantCtx{}, false
}

// variantCtx is one helper-eject build context: the GOOS, GOARCH,
// GOEXPERIMENT value, and full -tags list that select a group of inactive
// files with correct dependencies.
type variantCtx struct {
	goos       string
	goarch     string
	archName   string
	archValue  string
	experiment string
	tags       []string
	packages   []string
	files      []inactiveFile
}

func variantKey(vc variantCtx) string {
	return vc.goos + "\x00" + vc.goarch + "\x00" + vc.archValue + "\x00" + vc.experiment + "\x00" + strings.Join(vc.tags, ",")
}

// variantExperiment returns the GOEXPERIMENT value a variant helper must
// receive. Contexts selected without an experiment dimension carry the
// host value; selected ones carry the synthesized combination.
func variantExperiment(vc variantCtx) string {
	if vc.experiment == "" {
		return cfg.RawGOEXPERIMENT
	}
	return vc.experiment
}

// variantContextsForFiles groups inactive files by one satisfying
// context each, deduplicated so one helper eject covers every file
// its context selects.
func variantContextsForFiles(files []inactiveFile) []variantCtx {
	hostGOOS := cfg.BuildContext.GOOS
	hostGOARCH := cfg.BuildContext.GOARCH
	hostTags := slices.Clone(cfg.BuildContext.BuildTags)
	byKey := map[string]variantCtx{}
	var keys []string
	for _, f := range files {
		vc, ok := selectVariantContext(f.pkg.Dir, f.name, hostGOOS, hostGOARCH, hostTags)
		if !ok {
			base.Fatalf("logos eject: no build context selects inactive file %s: %s", filepath.Join(f.pkg.Dir, f.name), inactiveConstraintHint(f.pkg.Dir, f.name))
		}
		key := variantKey(vc)
		if existing, exists := byKey[key]; exists {
			vc = existing
		} else {
			keys = append(keys, key)
		}
		if f.pkg.ImportPath != "" && !slices.Contains(vc.packages, f.pkg.ImportPath) {
			vc.packages = append(vc.packages, f.pkg.ImportPath)
		}
		vc.files = append(vc.files, f)
		byKey[key] = vc
	}
	slices.Sort(keys)
	out := make([]variantCtx, 0, len(keys))
	for _, k := range keys {
		out = append(out, byKey[k])
	}
	return out
}

// inactiveConstraintHint names the build constraint no GOOS/GOARCH/tags/
// GOEXPERIMENT combination could satisfy, so the failure points at the
// file's own //go:build lines instead of the search. A file constrained
// only by its name gets the filename rule instead.
func inactiveConstraintHint(pkgDir, name string) string {
	const fixed = "no GOOS/GOARCH/tags/GOEXPERIMENT combination satisfies its build constraint"
	data, err := fsys.ReadFile(filepath.Join(pkgDir, name))
	if err != nil {
		return fixed
	}
	var lines []string
	for line := range strings.SplitSeq(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(trimmed, "//go:build "); ok {
			lines = append(lines, strings.TrimSpace(rest))
		} else if strings.HasPrefix(trimmed, "package ") {
			break
		}
	}
	if len(lines) == 0 {
		return "no GOOS/GOARCH/tags/GOEXPERIMENT combination satisfies its filename and build constraints"
	}
	return fixed + " (" + strings.Join(lines, " / ") + "); check for an unsatisfiable feature combination"
}

// spawnVariantEjects runs one plain helper eject per variant context.
// Each helper uses the real compiler in its own GOOS/GOARCH/tags context,
// so every inactive file gets type-checked lowering with correct
// imports and dependencies. Helpers set LOGOSEJECT_IN_VARIANT so they
// never recurse. Consistency needs idempotent merges: helpers may
// overlap host outputs, and the main tree merges each helper file
// exactly once by staging path, so repeated helper content is safe.
func spawnVariantEjects(ctx context.Context, ctxs []variantCtx) []string {
	exe, err := os.Executable()
	if err != nil {
		base.Fatalf("logos eject: find logos binary for variants: %v", err)
	}
	var dirs []string
	for _, vc := range ctxs {
		dir, err := os.MkdirTemp("", "logoseject-variant")
		if err != nil {
			base.Fatalf("logos eject: %v", err)
		}
		dirs = append(dirs, dir)
		args := []string{"eject", "-o", dir}
		if fsys.OverlayFile != "" {
			args = []string{"build", "-overlay=" + fsys.OverlayFile, "-emit-go=" + dir}
		}
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Args[0] = "logos"
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		kept := make([]string, 0, len(os.Environ()))
		for _, kv := range os.Environ() {
			if strings.HasPrefix(kv, "GOOS=") || strings.HasPrefix(kv, "GOARCH=") || strings.HasPrefix(kv, "LOGOSEJECT_IN_VARIANT=") || strings.HasPrefix(kv, "LOGOSEJECT_TAGS=") || strings.HasPrefix(kv, "LOGOSEJECT_PACKAGES=") || strings.HasPrefix(kv, "LOGOSEJECT_STATE=") || strings.HasPrefix(kv, "GOEXPERIMENT=") || strings.HasPrefix(kv, "CGO_ENABLED=") || vc.archName != "" && strings.HasPrefix(kv, vc.archName+"=") {
				continue
			}
			kept = append(kept, kv)
		}
		env := append(kept, "LOGOSEJECT_IN_VARIANT=1", "LOGOSEJECT_STATE=1")
		env = append(env, "LOGOSEJECT_PACKAGES="+strings.Join(vc.packages, "\n"))
		env = append(env, "GOOS="+vc.goos)
		env = append(env, "GOARCH="+vc.goarch)
		if vc.archName != "" {
			env = append(env, vc.archName+"="+vc.archValue)
		}
		env = append(env, "GOEXPERIMENT="+variantExperiment(vc))
		cgo := "0"
		if cfg.BuildContext.CgoEnabled {
			cgo = "1"
		}
		env = append(env, "CGO_ENABLED="+cgo)
		env = append(env, "LOGOSEJECT_TAGS="+strings.Join(vc.tags, ","))
		cmd.Env = env
		if err := cmd.Run(); err != nil {
			base.Fatalf("logos eject: variant eject for GOOS=%s GOARCH=%s tags=%q failed: %v", vc.goos, vc.goarch, strings.Join(vc.tags, ","), err)
		}
	}
	return dirs
}

// stageInactiveVariantsIntoStaging reaches a fixed point over owned module
// inventories. Each source/context request is dispatched once; a successful
// helper must supply every requested source. Only its actual dependency loads
// can extend the census, and every helper uses the normal compiler path.
func stageInactiveVariantsIntoStaging(ctx context.Context, plan *ejectPlan, staging string) []string {
	visited := map[string]bool{}
	var outs []string
	for {
		files := collectInactiveLogosFiles(plan, staging)
		if len(files) == 0 {
			return outs
		}
		contexts := variantContextsForFiles(files)
		for _, vc := range contexts {
			for _, f := range vc.files {
				key := f.pkg.ImportPath + "\x00" + filepath.Join(f.pkg.Dir, f.name) + "\x00" + variantKey(vc)
				if visited[key] {
					base.Fatalf("logos eject: variant made no progress for %s", filepath.Join(f.pkg.Dir, f.name))
				}
				visited[key] = true
			}
		}
		batch := spawnVariantEjects(ctx, contexts)
		for _, out := range batch {
			absorbVariantState(plan, out, staging, len(outs))
			outs = append(outs, out)
		}
		for _, f := range files {
			dst := filepath.Join(staging, filepath.FromSlash(f.pkg.ImportPath), logosStem(f.name)+".go")
			if _, err := os.Stat(dst); err != nil {
				base.Fatalf("logos eject: missing ejected output for %s: %v", filepath.Join(f.pkg.Dir, f.name), err)
			}
		}
	}
}

// isBuildIgnored reports whether source declares an ignore build constraint
// such as //go:build ignore or // +build ignore. It inspects only comments
// before the package clause, so syntax in the file body never interferes.
func isBuildIgnored(source []byte) bool {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", source, parser.PackageClauseOnly|parser.ParseComments)
	if err != nil {
		return false
	}
	expr, err := ownedHeaderExpr(f)
	if err != nil || expr == nil {
		return false
	}
	return hasIgnoreTag(expr)
}

// hasIgnoreTag reports whether the constraint expression requires the
// special "ignore" build tag.
func hasIgnoreTag(x constraint.Expr) bool {
	if x == nil {
		return false
	}
	switch e := x.(type) {
	case *constraint.TagExpr:
		return e.Tag == "ignore"
	case *constraint.NotExpr:
		return false
	case *constraint.AndExpr:
		return hasIgnoreTag(e.X) || hasIgnoreTag(e.Y)
	case *constraint.OrExpr:
		return hasIgnoreTag(e.X) && hasIgnoreTag(e.Y)
	}
	return false
}

// logosStem returns name without its loGo/s extension, keeping a _test
// suffix when present.
func logosStem(name string) string {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[:i]
	}
	return name
}

// ejectSkipDir returns the path of out relative to one source module
// when out sits inside it, so the module copy skips the destination
// instead of feeding ejected files back into the tree.
func ejectSkipDir(modDir, outAbs, out string) string {
	rel, err := filepath.Rel(modDir, outAbs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return ""
	}
	if rel == "." {
		base.Fatalf("logos eject: %s: output is the module directory", out)
	}
	return rel
}

// copyModuleTree copies the non-loGo/s files of one module: every
// regular file except .git contents, logos.toml files, loGo/s sources
// (emitted separately), the skip subtree (the destination when it
// sits inside the module), and files whose nearest enclosing nested
// module (a directory below the root that holds its own go.mod file) is
// not a local replace target of this module's go.mod. A foreign
// nested module is still walked when a kept one lies below it. As in
// the go command, a directory named go.mod marks no module, and a
// go.mod that is a symlink to a file does.
// Symlinks and special files stay out, so a link can never pull
// outside bytes into the tree. It reports whether any copied Go file
// imports the loGo/s support packages, which the copy remapped to
// the shared support module.
func copyModuleTree(srcRoot, dstRoot, skip string) bool {
	uses := false
	keep := localReplaceDirs(srcRoot)
	nested := map[string]bool{}
	err := filepath.WalkDir(srcRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dstRoot, 0777)
		}
		if skip != "" && (rel == skip || strings.HasPrefix(rel, skip+string(filepath.Separator))) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		for seg := range strings.SplitSeq(rel, string(filepath.Separator)) {
			if seg == ".git" {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			if fi, err := os.Stat(filepath.Join(path, "go.mod")); err == nil && !fi.IsDir() {
				if !keep[rel] && !holdsKept(keep, rel) {
					return filepath.SkipDir
				}
				nested[rel] = keep[rel]
			}
		}
		if !inCopiedModule(nested, rel) {
			return nil
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dstRoot, rel), 0777)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		name := filepath.Base(rel)
		if name == "logos.toml" || logosfile.Is(name) {
			return nil
		}
		fileUses, err := copyDataFile(path, filepath.Join(dstRoot, rel))
		if fileUses {
			uses = true
		}
		return err
	})
	if err != nil {
		base.Fatalf("logos eject: %v", err)
	}
	return uses
}

// holdsKept reports whether a kept nested module lies below dir.
func holdsKept(keep map[string]bool, dir string) bool {
	for k := range keep {
		if strings.HasPrefix(k, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// inCopiedModule reports whether rel belongs to the copied module:
// its nearest enclosing nested module, if any, is a kept one. nested
// maps each nested module directory seen so far to whether it is kept.
func inCopiedModule(nested map[string]bool, rel string) bool {
	for p := rel; p != "."; p = filepath.Dir(p) {
		if kept, ok := nested[p]; ok {
			return kept
		}
	}
	return true
}

// copyDataFile copies one non-loGo/s file. Go files that still parse
// are rewritten to the ejected import paths and formatted by the gofmt
// pass, so the tree stays gofmt-clean; anything else rides along byte
// for byte. It reports whether the written file imports the loGo/s
// support packages.
func copyDataFile(src, dst string) (bool, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return false, err
	}
	uses := false
	if strings.HasSuffix(src, ".go") {
		if fixed, fixedUses, ok := remapEjectedFile(data, ""); ok {
			data = fixed
			uses = fixedUses
			queueEjectGofmt(dst)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0777); err != nil {
		return false, err
	}
	if err := os.WriteFile(dst, data, 0666); err != nil {
		return false, err
	}
	return uses, nil
}

// rewriteEjectedGo rewrites one compiler-emitted file to the ejected
// import paths. Compiler output must parse; anything else is a loud
// failure, never a silent copy.
func rewriteEjectedGo(data []byte, name string) ([]byte, bool) {
	fixed, uses, ok := remapEjectedFile(data, siteNamespace(logosStem(name)))
	if !ok {
		base.Fatalf("logos eject: cannot parse ejected file for %s", name)
	}
	return fixed, uses
}

// sanitizeSiteStem maps a file stem to a valid identifier fragment:
// every run of non-alphanumeric/underscore becomes one underscore.
func sanitizeSiteStem(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	prevUnderscore := false
	for _, r := range s {
		if isSiteWordRune(r) {
			b.WriteRune(r)
			prevUnderscore = false
			continue
		}
		if !prevUnderscore {
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	out := b.String()
	if out == "" {
		return "file"
	}
	return out
}

func isSiteWordRune(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_'
}

// siteNamespace namespaces per-compilation site symbols by file stem
// plus a hash of the raw stem: each helper eject restarts
// packageLower.siteSeq at zero, so two variant files merged into one
// package would otherwise both declare _logos_site0. Site declarations
// and uses are file-local, so _logos_siteN becomes
// _logos_site_<sanitized>_<hash>_N, distinct per file and stable per
// stem across helpers. The hash disambiguates stems that sanitize
// identically (for example my-file versus my_file).
func siteNamespace(stem string) string {
	sanitized := sanitizeSiteStem(stem)
	h := uint32(2166136261)
	for i := range len(stem) {
		h ^= uint32(stem[i])
		h *= 16777619
	}
	return sanitized + "_" + strconv.FormatUint(uint64(h), 16)
}

func remapEjectedFile(data []byte, siteNS string) ([]byte, bool, bool) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", data, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, false, false
	}
	edits, uses := ejectedImportEdits(data, fset, f)
	edits = append(edits, siteIdentEdits(fset, f, siteNS)...)
	h2edits := h2ExtendedConnectEdits(fset, data, f)
	if off, overlap := h2OverlapOffset(edits, h2edits); overlap {
		pos := fset.Position(token.Pos(fset.File(f.Pos()).Base() + off))
		base.Errorf("logos eject: overlapping h2extendedconnect import edit at line %d: EnableHTTP2ExtendedConnect left unrewritten", pos.Line)
		h2edits = nil
	}
	edits = append(edits, h2edits...)
	out, ok := applyTextEdits(data, edits)
	if !ok {
		return nil, false, false
	}
	return out, uses, true
}

func ejectedImportEdits(data []byte, fset *token.FileSet, f *ast.File) ([]textEdit, bool) {
	var edits []textEdit
	uses := false
	for _, decl := range f.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.IMPORT {
			continue
		}
		if gen.Lparen.IsValid() {
			edits = append(edits, groupedImportEdits(data, fset, gen, &uses)...)
			continue
		}
		imp := gen.Specs[0].(*ast.ImportSpec)
		if path, err := strconv.Unquote(imp.Path.Value); err == nil && path == "comptime" {
			edits = append(edits, wholeLineEdit(data, fset, gen.Pos(), imp.End()))
			continue
		}
		if edit, ok := supportPathEdit(fset, imp, &uses); ok {
			edits = append(edits, edit)
		}
	}
	return edits, uses
}

func groupedImportEdits(data []byte, fset *token.FileSet, gen *ast.GenDecl, uses *bool) []textEdit {
	var edits []textEdit
	comptime := 0
	for _, spec := range gen.Specs {
		imp := spec.(*ast.ImportSpec)
		if path, err := strconv.Unquote(imp.Path.Value); err == nil && path == "comptime" {
			comptime++
			continue
		}
		if edit, ok := supportPathEdit(fset, imp, uses); ok {
			edits = append(edits, edit)
		}
	}
	if comptime == 0 {
		return edits
	}
	if comptime == len(gen.Specs) {
		return append(edits, wholeLineEdit(data, fset, gen.Pos(), gen.End()))
	}
	for _, spec := range gen.Specs {
		imp := spec.(*ast.ImportSpec)
		if path, err := strconv.Unquote(imp.Path.Value); err == nil && path == "comptime" {
			edits = append(edits, wholeLineEdit(data, fset, imp.Pos(), imp.End()))
		}
	}
	return edits
}

// supportRelPath maps a logos/ import path to its directory relative to
// the support module root, the same mapping the import rewrite and the
// support copy share. logos/rt is the root itself, so it maps to "".
// A leading rt/ segment is trimmed, so logos/rt/json and logos/json both
// map to "json". It reports false for paths outside logos/.

// supportDestinations maps each closure member to its destination directory
// relative to the support module root. Two distinct members mapping to one
// destination stop the eject, so logos/json and logos/rt/json can never
// silently overwrite each other. The input order does not matter: members
// are considered in sorted order, so the reported pair is deterministic.
func supportDestinations(paths []string) (map[string]string, error) {
	sorted := slices.Clone(paths)
	slices.Sort(sorted)
	dst := make(map[string]string, len(sorted))
	for _, path := range sorted {
		rel, ok := supportRelPath(path)
		if !ok {
			return nil, fmt.Errorf("support package %s is outside logos/", path)
		}
		if prev, dup := dst[rel]; dup {
			return nil, fmt.Errorf("support packages %s and %s map to the same path %s", prev, path, rel)
		}
		dst[rel] = path
	}
	return dst, nil
}

func supportRelPath(path string) (string, bool) {
	rest, ok := strings.CutPrefix(path, "logos/")
	if !ok {
		return "", false
	}
	if rest == "rt" {
		return "", true
	}
	return strings.TrimPrefix(rest, "rt/"), true
}
func supportPathEdit(fset *token.FileSet, imp *ast.ImportSpec, uses *bool) (textEdit, bool) {
	path, err := strconv.Unquote(imp.Path.Value)
	if err != nil {
		return textEdit{}, false
	}
	if path == "internal/jsonscan" {
		start := fset.Position(imp.Path.Pos()).Offset
		*uses = true
		return textEdit{start, start + len(imp.Path.Value), strconv.Quote(rtModulePath + "/internal/jsonscan")}, true
	}
	rel, ok := supportRelPath(path)
	if !ok {
		return textEdit{}, false
	}
	target := rtModulePath
	if rel != "" {
		target += "/" + rel
	}
	start := fset.Position(imp.Path.Pos()).Offset
	*uses = true
	return textEdit{start, start + len(imp.Path.Value), strconv.Quote(target)}, true
}

func wholeLineEdit(data []byte, fset *token.FileSet, from, to token.Pos) textEdit {
	start := fset.Position(from).Offset
	end := fset.Position(to).Offset
	lineStart := start
	for lineStart > 0 && data[lineStart-1] != '\n' {
		lineStart--
	}
	blank := true
	for _, c := range data[lineStart:start] {
		if c != ' ' && c != '\t' {
			blank = false
			break
		}
	}
	if blank {
		start = lineStart
	}
	if end < len(data) && data[end] == '\n' {
		end++
	}
	return textEdit{start, end, ""}
}

func siteIdentEdits(fset *token.FileSet, f *ast.File, ns string) []textEdit {
	if ns == "" {
		return nil
	}
	var edits []textEdit
	ast.Inspect(f, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if !ok || !isSiteIdent(id.Name) {
			return true
		}
		start := fset.Position(id.Pos()).Offset
		digits := strings.TrimPrefix(id.Name, "_logos_site")
		edits = append(edits, textEdit{start, start + len(id.Name), "_logos_site_" + ns + "_" + digits})
		return true
	})
	return edits
}

func isSiteIdent(name string) bool {
	rest, ok := strings.CutPrefix(name, "_logos_site")
	if !ok || rest == "" {
		return false
	}
	for i := range len(rest) {
		if rest[i] < '0' || rest[i] > '9' {
			return false
		}
	}
	return true
}

type textEdit struct {
	start, end int
	text       string
}

func applyTextEdits(data []byte, edits []textEdit) ([]byte, bool) {
	if len(edits) == 0 {
		return data, true
	}
	edits = slices.Clone(edits)
	slices.SortFunc(edits, func(a, b textEdit) int { return a.start - b.start })
	out := make([]byte, 0, len(data))
	prev := 0
	for _, e := range edits {
		if e.start < prev || e.end < e.start || e.end > len(data) {
			return nil, false
		}
		out = append(out, data[prev:e.start]...)
		out = append(out, e.text...)
		prev = e.end
	}
	return append(out, data[prev:]...), true
}

// writeSupportModule writes the shared, self-contained support module.
func writeSupportModule(out, goVer string, plan *ejectPlan) {
	src := filepath.Join(cfg.GOROOT, "src")
	dst := filepath.Join(out, "_logos_deps", "rt")
	if err := copyEjectedSupport(src, dst, ejectSupportRoots(plan)); err != nil {
		base.Fatalf("logos eject: %v", err)
	}
	copyJSONScanSupport(filepath.Join(src, "internal", "jsonscan"), filepath.Join(dst, "internal", "jsonscan"))
	if err := os.WriteFile(filepath.Join(dst, "go.mod"), []byte("module "+rtModulePath+"\n\ngo "+goVer+"\n"), 0666); err != nil {
		base.Fatalf("logos eject: %v", err)
	}
}

// ejectSupportRoots keeps support imports from owned packages, their tests,
// and inactive variant loads. The copy follows further imports in all variants.
func ejectSupportRoots(plan *ejectPlan) []string {
	var seeds []*load.Package
	seeds = append(seeds, plan.main.pkgs...)
	for _, m := range plan.deps {
		seeds = append(seeds, m.pkgs...)
	}
	seeds = append(seeds, plan.testRoots...)
	seen := map[string]bool{load.LogosRTPkg: true}
	roots := []string{load.LogosRTPkg}
	for _, p := range load.PackageList(seeds) {
		if p == nil || !strings.HasPrefix(p.ImportPath, "logos/") || seen[p.ImportPath] {
			continue
		}
		seen[p.ImportPath] = true
		roots = append(roots, p.ImportPath)
	}
	// Variant inventories retain package identities but not dependency edges.
	for path := range plan.provenance {
		if strings.HasPrefix(path, "logos/") && !seen[path] {
			seen[path] = true
			roots = append(roots, path)
		}
	}
	slices.Sort(roots)
	return roots
}

func copyJSONScanSupport(src, dst string) {
	if err := os.MkdirAll(dst, 0777); err != nil {
		base.Fatalf("logos eject: %v", err)
	}
	for _, name := range []string{"jsonscan.go", "jsonscan_generic.go"} {
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			base.Fatalf("logos eject: %v", err)
		}
		fixed, err := ejectedJSONScanSource(name, data)
		if err != nil {
			base.Fatalf("logos eject: %s: %v", name, err)
		}
		path := filepath.Join(dst, name)
		if err := os.WriteFile(path, fixed, 0666); err != nil {
			base.Fatalf("logos eject: %v", err)
		}
		queueEjectGofmt(path)
	}
}

func ejectedJSONScanSource(name string, data []byte) ([]byte, error) {
	if name != "jsonscan.go" && name != "jsonscan_generic.go" {
		return nil, fmt.Errorf("unexpected scanner source %q", name)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, name, data, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	if f.Name.Name != "jsonscan" {
		return nil, fmt.Errorf("unexpected scanner package %q", f.Name.Name)
	}
	var edits []textEdit
	source := func(n ast.Node) string {
		return string(data[fset.Position(n.Pos()).Offset:fset.Position(n.End()).Offset])
	}
	replace := func(n ast.Node, text string) {
		edits = append(edits, textEdit{fset.Position(n.Pos()).Offset, fset.Position(n.End()).Offset, text})
	}
	imports, calls, comments := 0, 0, 0
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			return nil, err
		}
		if imp.Name != nil {
			return nil, fmt.Errorf("unexpected named scanner import %s", path)
		}
		switch {
		case name == "jsonscan.go" && path == "internal/abi":
			edits = append(edits, wholeLineEdit(data, fset, imp.Pos(), imp.End()))
			imports++
		case name == "jsonscan_generic.go" && path == "internal/byteorder":
			replace(imp.Path, strconv.Quote("encoding/binary"))
			imports++
		case strings.HasPrefix(path, "internal/"):
			return nil, fmt.Errorf("unexpected scanner import %s", path)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if name == "jsonscan.go" && source(call) == "(*byte)(abi.NoEscape(unsafe.Pointer(unsafe.StringData(s))))" {
			replace(call, "unsafe.StringData(s)")
			calls++
			return false
		}
		if name == "jsonscan_generic.go" && source(call) == "byteorder.LEUint64(s[i:])" {
			replace(call.Fun, "binary.LittleEndian.Uint64")
			calls++
			return false
		}
		return true
	})
	commentEdits, err := ejectedJSONScanCommentEdits(name, data, fset, f)
	if err != nil {
		return nil, err
	}
	edits = append(edits, commentEdits...)
	comments = len(commentEdits)
	wantCalls, wantComments := 1, 2
	if name == "jsonscan_generic.go" {
		wantCalls, wantComments = 2, 1
	}
	if imports != 1 || calls != wantCalls || comments != wantComments {
		return nil, fmt.Errorf("unexpected scanner adaptation matches: imports=%d calls=%d comments=%d", imports, calls, comments)
	}
	fixed, ok := applyTextEdits(data, edits)
	if !ok {
		return nil, fmt.Errorf("overlapping scanner source edits")
	}
	if _, err := parser.ParseFile(token.NewFileSet(), name, fixed, parser.SkipObjectResolution); err != nil {
		return nil, fmt.Errorf("invalid adapted scanner: %w", err)
	}
	return fixed, nil
}

func ejectedJSONScanCommentEdits(name string, data []byte, fset *token.FileSet, f *ast.File) ([]textEdit, error) {
	var edits []textEdit
	for _, group := range f.Comments {
		if name == "jsonscan_generic.go" {
			start, end := fset.Position(group.Pos()).Offset, fset.Position(group.End()).Offset
			text := string(data[start:end])
			switch {
			case text == "//go:build !amd64 && !arm64":
				edits = append(edits, wholeLineEdit(data, fset, group.Pos(), group.End()))
			case strings.Contains(text, "//go:build") || strings.Contains(text, "// +build"):
				return nil, fmt.Errorf("unexpected scanner build constraint")
			}
			continue
		}
		var expected, replacement string
		switch {
		case group == f.Doc:
			expected = "// Package jsonscan finds the end of a run of plain ASCII bytes inside a JSON\n// string body. It uses SSE2 or AVX2 on amd64, NEON on arm64, and SWAR over\n// uint64 words on every other architecture. No function reads outside its\n// argument."
			replacement = "// Package jsonscan finds the end of a run of plain ASCII bytes inside a JSON\n// string body using portable SWAR. No function reads outside its argument."
		case strings.Contains(group.Text(), "NoEscape"):
			expected = "// SAFETY: the kernels only read s[0:len(s)] and keep no pointer, and the\n\t// returned pointer keeps the bytes alive as a call argument. NoEscape\n\t// stops the compiler from treating b as written by the bodyless assembly\n\t// function, which would force callers to copy a string converted to []byte."
			replacement = "// SAFETY: the kernels only read s[0:len(s)] and keep no pointer, and the\n\t// returned pointer keeps the bytes alive as a call argument."
		default:
			continue
		}
		start, end := fset.Position(group.Pos()).Offset, fset.Position(group.End()).Offset
		if string(data[start:end]) != expected {
			return nil, fmt.Errorf("unexpected scanner documentation")
		}
		edits = append(edits, textEdit{start, end, replacement})
	}
	return edits, nil
}

// isSupportCopyFile reports whether name is a Go source eject copies into
// the support module: runtime variants, test files, and non-Go files stay
// out.
func isSupportCopyFile(name string) bool {
	return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_runtime.go") && !strings.HasSuffix(name, "_test.go")
}

// ejectedBuildLines are the build lines that hold in every build of the
// support copy: the copy is the ejected form, and it has no monitored
// file, so the tag logosconc selects nothing in it.
var ejectedBuildLines = [][]byte{
	[]byte("//go:build logosejected"),
	[]byte("// +build logosejected"),
	[]byte("//go:build !logosconc"),
}

// copySupportPackage copies the Go sources of one support package directory
// without descending into subdirectories: every subdirectory is a separate
// package the closure names explicitly when the ejected tree needs it.
// Files are rewritten to the ejected import paths and formatted by the
// gofmt pass. A package holding only .lgo files stops the eject: eject
// lowers user .lgo packages through the compiler into staging, but
// support packages ride from GOROOT source and have no such lowering.

// stripEjectedBuildTag removes ejected build lines, so the support copy
// compiles under plain go with no tags set.

// stripEjectedBuildTag removes the ejectedBuildLines, so the support
// copy compiles under plain go with every set of tags.
func stripEjectedBuildTag(data []byte) []byte {
	lines := bytes.Split(data, []byte("\n"))
	kept := lines[:0]
	for _, l := range lines {
		trimmed := bytes.TrimSpace(l)
		if slices.ContainsFunc(ejectedBuildLines, func(line []byte) bool { return bytes.Equal(trimmed, line) }) {
			continue
		}
		kept = append(kept, l)
	}
	return bytes.Join(kept, []byte("\n"))
}

// parseGoModFile reads and parses one go.mod file.
func parseGoModFile(path string) *modfile.File {
	data, err := os.ReadFile(path)
	if err != nil {
		base.Fatalf("logos eject: %v", err)
	}
	f, err := modfile.Parse(path, data, nil)
	if err != nil {
		base.Fatalf("logos eject: %v", err)
	}
	return f
}

// writeGoModFile formats one go.mod file back to disk.
func writeGoModFile(path string, f *modfile.File) {
	if err := os.WriteFile(path, modfile.Format(f.Syntax), 0666); err != nil {
		base.Fatalf("logos eject: %v", err)
	}
}

// goVersionOf returns the go directive of f for the support go.mod.
func goVersionOf(f *modfile.File) string {
	if f.Go != nil && f.Go.Version != "" {
		return f.Go.Version
	}
	return "1.27"
}

// rewriteMainGoMod points the ejected main go.mod at every ejected
// dependency module with require plus replace lines.
func rewriteMainGoMod(out string, plan *ejectPlan) {
	path := filepath.Join(out, "go.mod")
	f := parseGoModFile(path)
	for _, m := range plan.deps {
		ensureGoRequire(f, m.path, m.version)
		setGoReplace(f, m.path, m.version, "./_logos_deps/"+m.path+"@"+m.version)
	}
	rebaseLocalReplaces(f, plan.main.dir, out, ejectedPaths(out, plan))
	writeGoModFile(path, f)
}

func ejectedPaths(out string, plan *ejectPlan) map[string]string {
	m := map[string]string{}
	for _, dep := range plan.deps {
		m[dep.path] = filepath.Join(out, "_logos_deps", dep.path+"@"+dep.version)
	}
	return m
}

// localReplaceDirs returns the module-relative directories inside
// modDir that the local replaces of its go.mod name, so the module
// copy keeps the nested modules those replaces need.
func localReplaceDirs(modDir string) map[string]bool {
	f := parseGoModFile(filepath.Join(modDir, "go.mod"))
	absMod, err := filepath.Abs(modDir)
	if err != nil {
		base.Fatalf("logos eject: %v", err)
	}
	dirs := map[string]bool{}
	for _, r := range f.Replace {
		if r.New.Version != "" || r.New.Path == "" || filepath.IsAbs(r.New.Path) {
			continue
		}
		target := filepath.Join(absMod, filepath.FromSlash(r.New.Path))
		if rel, err := filepath.Rel(absMod, target); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			dirs[filepath.Clean(rel)] = true
		}
	}
	return dirs
}

func rebaseLocalReplaces(f *modfile.File, origDir, dstDir string, ejected map[string]string) {
	absOrig, err := filepath.Abs(origDir)
	if err != nil {
		base.Fatalf("logos eject: %v", err)
	}
	absDst, err := filepath.Abs(dstDir)
	if err != nil {
		base.Fatalf("logos eject: %v", err)
	}
	type pending struct {
		oldPath string
		oldVers string
		newPath string
	}
	var work []pending
	for _, r := range slices.Clone(f.Replace) {
		if _, ok := ejected[r.Old.Path]; ok {
			continue
		}
		if r.New.Version != "" || r.New.Path == "" || filepath.IsAbs(r.New.Path) {
			continue
		}
		target := filepath.Join(absOrig, filepath.FromSlash(r.New.Path))
		if relOrig, err := filepath.Rel(absOrig, target); err == nil && relOrig != ".." && !strings.HasPrefix(relOrig, ".."+string(filepath.Separator)) {
			dstTarget := filepath.Join(absDst, relOrig)
			if fi, err := os.Stat(dstTarget); err == nil && fi.IsDir() {
				target = dstTarget
			}
		}
		rel, err := filepath.Rel(absDst, target)
		if err != nil {
			base.Fatalf("logos eject: %v", err)
		}
		work = append(work, pending{r.Old.Path, r.Old.Version, "./" + filepath.ToSlash(rel)})
	}
	for _, w := range work {
		setGoReplace(f, w.oldPath, w.oldVers, w.newPath)
	}
}

// addSupportRequire points one ejected go.mod at the support module
// with require plus replace lines.
func addSupportRequire(gomodPath, target string) {
	f := parseGoModFile(gomodPath)
	ensureGoRequire(f, rtModulePath, rtRequireVersion)
	setGoReplace(f, rtModulePath, rtRequireVersion, target)
	writeGoModFile(gomodPath, f)
}

// relEjectTarget returns the go.mod replace target from one ejected
// directory to another.
func relEjectTarget(from, to string) string {
	rel, err := filepath.Rel(from, to)
	if err != nil {
		base.Fatalf("logos eject: %v", err)
	}
	return "./" + filepath.ToSlash(rel)
}

// rewriteDepGoMod points one ejected dependency go.mod at every other
// ejected dependency module, and repoints its own replaces of ejected
// modules at the ejected copies.
func rewriteDepGoMod(plan *ejectPlan, m *ejectModule, ejected map[string]string) {
	dst := ejected[m.path]
	f := parseGoModFile(filepath.Join(dst, "go.mod"))
	for _, other := range plan.deps {
		if other.path == m.path {
			continue
		}
		target := relEjectTarget(dst, ejected[other.path])
		ensureGoRequire(f, other.path, other.version)
		setGoReplace(f, other.path, other.version, target)
	}
	var olds [][2]string
	for _, r := range f.Replace {
		if _, ok := ejected[r.Old.Path]; ok {
			olds = append(olds, [2]string{r.Old.Path, r.Old.Version})
		}
	}
	for _, o := range olds {
		setGoReplace(f, o[0], o[1], relEjectTarget(dst, ejected[o[0]]))
	}
	rebaseLocalReplaces(f, m.dir, dst, ejected)
	writeGoModFile(filepath.Join(dst, "go.mod"), f)
}

// ensureGoRequire adds a require line unless one already names path.
func ensureGoRequire(f *modfile.File, path, vers string) {
	for _, r := range f.Require {
		if r.Mod.Path == path {
			return
		}
	}
	if err := f.AddRequire(path, vers); err != nil {
		base.Fatalf("logos eject: %v", err)
	}
}

// setGoReplace points every replace of oldPath at newPath, dropping
// stale targets first so no duplicate replaces remain.
func setGoReplace(f *modfile.File, oldPath, oldVers, newPath string) {
	for _, r := range slices.Clone(f.Replace) {
		if r.Old.Path == oldPath && (r.New.Path != newPath || r.New.Version != "") {
			if err := f.DropReplace(r.Old.Path, r.Old.Version); err != nil {
				base.Fatalf("logos eject: %v", err)
			}
		}
	}
	for _, r := range f.Replace {
		if r.Old.Path == oldPath && r.New.Path == newPath && r.New.Version == "" {
			return
		}
	}
	if err := f.AddReplace(oldPath, oldVers, newPath, ""); err != nil {
		base.Fatalf("logos eject: %v", err)
	}
}

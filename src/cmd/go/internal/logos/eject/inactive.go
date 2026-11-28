// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"context"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"internal/logosfile"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"cmd/go/internal/base"
	"cmd/go/internal/cfg"
	"cmd/go/internal/fsys"
	"cmd/go/internal/load"
	"cmd/go/internal/modinfo"
	"cmd/go/internal/modload"
	"cmd/go/internal/search"
)

// collectInactiveLogosFiles inventories owned source directories independently
// of the host package load. Census-only package records describe source paths;
// they never enter a build root or the declaration-provenance plan. Only actual
// helper loads supply the packages later used for emission.
// cgoOnlyLogosGap reports an owned Go source the host build leaves out
// that imports C together with LoGOs packages the load never reaches.
// Lowering those packages without cgo would bake the wrong branch, and
// no helper can lower them with cgo while the host disables it, so the
// eject must refuse instead of emitting a tree that disagrees with the
// native build. A missing import whose whole logos closure is pure Go
// needs no lowering: the support copy carries it directly. A file left
// out while cgo is enabled is inactive by platform, not by toolchain, so
// the CGO_ENABLED=1 advice cannot help and no gap is reported. The second
// result names the file and the unreachable packages for the refusal.
func cgoOnlyLogosGap(path string, plan *ejectPlan) (string, bool) {
	data, err := fsys.ReadFile(path)
	if err != nil || isBuildIgnored(data) {
		return "", false
	}
	imports := goSourceImports(data)
	hasCgo := false
	var missing []string
	seen := map[string]bool{}
	for _, imp := range imports {
		if imp == "C" {
			hasCgo = true
			continue
		}
		rest, ok := strings.CutPrefix(imp, "logos/")
		if !ok || rest == "" || seen[imp] {
			continue
		}
		seen[imp] = true
		if _, ok := plan.provenance[imp]; !ok && imp != load.LogosRTPkg {
			if logosNeedsLowering(filepath.Join(cfg.GOROOT, "src"), imp, map[string]bool{}) {
				missing = append(missing, imp)
			}
		}
	}
	if !hasCgo || len(missing) == 0 || cfg.BuildContext.CgoEnabled {
		return "", false
	}
	slices.Sort(missing)
	return path + " imports C and unreachable " + strings.Join(missing, ", "), true
}

// logosNeedsLowering reports whether ejecting the GOROOT logos package
// imp needs compiler lowering: the package itself or any logos package
// it reaches through Go imports holds LoGOs sources. Anything unprovable
// (an unreadable directory or file) counts as needing lowering, so the
// gap stays fail-closed.
func logosNeedsLowering(srcDir, imp string, seen map[string]bool) bool {
	if seen[imp] {
		return false
	}
	seen[imp] = true
	dir := filepath.Join(srcDir, filepath.FromSlash(imp))
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
			continue
		}
		if logosfile.Is(name) {
			return true
		}
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := fsys.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return true
		}
		for _, dep := range goSourceImports(data) {
			rest, ok := strings.CutPrefix(dep, "logos/")
			if !ok || rest == "" {
				continue
			}
			if logosNeedsLowering(srcDir, dep, seen) {
				return true
			}
		}
	}
	return false
}

func collectInactiveLogosFiles(plan *ejectPlan, staging string) []inactiveFile {
	active := map[string]bool{}
	loaded := map[string]*load.Package{}
	for _, p := range plan.logos {
		loaded[p.Dir] = p
		for _, files := range [][]string{p.GoFiles, p.CgoFiles} {
			for _, name := range files {
				active[filepath.Join(p.Dir, name)] = true
			}
		}
		if plan.testBuilt[p] {
			for _, files := range [][]string{p.TestGoFiles, p.XTestGoFiles} {
				for _, name := range files {
					active[filepath.Join(p.Dir, name)] = true
				}
			}
		}
	}
	activeGo := map[string]bool{}
	for _, p := range plan.main.pkgs {
		for _, files := range [][]string{p.GoFiles, p.CgoFiles} {
			for _, name := range files {
				activeGo[filepath.Join(p.Dir, name)] = true
			}
		}
	}
	for _, m := range plan.deps {
		for _, p := range m.pkgs {
			for _, files := range [][]string{p.GoFiles, p.CgoFiles} {
				for _, name := range files {
					activeGo[filepath.Join(p.Dir, name)] = true
				}
			}
		}
	}
	var cgoGaps []string
	var out []inactiveFile
	for _, module := range append([]*ejectModule{plan.main}, plan.deps...) {
		modFile := parseGoModFile(filepath.Join(module.dir, "go.mod"))
		if module.path == "" {
			module.path = modFile.Module.Mod.Path
		}
		var patterns []string
		for _, ignored := range modFile.Ignore {
			patterns = append(patterns, ignored.Path)
		}
		ignored := search.NewIgnorePatterns(patterns)
		root := filepath.Clean(module.dir)
		// A module root itself may be a symlink. Nested directory symlinks
		// remain outside the package walk, like cmd/go's package matching.
		var walk func(string) error
		walk = func(dir string) error {
			entries, err := fsys.ReadDir(dir)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				name := entry.Name()
				path := filepath.Join(dir, name)
				if entry.IsDir() {
					rel, err := filepath.Rel(root, path)
					if err != nil {
						return err
					}
					if name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || ignored.ShouldIgnore(filepath.ToSlash(rel)) {
						continue
					}
					if info, err := fsys.Stat(filepath.Join(path, "go.mod")); err == nil && !info.IsDir() {
						continue
					} else if err != nil && !os.IsNotExist(err) {
						return err
					}
					if err := walk(path); err != nil {
						return err
					}
					continue
				}
				if entry.Type().IsRegular() && !strings.HasPrefix(name, ".") && !strings.HasPrefix(name, "_") && isSupportCopyFile(name) && !activeGo[path] {
					if cgoGap, ok := cgoOnlyLogosGap(path, plan); ok {
						cgoGaps = append(cgoGaps, cgoGap)
					}
				}
				if !entry.Type().IsRegular() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || !logosfile.Is(name) || active[path] {
					continue
				}
				p := loaded[dir]
				if p == nil {
					pkgRel, err := filepath.Rel(root, dir)
					if err != nil {
						return err
					}
					importPath := module.path
					if pkgRel != "." {
						importPath += "/" + filepath.ToSlash(pkgRel)
					}
					p = &load.Package{PackagePublic: load.PackagePublic{
						Dir: dir, ImportPath: importPath,
						Module: &modinfo.ModulePublic{Path: module.path, Version: module.version, Dir: module.dir, Main: module == plan.main},
					}}
					loaded[dir] = p
				}
				staged := filepath.Join(staging, filepath.FromSlash(p.ImportPath), logosStem(name)+".go")
				if _, err := os.Stat(staged); err == nil {
					continue
				} else if !os.IsNotExist(err) {
					return fmt.Errorf("stat staged source %s: %w", staged, err)
				}
				data, err := fsys.ReadFile(path)
				if err != nil {
					return fmt.Errorf("read inactive source %s: %w", path, err)
				}
				if !isBuildIgnored(data) {
					out = append(out, inactiveFile{pkg: p, name: name})
				}
			}
			return nil
		}
		if err := walk(root); err != nil {
			base.Fatalf("logos eject: inventory module %s: %v", module.path, err)
		}
		if len(cgoGaps) > 0 {
			slices.Sort(cgoGaps)
			base.Fatalf("logos eject: cgo-only Go sources reach logos packages the load cannot see without cgo:\n%s\nexport with CGO_ENABLED=1 and a C compiler", strings.Join(cgoGaps, "\n"))
		}
	}
	collectInactiveGorootLogosFiles(plan, staging, active, loaded, &out)
	slices.SortFunc(out, func(a, b inactiveFile) int {
		if order := strings.Compare(a.pkg.ImportPath, b.pkg.ImportPath); order != 0 {
			return order
		}
		return strings.Compare(a.name, b.name)
	})
	return out
}

// discoverGoImportLogos loads GOROOT logos/* packages that only inactive
// Go sources import. The loader never sees those imports, so without this
// the packages miss phase-one lowering and the support copy fails on their
// missing staged output even for the default build. Only files the support
// copy would read seed the search, and ignore-tagged files stay out exactly
// as in the copy.
// ejectDiscoverDirs returns the GOROOT source directories to scan for
// logos imports: every reached GOROOT logos package plus every
// platform-only dependency the host load could not resolve. Without the
// unloadable dirs, an inactive Go import that reaches another
// platform-only package hides that package's LoGOs dependencies from
// staging, and the support copy fails on their missing output.
func ejectDiscoverDirs(plan *ejectPlan, unloadable map[string]bool) []string {
	dirs := map[string]bool{}
	for _, path := range slices.Sorted(maps.Keys(plan.provenance)) {
		for _, p := range plan.provenance[path] {
			if !isGorootLogos(p) {
				continue
			}
			dirs[p.Dir] = true
		}
	}
	for path := range unloadable {
		dirs[filepath.Join(cfg.GOROOT, "src", filepath.FromSlash(path))] = true
	}
	return slices.Sorted(maps.Keys(dirs))
}

// ownedInactiveGoLogosImports scans owned modules for Go files the host
// build leaves out that import LoGOs packages the load never reached.
// With cgo enabled such a file is inactive by platform or tag, not by
// toolchain, so its imports must join discovery for the support copy to
// carry them. With cgo disabled nothing is reported here and the
// cgo-gap refusal keeps applying. Only files the support copy would
// read seed the search, exactly as in the variant inventory.
func ownedInactiveGoLogosImports(plan *ejectPlan, known, added map[string]bool) []string {
	if !cfg.BuildContext.CgoEnabled {
		return nil
	}
	activeGo := map[string]bool{}
	for _, module := range append([]*ejectModule{plan.main}, plan.deps...) {
		for _, p := range module.pkgs {
			for _, files := range [][]string{p.GoFiles, p.CgoFiles} {
				for _, name := range files {
					activeGo[filepath.Join(p.Dir, name)] = true
				}
			}
		}
	}
	var newPaths []string
	for _, module := range append([]*ejectModule{plan.main}, plan.deps...) {
		modFile := parseGoModFile(filepath.Join(module.dir, "go.mod"))
		if module.path == "" {
			module.path = modFile.Module.Mod.Path
		}
		var patterns []string
		for _, ignored := range modFile.Ignore {
			patterns = append(patterns, ignored.Path)
		}
		ignored := search.NewIgnorePatterns(patterns)
		root := filepath.Clean(module.dir)
		var walk func(string) error
		walk = func(dir string) error {
			entries, err := fsys.ReadDir(dir)
			if err != nil {
				return err
			}
			for _, entry := range entries {
				name := entry.Name()
				path := filepath.Join(dir, name)
				if entry.IsDir() {
					rel, err := filepath.Rel(root, path)
					if err != nil {
						return err
					}
					if name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || ignored.ShouldIgnore(filepath.ToSlash(rel)) {
						continue
					}
					if info, err := fsys.Stat(filepath.Join(path, "go.mod")); err == nil && !info.IsDir() {
						continue
					} else if err != nil && !os.IsNotExist(err) {
						return err
					}
					if err := walk(path); err != nil {
						return err
					}
					continue
				}
				if !entry.Type().IsRegular() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || !isSupportCopyFile(name) || activeGo[path] {
					continue
				}
				data, err := fsys.ReadFile(path)
				if err != nil {
					return fmt.Errorf("read inactive Go source %s: %w", path, err)
				}
				for _, imp := range goSourceImports(data) {
					rest, ok := strings.CutPrefix(imp, "logos/")
					if !ok || rest == "" || imp == load.LogosRTPkg || known[imp] || added[imp] {
						continue
					}
					added[imp] = true
					newPaths = append(newPaths, imp)
				}
			}
			return nil
		}
		if err := walk(root); err != nil {
			base.Fatalf("logos eject: inventory module %s: %v", module.path, err)
		}
	}
	return newPaths
}

func discoverGoImportLogos(moduleLoader *modload.Loader, ctx context.Context, opts load.PackageOpts, plan *ejectPlan) {
	unloadable := map[string]bool{}
	// Host-active GOROOT sources need no gap check: the main load already
	// sees their imports. Anything else the scan reads is inactive on the
	// host, so a cgo-only file among them reaches its logos imports
	// exactly like the owned-module case cgoOnlyLogosGap refuses.
	active := map[string]bool{}
	for _, path := range slices.Sorted(maps.Keys(plan.provenance)) {
		for _, p := range plan.provenance[path] {
			if !isGorootLogos(p) {
				continue
			}
			for _, files := range [][]string{p.GoFiles, p.CgoFiles} {
				for _, name := range files {
					active[filepath.Join(p.Dir, name)] = true
				}
			}
		}
	}
	for {
		dirs := ejectDiscoverDirs(plan, unloadable)
		known := map[string]bool{}
		for path := range plan.provenance {
			known[path] = true
		}
		for path := range unloadable {
			known[path] = true
		}
		added := map[string]bool{}
		var newPaths []string
		var cgoGaps []string
		for _, dir := range dirs {
			entries, err := fsys.ReadDir(dir)
			if err != nil {
				base.Fatalf("logos eject: inventory GOROOT sources in %s: %v", dir, err)
			}
			for _, entry := range entries {
				name := entry.Name()
				if entry.IsDir() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || !isSupportCopyFile(name) {
					continue
				}
				data, err := fsys.ReadFile(filepath.Join(dir, name))
				if err != nil {
					base.Fatalf("logos eject: read GOROOT source %s: %v", filepath.Join(dir, name), err)
				}
				for _, imp := range goSourceImports(data) {
					rest, ok := strings.CutPrefix(imp, "logos/")
					if !ok || rest == "" || known[imp] || added[imp] {
						continue
					}
					added[imp] = true
					newPaths = append(newPaths, imp)
				}
				if fpath := filepath.Join(dir, name); !active[fpath] {
					if gap, ok := cgoOnlyLogosGap(fpath, plan); ok {
						cgoGaps = append(cgoGaps, gap)
					}
				}
			}
		}
		// Owned inactive Go files join the same discovery when cgo is
		// enabled: the host load never sees their imports either.
		newPaths = append(newPaths, ownedInactiveGoLogosImports(plan, known, added)...)
		if len(cgoGaps) > 0 {
			slices.Sort(cgoGaps)
			base.Fatalf("logos eject: cgo-only Go sources reach logos packages the load cannot see without cgo:\n%s\nexport with CGO_ENABLED=1 and a C compiler", strings.Join(cgoGaps, "\n"))
		}
		if len(newPaths) == 0 {
			// A discovered package with no buildable source for the host
			// target stays out of provenance, but the variant inventory
			// still needs its host-inactive sources: a selecting-context
			// helper lowers them into staging for the support copy.
			// Without this, the copy reaches the package through the
			// inactive import edge and aborts on the missing staged output.
			if len(unloadable) > 0 {
				plan.unloadable = slices.Sorted(maps.Keys(unloadable))
			}
			return
		}
		slices.Sort(newPaths)
		pkgs := load.PackagesAndErrors(moduleLoader, ctx, opts, newPaths)
		checkDiscoveredPackageErrors(pkgs, added, unloadable)
		for _, p := range load.PackageList(pkgs) {
			if p == nil || p.Error != nil {
				continue
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
		}
	}
}

// checkDiscoveredPackageErrors fatals on failures loading discovered
// packages, except a discovered package with no Go files for the current
// target. A platform-only dependency (for example a helper holding only a
// windows-tagged file) cannot load on this target, and aborting would
// reject the valid alternate build the variant helpers lower. The same
// holds one level down: a platform-only package reached transitively
// through a newly discovered closure never entered the request list, but
// its NoGoError still surfaces here, so logos-prefixed transitive failures
// join the unloadable set too. Skipped packages stay out of provenance;
// anything they would have contributed to the current target fails loudly
// where it is used.
func checkDiscoveredPackageErrors(pkgs []*load.Package, discovered, unloadable map[string]bool) {
	load.PackageErrors(pkgs, func(p *load.Package) {
		var nogo *load.NoGoError
		if errors.As(p.Error, &nogo) && (discovered[p.ImportPath] || strings.HasPrefix(p.ImportPath, "logos/")) {
			unloadable[p.ImportPath] = true
			return
		}
		load.DefaultPrinter().Errorf(p, "%v", p.Error)
	})
	base.ExitIfErrors()
}

// goSourceImports returns the import paths of one Go source, or nil when
// the file does not parse or carries an ignore constraint. Unparseable and
// ignored files stay out of the discovery exactly as they stay out of the
// copy, which still fails loudly on them when it matters.
func goSourceImports(data []byte) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", data, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	expr, err := ownedHeaderExpr(f)
	if err == nil && hasIgnoreTag(expr) {
		return nil
	}
	var out []string
	for _, imp := range f.Imports {
		impPath, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		out = append(out, impPath)
	}
	return out
}

// collectInactiveGorootLogosFiles inventories inactive .lgo files of the
// reached GOROOT logos/* packages. Their host-inactive files need helper
// lowering like owned variants, so the support module emits them with
// their build constraints intact. Test files stay out: the support
// module ships no tests, and an unstageable test file would keep the
// variant fixed point from converging.
func collectInactiveGorootLogosFiles(plan *ejectPlan, staging string, active map[string]bool, loaded map[string]*load.Package, out *[]inactiveFile) {
	dirs := map[string]*load.Package{}
	for _, path := range slices.Sorted(maps.Keys(plan.provenance)) {
		for _, p := range plan.provenance[path] {
			if !isGorootLogos(p) {
				continue
			}
			for _, files := range [][]string{p.GoFiles, p.CgoFiles, p.TestGoFiles, p.XTestGoFiles} {
				for _, name := range files {
					active[filepath.Join(p.Dir, name)] = true
				}
			}
			if _, ok := loaded[p.Dir]; !ok {
				loaded[p.Dir] = p
			}
			if _, ok := dirs[p.Dir]; !ok {
				dirs[p.Dir] = p
			}
		}
	}
	// Packages the host load cannot resolve still need helper lowering
	// for the support copy, so seed the walk with census records for
	// them. Their sources are host-inactive by definition (an active
	// source would have loaded), and the walk below inventories exactly
	// those for a selecting-context helper.
	for _, path := range plan.unloadable {
		dir := filepath.Join(cfg.GOROOT, "src", filepath.FromSlash(path))
		if _, ok := dirs[dir]; ok {
			continue
		}
		p := &load.Package{PackagePublic: load.PackagePublic{Dir: dir, ImportPath: path}}
		dirs[dir] = p
		if _, ok := loaded[dir]; !ok {
			loaded[dir] = p
		}
	}
	var walk func(string) error
	walk = func(dir string) error {
		entries, err := fsys.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			path := filepath.Join(dir, name)
			if entry.IsDir() {
				if name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
					continue
				}
				if err := walk(path); err != nil {
					return err
				}
				continue
			}
			if !entry.Type().IsRegular() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || !logosfile.Is(name) || active[path] {
				continue
			}
			if strings.HasSuffix(logosStem(name), "_test") {
				continue
			}
			p := loaded[dir]
			if p == nil {
				continue
			}
			staged := filepath.Join(staging, filepath.FromSlash(p.ImportPath), logosStem(name)+".go")
			if _, err := os.Stat(staged); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				base.Fatalf("logos eject: stat staged source %s: %v", staged, err)
			}
			data, err := fsys.ReadFile(path)
			if err != nil {
				base.Fatalf("logos eject: read inactive source %s: %v", path, err)
			}
			if !isBuildIgnored(data) {
				*out = append(*out, inactiveFile{pkg: p, name: name})
			}
		}
		return nil
	}
	for _, dir := range slices.Sorted(maps.Keys(dirs)) {
		if err := walk(dir); err != nil {
			base.Fatalf("logos eject: inventory GOROOT package %s: %v", dirs[dir].ImportPath, err)
		}
	}
}

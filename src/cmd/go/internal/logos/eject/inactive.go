// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"context"
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
func discoverGoImportLogos(moduleLoader *modload.Loader, ctx context.Context, opts load.PackageOpts, plan *ejectPlan) {
	for {
		dirs := map[string]bool{}
		for _, path := range slices.Sorted(maps.Keys(plan.provenance)) {
			for _, p := range plan.provenance[path] {
				if !isGorootLogos(p) {
					continue
				}
				dirs[p.Dir] = true
			}
		}
		known := map[string]bool{}
		for path := range plan.provenance {
			known[path] = true
		}
		added := map[string]bool{}
		var newPaths []string
		for _, dir := range slices.Sorted(maps.Keys(dirs)) {
			entries, err := fsys.ReadDir(dir)
			if err != nil {
				base.Fatalf("logos eject: inventory GOROOT sources in %s: %v", dir, err)
			}
			for _, entry := range entries {
				name := entry.Name()
				if entry.IsDir() || !isSupportCopyFile(name) {
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
			}
		}
		if len(newPaths) == 0 {
			return
		}
		slices.Sort(newPaths)
		pkgs := load.PackagesAndErrors(moduleLoader, ctx, opts, newPaths)
		load.CheckPackageErrors(pkgs)
		for _, p := range load.PackageList(pkgs) {
			if p == nil {
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
			if !entry.Type().IsRegular() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || !isSupportLogosFile(name) || active[path] {
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

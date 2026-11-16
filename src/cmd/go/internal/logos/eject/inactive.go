// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"fmt"
	"internal/logosfile"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"cmd/go/internal/base"
	"cmd/go/internal/load"
	"cmd/go/internal/modinfo"
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
		err := filepath.WalkDir(root+string(filepath.Separator), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			name := entry.Name()
			if entry.IsDir() {
				if rel != "." {
					if name == "vendor" || name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || ignored.ShouldIgnore(filepath.ToSlash(rel)) {
						return filepath.SkipDir
					}
					if info, err := os.Stat(filepath.Join(path, "go.mod")); err == nil && !info.IsDir() {
						return filepath.SkipDir
					} else if err != nil && !os.IsNotExist(err) {
						return err
					}
				}
				return nil
			}
			if !entry.Type().IsRegular() || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || !logosfile.Is(name) || active[path] {
				return nil
			}
			dir := filepath.Dir(path)
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
				return nil
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("stat staged source %s: %w", staged, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read inactive source %s: %w", path, err)
			}
			if !isBuildIgnored(data) {
				out = append(out, inactiveFile{pkg: p, name: name})
			}
			return nil
		})
		if err != nil {
			base.Fatalf("logos eject: inventory module %s: %v", module.path, err)
		}
	}
	slices.SortFunc(out, func(a, b inactiveFile) int {
		if order := strings.Compare(a.pkg.ImportPath, b.pkg.ImportPath); order != 0 {
			return order
		}
		return strings.Compare(a.name, b.name)
	})
	return out
}

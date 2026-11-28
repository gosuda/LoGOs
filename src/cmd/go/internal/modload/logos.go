// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package modload

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"cmd/go/internal/cfg"
	"cmd/go/internal/fsys"
	"cmd/go/internal/search"
	"cmd/internal/pkgpattern"
)

// logosDirHasGoFiles reports whether dir holds a plausible Go-family
// source file: a regular file whose name satisfies cfg.IsGoFile.
// It mirrors fsys.IsGoDir (which stays untouched) for logos mode,
// including the symlink-target check, but admits .lgo and alias
// extensions through the shared cfg predicate.
func logosDirHasGoFiles(dir string) (bool, error) {
	fis, err := fsys.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		// Mirror fsys.IsGoDir: a path that exists but is not a
		// directory holds no Go files.
		if fi, statErr := fsys.Stat(dir); statErr == nil && !fi.IsDir() {
			return false, nil
		}
		return false, err
	}

	var firstErr error
	for _, d := range fis {
		if d.IsDir() || !cfg.IsGoFile(d.Name()) {
			continue
		}
		if d.Type().IsRegular() {
			return true, nil
		}

		// d is a non-directory, non-regular Go-family file.
		// Stat to see if it is a symlink, which we allow.
		if actual := fsys.Actual(filepath.Join(dir, d.Name())); actual != "" {
			fi, err := os.Stat(actual)
			if err == nil && fi.Mode().IsRegular() {
				return true, nil
			}
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}

	// No Go-family files found in directory.
	return false, firstErr
}

// matchLogosWorkspaceRoot handles logos-only wildcard patterns issued
// from the go.work directory itself, which contains workspace modules
// but is not inside any of them. Upstream matchLocalDirs rejects such
// patterns before any scan runs, and the per-directory walk skips
// nested modules, so neither path can enumerate the workspace members.
// In logos mode this helper unions the per-module matches instead.
// Plain go never reaches it.
//
// Each member is walked with an absolute sub-pattern through the
// existing search.Match scanning; nothing is registered anywhere.
// It returns true when member walks found directories (handled),
// false when the caller must fall through to the upstream rejection,
// so an empty union is never a silent zero-package match.
func matchLogosWorkspaceRoot(ld *Loader, m *search.Match, modRoots []string, absDir string) bool {
	if !cfg.Logos {
		return false
	}
	if !strings.Contains(m.Pattern(), "...") {
		return false
	}
	if !ld.inWorkspaceMode() {
		return false
	}
	if filepath.Clean(absDir) != filepath.Clean(filepath.Dir(WorkFilePath(ld))) {
		return false
	}

	_, suffix, _ := strings.Cut(filepath.ToSlash(m.Pattern()), "...")

	seen := make(map[string]bool)
	m.Dirs = []string{}
	for _, mr := range modRoots {
		if mr == "" {
			continue
		}
		mr = filepath.Clean(mr)
		subMatch := search.NewMatch(filepath.ToSlash(mr) + "/..." + suffix)
		subMatch.MatchDirs([]string{mr})
		m.Errs = append(m.Errs, subMatch.Errs...)
		for _, d := range subMatch.Dirs {
			if !seen[d] {
				seen[d] = true
				m.Dirs = append(m.Dirs, d)
			}
		}
	}
	return len(m.Dirs) > 0
}

// logosCandidates collects directories that hold LoGOs sources and no Go
// files while the resolver looks for a Go provider. dirInModule stays free
// of relaunch side effects; the import trigger fires only when the resolver
// found no Go provider and exactly one such directory.
type logosCandidates struct {
	seen map[string]bool
	dirs []string
}

// add records dir once.
func (c *logosCandidates) add(dir string) {
	if dir == "" {
		return
	}
	if c.seen == nil {
		c.seen = make(map[string]bool)
	}
	if !c.seen[dir] {
		c.seen[dir] = true
		c.dirs = append(c.dirs, dir)
	}
}

// single reports the one LoGOs-only directory when exactly one exists.
func (c *logosCandidates) single() (string, bool) {
	if len(c.dirs) == 1 {
		return c.dirs[0], true
	}
	return "", false
}

// logosPatternDirs returns the directories under root whose import path
// (prefix joined with the slash-separated path relative to root) matches
// the ...-style pattern and that hold LoGOs sources and no .go files. It
// mirrors the package walker's eligibility: dot, underscore, and testdata
// trees are skipped, vendor subtrees are pruned, nested modules are
// skipped, go.mod ignore rules apply, and subtrees the pattern cannot
// match are pruned. Only a directory the full pattern matches is
// returned, so a wildcard that matches no package records no candidate.
func logosPatternDirs(root, prefix, pattern string, ignore *search.IgnorePatterns, isLocal bool) []string {
	var dirs []string
	match := pkgpattern.MatchPattern(pattern)
	treeCanMatch := pkgpattern.TreeCanMatchPattern(pattern)
	stack := []string{root}
	for len(stack) > 0 {
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		relSlash := ""
		if d != root {
			rel, err := filepath.Rel(root, d)
			if err != nil {
				continue
			}
			relSlash = filepath.ToSlash(rel)
			_, elem := filepath.Split(d)
			if strings.HasPrefix(elem, ".") || strings.HasPrefix(elem, "_") || elem == "testdata" || elem == "vendor" {
				continue
			}
			if isLocal {
				if info, err := os.Stat(filepath.Join(d, "go.mod")); err == nil && !info.IsDir() {
					continue
				}
			}
		}
		name := prefix
		if relSlash != "" {
			name = path.Join(prefix, relSlash)
		}
		if !treeCanMatch(name) {
			continue
		}
		if ignore != nil && relSlash != "" && ignore.ShouldIgnore(relSlash) {
			continue
		}
		if match(name) && cfg.LogosOnlyDir(d) {
			dirs = append(dirs, d)
		}
		fis, err := fsys.ReadDir(d)
		if err != nil {
			continue
		}
		for _, fi := range fis {
			if fi.IsDir() {
				stack = append(stack, filepath.Join(d, fi.Name()))
			}
		}
	}
	return dirs
}

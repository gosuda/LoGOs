// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package settings

import (
	"errors"
	"internal/logosfile"
	"io/fs"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"cmd/go/internal/logos/config"
)

type Module struct {
	Root string
	File *config.File
	Err  error

	scanOnce  sync.Once
	logos     bool
	censusErr error
	reported  atomic.Bool
}

var moduleCache = struct {
	sync.Mutex
	byRoot map[string]*Module
}{byRoot: make(map[string]*Module)}

var moduleParseCount int

func ModuleAt(root string) *Module {
	moduleCache.Lock()
	defer moduleCache.Unlock()
	if m, ok := moduleCache.byRoot[root]; ok {
		return m
	}
	file, err := config.Load(root)
	m := &Module{Root: root, File: file, Err: err}
	moduleParseCount++
	moduleCache.byRoot[root] = m
	return m
}

func (m *Module) census() (bool, error) {
	if m.Root == "" {
		return false, nil
	}
	m.scanOnce.Do(func() { m.logos, m.censusErr = censusTreeWalk(m.Root) })
	return m.logos, m.censusErr
}

func (m *Module) Logos() bool {
	found, _ := m.census()
	return found
}

// CensusErr reports a resource failure of the .lgo census walk over the
// module root: out of memory, out of file descriptors, or an I/O error.
// A nil error means the walk completed and Logos is trustworthy; a
// non-nil error means the walk could have missed .lgo files, so the
// caller must fail instead of treating the module as a Go module.
func (m *Module) CensusErr() error {
	_, err := m.census()
	return err
}

func (m *Module) ClaimError() bool {
	return !m.reported.Swap(true)
}

func (m *Module) CgoOpinion(cgoEnv string) (enabled, opinion bool) {
	if m.Err != nil || m.File == nil {
		return false, true
	}
	if m.File.Build.CgoSet {
		if !m.Logos() {
			return false, false
		}
		return m.File.Build.Cgo == "allow" || m.File.Build.Cgo == "restricted", true
	}
	if cgoEnv == "0" || cgoEnv == "1" {
		return cgoEnv == "1", true
	}
	if !m.Logos() {
		return false, false
	}
	return false, true
}

const cgoDeny = "[restriction::cgo_usage] cgo is disabled"

func (m *Module) CgoAllow(cgoEnv, importPath string) (bool, string) {
	if m.File == nil {
		return false, cgoDeny
	}
	if m.File.Build.CgoSet {
		switch m.File.Build.Cgo {
		case "allow":
			return true, ""
		case "restricted":
			if CgoAllowlistAllows(m.File.Build.CgoAllowlist, importPath) {
				return true, ""
			}
			return false, "[restriction::cgo_usage] package " + importPath + " uses cgo and is not in cgo_allowlist"
		default:
			return false, cgoDeny
		}
	}
	if cgoEnv == "1" {
		return true, ""
	}
	return false, cgoDeny
}

func (m *Module) Severity(group, def string) string {
	if m.Err != nil || m.File == nil {
		return def
	}
	if v, ok := m.File.Lints.Groups[group]; ok {
		return v
	}
	return def
}

func CgoAllowlistAllows(allowlist []string, importPath string) bool {
	for _, pattern := range allowlist {
		if cgoPatternAllows(pattern, importPath) {
			return true
		}
	}
	return false
}

func cgoPatternAllows(pattern, importPath string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
		return importPath == prefix || strings.HasPrefix(importPath, prefix+"/")
	}
	if prefix, ok := strings.CutSuffix(pattern, "**"); ok {
		return strings.HasPrefix(importPath, prefix)
	}
	ok, _ := path.Match(pattern, importPath)
	return ok
}

// censusFatal reports whether a census walk error leaves the result
// untrustworthy. Routine errors (an unreadable or vanished entry) skip
// the subtree they came from, as before. Resource and integrity failures
// fail the census: a "no .lgo files" answer computed through them could
// silently drop the [build] cgo policy, including the -race exception,
// and surface later as a misleading refusal. An fs.ErrInvalid walk entry
// is fatal too: DirFS rejects non-UTF-8 names, which the native walker
// traversed, so skipping one could hide the only .lgo files. Native OS
// resource failures the POSIX compatibility values miss (Windows) count
// through censusFatalErrno.
func censusFatal(err error) bool {
	if errors.Is(err, fs.ErrInvalid) {
		return true
	}
	if errors.Is(err, syscall.ENOMEM) ||
		errors.Is(err, syscall.ENFILE) ||
		errors.Is(err, syscall.EMFILE) ||
		errors.Is(err, syscall.EIO) {
		return true
	}
	var errno syscall.Errno
	return errors.As(err, &errno) && censusFatalErrno(errno)
}

// censusTreeWalk is replaceable in tests to fault-inject the census.
var censusTreeWalk = treeHasLogos

func treeHasLogos(root string) (bool, error) {
	return treeHasLogosFS(os.DirFS(root))
}

// treeHasLogosFS walks fsys, which holds the module tree, and reports
// whether it holds a LoGOs source file. The walk takes an fs.FS so tests
// can inject faults; production passes the module root directory.
func treeHasLogosFS(fsys fs.FS) (bool, error) {
	var firstErr error
	found := false
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if found {
			return fs.SkipAll
		}
		if err != nil {
			if firstErr == nil && censusFatal(err) {
				firstErr = err
			}
			return nil
		}
		if d.IsDir() {
			if p != "." {
				skip, serr := skipsDirFS(fsys, p, d.Name())
				if serr != nil {
					if firstErr == nil {
						firstErr = serr
					}
					// A directory that might be a nested module cannot
					// contribute its .lgo files either way; fail closed.
					return fs.SkipDir
				}
				if skip {
					return fs.SkipDir
				}
			}
			return nil
		}
		if logosfile.Is(d.Name()) {
			found = true
		}
		return nil
	})
	// A found answer is already correct: a fatal walk error alongside it
	// does not make the cgo policy untrustworthy.
	if found {
		return true, nil
	}
	return false, firstErr
}

// skipsDirFS reports whether the walk should skip the directory p/name,
// and returns a fatal census error when the nested go.mod probe itself
// fails: a resource failure there could hide a nested module's .lgo
// files just as a failed directory open could.
func skipsDirFS(fsys fs.FS, p, name string) (bool, error) {
	if name == "vendor" || name == "testdata" {
		return true, nil
	}
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") {
		return true, nil
	}
	info, err := fs.Stat(fsys, path.Join(p, "go.mod"))
	if err != nil {
		if censusFatal(err) {
			return false, err
		}
		return false, nil
	}
	return !info.IsDir(), nil
}

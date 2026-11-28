// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cfg

import (
	"fmt"
	"internal/logosfile"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"cmd/go/internal/fsys"
	"golang.org/x/mod/modfile"
)

// IsLogosOnlyCommand reports whether name is a subcommand that exists
// only under logos. Plain go has no such command and reports it unknown.
func IsLogosOnlyCommand(name string) bool {
	switch name {
	case "lint", "eject", "context", "next", "conc", "lsp":
		return true
	}
	return false
}

// IsLogosRelaunchExcluded reports whether name is a command that never
// re-executes as logos. Their text stays byte-identical to upstream.
// fmt formats .go files and ignores .lgo files without re-executing.
func IsLogosRelaunchExcluded(name string) bool {
	switch name {
	case "version", "env", "help", "bug", "telemetry", "fmt":
		return true
	}
	return false
}

// DirHoldsOnlyLogosSources reports whether dir holds at least one LoGOs
// source file and no .go files. It reads entry names only, never contents.
func DirHoldsOnlyLogosSources(dir string) bool {
	fis, err := fsys.ReadDir(dir)
	if err != nil {
		return false
	}
	return dirEntriesHoldOnlyLogosSources(fis, dir)
}

// dirEntriesHoldOnlyLogosSources classifies already-read entries with the
// same rule DirHoldsOnlyLogosSources applies, so the early tree walk shares
// it instead of drifting.
func dirEntriesHoldOnlyLogosSources(fis []fs.DirEntry, dir string) bool {
	found := false
	for _, d := range fis {
		if d.IsDir() {
			continue
		}
		name := d.Name()
		if strings.HasSuffix(name, ".go") {
			return false
		}
		if !logosfile.Is(name) {
			continue
		}
		if d.Type().IsRegular() {
			found = true
			continue
		}
		// A non-regular LoGOs file counts only when it links to one.
		if target := fsys.Actual(filepath.Join(dir, name)); target != "" {
			if fi, err := os.Stat(target); err == nil && fi.Mode().IsRegular() {
				found = true
			}
		}
	}
	return found
}

// LogosOnlyDir reports whether dir holds LoGOs sources and no .go files,
// outside the standard library and outside excluded commands. It is the
// side-effect-free probe behind the late import trigger: the resolver
// records candidates with it and re-executes only when no Go provider
// exists and exactly one candidate does.
func LogosOnlyDir(dir string) bool {
	if dir == "" || Logos || !relaunchAllowed() {
		return false
	}
	if IsLogosRelaunchExcluded(CmdName) {
		return false
	}
	if UnderGoRoot(dir) {
		return false
	}
	return DirHoldsOnlyLogosSources(dir)
}

// MainModuleMarkedByLogos reports whether the module that contains the
// working directory carries a logos.toml next to its go.mod. When a
// go.work is in effect it reports whether any workspace member does.
// Every read goes through the overlay-aware file system, so an -overlay
// file on the command line is honored. Call only after initOverlayFromArgs.
func MainModuleMarkedByLogos() bool {
	if work := Getenv("GOWORK"); work == "off" {
		return moduleMarkedByLogos()
	} else if work != "" && work != "auto" {
		return workspaceMarkedByLogos(work)
	}
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	dir := cwd
	var modDir string
	for {
		if modDir == "" && isFile(filepath.Join(dir, "go.mod")) {
			modDir = dir
		}
		if isFile(filepath.Join(dir, "go.work")) {
			return workspaceMarkedByLogos(filepath.Join(dir, "go.work"))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if modDir == "" {
		return false
	}
	return isFile(filepath.Join(modDir, "logos.toml"))
}

// moduleMarkedByLogos walks up from the working directory and reports
// whether the nearest go.mod has a logos.toml beside it.
func moduleMarkedByLogos() bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	dir := cwd
	for {
		if isFile(filepath.Join(dir, "go.mod")) {
			return isFile(filepath.Join(dir, "logos.toml"))
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

// workspaceMarkedByLogos reports whether any member of the workspace
// named by workFile carries a logos.toml next to its go.mod.
func workspaceMarkedByLogos(workFile string) bool {
	data, err := fsys.ReadFile(workFile)
	if err != nil {
		return false
	}
	wf, err := modfile.ParseWork(workFile, data, nil)
	if err != nil {
		return false
	}
	workDir := filepath.Dir(workFile)
	for _, u := range wf.Use {
		mdir := u.Path
		if !filepath.IsAbs(mdir) {
			mdir = filepath.Join(workDir, mdir)
		}
		if isFile(filepath.Join(mdir, "go.mod")) && isFile(filepath.Join(mdir, "logos.toml")) {
			return true
		}
	}
	return false
}

// RelaunchCleanup runs before a late re-execution replaces the process.
// Commands that allocate a work directory before loading packages set it
// after creating their builder, so the original builder is cleaned up
// instead of leaking its work directory.
var RelaunchCleanup func()

// runRelaunchCleanup runs RelaunchCleanup once when set.
func runRelaunchCleanup() {
	if fn := RelaunchCleanup; fn != nil {
		RelaunchCleanup = nil
		fn()
	}
}

// lateRelaunchGate gives a concurrent late re-execution exactly one owner.
// On Unix the winner replaces the process, so losers vanish with it; on
// Windows the winner waits for the child, so losers wait for the outcome
// and exit with the child's status instead of starting their own child.
type lateRelaunchGate struct {
	mu      sync.Mutex
	claimed bool
	done    chan struct{}
	ok      bool
	code    int
}

func newLateRelaunchGate() *lateRelaunchGate {
	return &lateRelaunchGate{done: make(chan struct{})}
}

// claim reports whether the caller owns the late re-execution.
func (g *lateRelaunchGate) claim() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.claimed {
		return false
	}
	g.claimed = true
	return true
}

// finish publishes the winner's outcome and wakes the losers. ok is false
// only when the child could not be started; the caller then reports the
// error it would have reported before.
func (g *lateRelaunchGate) finish(ok bool, code int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.ok = ok
	g.code = code
	close(g.done)
}

// await waits for the winner's outcome.
func (g *lateRelaunchGate) await() (bool, int) {
	<-g.done
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ok, g.code
}

var lateRelaunch = newLateRelaunchGate()

// RelaunchAsLogos re-executes the current process as the logos command
// with the same arguments, environment, and working directory. The child
// runs as logos and never re-executes, so this happens exactly once. If
// the execution fails, it prints one line and returns, and the caller
// reports the error it would have reported before.
func RelaunchAsLogos(reason string) {
	runRelaunchCleanup()
	exe, argv0 := logosExecutable()
	if code, started, err := relaunchExecCode(exe, argv0); err != nil {
		fmt.Fprintf(os.Stderr, "go: cannot re-execute as logos (%s): %v\n", reason, err)
	} else if started {
		os.Exit(code)
	}
}

// RelaunchAsLogosLate re-executes as logos from package loading, the only
// trigger that still runs that late. It passes the environment as it was
// when the process started, runs the work-directory cleanup of a builder
// that already exists, and never returns on Windows: it exits with the
// status of the child. It returns only when the child could not be
// started, and the caller reports the error it would have reported before.
// It is a no-op when the command already runs as logos.
func RelaunchAsLogosLate(reason string) {
	if Logos {
		return
	}
	if !lateRelaunch.claim() {
		if ok, code := lateRelaunch.await(); ok {
			os.Exit(code)
		}
		return
	}
	runRelaunchCleanup()
	exe, argv0 := logosExecutable()
	code, started, err := relaunchExecCode(exe, argv0)
	if started {
		lateRelaunch.finish(true, code)
		os.Exit(code)
	}
	lateRelaunch.finish(false, 0)
	fmt.Fprintf(os.Stderr, "go: cannot re-execute as logos (%s): %v\n", reason, err)
}

// logosExecutable selects the logos binary: $GOROOT/bin/logos when that
// file is executable, else this process's own executable with argv[0]
// set to logos.
func logosExecutable() (exe, argv0 string) {
	if GOROOTbin != "" {
		cand := filepath.Join(GOROOTbin, "logos"+logosExeSuffix())
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return cand, "logos"
		}
	}
	if self, err := os.Executable(); err == nil {
		return self, "logos"
	}
	return "", "logos"
}

// relaunchAllowed reports whether re-execution may replace this process.
// The cmd/go unit-test binaries link this code; their arguments carry
// -test.* flags, while every real command invocation (including the
// script-test driver, whose arguments come from the script) has none.
func relaunchAllowed() bool {
	for _, a := range os.Args[1:] {
		if len(a) == 0 || a[0] != '-' {
			break
		}
		if strings.HasPrefix(a, "-test.") {
			return false
		}
	}
	return true
}

// UnderGoRoot reports whether dir lies inside the toolchain source tree.
// The standard library never triggers a re-execution.
func UnderGoRoot(dir string) bool {
	if GOROOTsrc == "" {
		return false
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(GOROOTsrc, abs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// isFile reports whether path names a file in the overlay-aware file
// system, so the marker check sees -overlay replacements and deletions.
func isFile(path string) bool {
	fi, err := fsys.Stat(path)
	return err == nil && !fi.IsDir()
}

func logosExeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

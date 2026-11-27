// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cfg

import (
	"fmt"
	"internal/logosfile"
	"os"
	"path/filepath"
	"runtime"
	"strings"

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

// MaybeRelaunchForArgs implements the first two re-execution triggers:
// a LoGOs-only subcommand, and a logos.toml next to the go.mod of the
// module that contains the working directory. Call after the command and
// flags are known and before any package is loaded.
func MaybeRelaunchForArgs(args []string) {
	if Logos || len(args) == 0 || !relaunchAllowed() {
		return
	}
	if IsLogosRelaunchExcluded(args[0]) {
		return
	}
	if IsLogosOnlyCommand(args[0]) {
		RelaunchAsLogos("logos-only command " + args[0])
		return
	}
	if MainModuleMarkedByLogos() {
		RelaunchAsLogos("logos.toml beside go.mod")
	}
}

// MaybeRelaunchForDir implements the last two re-execution triggers: a
// main-module directory the command named, or an import inside the main
// modules, that resolves to LoGOs sources and no .go files, and a
// ./...-style walk that meets such a directory. Call only where plain go
// already decided that dir is not a Go package, so a module without .lgo
// sources pays nothing.
func MaybeRelaunchForDir(dir string, inMain bool, reason string) {
	if Logos || !inMain || !relaunchAllowed() {
		return
	}
	if IsLogosRelaunchExcluded(CmdName) {
		return
	}
	if UnderGoRoot(dir) {
		return
	}
	if DirHoldsOnlyLogosSources(dir) {
		RelaunchAsLogos(reason)
	}
}

// MainModuleMarkedByLogos reports whether the module that contains the
// working directory carries a logos.toml next to its go.mod. When a
// go.work is in effect it reports whether any workspace module does.
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
	data, err := os.ReadFile(workFile)
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

// RelaunchAsLogos re-executes the current process as the logos command
// with the same arguments, environment, and working directory. The child
// runs as logos and never re-executes, so this happens exactly once. If
// the execution fails, it prints one line and returns, and the caller
// reports the error it would have reported before.
func RelaunchAsLogos(reason string) {
	exe, argv0 := logosExecutable()
	if err := relaunchExec(exe, argv0); err != nil {
		fmt.Fprintf(os.Stderr, "go: cannot re-execute as logos (%s): %v\n", reason, err)
	}
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

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

func logosExeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

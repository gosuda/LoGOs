// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cfg

import (
	"os"
	"path/filepath"
	"strings"

	"cmd/go/internal/fsys"
	"golang.org/x/mod/modfile"
)

// MaybeRelaunchEarly decides every re-execution trigger that can be decided
// from arguments and files, before any toolchain selection. Call from main
// after the -C flag is handled and before toolchain.Select. The triggers,
// in order:
//
//   - a LoGOs-only subcommand, or a help topic naming one;
//   - a logos.toml next to the go.mod of the working module, or next to any
//     workspace member's go.mod, read through the -overlay flag when one is
//     named on the command line;
//   - a directory the command names (an absolute or relative path, ".", or
//     the working directory when no path is named), a ./...-style pattern
//     (including the workspace-root pattern, which unions the workspace
//     members), the "all" pattern, or a package path pattern that maps onto
//     a main-module directory, when it holds LoGOs sources and no .go
//     files. A named file is an input to the command, not a package, and
//     never triggers.
//
// A stock flag parser runs only after this check, so a LoGOs-only flag such
// as -pedantic or -json-diagnostics reaches the directory check before the
// parser can reject it. When neither a marker nor a source trigger applies,
// this function returns and the caller reports the flag error as upstream
// would. Only the import trigger still runs during package loading.
func MaybeRelaunchEarly() {
	if Logos || !relaunchAllowed() {
		return
	}
	raw := os.Args[1:]
	cmd, rest := splitGoCommand(raw)
	if cmd == "" {
		return
	}
	if IsLogosRelaunchExcluded(cmd) {
		if cmd == "help" {
			if topic := firstNonFlagArg(rest); IsLogosOnlyCommand(topic) {
				RelaunchAsLogos("logos-only command " + topic)
			}
		}
		return
	}
	if IsLogosOnlyCommand(cmd) {
		RelaunchAsLogos("logos-only command " + cmd)
		return
	}
	// The standard library tree never triggers on files: its testdata
	// holds .lgo fixtures and src/logos holds LoGOs sources, and no plain
	// go command run inside the toolchain tree may claim them.
	if UnderGoRoot(".") {
		return
	}
	if initOverlayFromArgs(raw, cmd) {
		defer fsys.ResetOverlay()
	}
	if MainModuleMarkedByLogos() {
		RelaunchAsLogos("logos.toml beside go.mod")
		return
	}
	if reason, ok := namedLogosTarget(rest); ok {
		RelaunchAsLogos(reason)
		return
	}
}

// splitGoCommand reports the first argument that does not look like a flag
// as the command, with the arguments that follow it. It mirrors how main
// and toolchain selection read os.Args: the command is the first bare word.
func splitGoCommand(args []string) (cmd string, rest []string) {
	for i, a := range args {
		if a == "" || a[0] == '-' {
			continue
		}
		return a, args[i+1:]
	}
	return "", nil
}

// firstNonFlagArg reports the first argument that does not look like a flag.
func firstNonFlagArg(args []string) string {
	for _, a := range args {
		if a == "" || a[0] == '-' {
			continue
		}
		return a
	}
	return ""
}

// valueFlags names bare flags whose next argument is a value, so the early
// directory scan skips that value instead of mistaking it for a path.
// Flags spelled with "=" need no entry: the value never stands alone.
var valueFlags = map[string]bool{
	"-o": true, "--o": true,
	"-mod": true, "--mod": true,
	"-modfile": true, "--modfile": true,
	"-overlay": true, "--overlay": true,
	"-C": true, "--C": true,
	"-tags": true, "--tags": true,
	"-ldflags": true, "--ldflags": true,
	"-gcflags": true, "--gcflags": true,
	"-asmflags": true, "--asmflags": true,
	"-coverpkg": true, "--coverpkg": true,
	"-toolexec": true, "--toolexec": true,
	"-buildmode": true, "--buildmode": true,
	"-compiler": true, "--compiler": true,
	"-installsuffix": true, "--installsuffix": true,
	"-coverprofile": true, "--coverprofile": true,
	"-covermode": true, "--covermode": true,
	"-exec": true, "--exec": true,
}

// namedLogosTarget reports whether the command's remaining arguments name a
// directory or pattern that holds LoGOs sources and no .go files.
// With no path-like arguments the command targets the working directory.
func namedLogosTarget(args []string) (string, bool) {
	var paths []string
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if a == "" || a[0] == '-' {
			if !strings.Contains(a, "=") && valueFlags[a] {
				skipNext = true
			}
			continue
		}
		if strings.Contains(a, "@") {
			continue
		}
		paths = append(paths, a)
	}
	if len(paths) == 0 {
		if dirTargetHoldsLogos(".") {
			return "command names LoGOs sources", true
		}
		return "", false
	}
	for _, p := range paths {
		if reason, ok := argTargetHoldsLogos(p); ok {
			return reason, true
		}
	}
	return "", false
}

// argTargetHoldsLogos reports whether one command argument names a local
// directory that needs logos, or a pattern whose walk would meet one.
// Package paths without wildcards resolve during package loading, so only
// the import trigger decides those.
func argTargetHoldsLogos(p string) (string, bool) {
	if fi, err := fsys.Stat(p); err == nil {
		if !fi.IsDir() {
			// A named file is an input to the command (go run, go vet),
			// not a package; only package loading decides it.
			return "", false
		}
		if dirTargetHoldsLogos(p) {
			return "command names LoGOs sources", true
		}
		return "", false
	}
	if p == "all" {
		if scopeHoldsLogos() {
			return "all pattern meets LoGOs sources", true
		}
		return "", false
	}
	if strings.Contains(p, "...") {
		if patternMeetsLogos(p) {
			return "walk meets LoGOs sources", true
		}
		return "", false
	}
	return "", false
}

// dirTargetHoldsLogos reports whether dir holds LoGOs sources and no .go
// files, outside the standard library.
func dirTargetHoldsLogos(dir string) bool {
	if UnderGoRoot(dir) {
		return false
	}
	return DirHoldsOnlyLogosSources(dir)
}

// initOverlayFromArgs points the overlay-aware file system at the -overlay
// file named on the command line, so the marker check reads the overlaid
// go.work, go.mod, and logos.toml contents. Only build arguments are
// scanned: for run the scan stops at the package argument, and everywhere
// it stops at -args and --, so a program's own -overlay never becomes a
// build overlay. It reports whether the overlay was loaded; the caller
// resets it before returning without relaunching, so toolchain selection
// observes the disk exactly as plain go would. A bad value restores the
// previous state; flag parsing and InitWorkfile report the error later.
func initOverlayFromArgs(args []string, cmd string) bool {
	var val string
	for i, a := range args {
		if a == "--" || a == "-args" {
			break
		}
		if a == "" || a[0] != '-' {
			// A bare word starts the package for run, and a value or a
			// package path everywhere else; neither can be a build flag.
			if cmd == "run" {
				break
			}
			continue
		}
		switch {
		case a == "-overlay" || a == "--overlay":
			if i+1 < len(args) && args[i+1] != "" && args[i+1][0] != '-' {
				val = args[i+1]
			}
		case strings.HasPrefix(a, "-overlay="):
			val = a[len("-overlay="):]
		case strings.HasPrefix(a, "--overlay="):
			val = a[len("--overlay="):]
		}
	}
	if val == "" {
		return false
	}
	saved := fsys.OverlayFile
	fsys.OverlayFile = val
	if err := fsys.Init(); err != nil {
		fsys.OverlayFile = saved
		return false
	}
	return true
}

// logosScopeModule is one main module the early check can see: a workspace
// member, or the single module that contains the working directory.
type logosScopeModule struct {
	path string
	root string
}

// logosScope is the module context for the early directory and pattern
// checks. workDir is the workspace root when a go.work file is in effect.
type logosScope struct {
	members []logosScopeModule
	workDir string
}

// discoverLogosScope finds the main modules without loading packages: the
// workspace members when a go.work file is in effect (read through the
// overlay), else the single module that contains the working directory.
// It returns an empty scope outside any module context.
func discoverLogosScope() logosScope {
	var s logosScope
	cwd, err := os.Getwd()
	if err != nil {
		return s
	}
	switch work := Getenv("GOWORK"); {
	case work == "off":
	case work != "" && work != "auto":
		return scopeFromWorkFile(work)
	default:
		if dir := findUpFile(cwd, "go.work"); dir != "" {
			return scopeFromWorkFile(filepath.Join(dir, "go.work"))
		}
	}
	if dir := findUpFile(cwd, "go.mod"); dir != "" {
		s.members = []logosScopeModule{{path: goModPath(dir), root: dir}}
	}
	return s
}

// scopeFromWorkFile reads the workspace members named by a go.work file
// through the overlay-aware file system.
func scopeFromWorkFile(workFile string) logosScope {
	var s logosScope
	data, err := fsys.ReadFile(workFile)
	if err != nil {
		return s
	}
	wf, err := modfile.ParseWork(workFile, data, nil)
	if err != nil {
		return s
	}
	s.workDir = filepath.Dir(workFile)
	for _, u := range wf.Use {
		mdir := u.Path
		if !filepath.IsAbs(mdir) {
			mdir = filepath.Join(s.workDir, mdir)
		}
		mdir = filepath.Clean(mdir)
		if st, err := fsys.Stat(filepath.Join(mdir, "go.mod")); err != nil || st.IsDir() {
			continue
		}
		s.members = append(s.members, logosScopeModule{path: goModPath(mdir), root: mdir})
	}
	return s
}

// goModPath reports the module path declared by dir/go.mod, or "".
func goModPath(dir string) string {
	data, err := fsys.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	f, err := modfile.Parse("go.mod", data, nil)
	if err != nil || f.Module == nil {
		return ""
	}
	return f.Module.Mod.Path
}

// scopeHoldsLogos reports whether any scope member tree holds a directory
// with LoGOs sources and no .go files.
func scopeHoldsLogos() bool {
	s := discoverLogosScope()
	if len(s.members) == 0 {
		return false
	}
	roots := memberRootSet(s)
	for _, m := range s.members {
		if treeHoldsLogosOnly(m.root, roots) {
			return true
		}
	}
	return false
}

// findUpFile walks up from dir and reports the nearest directory holding
// name, or "".
func findUpFile(dir, name string) string {
	for {
		if st, err := fsys.Stat(filepath.Join(dir, name)); err == nil && !st.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// patternMeetsLogos reports whether a ...-style pattern's walk would meet a
// directory with LoGOs sources and no .go files. A workspace-root pattern
// unions the workspace members, mirroring matchLogosWorkspaceRoot.
func patternMeetsLogos(p string) bool {
	if dir, ok := patternLocalDir(p); ok {
		s := discoverLogosScope()
		if s.workDir != "" && dir == s.workDir {
			roots := memberRootSet(s)
			for _, m := range s.members {
				if treeHoldsLogosOnly(m.root, roots) {
					return true
				}
			}
			return false
		}
		return treeHoldsLogosOnly(dir, memberRootSet(s))
	}
	s := discoverLogosScope()
	if len(s.members) == 0 {
		return false
	}
	roots := memberRootSet(s)
	for _, m := range s.members {
		if m.path == "" {
			continue
		}
		if p == m.path+"/..." {
			return treeHoldsLogosOnly(m.root, roots)
		}
		if rest, ok := strings.CutPrefix(p, m.path+"/"); ok {
			if i := strings.Index(rest, "..."); i >= 0 {
				return treeHoldsLogosOnly(filepath.Join(m.root, filepath.FromSlash(rest[:i])), roots)
			}
			return false
		}
	}
	return false
}

// TreeHoldsLogosOnly reports whether root or any directory below it holds
// LoGOs sources and no .go files, skipping nested modules. It shares the
// rule with the early walk.
func TreeHoldsLogosOnly(root string) bool {
	return treeHoldsLogosOnly(root, nil)
}

// patternLocalDir resolves the directory part of a ...-style pattern when
// it names a local directory.
func patternLocalDir(p string) (string, bool) {
	i := strings.Index(p, "...")
	if i < 0 {
		return "", false
	}
	abs, err := filepath.Abs(p[:i])
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)
	if st, err := fsys.Stat(abs); err != nil || !st.IsDir() {
		return "", false
	}
	return abs, true
}

// memberRootSet maps every scope member root for nested-module skipping.
func memberRootSet(s logosScope) map[string]bool {
	roots := make(map[string]bool, len(s.members))
	for _, m := range s.members {
		roots[m.root] = true
	}
	return roots
}

// treeHoldsLogosOnly reports whether root or any directory below it holds
// LoGOs sources and no .go files. Nested modules are skipped, except scope
// members. Dot, underscore, and testdata trees are skipped, matching the
// package walk. Reads go through the overlay-aware file system.
func treeHoldsLogosOnly(root string, memberRoots map[string]bool) bool {
	if st, err := fsys.Stat(root); err != nil || !st.IsDir() {
		return false
	}
	stack := []string{root}
	for len(stack) > 0 {
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		fis, err := fsys.ReadDir(d)
		if err != nil {
			continue
		}
		if dirEntriesHoldOnlyLogosSources(fis, d) {
			return true
		}
		for _, fi := range fis {
			if !fi.IsDir() {
				continue
			}
			name := fi.Name()
			if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_") || name == "testdata" {
				continue
			}
			sub := filepath.Join(d, name)
			if sub != root && !memberRoots[sub] {
				if st, err := fsys.Stat(filepath.Join(sub, "go.mod")); err == nil && !st.IsDir() {
					continue
				}
			}
			stack = append(stack, sub)
		}
	}
	return false
}

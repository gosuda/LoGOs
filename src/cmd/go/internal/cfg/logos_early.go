// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cfg

import (
	"os"
	"path"
	"path/filepath"
	"strings"

	"cmd/go/internal/fsys"
	"cmd/internal/pkgpattern"
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
	if reason, ok := namedLogosTarget(cmd, rest); ok {
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
// target scan skips that value instead of mistaking it for a package path.
// Flags spelled with "=" need no entry: the value never stands alone.
// The set is the union of the value-taking build, cover, test, list, run,
// and mod flags. Bool flags take no entry even when they accept an
// "=value" form (for example -v): a bare word after one is a package
// argument, exactly as the flag parser leaves it.
var valueFlags = map[string]bool{
	"-o":                    true,
	"-mod":                  true,
	"-modfile":              true,
	"-overlay":              true,
	"-C":                    true,
	"-tags":                 true,
	"-ldflags":              true,
	"-gcflags":              true,
	"-asmflags":             true,
	"-gccgoflags":           true,
	"-coverpkg":             true,
	"-toolexec":             true,
	"-exec":                 true,
	"-buildmode":            true,
	"-compiler":             true,
	"-installsuffix":        true,
	"-coverprofile":         true,
	"-covermode":            true,
	"-p":                    true,
	"-pkgdir":               true,
	"-pgo":                  true,
	"-vettool":              true,
	"-debug-actiongraph":    true,
	"-debug-runtime-trace":  true,
	"-debug-trace":          true,
	"-bench":                true,
	"-benchtime":            true,
	"-blockprofile":         true,
	"-blockprofilerate":     true,
	"-count":                true,
	"-cpu":                  true,
	"-cpuprofile":           true,
	"-fuzz":                 true,
	"-fuzzminimizetime":     true,
	"-fuzztime":             true,
	"-list":                 true,
	"-memprofile":           true,
	"-memprofilerate":       true,
	"-mutexprofile":         true,
	"-mutexprofilefraction": true,
	"-outputdir":            true,
	"-parallel":             true,
	"-run":                  true,
	"-skip":                 true,
	"-timeout":              true,
	"-trace":                true,
	"-shuffle":              true,
	"-vet":                  true,
	"-f":                    true,
	"-reuse":                true,
	"-go":                   true,
	"-compat":               true,
	"-toolchain":            true,
	"-module":               true,
}

// takesValue reports whether a bare flag consumes the next argument as its
// value. Both the one-dash and the two-dash spellings count.
func takesValue(flag string) bool {
	if valueFlags[flag] {
		return true
	}
	if strings.HasPrefix(flag, "--") {
		return valueFlags["-"+flag[2:]]
	}
	return false
}

// namedLogosTarget reports whether the command's remaining arguments name a
// directory or pattern that holds LoGOs sources and no .go files.
// A bare word that names no path is no target: flag values are skipped,
// version queries are skipped, and words the file system does not resolve
// fall back to the working-directory rule below.
func namedLogosTarget(cmd string, args []string) (string, bool) {
	if cmd == "mod" && firstNonFlagArg(args) == "tidy" {
		// `go mod tidy` takes no package arguments and loads "all":
		// without this branch the word "tidy" would read as a package
		// and plain go would drop dependencies its LoGOs packages need.
		if scopeHoldsLogos() {
			return "all pattern meets LoGOs sources", true
		}
		return "", false
	}
	var paths []string
	skipNext := false
	for _, a := range args {
		if skipNext {
			skipNext = false
			continue
		}
		if a == "--" {
			continue
		}
		if a == "-args" {
			// Words after -args belong to the test binary or the
			// program, never to the go command.
			break
		}
		if a == "" || a[0] == '-' {
			if !strings.Contains(a, "=") && takesValue(a) {
				skipNext = true
			}
			continue
		}
		if strings.Contains(a, "@") {
			continue
		}
		paths = append(paths, a)
	}
	if cmd == "run" {
		paths = runPackageArgs(paths)
	}
	for _, p := range paths {
		if reason, ok := argTargetHoldsLogos(p); ok {
			return reason, true
		}
	}
	if !anyTargetResolves(paths) && dirTargetHoldsLogos(".") {
		return "command names LoGOs sources", true
	}
	return "", false
}

// runPackageArgs truncates words to the package position of `go run`:
// leading existing files form the file list and one more word is the
// package; the rest are program arguments and name no package.
func runPackageArgs(paths []string) []string {
	if len(paths) == 0 {
		return paths
	}
	if isFile(paths[0]) {
		n := 0
		for n < len(paths) && isFile(paths[n]) {
			n++
		}
		return paths[:n]
	}
	return paths[:1]
}

// anyTargetResolves reports whether any collected word names a package:
// an existing file or directory, the "all" pattern, a wildcard pattern,
// or a main-module directory through the exact import-path mapping.
// Words that resolve to nothing name no package, so the caller falls back
// to the working directory instead of skipping its check.
func anyTargetResolves(paths []string) bool {
	for _, p := range paths {
		if p == "all" || strings.Contains(p, "...") {
			return true
		}
		if _, err := fsys.Stat(p); err == nil {
			return true
		}
		if _, ok := exactModuleDir(p); ok {
			return true
		}
	}
	return false
}

// argTargetHoldsLogos reports whether one command argument names a local
// directory that needs logos, or a pattern whose walk would meet one.
// An exact import path resolves here, against the module path of the
// nearest go.mod, so a LoGOs-only flag does not reach the flag parser
// before the directory check can see the sources. Anything subtler
// resolves during package loading, where only the import trigger decides.
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
	if dir, ok := exactModuleDir(p); ok {
		if dirTargetHoldsLogos(dir) {
			return "command names LoGOs sources", true
		}
		return "", false
	}
	return "", false
}

// exactModuleDir maps an exact import path onto a main-module directory.
// It matches the argument against the module path of the nearest go.mod,
// read through the overlay, on component boundaries: the path itself or
// the path plus a slash maps the suffix onto the module root. The suffix
// decides: a version query, a wildcard, an element no import can name
// (vendor, testdata, dot and underscore trees), an escape above the root,
// or a directory owned by a nested module outside the scope never maps.
func exactModuleDir(p string) (string, bool) {
	if strings.Contains(p, "@") || strings.Contains(p, "...") {
		return "", false
	}
	s := discoverLogosScope()
	if len(s.members) == 0 {
		return "", false
	}
	roots := memberRootSet(s)
	for _, m := range s.members {
		if m.path == "" {
			continue
		}
		var suffix string
		if p == m.path {
			suffix = ""
		} else if rest, ok := strings.CutPrefix(p, m.path+"/"); ok {
			suffix = rest
		} else {
			continue
		}
		dir := m.root
		if suffix != "" {
			for _, el := range strings.Split(suffix, "/") {
				if el == "" || el == "." || el == ".." || el == "vendor" || el == "testdata" || strings.HasPrefix(el, ".") || strings.HasPrefix(el, "_") {
					dir = ""
					break
				}
			}
			if dir == "" {
				continue
			}
			dir = filepath.Join(m.root, filepath.FromSlash(suffix))
			// A nested go.mod between the member root and the directory
			// owns the package, unless it is itself a scope member.
			nested := false
			for d := dir; d != m.root; d = filepath.Dir(d) {
				if isFile(filepath.Join(d, "go.mod")) && !roots[d] {
					nested = true
					break
				}
				if parent := filepath.Dir(d); parent == d {
					break
				}
			}
			if nested {
				continue
			}
		}
		if fi, err := fsys.Stat(dir); err != nil || !fi.IsDir() {
			continue
		}
		if UnderGoRoot(dir) {
			continue
		}
		return dir, true
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
	if !filepath.IsAbs(s.workDir) {
		if cwd, err := os.Getwd(); err == nil {
			s.workDir = filepath.Join(cwd, s.workDir)
		}
	}
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
// with LoGOs sources and no .go files. Eligibility mirrors the package
// walker: vendor trees, go.mod ignore exclusions, nested modules outside
// the scope, and dot, underscore, and testdata trees never count.
func scopeHoldsLogos() bool {
	s := discoverLogosScope()
	if len(s.members) == 0 {
		return false
	}
	always := func(string) bool { return true }
	return walkScopeLogos(s, always, always)
}

// walkScopeLogos walks every scope member and reports whether a directory
// whose import path matches match holds LoGOs sources and no .go files.
// treeCanMatch prunes subtrees the pattern cannot match.
func walkScopeLogos(s logosScope, match, treeCanMatch func(string) bool) bool {
	ignores := make(map[string]*logosIgnorePatterns, len(s.members))
	for _, m := range s.members {
		if m.path == "" {
			continue
		}
		if !treeCanMatch(m.path) {
			continue
		}
		ignore, ok := ignores[m.root]
		if !ok {
			ignore = parseLogosIgnorePatterns(m.root)
			ignores[m.root] = ignore
		}
		if walkMemberLogos(m, match, treeCanMatch, ignore) {
			return true
		}
	}
	return false
}

// walkMemberLogos walks the member root and reports whether a directory
// whose import path (the member path joined with the slash-separated path
// relative to the root) matches match holds LoGOs sources and no .go
// files. Eligibility mirrors the package walker: dot, underscore, and
// testdata trees are skipped, vendor subtrees are pruned, nested modules
// are skipped (they get their own walk when they are scope members),
// go.mod ignore rules apply, and subtrees the pattern cannot match are
// pruned.
func walkMemberLogos(m logosScopeModule, match, treeCanMatch func(string) bool, ignore *logosIgnorePatterns) bool {
	root := m.root
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
			if skipDirElem(elem) || elem == "vendor" {
				continue
			}
			if st, err := fsys.Stat(filepath.Join(d, "go.mod")); err == nil && !st.IsDir() {
				continue
			}
		}
		name := m.path
		if relSlash != "" {
			name = path.Join(m.path, relSlash)
		}
		if !treeCanMatch(name) {
			continue
		}
		if ignore.shouldIgnore(relSlash) {
			continue
		}
		fis, err := fsys.ReadDir(d)
		if err != nil {
			continue
		}
		if match(name) && dirEntriesHoldOnlyLogosSources(fis, d) {
			return true
		}
		for _, fi := range fis {
			if fi.IsDir() {
				stack = append(stack, filepath.Join(d, fi.Name()))
			}
		}
	}
	return false
}

// logosIgnorePatterns holds a module's go.mod ignore rules in the form
// package discovery uses: a "./x" rule pins the module-rooted path x,
// while a bare name matches at any depth. Wildcards are not supported.
type logosIgnorePatterns struct {
	relative []string
	any      []string
}

// parseLogosIgnorePatterns reads the ignore rules from the go.mod in
// modRoot through the overlay-aware file system. It returns nil when the
// file cannot be read or names no rules.
func parseLogosIgnorePatterns(modRoot string) *logosIgnorePatterns {
	data, err := fsys.ReadFile(filepath.Join(modRoot, "go.mod"))
	if err != nil {
		return nil
	}
	f, err := modfile.Parse("go.mod", data, nil)
	if err != nil || f == nil {
		return nil
	}
	var p *logosIgnorePatterns
	for _, ig := range f.Ignore {
		rule, relative := strings.CutPrefix(ig.Path, "./")
		rule = filepath.ToSlash(rule)
		if !strings.HasPrefix(rule, "/") {
			rule = "/" + rule
		}
		if !strings.HasSuffix(rule, "/") {
			rule += "/"
		}
		if p == nil {
			p = &logosIgnorePatterns{}
		}
		if relative {
			p.relative = append(p.relative, rule)
		} else {
			p.any = append(p.any, rule)
		}
	}
	return p
}

// shouldIgnore reports whether the slash-separated module-relative dir is
// ignored by these rules. A nil receiver ignores nothing.
func (p *logosIgnorePatterns) shouldIgnore(dir string) bool {
	if p == nil || dir == "" {
		return false
	}
	dir = "/" + dir + "/"
	for _, pattern := range p.relative {
		if strings.HasPrefix(dir, pattern) {
			return true
		}
	}
	for _, pattern := range p.any {
		if strings.Contains(dir, pattern) {
			return true
		}
	}
	return false
}

// skipDirElem reports whether the package walk never descends into a
// directory with this base name: dot, underscore, and testdata trees.
// The names "." and ".." are walk roots, not hidden trees.
func skipDirElem(elem string) bool {
	return (strings.HasPrefix(elem, ".") && elem != "." && elem != "..") || strings.HasPrefix(elem, "_") || elem == "testdata"
}

// inDir reports whether path lies inside dir, lexically.
func inDir(path, dir string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
// directory with LoGOs sources and no .go files. Only a directory the full
// pattern matches counts; vendor trees and go.mod ignore exclusions never
// do. A workspace-root pattern unions the workspace members, mirroring
// matchLogosWorkspaceRoot.
func patternMeetsLogos(p string) bool {
	if isLocalPattern(p) {
		return localPatternMeetsLogos(p)
	}
	s := discoverLogosScope()
	if len(s.members) == 0 {
		return false
	}
	return walkScopeLogos(s, pkgpattern.MatchPattern(p), pkgpattern.TreeCanMatchPattern(p))
}

// isLocalPattern reports whether p names the file system: a relative ./
// or ../ path or an absolute one, mirroring search.Match.IsLocal.
func isLocalPattern(p string) bool {
	return p == "." || p == ".." || strings.HasPrefix(p, "./") || strings.HasPrefix(p, "../") || filepath.IsAbs(p)
}

// localPatternMeetsLogos reports whether the local ...-style pattern p
// matches a directory holding LoGOs sources and no .go files. The walk
// root and the match predicate mirror package discovery: the root is the
// directory part of the text before "..." (so ./cmd... starts at "." and
// ./services/.../cmd starts at ./services), and the full pattern, ./
// prefix included, decides every candidate (so ./services/.../cmd never
// counts services/experimental).
func localPatternMeetsLogos(p string) bool {
	cleanPattern := filepath.Clean(p)
	prefix := ""
	if cleanPattern != "." && strings.HasPrefix(p, "./") {
		prefix = "./"
		cleanPattern = "." + string(filepath.Separator) + cleanPattern
	}
	i := strings.Index(cleanPattern, "...")
	if i < 0 {
		return false
	}
	// Walk root exactly as package discovery computes it: the directory
	// part of the text before "..." (so ./cmd... starts at "." and
	// ./services/.../cmd starts at ./services).
	dir, _ := filepath.Split(cleanPattern[:i])
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	abs := dir
	if !filepath.IsAbs(dir) {
		abs = filepath.Join(cwd, dir)
	}
	s := discoverLogosScope()
	var ignore *logosIgnorePatterns
	ownerRoot := ""
	if len(s.members) != 0 {
		if s.workDir != "" && filepath.Clean(abs) == filepath.Clean(s.workDir) {
			return workspacePatternMeetsLogos(s, p)
		}
		// Mirror the loader guard: a local wildcard outside the main
		// modules matches nothing.
		inside := false
		for _, m := range s.members {
			if inDir(abs, m.root) {
				inside = true
				break
			}
		}
		if !inside {
			return false
		}
		for _, m := range s.members {
			if inDir(abs, m.root) && len(m.root) >= len(ownerRoot) {
				ownerRoot = m.root
			}
		}
		if ownerRoot != "" {
			ignore = parseLogosIgnorePatterns(ownerRoot)
		}
	}
	// Predicate exactly as package discovery builds it: "..." means any
	// string, and a wildcard element never matches a vendor element
	// unless the pattern names that vendor path.
	slashPattern := filepath.ToSlash(cleanPattern)
	match := pkgpattern.MatchPattern(slashPattern)
	return walkLocalLogos(dir, cwd, prefix, filepath.IsAbs(p), match, ignore, ownerRoot)
}

// workspacePatternMeetsLogos unions the workspace members for a ...-style
// pattern issued from the go.work directory itself, mirroring
// matchLogosWorkspaceRoot: each member is walked with the member-rooted
// form of the pattern. The walk of a member never enters this union
// again, so a member whose root is the go.work directory ends the walk.
func workspacePatternMeetsLogos(s logosScope, p string) bool {
	_, suffix, _ := strings.Cut(p, "...")
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	for _, m := range s.members {
		root := m.root
		if !filepath.IsAbs(root) {
			root = filepath.Join(cwd, root)
		}
		if memberTreeMeetsLogos(root, suffix) {
			return true
		}
	}
	return false
}

// memberTreeMeetsLogos walks one workspace member with the member-rooted
// form of a ... pattern, under the ignore rules of the member's go.mod.
func memberTreeMeetsLogos(root, suffix string) bool {
	pattern := filepath.ToSlash(filepath.Clean(root)) + "/..." + suffix
	match := pkgpattern.MatchPattern(pattern)
	return walkLocalLogos(root, "", "", true, match, parseLogosIgnorePatterns(root), root)
}

// walkLocalLogos walks dir and reports whether a directory the full
// pattern matches holds LoGOs sources and no .go files. Names are built
// exactly as package discovery builds them: for a relative pattern the
// ./ prefix plus the slash-separated path relative to the working
// directory, for an absolute pattern the absolute slash-separated path.
// Eligibility mirrors search.MatchDirs: dot, underscore, and testdata
// trees are skipped, nested modules are skipped, and the owning module's
// go.mod ignore rules apply. Vendor subtrees are walked but the
// predicate decides them, so explicitly named vendor patterns still
// match.
func walkLocalLogos(dir, cwd, prefix string, absolute bool, match func(string) bool, ignore *logosIgnorePatterns, ownerRoot string) bool {
	top := filepath.Clean(dir)
	stack := []string{dir}
	for len(stack) > 0 {
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		dc := filepath.Clean(d)
		_, elem := filepath.Split(dc)
		if skipDirElem(elem) {
			continue
		}
		isTop := dc == top
		// A go.mod below the walk root marks another module, whose
		// packages this pattern never names. It gets its own walk when
		// it is itself a scope member.
		if !isTop {
			if st, err := fsys.Stat(filepath.Join(dc, "go.mod")); err == nil && !st.IsDir() {
				continue
			}
		}
		dAbs := dc
		if !filepath.IsAbs(dc) {
			dAbs = filepath.Join(cwd, dc)
		}
		var name string
		if absolute {
			name = filepath.ToSlash(dAbs)
		} else {
			rel, err := filepath.Rel(cwd, dAbs)
			if err != nil {
				continue
			}
			name = prefix + filepath.ToSlash(rel)
		}
		if ownerRoot != "" {
			if modRel, err := filepath.Rel(ownerRoot, dAbs); err == nil {
				if ignore.shouldIgnore(filepath.ToSlash(modRel)) {
					continue
				}
			}
		}
		fis, err := fsys.ReadDir(dc)
		if err != nil {
			continue
		}
		if match(name) && dirEntriesHoldOnlyLogosSources(fis, dc) {
			return true
		}
		for _, fi := range fis {
			if fi.IsDir() {
				stack = append(stack, filepath.Join(dc, fi.Name()))
			}
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

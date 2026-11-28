// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cfg

import (
	"path/filepath"
	"testing"

	"cmd/go/internal/fsys"
)

func TestSplitGoCommand(t *testing.T) {
	cases := map[string]struct {
		args     []string
		cmd      string
		restLen  int
		restLast string
	}{
		"command with args": {
			args:     []string{"build", "-o", "x", "."},
			cmd:      "build",
			restLen:  3,
			restLast: ".",
		},
		"leading flags skipped": {
			args:     []string{"-x", "test", "./..."},
			cmd:      "test",
			restLen:  1,
			restLast: "./...",
		},
		"no command": {
			args: []string{"-x"},
			cmd:  "",
		},
		"empty": {
			args: nil,
			cmd:  "",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cmd, rest := splitGoCommand(tc.args)
			if cmd != tc.cmd {
				t.Errorf("splitGoCommand(%q) cmd = %q, want %q", tc.args, cmd, tc.cmd)
			}
			if len(rest) != tc.restLen {
				t.Errorf("splitGoCommand(%q) rest = %q, want length %d", tc.args, rest, tc.restLen)
			}
			if tc.restLen > 0 && rest[len(rest)-1] != tc.restLast {
				t.Errorf("splitGoCommand(%q) last = %q, want %q", tc.args, rest[len(rest)-1], tc.restLast)
			}
		})
	}
}

// writeLogosModule writes a module whose root package is LoGOs-only.
func writeLogosModule(t *testing.T, root, modPath string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "go.mod"), "module "+modPath+"\n\ngo 1.27\n")
	writeFile(t, filepath.Join(root, "main.lgo"), "package main\n\nfunc main() {}\n")
}

// writeGoModule writes a module with one Go package under root.
func writeGoModule(t *testing.T, root, modPath string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "go.mod"), "module "+modPath+"\n\ngo 1.27\n")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n\nfunc main() {}\n")
}

func TestNamedLogosTargetCwd(t *testing.T) {
	root := t.TempDir()
	writeLogosModule(t, filepath.Join(root, "l"), "example.com/l")
	t.Chdir(filepath.Join(root, "l"))
	if reason, ok := namedLogosTarget("build", nil); !ok {
		t.Errorf("namedLogosTarget(nil) in a LoGOs-only dir = false, want true")
	} else if reason == "" {
		t.Errorf("namedLogosTarget(nil) returned an empty reason")
	}

	pure := t.TempDir()
	writeGoModule(t, pure, "example.com/pure")
	t.Chdir(pure)
	if _, ok := namedLogosTarget("build", nil); ok {
		t.Errorf("namedLogosTarget(nil) in a pure Go dir = true, want false")
	}
}

func TestArgTargetHoldsLogos(t *testing.T) {
	root := t.TempDir()
	writeLogosModule(t, filepath.Join(root, "l"), "example.com/l")
	writeGoModule(t, filepath.Join(root, "g"), "example.com/g")
	t.Chdir(root)

	cases := map[string]struct {
		arg  string
		want bool
	}{
		"logos directory":     {arg: "l", want: true},
		"logos file is input": {arg: filepath.Join("l", "main.lgo"), want: false},
		"go directory":        {arg: "g", want: false},
		"go file":             {arg: filepath.Join("g", "main.go"), want: false},
		"missing path":        {arg: "nosuchdir", want: false},
		"logos walk":          {arg: "./l/...", want: true},
		"go walk":             {arg: "./g/...", want: false},
		"package single":      {arg: "example.com/l/lib", want: false},
		"at version":          {arg: "example.com/l/lib@v1.0.0", want: false},
		"all without scope":   {arg: "all", want: false},
		"standard library":    {arg: "std", want: false},
		"unrelated pattern":   {arg: "example.com/other/...", want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Each case needs its own scope: run from root except the
			// all/pattern cases, which need a module or workspace context.
			if _, ok := argTargetHoldsLogos(tc.arg); ok != tc.want {
				t.Errorf("argTargetHoldsLogos(%q) = %v, want %v", tc.arg, ok, tc.want)
			}
		})
	}
}

func TestScopeHoldsLogosWorkspace(t *testing.T) {
	t.Setenv("GOWORK", "auto")
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.work"), "go 1.27\n\nuse (\n\t./g\n\t./l\n)\n")
	writeGoModule(t, filepath.Join(root, "g"), "example.com/g")
	writeLogosModule(t, filepath.Join(root, "l"), "example.com/l")
	t.Chdir(root)
	if !scopeHoldsLogos() {
		t.Errorf("scopeHoldsLogos() = false with a LoGOs workspace member, want true")
	}
	if !patternMeetsLogos("./...") {
		t.Errorf("patternMeetsLogos(./...) = false from the workspace root, want true")
	}
	if !patternMeetsLogos("example.com/l/...") {
		t.Errorf("patternMeetsLogos(example.com/l/...) = false, want true")
	}
	if _, ok := argTargetHoldsLogos("all"); !ok {
		t.Errorf("argTargetHoldsLogos(all) = false with a LoGOs member, want true")
	}

	pure := t.TempDir()
	writeFile(t, filepath.Join(pure, "go.work"), "go 1.27\n\nuse ./g\n")
	writeGoModule(t, filepath.Join(pure, "g"), "example.com/g")
	t.Chdir(pure)
	if scopeHoldsLogos() {
		t.Errorf("scopeHoldsLogos() = true with no LoGOs member, want false")
	}
	if patternMeetsLogos("./...") {
		t.Errorf("patternMeetsLogos(./...) = true with no LoGOs member, want false")
	}
}

func TestScopeIgnoredUnderGoworkOff(t *testing.T) {
	t.Setenv("GOWORK", "off")
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.work"), "go 1.27\n\nuse (\n\t./g\n\t./l\n)\n")
	writeGoModule(t, filepath.Join(root, "g"), "example.com/g")
	writeLogosModule(t, filepath.Join(root, "l"), "example.com/l")
	t.Chdir(root)
	// With the workspace off the members are invisible: no scope, so no
	// member union and no package-path mapping through the workspace.
	if scopeHoldsLogos() {
		t.Errorf("scopeHoldsLogos() = true with GOWORK=off, want false")
	}
	if _, ok := argTargetHoldsLogos("all"); ok {
		t.Errorf("argTargetHoldsLogos(all) = true with GOWORK=off, want false")
	}
	// An explicit local pattern still works: it names a directory.
	if !patternMeetsLogos("./l/...") {
		t.Errorf("patternMeetsLogos(./l/...) = false with GOWORK=off, want true")
	}
}

func TestInitOverlayFromArgsBad(t *testing.T) {
	saved := fsys.OverlayFile
	if initOverlayFromArgs([]string{"build", "-overlay=" + filepath.Join(t.TempDir(), "nosuch.json"), "."}, "build") {
		t.Errorf("initOverlayFromArgs with a bad overlay reported success")
	}
	if got := fsys.OverlayFile; got != saved {
		t.Errorf("initOverlayFromArgs with a bad overlay left OverlayFile = %q, want %q", got, saved)
	}
	if initOverlayFromArgs([]string{"build", "."}, "build") {
		t.Errorf("initOverlayFromArgs without -overlay reported success")
	}
	if got := fsys.OverlayFile; got != saved {
		t.Errorf("initOverlayFromArgs without -overlay left OverlayFile = %q, want %q", got, saved)
	}
}

func TestInitOverlayFromArgsStopsAtProgram(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "overlay.json"), `{"Replace": {}}`)
	t.Chdir(dir)
	defer func() {
		fsys.OverlayFile = ""
		fsys.ResetOverlay()
	}()
	// A build flag before the package still counts.
	fsys.OverlayFile = ""
	if !initOverlayFromArgs([]string{"-overlay", "overlay.json", "."}, "run") {
		t.Errorf("initOverlayFromArgs missed a build -overlay before the package")
	}
	fsys.ResetOverlay()
	fsys.OverlayFile = ""
	// A program argument after the package does not.
	if initOverlayFromArgs([]string{".", "-overlay=overlay.json"}, "run") {
		t.Errorf("initOverlayFromArgs took the program -overlay as a build overlay")
	}
	fsys.OverlayFile = ""
	// Test flags after -args belong to the test binary.
	if initOverlayFromArgs([]string{"test", "-args", "-overlay=overlay.json"}, "test") {
		t.Errorf("initOverlayFromArgs took the -args -overlay as a build overlay")
	}
	fsys.OverlayFile = ""
}

func TestFirstNonFlagArg(t *testing.T) {
	if got := firstNonFlagArg([]string{"-x", "lint", "./..."}); got != "lint" {
		t.Errorf("firstNonFlagArg = %q, want lint", got)
	}
	if got := firstNonFlagArg([]string{"-x"}); got != "" {
		t.Errorf("firstNonFlagArg = %q, want empty", got)
	}
}

func TestLateRelaunchGateFailure(t *testing.T) {
	g := newLateRelaunchGate()
	if !g.claim() {
		t.Fatal("first claim lost, want win")
	}
	if g.claim() {
		t.Error("second claim won, want loss")
	}
	waited := make(chan [2]int, 1)
	go func() {
		ok, code := g.await()
		if ok {
			code = -code - 1
		}
		waited <- [2]int{code, 0}
	}()
	g.finish(false, 0)
	if got := <-waited; got[0] != 0 {
		t.Errorf("await after failure = %d, want 0", got[0])
	}
}

func TestLateRelaunchGateSuccess(t *testing.T) {
	g := newLateRelaunchGate()
	if !g.claim() {
		t.Fatal("first claim lost, want win")
	}
	waited := make(chan int, 1)
	go func() {
		ok, code := g.await()
		if !ok {
			code = -1
		}
		waited <- code
	}()
	g.finish(true, 3)
	if got := <-waited; got != 3 {
		t.Errorf("await after success = %d, want 3", got)
	}
	// The exec path is not run here: os.Exit and process replacement have
	// no unit-test form. The script suite covers the child behavior.
}

func TestRunRelaunchCleanupOnce(t *testing.T) {
	old := RelaunchCleanup
	defer func() { RelaunchCleanup = old }()
	calls := 0
	RelaunchCleanup = func() { calls++ }
	runRelaunchCleanup()
	runRelaunchCleanup()
	if calls != 1 {
		t.Errorf("cleanup ran %d times, want 1", calls)
	}
	if RelaunchCleanup != nil {
		t.Errorf("RelaunchCleanup not cleared after running")
	}
}

func TestNamedLogosTargetFlagValues(t *testing.T) {
	root := t.TempDir()
	writeLogosModule(t, filepath.Join(root, "l"), "example.com/l")
	t.Chdir(filepath.Join(root, "l"))
	cases := map[string][]string{
		"run filter":        {"-run", "TestFoo"},
		"run filter equal":  {"-run=TestFoo"},
		"double dash run":   {"--run", "TestFoo"},
		"count":             {"-count", "1"},
		"timeout":           {"-timeout", "60s"},
		"bench":             {"-bench", "BenchmarkX"},
		"build p":           {"-p", "4"},
		"tags value":        {"-tags", "foo"},
		"ldflags value":     {"-ldflags", "-X main.x=1"},
		"list format":       {"-f", "{{.Dir}}"},
		"two flags":         {"-run", "TestFoo", "-count", "1"},
		"flag then package": {"-run", "TestFoo", "."},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			cmd := "test"
			if name == "build p" {
				cmd = "build"
			}
			if name == "list format" {
				cmd = "list"
			}
			if _, ok := namedLogosTarget(cmd, args); !ok {
				t.Errorf("namedLogosTarget(%q, %q) = false, want true", cmd, args)
			}
		})
	}
}

func TestNamedLogosTargetBareWordFallback(t *testing.T) {
	root := t.TempDir()
	writeLogosModule(t, filepath.Join(root, "l"), "example.com/l")
	t.Chdir(filepath.Join(root, "l"))
	// A bare word that resolves to nothing names no package: the
	// working-directory rule still applies.
	if _, ok := namedLogosTarget("test", []string{"TestFoo"}); !ok {
		t.Errorf("namedLogosTarget with an unresolving word = false, want true")
	}
	// A word that names a package suppresses the fallback: a Go file is
	// an input to the command, not a LoGOs trigger.
	writeFile(t, filepath.Join(root, "l", "main.go"), "package main\n")
	if _, ok := namedLogosTarget("test", []string{"main.go"}); ok {
		t.Errorf("namedLogosTarget with a Go file = true, want false")
	}
}

func TestNamedLogosTargetRunProgramArgs(t *testing.T) {
	root := t.TempDir()
	writeGoModule(t, filepath.Join(root, "g"), "example.com/g")
	writeLogosModule(t, filepath.Join(root, "g", "sub"), "example.com/g/sub")
	t.Chdir(filepath.Join(root, "g"))
	// Words after the run package are program arguments: ./sub must not
	// trigger through them when the package itself is pure Go.
	if _, ok := namedLogosTarget("run", []string{".", "./sub"}); ok {
		t.Errorf("namedLogosTarget(run . ./sub) = true, want false")
	}
}

func TestNamedLogosTargetModTidy(t *testing.T) {
	root := t.TempDir()
	writeGoModule(t, filepath.Join(root, "g"), "example.com/g")
	t.Chdir(filepath.Join(root, "g"))
	if _, ok := namedLogosTarget("mod", []string{"tidy"}); ok {
		t.Errorf("namedLogosTarget(mod tidy) in a pure module = true, want false")
	}
	if _, ok := namedLogosTarget("mod", []string{"-e", "tidy"}); ok {
		t.Errorf("namedLogosTarget(mod -e tidy) in a pure module = true, want false")
	}
}

func TestNamedLogosTargetModTidyScope(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "go.mod"), "module example.com/t\n\ngo 1.27\n")
	writeFile(t, filepath.Join(root, "cmd", "app", "main.lgo"), "package main\n")
	t.Chdir(root)
	// `go mod tidy` loads "all": a LoGOs-only subpackage triggers even
	// when the word "tidy" names no package and the root holds no sources.
	if _, ok := namedLogosTarget("mod", []string{"tidy"}); !ok {
		t.Errorf("namedLogosTarget(mod tidy) with a LoGOs subpackage = false, want true")
	}
}

func TestExactModuleDir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "m", "go.mod"), "module example.com/m\n\ngo 1.27\n")
	writeFile(t, filepath.Join(root, "m", "lib", "lib.lgo"), "package lib\n")
	writeGoModule(t, filepath.Join(root, "m", "g"), "example.com/m/g")
	writeFile(t, filepath.Join(root, "m", "vendor", "v", "v.lgo"), "package v\n")
	t.Chdir(filepath.Join(root, "m"))
	cases := map[string]struct {
		arg  string
		want bool
	}{
		"root package":     {arg: "example.com/m", want: true},
		"logos subpackage": {arg: "example.com/m/lib", want: true},
		"nested module":    {arg: "example.com/m/g", want: false},
		"missing dir":      {arg: "example.com/m/nope", want: false},
		"other module":     {arg: "example.com/other/lib", want: false},
		"escape":           {arg: "example.com/m/../evil", want: false},
		"vendored":         {arg: "example.com/m/vendor/v", want: false},
		"version query":    {arg: "example.com/m/lib@v1.0.0", want: false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, got := exactModuleDir(tc.arg)
			if got != tc.want {
				t.Errorf("exactModuleDir(%q) = %v, want %v", tc.arg, got, tc.want)
			}
		})
	}
	if _, ok := argTargetHoldsLogos("example.com/m/lib"); !ok {
		t.Errorf("argTargetHoldsLogos(lib import path) = false, want true")
	}
	if _, ok := argTargetHoldsLogos("example.com/m"); ok {
		t.Errorf("argTargetHoldsLogos(root import path) = true, want false (no sources at root)")
	}
	if _, ok := namedLogosTarget("build", []string{"example.com/m/lib"}); !ok {
		t.Errorf("namedLogosTarget(build lib import path) = false, want true")
	}
}

func TestExactModuleDirGoPackageSuppressesFallback(t *testing.T) {
	root := t.TempDir()
	t.Setenv("GOWORK", "auto")
	writeLogosModule(t, filepath.Join(root, "l"), "example.com/l")
	writeGoModule(t, filepath.Join(root, "g"), "example.com/g")
	writeFile(t, filepath.Join(root, "go.work"), "go 1.27\n\nuse (\n\t./g\n\t./l\n)\n")
	// The working directory is LoGOs-only, but the command names a Go
	// package of another member: the exact mapping resolves it, so the
	// working-directory fallback must not fire.
	t.Chdir(filepath.Join(root, "l"))
	if _, ok := namedLogosTarget("build", []string{"example.com/g"}); ok {
		t.Errorf("namedLogosTarget(build Go import path) = true, want false")
	}
	if _, ok := namedLogosTarget("build", []string{"example.com/l"}); !ok {
		t.Errorf("namedLogosTarget(build LoGOs import path) = false, want true")
	}
}

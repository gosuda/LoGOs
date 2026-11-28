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

// writeLogosModule writes a module with one LoGOs-only package under root.
func writeLogosModule(t *testing.T, root, modPath string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "go.mod"), "module "+modPath+"\n\ngo 1.27\n")
	writeFile(t, filepath.Join(root, "lib", "lib.lgo"), "package lib\n\nfunc Msg() string { return \"x\" }\n")
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
	if reason, ok := namedLogosTarget(nil); !ok {
		t.Errorf("namedLogosTarget(nil) in a LoGOs-only dir = false, want true")
	} else if reason == "" {
		t.Errorf("namedLogosTarget(nil) returned an empty reason")
	}

	pure := t.TempDir()
	writeGoModule(t, pure, "example.com/pure")
	t.Chdir(pure)
	if _, ok := namedLogosTarget(nil); ok {
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
		"logos directory":   {arg: "l", want: true},
		"logos nested dir":  {arg: filepath.Join("l", "lib"), want: true},
		"logos file":        {arg: filepath.Join("l", "lib", "lib.lgo"), want: true},
		"go directory":      {arg: "g", want: false},
		"go file":           {arg: filepath.Join("g", "main.go"), want: false},
		"missing path":      {arg: "nosuchdir", want: false},
		"logos walk":        {arg: "./l/...", want: true},
		"go walk":           {arg: "./g/...", want: false},
		"package single":    {arg: "example.com/l/lib", want: false},
		"at version":        {arg: "example.com/l/lib@v1.0.0", want: false},
		"all without scope": {arg: "all", want: false},
		"standard library":  {arg: "std", want: false},
		"unrelated pattern": {arg: "example.com/other/...", want: false},
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

func TestInitOverlayFromArgsBad(t *testing.T) {
	saved := fsys.OverlayFile
	initOverlayFromArgs([]string{"build", "-overlay=" + filepath.Join(t.TempDir(), "nosuch.json"), "."})
	if got := fsys.OverlayFile; got != saved {
		t.Errorf("initOverlayFromArgs with a bad overlay left OverlayFile = %q, want %q", got, saved)
	}
	initOverlayFromArgs([]string{"build", "."})
	if got := fsys.OverlayFile; got != saved {
		t.Errorf("initOverlayFromArgs without -overlay left OverlayFile = %q, want %q", got, saved)
	}
}

func TestFirstNonFlagArg(t *testing.T) {
	if got := firstNonFlagArg([]string{"-x", "lint", "./..."}); got != "lint" {
		t.Errorf("firstNonFlagArg = %q, want lint", got)
	}
	if got := firstNonFlagArg([]string{"-x"}); got != "" {
		t.Errorf("firstNonFlagArg = %q, want empty", got)
	}
}

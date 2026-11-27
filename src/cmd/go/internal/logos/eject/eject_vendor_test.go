// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"bytes"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"testing"
)

func parseEjectedGuard(t *testing.T, line string) constraint.Expr {
	t.Helper()
	expr, err := constraint.Parse(line)
	if err != nil {
		t.Fatalf("parse %q: %v", line, err)
	}
	return expr
}

// A native-only guard stays rejected no matter how many other tags share
// the line: the solver decides satisfiability without enumerating tags,
// so no cutoff can admit it.
func TestEjectedSelectedRejectsNativeOnly(t *testing.T) {
	cases := []struct {
		line string
		want bool
	}{
		{"//go:build logosejected", true},
		{"//go:build !logosejected", false},
		{"//go:build !windows", true},
		{"//go:build windows", true},
		{"//go:build !ejecttestjsonoff", true},
		{"//go:build !logosejected || !windows", true},
		{"//go:build logosejected && windows", true},
		{"//go:build !logosejected && t1 && t2 && t3 && t4 && t5 && t6 && t7 && t8 && t9 && t10 && t11", false},
		{"//go:build logosejected && t1 && t2 && t3 && t4 && t5 && t6 && t7 && t8 && t9 && t10 && t11", true},
	}
	for _, tc := range cases {
		if got := ejectedSelected(parseEjectedGuard(t, tc.line)); got != tc.want {
			t.Errorf("ejectedSelected(%q) = %v, want %v", tc.line, got, tc.want)
		}
	}
	if !ejectedSelected(nil) {
		t.Errorf("ejectedSelected(nil) = false, want true")
	}
}

// Specializing the emitted guard must preserve plain-Go selection: the
// residual drops only ejected-mode tags, and a constantly-false residual
// excludes the file instead of emitting dead code.
func TestFoldEjectedGuardSpecializes(t *testing.T) {
	cases := []struct {
		line     string
		residual string // "" means nil (unconditional)
		excluded bool
	}{
		{"//go:build !logosejected || !windows", "!windows", false},
		{"//go:build logosejected", "", false},
		{"//go:build !logosejected", "", true},
		{"//go:build windows", "windows", false},
		{"//go:build logosejected && windows", "windows", false},
		{"//go:build !logosconc", "", false},
		{"//go:build logosconc", "", true},
		{"//go:build !ejecttestjsonoff", "!ejecttestjsonoff", false},
		// A folded release tag would pin the file to that Go version's
		// semantics, so the residual keeps the original structure with
		// version-neutral mode tags instead.
		{"//go:build !logosejected || go1.21", "ignore || go1.21", false},
		{"//go:build !logosconc || go1.21", "", false},
		// The original already implies this version, so the folded tag
		// stays: no structure is needed to preserve it.
		{"//go:build logosejected && go1.21", "go1.21", false},
	}
	for _, tc := range cases {
		guard, excluded := foldEjectedGuard(parseEjectedGuard(t, tc.line))
		if excluded != tc.excluded {
			t.Errorf("foldEjectedGuard(%q) excluded = %v, want %v", tc.line, excluded, tc.excluded)
			continue
		}
		got := ""
		if guard != nil {
			got = guard.String()
		}
		if got != tc.residual {
			t.Errorf("foldEjectedGuard(%q) = %q, want %q", tc.line, got, tc.residual)
		}
	}
}

// The residual must imply the same Go version as the original guard: a
// folded release tag would pin the emitted file to older language
// semantics while the original left the package default in force.
func TestFoldEjectedGuardPreservesGoVersion(t *testing.T) {
	lines := []string{
		"//go:build !logosejected || !windows",
		"//go:build logosejected",
		"//go:build windows",
		"//go:build logosejected && windows",
		"//go:build !logosconc",
		"//go:build !ejecttestjsonoff",
		"//go:build !logosejected || go1.21",
		"//go:build !logosconc || go1.21",
		"//go:build logosejected && go1.21",
		"//go:build logosejected || go1.21",
		"//go:build (!logosejected || windows) && go1.24",
	}
	for _, line := range lines {
		expr := parseEjectedGuard(t, line)
		guard, excluded := foldEjectedGuard(expr)
		if excluded {
			continue
		}
		var got string
		if guard != nil {
			got = constraint.GoVersion(guard)
		}
		if want := constraint.GoVersion(expr); got != want {
			t.Errorf("foldEjectedGuard(%q) implies Go version %q, want %q (residual %v)", line, got, want, guard)
		}
	}
}

// A UTF-8 BOM before the build line must not duplicate it: the guard
// line is dropped with its BOM and the residual is emitted once, so the
// exported package still builds.
func TestRewriteEjectedGuardDropsBOMGuard(t *testing.T) {
	guard := parseEjectedGuard(t, "//go:build !windows")
	data := []byte("\ufeff//go:build !windows\n\npackage p\n")
	out := rewriteEjectedGuard(data, guard)
	if n := bytes.Count(out, []byte("//go:build")); n != 1 {
		t.Fatalf("rewrote BOM header to %d build lines, want 1:\n%s", n, out)
	}
	if !bytes.Contains(out, []byte("//go:build !windows\n")) {
		t.Errorf("rewrote BOM header without the residual:\n%s", out)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "", out, parser.PackageClauseOnly); err != nil {
		t.Errorf("rewrote BOM header to unparsable Go: %v\n%s", err, out)
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"go/build/constraint"
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

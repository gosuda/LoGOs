// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"internal/testenv"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cmd/compile/internal/logos/lint"
)

func TestEveryLintRuleHasFixture(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	lintDir := filepath.Join(testenv.GOROOT(t), "test", "logos", "lint")
	entries, err := os.ReadDir(lintDir)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.Name()] = true
	}
	scriptDir := filepath.Join(testenv.GOROOT(t), "src", "cmd", "go", "testdata", "script")
	scripts, err := filepath.Glob(filepath.Join(scriptDir, "logos_*.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range lint.Rules() {
		_, name, _ := strings.Cut(r.ID, "::")
		if name == "" {
			t.Errorf("rule %q has no name part", r.ID)
			continue
		}
		bodies := lintFixtureBodies(t, lintDir, names, r.ID, name)
		if len(bodies) == 0 {
			t.Errorf("rule %s has no fixture in test/logos/lint", r.ID)
			continue
		}
		if lintFixtureAsserts(bodies, r.ID, name) {
			continue
		}
		if s := lintScriptAsserting(t, scripts, r.ID); s != "" {
			continue
		}
		t.Errorf("rule %s has no positive evidence: no ERROR marker names it and no logos_lint script matches its ID", r.ID)
	}
}

func lintFixtureBodies(t *testing.T, lintDir string, names map[string]bool, id, name string) []string {
	t.Helper()
	var bodies []string
	for _, cand := range []string{name + ".lgo", name + ".go", name + "_go.go"} {
		if !names[cand] {
			continue
		}
		body, err := os.ReadFile(filepath.Join(lintDir, cand))
		if err != nil {
			t.Errorf("rule %s fixture %s unreadable: %v", id, cand, err)
			continue
		}
		bodies = append(bodies, string(body))
	}
	return bodies
}

func lintFixtureAsserts(bodies []string, id, name string) bool {
	for _, body := range bodies {
		for line := range strings.SplitSeq(body, "\n") {
			if strings.Contains(line, "ERROR") && (strings.Contains(line, id) || strings.Contains(line, name)) {
				return true
			}
		}
	}
	return false
}

func lintScriptAsserting(t *testing.T, scripts []string, id string) string {
	t.Helper()
	for _, s := range scripts {
		body, err := os.ReadFile(s)
		if err != nil {
			t.Errorf("script %s unreadable: %v", filepath.Base(s), err)
			continue
		}
		for line := range strings.SplitSeq(string(body), "\n") {
			cmd := strings.TrimSpace(line)
			if cmd == "" || strings.HasPrefix(cmd, "!") || strings.HasPrefix(cmd, "#") {
				continue
			}
			if !strings.HasPrefix(cmd, "stderr") && !strings.HasPrefix(cmd, "stdout") && !strings.HasPrefix(cmd, "grep ") && !strings.HasPrefix(cmd, "cmp ") {
				continue
			}
			if strings.Contains(cmd, id) {
				return filepath.Base(s)
			}
		}
	}
	return ""
}

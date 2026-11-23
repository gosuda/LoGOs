// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"internal/testenv"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCompileConstSwitchDeadTypes(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExec(t)
	logosTool := wasmLogos(t)

	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/const_switch_dead_types")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/deadtypes\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) ([]byte, error) {
		cmd := testenv.Command(t, logosTool, args...)
		cmd.Dir = dir
		cmd.Env = append(archDiffEnv(cmd, runtime.GOOS, runtime.GOARCH), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
		return cmd.CombinedOutput()
	}

	output, err := run("run", "-p=1", "-buildvcs=false", ".")
	if err != nil {
		t.Fatalf("logos run with dead declarations: %v\n%s", err, output)
	}
	if want := "42\n"; string(output) != want {
		t.Fatalf("logos run output = %q, want %q", output, want)
	}

	if err := os.WriteFile(filepath.Join(dir, "live_cycle.lgo"), []byte("package main\n\nvar _ = pick[string]()\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	const want = "invalid recursive type: anonymous interface refers to itself"
	for _, args := range [][]string{
		{"run", "-p=1", "-buildvcs=false", "."},
		{"eject", "-o", filepath.Join(t.TempDir(), "ejected")},
	} {
		output, err := run(args...)
		if err == nil || !strings.Contains(string(output), want) || strings.Contains(string(output), "internal compiler error") {
			t.Fatalf("logos %s with live cycle = %v, want diagnostic %q without an ICE:\n%s", args[0], err, want, output)
		}
	}
}

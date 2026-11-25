// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"bytes"
	"internal/testenv"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
)

// TestCompileComptimeSharedBox calls one instance, which its defining
// package cannot name, from two packages. Natively a write through the
// const map that the instance materializes shows in the other package.
// logos eject would print the instance once in each package, each copy
// with its own body type and its own map, so it must refuse.
func TestCompileComptimeSharedBox(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExec(t)
	logosTool := wasmLogos(t)

	src := t.TempDir()
	if err := os.CopyFS(src, os.DirFS("testdata/comptime_shared_box")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.com/sharedbox\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command := func(args ...string) ([]byte, []byte, error) {
		cmd := testenv.Command(t, logosTool, args...)
		cmd.Dir = src
		cmd.Env = append(archDiffEnv(cmd, runtime.GOOS, runtime.GOARCH), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.Output()
		return stdout, stderr.Bytes(), err
	}

	stdout, stderr, err := command("run", "-p=1", "-buildvcs=false", ".")
	if err != nil {
		t.Fatalf("logos run: %v\n%s", err, stderr)
	}
	if got, want := string(stdout), "77 77 true\n"; got != want {
		t.Fatalf("materialized map seen from two packages:\n%s\nwant:\n%s", got, want)
	}

	_, stderr, err = command("eject", "-o", filepath.Join(t.TempDir(), "ejected"))
	refusal := regexp.MustCompile(`\[comptime::copied_instance_state\] a comptime instance of example\.com/sharedbox/owner\.Probe ejected into package example\.com/sharedbox/(left|right) keeps a variable over Local, a type declared in its body`)
	if err == nil || !refusal.Match(stderr) {
		t.Fatalf("logos eject: err %v, want the copied_instance_state refusal\n%s", err, stderr)
	}
}

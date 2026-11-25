// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"internal/testenv"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCompileComptimeLocalIdentity(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExec(t)
	logosTool := wasmLogos(t)

	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/comptime_local_identity")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/localidentity\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, dir, tool string, args ...string) string {
		t.Helper()
		cmd := testenv.Command(t, tool, args...)
		cmd.Dir = dir
		cmd.Env = append(archDiffEnv(cmd, runtime.GOOS, runtime.GOARCH), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", filepath.Base(tool), args, err, output)
		}
		return string(output)
	}

	const want = "captures 1 8 true true\nfields [1 8 1] true\n"
	t.Run("native", func(t *testing.T) {
		if got := run(t, dir, logosTool, "run", "-p=1", "-buildvcs=false", "."); got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	})
	t.Run("eject", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "out")
		run(t, dir, logosTool, "eject", "-o", out)
		if got := run(t, out, testenv.GoToolPath(t), "run", "-p=1", "-buildvcs=false", "."); got != want {
			t.Fatalf("output = %q, want %q", got, want)
		}
	})
}

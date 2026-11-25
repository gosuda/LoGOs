// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"bytes"
	"internal/testenv"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestCompileComptimeForeignLocal ejects an instance into a package other
// than its defining package. The constants of types declared in the
// instance body must spell those types as the body declares them.
func TestCompileComptimeForeignLocal(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExec(t)
	logosTool := wasmLogos(t)

	run := func(t *testing.T, dir, tool string, args ...string) string {
		t.Helper()
		cmd := testenv.Command(t, tool, args...)
		cmd.Dir = dir
		cmd.Env = append(archDiffEnv(cmd, runtime.GOOS, runtime.GOARCH), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s %v: %v\nstdout:\n%s\nstderr:\n%s", filepath.Base(tool), args, err, stdout, &stderr)
		}
		return string(stdout)
	}

	const want = "5 9\n"
	for _, mode := range []string{"native", "ejected"} {
		t.Run(mode, func(t *testing.T) {
			src := t.TempDir()
			if err := os.CopyFS(src, os.DirFS("testdata/comptime_foreign_local")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.com/foreignlocal\n\ngo 1.27\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			dir, tool := src, logosTool
			if mode == "ejected" {
				dir = filepath.Join(t.TempDir(), "ejected")
				run(t, src, logosTool, "eject", "-o", dir)
				tool = testenv.GoToolPath(t)
			}
			if got := run(t, dir, tool, "run", "-p=1", "-buildvcs=false", "."); got != want {
				t.Fatalf("body type constants:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

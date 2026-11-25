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

func TestCompileComptimeLocalHelper(t *testing.T) {
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

	const want = `first outer 3 19 101 true
first inner 3 29 29 true
first separate 19
second outer 3 19 19 true
second inner 3 29 29 true
second separate 19
first again outer 3 19 101 true
first again inner 3 29 29 true
first again separate 19
identity 4 true true
`
	for _, mode := range []string{"native", "ejected"} {
		t.Run(mode, func(t *testing.T) {
			src := t.TempDir()
			if err := os.CopyFS(src, os.DirFS("testdata/comptime_local_helper")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.com/localhelper\n\ngo 1.27\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			dir, tool := src, logosTool
			if mode == "ejected" {
				dir = filepath.Join(t.TempDir(), "ejected")
				run(t, src, logosTool, "eject", "-o", dir)
				tool = testenv.GoToolPath(t)
			}
			if got := run(t, dir, tool, "run", "-p=1", "-buildvcs=false", "."); got != want {
				t.Fatalf("local type and map results:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

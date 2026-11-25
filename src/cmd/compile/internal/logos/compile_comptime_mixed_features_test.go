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

func TestCompileComptimeMixedFeatures(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExec(t)
	logosTool := wasmLogos(t)

	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/comptime_mixed_features")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/mixedfeatures\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, dir, tool string, args ...string) string {
		t.Helper()
		cmd := testenv.Command(t, tool, args...)
		cmd.Dir = dir
		cmd.Env = append(archDiffEnv(cmd, runtime.GOOS, runtime.GOARCH), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", tool, args, err, output)
		}
		return string(output)
	}

	const want = "ok 70 <nil>\n" +
		"enter:First read:First map:First:4 map:First:5 value:First:18 leave:First " +
		"enter:Middle default:Middle map:Middle:5 map:Middle:6 value:Middle:22 leave:Middle " +
		"enter:Last read:Last map:Last:7 map:Last:8 value:Last:30 leave:Last return\n" +
		"bad 0 strconv.Atoi: parsing \"x\": invalid syntax\n" +
		"enter:First read:First map:First:4 map:First:5 value:First:18 leave:First " +
		"enter:Middle read:Middle leave:Middle return\n"

	t.Run("native", func(t *testing.T) {
		if got := run(t, dir, logosTool, "run", "-p=1", "-buildvcs=false", "."); got != want {
			t.Fatalf("output:\n%s\nwant:\n%s", got, want)
		}
	})
	t.Run("ejected", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "ejected")
		run(t, dir, logosTool, "eject", "-o", out)
		if got := run(t, out, testenv.GoToolPath(t), "run", "-p=1", "-buildvcs=false", "."); got != want {
			t.Fatalf("output:\n%s\nwant:\n%s", got, want)
		}
	})
}

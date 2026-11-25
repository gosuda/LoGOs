// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"bytes"
	"fmt"
	"internal/testenv"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

func TestCompileComptimeConstGroup(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExec(t)
	logosTool := wasmLogos(t)

	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/comptime_const_group")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/comptimeconstgroup\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, dir, tool string, args ...string) []byte {
		t.Helper()
		cmd := testenv.Command(t, tool, args...)
		cmd.Dir = dir
		cmd.Env = append(archDiffEnv(cmd, runtime.GOOS, runtime.GOARCH), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s %v: %v\nstdout:\n%s\nstderr:\n%s", tool, args, err, output, stderr.Bytes())
		}
		return output
	}
	check := func(t *testing.T, output []byte) {
		t.Helper()
		stdout := bytes.NewReader(output)
		for _, tc := range []struct {
			scope string
			want  []int
		}{
			{"function scope", []int{1, 1, 2}},
			{"file scope", []int{1, 1, 2, 1, 2}},
		} {
			var values [10]int
			count := len(tc.want)
			for i := range values[:2*count] {
				if _, err := fmt.Fscan(stdout, &values[i]); err != nil {
					t.Fatalf("%s: read constant group values: %v\nstdout:\n%s", tc.scope, err, output)
				}
			}
			ordinaryValues, compositeValues := values[:count], values[count:2*count]
			if !slices.Equal(ordinaryValues, tc.want) {
				t.Errorf("%s: scalar-led group = %v, want %v", tc.scope, ordinaryValues, tc.want)
			}
			if !slices.Equal(compositeValues, ordinaryValues) {
				t.Errorf("%s: composite-led group = %v, want scalar-led values %v", tc.scope, compositeValues, ordinaryValues)
			}
		}
	}

	t.Run("native", func(t *testing.T) {
		check(t, run(t, dir, logosTool, "run", "-p=1", "-buildvcs=false", "."))
	})
	t.Run("ejected", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "ejected")
		run(t, dir, logosTool, "eject", "-o", out)
		check(t, run(t, out, testenv.GoToolPath(t), "run", "-p=1", "-buildvcs=false", "."))
	})
}

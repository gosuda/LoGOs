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
	"testing"
)

func TestCompileNestedStructDefault(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExec(t)
	logosTool := wasmLogos(t)

	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/struct_default_nested")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/structdefaults\n\ngo 1.27\n"), 0o644); err != nil {
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
		var direct, nested, explicit, override int
		if _, err := fmt.Fscan(bytes.NewReader(output), &direct, &nested, &explicit, &override); err != nil {
			t.Fatalf("read timeouts: %v\n%s", err, output)
		}
		if direct != 10 {
			t.Errorf("direct Policy.Timeout = %d, want 10", direct)
		}
		if nested != direct {
			t.Errorf("Client.Policy.Timeout = %d, want direct Policy.Timeout = %d", nested, direct)
		}
		if explicit != 27 {
			t.Errorf("Client.ExplicitPolicy.Timeout = %d, want 27", explicit)
		}
		if override != 31 {
			t.Errorf("explicitly set Client.Policy.Timeout = %d, want 31", override)
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

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
	"strings"
	"testing"
)

func TestCompileComptimeSemanticIdentity(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExec(t)
	logosTool := wasmLogos(t)

	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/comptime_semantic_identity")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/semanticidentity\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const want = "forward [non-nil optional]\n" +
		"reverse [optional non-nil]\n" +
		"shared [non-nil optional]\n" +
		"named [first-id second-id]\n"

	run := func(t *testing.T, workdir, tool string, args ...string) string {
		t.Helper()
		cmd := testenv.Command(t, tool, args...)
		cmd.Dir = workdir
		cmd.Env = append(archDiffEnv(cmd, runtime.GOOS, runtime.GOARCH), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%s: %v\n%s%s", cmd, err, out, &stderr)
		}
		return string(out)
	}

	t.Run("native", func(t *testing.T) {
		if got := run(t, dir, logosTool, "run", "-p=1", "-buildvcs=false", "."); got != want {
			t.Fatalf("stdout = %q, want %q", got, want)
		}
	})
	t.Run("ejected", func(t *testing.T) {
		src := t.TempDir()
		if err := os.CopyFS(src, os.DirFS(dir)); err != nil {
			t.Fatal(err)
		}
		for _, refused := range []string{"forward", "consumer"} {
			if err := os.RemoveAll(filepath.Join(src, refused)); err != nil {
				t.Fatal(err)
			}
		}
		outDir := filepath.Join(t.TempDir(), "ejected")
		run(t, src, logosTool, "eject", "-o", outDir)
		if got := run(t, outDir, testenv.GoToolPath(t), "run", "-p=1", "-buildvcs=false", "."); got != want {
			t.Fatalf("stdout = %q, want %q", got, want)
		}
	})
	t.Run("exported_forwarder", func(t *testing.T) {
		t.Helper()
		cmd := testenv.Command(t, logosTool, "build", "-p=1", "-buildvcs=false", "./...")
		cmd.Dir = dir
		cmd.Env = append(archDiffEnv(cmd, runtime.GOOS, runtime.GOARCH), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if out, err := cmd.Output(); err == nil {
			t.Fatalf("logos build ./... succeeded, want [comptime::exported_forwarder]\n%s", out)
		} else if got := stderr.String(); !strings.Contains(got, "[comptime::exported_forwarder]") || !strings.Contains(got, "forward/forward.lgo") {
			t.Fatalf("stderr = %q, want [comptime::exported_forwarder] for forward/forward.lgo", got)
		}
	})
}

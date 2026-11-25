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

func TestCompileConstForOwnerScope(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExec(t)
	logosTool := wasmLogos(t)

	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/const_for_owner_scope")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/ownerscope\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := testenv.Command(t, logosTool, "run", "-p=1", "-buildvcs=false", ".")
	cmd.Dir = dir
	cmd.Env = append(archDiffEnv(cmd, runtime.GOOS, runtime.GOARCH), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("logos run: %v\n%s", err, output)
	}
	if want := "7 17\n"; string(output) != want {
		t.Fatalf("logos run output = %q, want %q", output, want)
	}
}

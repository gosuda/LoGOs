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

// TestCompileComptimeForwarding builds a package whose exported generic
// Layouts forwards its type parameter to a forwarding function of its
// package. A caller in another package cannot instantiate it, so the build
// fails in the defining package at compile time (SPEC 2.5, T-205) instead of
// panicking when main calls it, and logos eject refuses the package with the
// same diagnostic.
func TestCompileComptimeForwarding(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExec(t)
	logosTool := wasmLogos(t)

	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/comptime_forwarding")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/forwarding\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	const want = "owner.lgo:16:9: [comptime::exported_forwarder] exported function Layouts forwards type parameter T to forwarding function layouts; a caller in another package cannot instantiate it"
	for _, args := range [][]string{
		{"run", "-p=1", "-buildvcs=false", "."},
		{"eject", "-o", filepath.Join(t.TempDir(), "ejected")},
	} {
		cmd := testenv.Command(t, logosTool, args...)
		cmd.Dir = dir
		cmd.Env = append(archDiffEnv(cmd, runtime.GOOS, runtime.GOARCH), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
		output, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatalf("logos %s succeeded; want the exported forwarder diagnostic\n%s", args[0], output)
		}
		if !strings.Contains(string(output), want) {
			t.Fatalf("logos %s output misses %q\n%s", args[0], want, output)
		}
		if strings.Contains(string(output), "panic") || strings.Contains(string(output), "internal compiler error") {
			t.Fatalf("logos %s reached run time or crashed\n%s", args[0], output)
		}
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"crypto/fips140"
	"debug/elf"
	"internal/platform"
	"internal/testenv"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCompileComptimePreparedOnce pins duplicate-safe prepared helpers:
// two consumers (left, right) prepare the same owner helper. The linked
// binary must hold exactly one definition of each helper global, the
// prepared storage must be owner-qualified, the prepared ASan tasks must
// exist, and the ASan run must report no leak, double free or error.
func TestCompileComptimePreparedOnce(t *testing.T) {
	if !platform.ASanSupported(runtime.GOOS, runtime.GOARCH) {
		t.Skipf("-asan is not supported on %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveCGO(t)
	if fips140.Enabled() {
		t.Skip("-asan is not supported in FIPS 140 mode")
	}
	overcommit, err := os.ReadFile("/proc/sys/vm/overcommit_memory")
	if err != nil || strings.TrimSpace(string(overcommit)) == "2" {
		t.Skipf("ASan requires memory overcommit (vm.overcommit_memory=%q, read error: %v)", strings.TrimSpace(string(overcommit)), err)
	}

	logosTool := filepath.Join(filepath.Dir(testenv.GoToolPath(t)), "logos")
	dir := t.TempDir()
	if err := os.CopyFS(dir, os.DirFS("testdata/comptime_asan")); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "fixture")
	build := testenv.Command(t, logosTool, "build", "-p=1", "-buildvcs=false", "-buildmode=exe", "-asan", "-ldflags=-strictdups=2", "-o", exe, ".")
	build.Dir = dir
	build.Env = append(archDiffEnv(build, runtime.GOOS, runtime.GOARCH),
		"CGO_ENABLED=1", "GOPROXY=off", "GONOPROXY=none", "GOSUMDB=off", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("logos build -asan: %v\n%s", err, out)
	}

	run := testenv.Command(t, exe)
	run.Dir = dir
	run.Env = append(run.Environ(), "ASAN_OPTIONS=halt_on_error=1", "GODEBUG=")
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("ASan fixture: %v\n%s", err, out)
	}
	if string(out) != "comptime ASan OK\n" {
		t.Fatalf("ASan fixture output = %q, want %q", out, "comptime ASan OK\n")
	}
	checkPreparedOnce(t, exe)
}

func checkPreparedOnce(t *testing.T, exe string) {
	t.Helper()
	const modulePrefix = "example.com/comptimeasan/"
	const preparedPrefix = "!go:logos-asan."
	f, err := elf.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	symbols, err := f.Symbols()
	if err != nil {
		t.Fatal(err)
	}
	byName := make(map[string]elf.Symbol)
	for _, s := range symbols {
		if s.Section == elf.SHN_UNDEF {
			continue
		}
		if !strings.HasPrefix(s.Name, modulePrefix) && !strings.HasPrefix(s.Name, preparedPrefix+modulePrefix) {
			continue
		}
		if _, exists := byName[s.Name]; exists {
			t.Fatalf("duplicate linked symbol %q", s.Name)
		}
		byName[s.Name] = s
	}
	var backing, table, memo, tasks int
	for name := range byName {
		storage, ok := strings.CutPrefix(name, preparedPrefix)
		if !ok {
			continue
		}
		storage, ok = strings.CutSuffix(storage, "..inittask")
		if !ok {
			continue
		}
		tasks++
		if !strings.HasPrefix(storage, modulePrefix+"owner.") {
			t.Errorf("prepared storage is not owner-qualified: %s", storage)
		}
		if _, ok := byName[storage]; !ok {
			t.Errorf("registration task has no linked storage: %s", storage)
		}
		switch {
		case strings.HasSuffix(storage, "_tab"):
			table++
		case strings.HasSuffix(storage, "_mat"):
			memo++
		case strings.HasSuffix(storage, ".logos_backing0") && !strings.Contains(storage, "_sorted.") && !strings.Contains(storage, "_tab."):
			backing++
		}
	}
	if tasks == 0 {
		t.Errorf("no prepared ASan registration tasks linked")
	}
	if backing != 1 || table != 1 || memo != 1 {
		t.Errorf("linked prepared helpers: slice backing = %d, map table = %d, map memo = %d; want one of each", backing, table, memo)
	}
}

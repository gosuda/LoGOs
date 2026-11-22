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

const comptimeASanModule = "example.com/comptimeasan"

func TestCompileComptimeASan(t *testing.T) {
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
	for _, tc := range []struct {
		name, tag, output string
	}{
		{name: "importers-only", output: "comptime ASan OK\n"},
		{name: "owner-and-importers", tag: "comptimeasan_owner", output: "owner init OK\ncomptime ASan OK\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.CopyFS(dir, os.DirFS("testdata/comptime_asan")); err != nil {
				t.Fatal(err)
			}
			exe := filepath.Join(dir, "fixture")
			build := testenv.Command(t, logosTool, "build", "-p=1", "-buildvcs=false", "-buildmode=exe", "-asan", "-ldflags=-strictdups=2", "-tags="+tc.tag, "-o", exe, ".")
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
			if string(out) != tc.output {
				t.Errorf("ASan fixture output = %q, want %q", out, tc.output)
			}
			checkComptimeASanTasks(t, exe)
		})
	}
}

func checkComptimeASanTasks(t *testing.T, exe string) {
	t.Helper()
	const scheduleName = "go:main.inittasks"
	const modulePrefix = comptimeASanModule + "/"
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
		if s.Name != scheduleName && !strings.HasPrefix(s.Name, modulePrefix) && !strings.HasPrefix(s.Name, preparedPrefix+modulePrefix) {
			continue
		}
		if _, exists := byName[s.Name]; exists {
			t.Fatalf("duplicate linked symbol %q", s.Name)
		}
		byName[s.Name] = s
	}

	schedule := byName[scheduleName]
	if f.Class != elf.ELFCLASS64 || f.Type != elf.ET_EXEC || schedule.Section == elf.SHN_UNDEF || int(schedule.Section) >= len(f.Sections) || schedule.Size == 0 || schedule.Size%8 != 0 {
		t.Fatalf("cannot read linked init schedule: %s %s %+v", f.Class, f.Type, schedule)
	}
	section := f.Sections[schedule.Section]
	if schedule.Value < section.Addr {
		t.Fatalf("init schedule address %#x precedes %s", schedule.Value, section.Name)
	}
	data := make([]byte, schedule.Size)
	if _, err := section.ReadAt(data, int64(schedule.Value-section.Addr)); err != nil {
		t.Fatalf("read linked init schedule: %v", err)
	}
	positions := make(map[uint64][]int)
	for off := 0; off < len(data); off += 8 {
		addr := f.ByteOrder.Uint64(data[off:])
		positions[addr] = append(positions[addr], off/8)
	}
	firstConsumer := len(data) / 8
	for _, pkg := range []string{"owner", "left", "right"} {
		name := modulePrefix + pkg + "..inittask"
		s, exists := byName[name]
		if pkg == "owner" && !exists {
			continue
		}
		at := positions[s.Value]
		if len(at) != 1 {
			t.Fatalf("linked init schedule entries for %s = %v, want one", name, at)
		}
		firstConsumer = min(firstConsumer, at[0])
	}

	var sliceBacking, mapTable, mapMemo int
	for name, task := range byName {
		storage, ok := strings.CutPrefix(name, preparedPrefix)
		if !ok {
			continue
		}
		storage, ok = strings.CutSuffix(storage, "..inittask")
		if !ok {
			continue
		}
		if !strings.HasPrefix(storage, modulePrefix+"owner.") {
			t.Errorf("prepared storage is not owner-qualified: %s", storage)
		}
		if _, ok := byName[storage]; !ok {
			t.Errorf("registration task has no linked storage: %s", storage)
		}
		if at := positions[task.Value]; len(at) != 1 || at[0] >= firstConsumer {
			t.Errorf("linked ASan task %s scheduled at %v, want once before fixture init at %d", name, at, firstConsumer)
		}
		switch {
		case strings.HasSuffix(storage, "_tab"):
			mapTable++
		case strings.HasSuffix(storage, "_mat"):
			mapMemo++
		case strings.HasSuffix(storage, ".logos_backing0") && !strings.Contains(storage, "_sorted.") && !strings.Contains(storage, "_tab."):
			sliceBacking++
		}
	}
	if sliceBacking != 1 || mapTable != 1 || mapMemo != 1 {
		t.Errorf("linked ASan tasks: slice backing = %d, map table = %d, map memo = %d; want one of each", sliceBacking, mapTable, mapMemo)
	}
}

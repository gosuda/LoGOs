// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"bytes"
	"errors"
	"fmt"
	"internal/testenv"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCompileComptimeHelperUnit(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExec(t)
	logosTool := wasmLogos(t)

	run := func(t *testing.T, dir, tool string, args ...string) []byte {
		t.Helper()
		cmd := testenv.Command(t, tool, args...)
		cmd.Dir = dir
		cmd.Env = append(archDiffEnv(cmd, runtime.GOOS, runtime.GOARCH), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", tool, args, err, output)
		}
		return output
	}

	for _, mode := range []string{"native", "ejected"} {
		t.Run(mode, func(t *testing.T) {
			src := t.TempDir()
			if err := os.CopyFS(src, os.DirFS("testdata/comptime_helper_unit")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.com/helperunit\n\ngo 1.27\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			dir, tool := src, logosTool
			if mode == "ejected" {
				dir = filepath.Join(t.TempDir(), "ejected")
				run(t, src, logosTool, "eject", "-o", dir)
				tool = testenv.GoToolPath(t)
			}
			checkComptimeHelperUnit(t, run(t, dir, tool, "run", "-p=1", "-buildvcs=false", "."))
		})
	}
}

func checkComptimeHelperUnit(t *testing.T, output []byte) {
	t.Helper()
	input := bytes.NewReader(output)
	scan := func(label string, fields ...any) {
		t.Helper()
		if _, err := fmt.Fscan(input, fields...); err != nil {
			t.Fatalf("read %s: %v\n%s", label, err, output)
		}
	}

	var early, late, calls int
	scan("initialization", &early, &late, &calls)
	if early != 41 || late != 41 || calls != 1 {
		t.Errorf("initialization: early = %d, late = %d, calls = %d; want 41, 41, 1", early, late, calls)
	}

	type mutation struct{ key, value int }
	sharedUpdates := [2]mutation{{7, 701}, {8, 1801}}
	distinctUpdates := [2]mutation{{8, 803}, {7, 1703}}
	checkEntries := func(name string, block int, update *mutation) {
		t.Helper()
		for key := range 9 {
			var gotKey, constant, materialized int
			scan(name, &gotKey, &constant, &materialized)
			constValue := 100*block + 10*key
			mapValue := constValue
			if update != nil && key == update.key {
				mapValue = update.value
			}
			if gotKey != key || constant != constValue || materialized != mapValue {
				t.Errorf("%s map %d: entry %d has key %d, const value %d, materialized value %d; want %d, %d, %d",
					name, block, key, gotKey, constant, materialized, key, constValue, mapValue)
			}
		}
	}

	var addresses [2][2]uint64
	for _, want := range []struct {
		name     string
		instance int
		key      int
		offset   int
		updates  *[2]mutation
	}{
		{"left shared", 1, 0, 2, nil},
		{"right shared", 1, 7, 3, &sharedUpdates},
		{"right distinct", 2, 0, 2, nil},
		{"left shared again", 1, 8, 5, &sharedUpdates},
		{"right distinct again", 2, 7, 3, &distinctUpdates},
	} {
		var instance, offset int
		scan(want.name, &instance, &offset)
		if instance != want.instance || offset != want.offset {
			t.Errorf("%s: instance = %d, slice element = %d; want %d, %d", want.name, instance, offset, want.instance, want.offset)
		}
		for block := range 2 {
			var pointer, assigned uint64
			var hit, miss, length int
			scan(want.name, &pointer, &assigned, &hit, &miss, &length)
			value := 100*block + 10*want.key
			if hit != value || miss != -1 || length != 9 {
				t.Errorf("%s map %d: hit = %d, miss = %d, materialized length = %d; want %d, -1, 9", want.name, block, hit, miss, length, value)
			}
			if pointer != assigned {
				t.Errorf("%s map %d: formatting sees map %#x, assignment sees %#x", want.name, block, pointer, assigned)
			}
			if pointer == 0 {
				t.Errorf("%s map %d: materialized a nil map", want.name, block)
			}
			first := &addresses[want.instance-1][block]
			if want.updates == nil {
				*first = pointer
				checkEntries(want.name, block, nil)
				continue
			}
			if pointer != *first {
				t.Errorf("%s map %d: materialization changed from %#x to %#x across calls", want.name, block, *first, pointer)
			}
			checkEntries(want.name, block, &want.updates[block])
		}
	}
	for _, want := range []struct {
		name   string
		values [4]int
	}{
		{"retained shared maps", [4]int{701, 80, 170, 1801}},
		{"retained distinct maps", [4]int{70, 803, 1703, 180}},
	} {
		var got [4]int
		scan(want.name, &got[0], &got[1], &got[2], &got[3])
		if got != want.values {
			t.Errorf("%s: values after later calls = %v; want %v", want.name, got, want.values)
		}
	}

	var ownerValue, leftValue int
	scan("package map before mutation", &ownerValue, &leftValue)
	if ownerValue != 51 || leftValue != 51 {
		t.Errorf("package map initial values: owner = %d, left = %d; want 51, 51", ownerValue, leftValue)
	}
	var ownerMap, leftMap, rightMap uint64
	scan("package map identity", &ownerMap, &leftMap, &rightMap)
	if ownerMap == 0 || ownerMap != leftMap || ownerMap != rightMap {
		t.Errorf("package map identity: owner = %#x, left = %#x, right = %#x; want one nonzero address", ownerMap, leftMap, rightMap)
	}
	var rightValue, constantValue int
	scan("package map after mutation", &ownerValue, &rightValue, &constantValue)
	if ownerValue != 90 || rightValue != 90 || constantValue != 51 {
		t.Errorf("package map after left mutation: owner = %d, right = %d, const lookup = %d; want 90, 90, 51", ownerValue, rightValue, constantValue)
	}

	var extra string
	if _, err := fmt.Fscan(input, &extra); !errors.Is(err, io.EOF) {
		t.Fatalf("unexpected trailing output %q (scan error: %v)\n%s", extra, err, output)
	}
}

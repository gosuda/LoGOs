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

// The reader package exports comptime generics that read const maps declared
// in the owner package under an owner-named key type. The consumer below
// instantiates them without importing owner, so the generated const-map
// lowering must add an import of the owner package and use it, in both the
// native and the ejected channels. The aliased variant imports the owner under
// a distinct name that the source itself uses, which the lowering must keep
// alive instead of minting a colliding spelling or blanking the existing one.
func TestCompileComptimeMapImportBoundary(t *testing.T) {
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

	write := func(t *testing.T, dir, name, text string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	const ownerSrc = `package owner

type Key int

const Tag = "mapboundary-owner"

const Table = map[Key]int{3: 31, 1: 11}

const Wide = map[Key]int{0: 200, 1: 201, 2: 202, 3: 203, 4: 204, 5: 205, 6: 206, 7: 207, 8: 208, 9: 209, 10: 210, 11: 211}
`

	const readerSrc = `package reader

import (
	"comptime"

	"example.com/mapboundary/owner"
)

type Marker struct{ N int }

func Read[T any](k owner.Key) int {
	const if comptime.IsStruct[T]() {
		return owner.Table[k] ?? -1
	}
	return -1
}

func ReadWide[T any](k owner.Key) int {
	const if comptime.IsStruct[T]() {
		return owner.Wide[k] ?? -1
	}
	return -1
}
`

	const mainAbsent = `package main

import (
	"fmt"

	"example.com/mapboundary/reader"
)

func main() {
	fmt.Println(
		reader.Read[reader.Marker](3),
		reader.Read[reader.Marker](1),
		reader.Read[reader.Marker](9),
		reader.ReadWide[reader.Marker](4),
		reader.ReadWide[reader.Marker](11),
		reader.ReadWide[reader.Marker](40),
	)
}
`

	const mainAliased = `package main

import (
	"fmt"

	ow "example.com/mapboundary/owner"
	"example.com/mapboundary/reader"
)

func main() {
	fmt.Println(
		ow.Tag,
		reader.Read[reader.Marker](3),
		reader.Read[reader.Marker](1),
		reader.Read[reader.Marker](9),
		reader.ReadWide[reader.Marker](4),
		reader.ReadWide[reader.Marker](11),
		reader.ReadWide[reader.Marker](40),
	)
}
`

	module := func(t *testing.T, mainSrc string) string {
		t.Helper()
		dir := t.TempDir()
		write(t, dir, "go.mod", "module example.com/mapboundary\n\ngo 1.27\n")
		write(t, dir, "main.lgo", mainSrc)
		write(t, dir, "owner/owner.lgo", ownerSrc)
		write(t, dir, "reader/reader.lgo", readerSrc)
		return dir
	}

	for _, variant := range []struct {
		name, mainSrc, want string
	}{
		{"no owner import", mainAbsent, "31 11 -1 204 211 -1\n"},
		{"aliased owner import", mainAliased, "mapboundary-owner 31 11 -1 204 211 -1\n"},
	} {
		for _, mode := range []string{"native", "ejected"} {
			t.Run(variant.name+"/"+mode, func(t *testing.T) {
				src := module(t, variant.mainSrc)
				dir, tool := src, logosTool
				if mode == "ejected" {
					dir = filepath.Join(t.TempDir(), "ejected")
					run(t, src, logosTool, "eject", "-o", dir)
					tool = testenv.GoToolPath(t)
				}
				if got := run(t, dir, tool, "run", "-p=1", "-buildvcs=false", "."); got != variant.want {
					t.Fatalf("const map import boundary results:\n%s\nwant:\n%s", got, variant.want)
				}
			})
		}
	}
}

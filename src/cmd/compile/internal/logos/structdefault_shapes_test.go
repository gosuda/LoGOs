// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"internal/testenv"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStructDefaultGenericInstances(t *testing.T) {
	const src = `package main

type Box[T any] struct {
	Value T
	Count int = 17
	Items []T = []T{}
}

func main() {
	a := Box[string]{Value: "a"}
	b := Box[int]{Value: 2}
	c := Box[int]{Value: 3, Count: 23}
	if a.Count != 17 || b.Count != 17 || c.Count != 23 {
		panic("generic instances lost their origin defaults")
	}
	if a.Items == nil || b.Items == nil {
		panic("generic composite default became a zero value")
	}
	println("ok generic defaults")
}
`
	runStructDefaultLowerings(t, src, "ok generic defaults\n")
}

func TestStructDefaultAnonymousAliasBinding(t *testing.T) {
	const src = `package main

const count = 17
type Cfg = struct { Count int = count }

func main() {
	const count = 99
	_ = count
	if (Cfg{}).Count != 17 {
		panic("anonymous alias default captured use-site names")
	}
	println("ok anonymous defaults")
}
`
	runStructDefaultLowerings(t, src, "ok anonymous defaults\n")
}

func TestStructDefaultExportedPrivateFields(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	dir := t.TempDir()
	for name, src := range map[string]string{
		"go.mod": "module example.com/defaultprivate\n\ngo 1.27\n",
		"lib/defaults.lgo": `package lib
type Cfg struct {
	private int = 13
	Public int = 7
}
type hidden struct {
	private int = 17
	Public int = 9
}
type Alias = hidden
func Check(a Cfg, b Alias) bool {
	return a.private == 13 && a.Public == 23 && b.private == 17 && b.Public == 29
}
`,
		"main.lgo": `package main
import "example.com/defaultprivate/lib"
func main() {
	a := lib.Cfg{Public: 23}
	b := lib.Alias{Public: 29}
	if !lib.Check(a, b) { panic("imported private defaults were not applied") }
	println("ok private defaults")
}
`,
	} {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	goTool := testenv.GoToolPath(t)
	logosName := "logos"
	if runtime.GOOS == "windows" {
		logosName += ".exe"
	}
	logosTool := filepath.Join(filepath.Dir(goTool), logosName)
	env := append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local", "GOWORK=off", "GOAMD64=v1")
	t.Run("native", func(t *testing.T) {
		cmd := exec.Command(logosTool, "run", ".")
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil || string(out) != "ok private defaults\n" {
			t.Fatalf("native private defaults: %v\n%s", err, out)
		}
	})
	t.Run("ejected", func(t *testing.T) {
		cmd := exec.Command(logosTool, "eject", "-o", "out")
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("eject private defaults: %v\n%s", err, out)
		}
		cmd = exec.Command(goTool, "run", ".")
		cmd.Dir = filepath.Join(dir, "out")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil || string(out) != "ok private defaults\n" {
			t.Fatalf("ejected private defaults: %v\n%s", err, out)
		}
	})
}

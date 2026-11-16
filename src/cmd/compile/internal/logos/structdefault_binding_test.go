// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"bytes"
	"internal/testenv"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// Both forms must retain declaration bindings. The Go compiler that runs
// the result is an independent oracle for lexical scope and constant types.
func TestStructDefaultDeclarationBinding(t *testing.T) {
	const src = `package main

const port = 8080
const key = 7
type code int
const status code = 9

type Cfg struct {
	Port int = port
	Items []int = []int{port, 2}
	Entries map[int]int = map[int]int{key: port}
	Status any = status
}

func main() {
	const port = 99
	const key = 3
	const status = "shadow"
	_ = port
	_ = key
	_ = status
	c := Cfg{}
	entry, present := c.Entries[7]
	if c.Port != 8080 || c.Items[0] != 8080 || !present || entry != 8080 {
		panic("field defaults captured use-site names")
	}
	if _, ok := c.Status.(code); !ok {
		panic("field default lost its declared constant type")
	}
	d := Cfg{Port: 23}
	if d.Port != 23 {
		panic("field default replaced an explicit initializer")
	}
	c.Items[0] = 1
	if d.Items[0] != 8080 {
		panic("default composites share mutable storage")
	}
	println("ok declaration bindings")
}
`
	runStructDefaultLowerings(t, src, "ok declaration bindings\n")
}

func TestStructDefaultLexicalScope(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  string
	}{
		{
			name: "file import",
			src: `package main
import "time"
type Cfg struct { Delay time.Duration = time.Second }
func main() {
	if (Cfg{}).Delay != 1000000000 { panic("wrong imported constant") }
	println("ok lexical scope")
}
`,
		},
		{
			name: "local constant",
			src: `package main
func main() {
	const local = 17
	type Cfg struct { Value int = local }
	if (Cfg{}).Value != 17 { panic("wrong local constant") }
	println("ok lexical scope")
}
`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runStructDefaultLowerings(t, tc.src, "ok lexical scope\n")
		})
	}
}

func TestStructDefaultRejectsInvalidDeclaration(t *testing.T) {
	for _, tc := range []struct {
		name string
		decl string
		want string
	}{
		{"byte below minimum", "Value uint8 = -1", "overflows"},
		{"byte minimum", "Value uint8 = 0", ""},
		{"byte maximum", "Value uint8 = 255", ""},
		{"byte above maximum", "Value uint8 = 256", "overflows"},
		{"wrong scalar type", `Value int = "bad"`, "cannot use"},
		{"wrong composite type", `Value []int = []string{"bad"}`, "cannot use"},
		{"dynamic map key", `Value map[int]int = map[int]int{key(): 1}`, "default_not_constant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "package p\nfunc key() int { return 1 }\ntype Unused struct { " + tc.decl + " }\n"
			file := ejectParse(t, syntax.NewFileBase("defaultcheck.lgo"), []byte(src), syntax.LogosMode)
			var errors []string
			conf := types2.Config{
				Logos: true,
				Error: func(err error) { errors = append(errors, err.Error()) },
			}
			_, err := conf.Check("p", []*syntax.File{file}, &types2.Info{})
			if tc.want == "" {
				if err != nil {
					t.Fatalf("valid field default rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(strings.Join(errors, "\n"), tc.want) {
				t.Fatalf("field default errors = %v, want %q without constructing Unused", errors, tc.want)
			}
		})
	}
}

func TestStructDefaultExportedConstantBindings(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	dir := t.TempDir()
	for name, src := range map[string]string{
		"go.mod": "module example.com/defaultbinding\n\ngo 1.27\n",
		"producer/defaults.lgo": `package producer
const hidden = 8080
const index = 1
type code int
const status code = 9
type Cfg struct {
	Port int = hidden
	Half float64 = 1.0 / 2
	Values [2]int = [2]int{index: hidden}
	Status any = status
}
func HasCode(c Cfg) bool {
	_, ok := c.Status.(code)
	return ok
}
`,
		"main.lgo": `package main
import renamed "example.com/defaultbinding/producer"
const hidden = 99
const index = 0
const status = "shadow"
func main() {
	c := renamed.Cfg{}
	if c.Port != 8080 || c.Half != 0.5 || c.Values != [2]int{0, 8080} {
		panic("exported defaults captured consumer names")
	}
	if !renamed.HasCode(c) { panic("exported default lost its private type") }
	println("ok exported bindings")
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
		if err != nil || string(out) != "ok exported bindings\n" {
			t.Fatalf("native imported defaults: %v\n%s", err, out)
		}
	})
	t.Run("ejected", func(t *testing.T) {
		cmd := exec.Command(logosTool, "eject", "-o", "out")
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("eject imported defaults: %v\n%s", err, out)
		}
		cmd = exec.Command(goTool, "run", ".")
		cmd.Dir = filepath.Join(dir, "out")
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil || string(out) != "ok exported bindings\n" {
			t.Fatalf("ejected imported defaults: %v\n%s", err, out)
		}
	})
}

func runStructDefaultLowerings(t *testing.T, src, want string) {
	t.Helper()
	data := []byte(src)
	base := syntax.NewFileBase("defaultbinding.lgo")
	file := ejectParse(t, base, data, syntax.LogosMode|syntax.CheckBranches)
	pkg, info := ejectCheck(t, file)
	edits := ejectLower(t, pkg, info, file, data)
	ejected, err := ApplyEdits(data, base, edits)
	if err != nil {
		t.Fatalf("eject field defaults: %v", err)
	}
	var native bytes.Buffer
	if _, err := syntax.Fprint(&native, file, 0); err != nil {
		t.Fatalf("print lowered field defaults: %v", err)
	}
	t.Run("native tree", func(t *testing.T) {
		runStructDefaultGo(t, native.Bytes(), want)
	})
	t.Run("ejected text", func(t *testing.T) {
		runStructDefaultGo(t, ejected, want)
	})
}

func runStructDefaultGo(t *testing.T, src []byte, want string) {
	t.Helper()
	testenv.MustHaveGoBuild(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module defaultbinding\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), src, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(testenv.GoToolPath(t), "run", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local", "GOWORK=off")
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != want {
		t.Fatalf("field default behavior: %v\n%s\nwant %s", err, out, want)
	}
}

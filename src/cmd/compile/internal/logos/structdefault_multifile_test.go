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
	"testing"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

func TestStructDefaultContainerAcrossFiles(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	// Keep the constructor file first: bindings must be available before the
	// file containing the anonymous element type reaches its lowering pass.
	inputs := []struct {
		name string
		src  string
	}{
		{"a", `package main

func main() {
	r := Rows{{}}
	if r[0].Value != 17 {
		panic("anonymous element default depends on file lowering order")
	}
	println("ok defaults across files")
}
`},
		{"b", `package main

const v = 17
type Rows []struct { Value int = v }
`},
	}
	var files []*syntax.File
	srcs := make(map[*syntax.File][]byte)
	for _, input := range inputs {
		data := []byte(input.src)
		file := ejectParse(t, syntax.NewFileBase(input.name+".lgo"), data, syntax.LogosMode|syntax.CheckBranches)
		files = append(files, file)
		srcs[file] = data
	}
	info := &types2.Info{
		Types:      make(map[syntax.Expr]types2.TypeAndValue),
		Instances:  make(map[*syntax.Name]types2.Instance),
		Defs:       make(map[*syntax.Name]types2.Object),
		Uses:       make(map[*syntax.Name]types2.Object),
		Implicits:  make(map[syntax.Node]types2.Object),
		Selections: make(map[*syntax.SelectorExpr]*types2.Selection),
		Scopes:     make(map[syntax.Node]*types2.Scope),
	}
	conf := types2.Config{Logos: true}
	pkg, err := conf.Check("defaultmultifile", files, info)
	if err != nil {
		t.Fatalf("check defaults across files: %v", err)
	}
	_, edits := Lower(pkg, info, files, srcs)
	for _, form := range []struct {
		name   string
		native bool
	}{
		{"native tree", true},
		{"ejected text", false},
	} {
		t.Run(form.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module defaultmultifile\n\ngo 1.27\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			for i, file := range files {
				var data []byte
				if form.native {
					var b bytes.Buffer
					if _, err := syntax.Fprint(&b, file, 0); err != nil {
						t.Fatalf("print lowered %s: %v", inputs[i].name, err)
					}
					data = b.Bytes()
				} else {
					var err error
					data, err = ApplyEdits(srcs[file], file.Pos().FileBase(), edits[file])
					if err != nil {
						t.Fatalf("eject %s: %v", inputs[i].name, err)
					}
				}
				if err := os.WriteFile(filepath.Join(dir, inputs[i].name+".go"), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(testenv.GoToolPath(t), "run", ".")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local", "GOWORK=off")
			out, err := cmd.CombinedOutput()
			const want = "ok defaults across files\n"
			if err != nil || string(out) != want {
				t.Fatalf("defaults across files: %v\n%s\nwant %s", err, out, want)
			}
		})
	}
}

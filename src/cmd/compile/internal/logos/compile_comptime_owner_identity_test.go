// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"go/ast"
	"go/parser"
	"go/token"
	"internal/testenv"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestCompileComptimeOwnerIdentity runs a program whose packages left and
// right instantiate owner.Adjust. The program checks at run time that one
// type argument list has one entry point and that it lies in package owner
// (SPEC 2.5). The ejected tree must run the same checks and print each
// instance once, in package owner.
func TestCompileComptimeOwnerIdentity(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExec(t)
	logosTool := wasmLogos(t)

	run := func(t *testing.T, dir, tool string, args ...string) {
		t.Helper()
		cmd := testenv.Command(t, tool, args...)
		cmd.Dir = dir
		cmd.Env = append(archDiffEnv(cmd, runtime.GOOS, runtime.GOARCH), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", tool, args, err, output)
		}
	}

	for _, mode := range []string{"native", "ejected"} {
		t.Run(mode, func(t *testing.T) {
			src := t.TempDir()
			if err := os.CopyFS(src, os.DirFS("testdata/comptime_owner_identity")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(src, "go.mod"), []byte("module example.com/owneridentity\n\ngo 1.27\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			dir, tool := src, logosTool
			if mode == "ejected" {
				dir = filepath.Join(t.TempDir(), "ejected")
				run(t, src, logosTool, "eject", "-o", dir)
				tool = testenv.GoToolPath(t)
				if got, want := adjustInstancesByPackage(t, dir), map[string]int{"owner": 2}; !maps.Equal(got, want) {
					t.Fatalf("ejected Adjust instances by package = %v, want %v", got, want)
				}
			}
			run(t, dir, tool, "run", "-p=1", "-buildvcs=false", ".")
		})
	}
}

// adjustInstancesByPackage counts, per directory of the ejected tree, the
// package-level functions without type parameters whose name begins with
// Adjust: the printed instances of owner.Adjust.
func adjustInstancesByPackage(t *testing.T, root string) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, filepath.Dir(path))
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Type.TypeParams == nil && strings.HasPrefix(fn.Name.Name, "Adjust") {
				counts[filepath.ToSlash(rel)]++
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

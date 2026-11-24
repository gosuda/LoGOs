// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"internal/testenv"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The support copy has one form. A build tag of the toolchain that
// still selects files in it lets a tag of the user remove a function
// that the copy needs.
func TestSupportCopyDoesNotDependOnToolchainBuildTags(t *testing.T) {
	saved := ejectGofmtFiles
	t.Cleanup(func() { ejectGofmtFiles = saved })
	src := filepath.Join(testenv.GOROOT(t), "src")
	for _, pkg := range []string{"rt", "sync"} {
		dst := t.TempDir()
		if err := copyEjectedSupport(src, "", dst, []string{"logos/" + pkg}); err != nil {
			t.Fatalf("logos/%s: %v", pkg, err)
		}
		rel, _ := supportRelPath("logos/" + pkg)
		own, err := filepath.Glob(filepath.Join(dst, filepath.FromSlash(rel), "*.go"))
		if err != nil || len(own) == 0 {
			t.Fatalf("logos/%s: the copy holds %d files, error %v", pkg, len(own), err)
		}
		var copied []string
		err = filepath.WalkDir(dst, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(path, ".go") {
				copied = append(copied, path)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range copied {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for line := range strings.SplitSeq(string(data), "\n") {
				if !strings.HasPrefix(line, "//go:build ") {
					continue
				}
				if strings.Contains(line, "logosconc") || strings.Contains(line, "logosejected") {
					t.Errorf("logos/%s: %s keeps the build line %q", pkg, file, line)
				}
			}
		}
	}
}

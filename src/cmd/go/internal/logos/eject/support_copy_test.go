// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"internal/testenv"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The support copy has one form. A build tag of the toolchain that
// still selects files in it lets a tag of the user remove a function
// that the copy needs.
func TestSupportCopyDoesNotDependOnToolchainBuildTags(t *testing.T) {
	for _, pkg := range []string{"rt", "sync"} {
		dst := t.TempDir()
		copySupportDir(filepath.Join(testenv.GOROOT(t), "src", "logos", pkg), dst)
		files, err := filepath.Glob(filepath.Join(dst, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("logos/%s: the copy holds %d files, error %v", pkg, len(files), err)
		}
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for line := range strings.SplitSeq(string(data), "\n") {
				if !strings.HasPrefix(line, "//go:build ") {
					continue
				}
				if strings.Contains(line, "logosconc") || strings.Contains(line, "logosejected") {
					t.Errorf("logos/%s/%s keeps the build line %q", pkg, filepath.Base(file), line)
				}
			}
		}
	}
}

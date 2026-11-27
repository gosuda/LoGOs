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
		if _, err := copyEjectedSupport(src, "", dst, []string{"logos/" + pkg}); err != nil {
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

// A source that already owns a companion stem must not collide with the
// JSONv2 companion the support copy emits: copyPackage reserves every
// output stem up front, so the companion takes the next free name.
func TestSupportCopyReservesCompanionStems(t *testing.T) {
	src := t.TempDir()
	staging := t.TempDir()
	out := t.TempDir()
	pkg := filepath.Join(src, "logos", "ejecttestcopy")
	if err := os.MkdirAll(pkg, 0777); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"item.lgo":        "package ejecttestcopy\n",
		"item_jsonv2.lgo": "package ejecttestcopy\n\nconst OwnedMarker = 1\n",
	} {
		if err := os.WriteFile(filepath.Join(pkg, name), []byte(source), 0666); err != nil {
			t.Fatal(err)
		}
	}
	staged := filepath.Join(staging, "logos", "ejecttestcopy")
	if err := os.MkdirAll(staged, 0777); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"item.go": `package ejecttestcopy

import _logos_jsontext "encoding/json/jsontext"

type Item struct{}

func (_logos_v Item) MarshalJSONTo(_logos_enc *_logos_jsontext.Encoder) error { return nil }
`,
		"item_jsonv2.go": "package ejecttestcopy\n\nconst OwnedMarker = 1\n",
	} {
		if err := os.WriteFile(filepath.Join(staged, name), []byte(source), 0666); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	c := supportCopy{
		src:          root,
		srcDir:       src,
		staging:      staging,
		dst:          out,
		destinations: map[string]string{},
		written:      map[string]string{},
	}
	p := supportSourcePackage{src: "logos/ejecttestcopy", dst: "rt/logos/ejecttestcopy"}
	if err := c.copyPackage(p); err != nil {
		t.Fatalf("copyPackage: %v", err)
	}
	for file, declaration := range map[string]string{
		"item.go":               "type Item struct{}",
		"item_jsonv2.go":        "OwnedMarker",
		"item_jsonv2_jsonv2.go": "MarshalJSONTo",
	} {
		data, err := os.ReadFile(filepath.Join(out, "rt", "logos", "ejecttestcopy", file))
		if err != nil {
			t.Error(err)
			continue
		}
		if !strings.Contains(string(data), declaration) {
			t.Errorf("%s lost %s:\n%s", file, declaration, data)
		}
	}
}

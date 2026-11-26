// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"cmd/go/internal/load"
)

func TestJSONv2CompanionPlatformBasenames(t *testing.T) {
	for _, stem := range []string{"linux", "amd64", "wasip1", "linux_test"} {
		want := stem + "_jsonv2"
		if stem == "linux_test" {
			want = "linux_jsonv2_test"
		}
		if got := jsonv2ExtraStem(stem); got != want {
			t.Errorf("companion for %q = %q, want %q", stem, got, want)
		}
	}
}

func TestJSONv2CompanionReservesLogicalSources(t *testing.T) {
	for _, inactive := range []bool{false, true} {
		name := "active"
		if inactive {
			name = "inactive"
		}
		t.Run(name, func(t *testing.T) {
			saved := ejectGofmtFiles
			t.Cleanup(func() { ejectGofmtFiles = saved })
			dir, staging, out := t.TempDir(), t.TempDir(), t.TempDir()
			pkg := &load.Package{PackagePublic: load.PackagePublic{
				Dir: dir, ImportPath: "example.com/p", Name: "p",
				GoFiles: []string{"data.lgo"},
			}}
			// The logical overlay source is in the emission plan but has no
			// physical directory entry, whether active or build-constrained.
			if inactive {
				pkg.IgnoredGoFiles = []string{"data_jsonv2.lgo"}
			} else {
				pkg.GoFiles = append(pkg.GoFiles, "data_jsonv2.lgo")
			}
			staged := filepath.Join(staging, filepath.FromSlash(pkg.ImportPath))
			if err := os.MkdirAll(staged, 0777); err != nil {
				t.Fatal(err)
			}
			for file, source := range map[string]string{
				"data.go": `package p

import _logos_jsontext "encoding/json/jsontext"

type Data struct{}

func (_logos_v Data) MarshalJSONTo(_logos_enc *_logos_jsontext.Encoder) error { return nil }
`,
				"data_jsonv2.go": "package p\n\nconst OverlayMarker = 7\n",
			} {
				if err := os.WriteFile(filepath.Join(staged, file), []byte(source), 0666); err != nil {
					t.Fatal(err)
				}
			}
			emitPackageGoFiles(pkg, &ejectPlan{}, staging, out, dir)
			for file, declaration := range map[string]string{
				"data_jsonv2.go":        "OverlayMarker",
				"data_jsonv2_jsonv2.go": "MarshalJSONTo",
			} {
				data, err := os.ReadFile(filepath.Join(out, file))
				if err != nil {
					t.Error(err)
					continue
				}
				if !bytes.Contains(data, []byte(declaration)) {
					t.Errorf("%s lost %s:\n%s", file, declaration, data)
				}
			}
		})
	}
}

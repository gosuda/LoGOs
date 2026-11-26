// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"encoding/json"
	"internal/testenv"
	"os"
	"path/filepath"
	"testing"

	"cmd/go/internal/fsys"
)

func TestSupportCopyOverlayAssets(t *testing.T) {
	// The effective filesystem holds one immutable overlay per process.
	if os.Getenv("LOGOS_TEST_SUPPORT_ASSET_OVERLAY") != "1" {
		cmd := testenv.Command(t, os.Args[0], "-test.run=^TestSupportCopyOverlayAssets$")
		cmd.Env = append(os.Environ(), "LOGOS_TEST_SUPPORT_ASSET_OVERLAY=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("overlay support copy: %v\n%s", err, output)
		}
		return
	}
	src, backing := t.TempDir(), t.TempDir()
	for name, contents := range map[string]string{
		"logos/p/p.go":                  "package p\n\nimport _ \"example.com/v\"\nimport _ \"embed\"\n\n//go:embed replaced.h\nvar Header string\n",
		"logos/inserted/inserted.go":    "package inserted\n\nimport _ \"embed\"\n\n//go:embed inserted.h\nvar Header string\n",
		"logos/p/replaced.h":            "physical header\n",
		"logos/p/untouched.h":           "untouched header\n",
		"vendor/example.com/v/v.go":     "package v\n",
		"vendor/example.com/v/vendor.h": "physical vendor header\n",
	} {
		file := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0777); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(contents), 0666); err != nil {
			t.Fatal(err)
		}
	}
	replacements := map[string]string{}
	for name, contents := range map[string]string{
		"logos/p/inserted.h":            "inserted header\n",
		"logos/inserted/inserted.h":     "inserted header\n",
		"logos/p/replaced.h":            "replacement header\n",
		"vendor/example.com/v/vendor.h": "overlay vendor header\n",
	} {
		file := filepath.Join(backing, filepath.Base(name))
		if err := os.WriteFile(file, []byte(contents), 0666); err != nil {
			t.Fatal(err)
		}
		replacements[filepath.Join(src, filepath.FromSlash(name))] = file
	}
	data, err := json.Marshal(struct{ Replace map[string]string }{replacements})
	if err != nil {
		t.Fatal(err)
	}
	fsys.OverlayFile = filepath.Join(backing, "overlay.json")
	if err := os.WriteFile(fsys.OverlayFile, data, 0666); err != nil {
		t.Fatal(err)
	}
	if err := fsys.Init(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want map[string]string
	}{
		{"logos/p", map[string]string{
			"logos/p/inserted.h":                              "inserted header\n",
			"logos/p/replaced.h":                              "replacement header\n",
			"logos/p/untouched.h":                             "untouched header\n",
			"internal/toolchainvendor/example.com/v/vendor.h": "physical vendor header\n",
		}},
		{"logos/inserted", map[string]string{"logos/inserted/inserted.h": "inserted header\n"}},
	} {
		t.Run(tc.path, func(t *testing.T) {
			out := t.TempDir()
			if err := copyEjectedSupport(src, "", out, []string{tc.path}); err != nil {
				t.Fatal(err)
			}
			for name, want := range tc.want {
				data, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(name)))
				if err != nil {
					t.Error(err)
				} else if string(data) != want {
					t.Errorf("%s = %q, want %q", name, data, want)
				}
			}
		})
	}
}

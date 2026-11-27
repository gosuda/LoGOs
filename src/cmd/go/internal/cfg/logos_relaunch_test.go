// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cfg

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o666); err != nil {
		t.Fatal(err)
	}
}

func TestDirHoldsOnlyLogosSources(t *testing.T) {
	root := t.TempDir()
	cases := map[string]struct {
		files map[string]string
		want  bool
	}{
		"empty":   {nil, false},
		"go only": {map[string]string{"p.go": "package p\n"}, false},
		"logos only": {
			map[string]string{"p.lgo": "package p\n"},
			true,
		},
		"alias only": {
			map[string]string{"p.⚡": "package p\n"},
			true,
		},
		"mixed": {
			map[string]string{"p.go": "package p\n", "q.lgo": "package q\n"},
			false,
		},
		"logos and text": {
			map[string]string{"p.lgo": "package p\n", "note.txt": "hi\n"},
			true,
		},
		"logos in subdir only": {
			map[string]string{"sub/p.lgo": "package p\n"},
			false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := filepath.Join(root, name)
			if err := os.MkdirAll(d, 0o777); err != nil {
				t.Fatal(err)
			}
			for f, c := range tc.files {
				writeFile(t, filepath.Join(d, f), c)
			}
			if got := DirHoldsOnlyLogosSources(d); got != tc.want {
				t.Errorf("DirHoldsOnlyLogosSources(%q) = %v, want %v", d, got, tc.want)
			}
		})
	}
	if got := DirHoldsOnlyLogosSources(filepath.Join(root, "missing")); got {
		t.Errorf("DirHoldsOnlyLogosSources(missing) = true, want false")
	}
}

func TestUnderGoRoot(t *testing.T) {
	if GOROOTsrc == "" {
		t.Skip("GOROOTsrc is empty")
	}
	if !UnderGoRoot(GOROOTsrc) {
		t.Errorf("UnderGoRoot(GOROOTsrc) = false, want true")
	}
	if !UnderGoRoot(filepath.Join(GOROOTsrc, "logos", "rt")) {
		t.Errorf("UnderGoRoot($GOROOT/src/logos/rt) = false, want true")
	}
	if UnderGoRoot(t.TempDir()) {
		t.Errorf("UnderGoRoot(tempdir) = true, want false")
	}
}

func TestMainModuleMarkedByLogos(t *testing.T) {
	t.Setenv("GOWORK", "off")

	t.Run("marked module", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.27\n")
		writeFile(t, filepath.Join(root, "logos.toml"), "[project]\n")
		sub := filepath.Join(root, "sub", "deep")
		if err := os.MkdirAll(sub, 0o777); err != nil {
			t.Fatal(err)
		}
		t.Chdir(sub)
		if !MainModuleMarkedByLogos() {
			t.Errorf("MainModuleMarkedByLogos() = false in a marked module, want true")
		}
	})

	t.Run("unmarked module", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "go.mod"), "module example.com/m\n\ngo 1.27\n")
		t.Chdir(root)
		if MainModuleMarkedByLogos() {
			t.Errorf("MainModuleMarkedByLogos() = true without logos.toml, want false")
		}
	})

	t.Run("no module", func(t *testing.T) {
		t.Chdir(t.TempDir())
		if MainModuleMarkedByLogos() {
			t.Errorf("MainModuleMarkedByLogos() = true outside a module, want false")
		}
	})
}

func TestMainModuleMarkedByLogosWorkspace(t *testing.T) {
	setup := func(t *testing.T, marked bool) string {
		t.Helper()
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "go.work"), "go 1.27\n\nuse (\n\t./g\n\t./l\n)\n")
		writeFile(t, filepath.Join(root, "g", "go.mod"), "module example.com/g\n\ngo 1.27\n")
		writeFile(t, filepath.Join(root, "l", "go.mod"), "module example.com/l\n\ngo 1.27\n")
		if marked {
			writeFile(t, filepath.Join(root, "l", "logos.toml"), "[project]\n")
		}
		return root
	}

	t.Run("marked member", func(t *testing.T) {
		root := setup(t, true)
		t.Setenv("GOWORK", filepath.Join(root, "go.work"))
		t.Chdir(filepath.Join(root, "g"))
		if !MainModuleMarkedByLogos() {
			t.Errorf("MainModuleMarkedByLogos() = false with a marked workspace member, want true")
		}
	})

	t.Run("no marked member", func(t *testing.T) {
		root := setup(t, false)
		t.Setenv("GOWORK", filepath.Join(root, "go.work"))
		t.Chdir(filepath.Join(root, "g"))
		if MainModuleMarkedByLogos() {
			t.Errorf("MainModuleMarkedByLogos() = true with no marked member, want false")
		}
	})

	t.Run("walk finds work file", func(t *testing.T) {
		root := setup(t, true)
		t.Setenv("GOWORK", "")
		t.Chdir(filepath.Join(root, "g"))
		if !MainModuleMarkedByLogos() {
			t.Errorf("MainModuleMarkedByLogos() = false walking up to a marking go.work, want true")
		}
	})
}

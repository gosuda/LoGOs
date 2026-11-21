// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package settings

import (
	"io/fs"
	"os"
	"syscall"
	"testing"
)

func TestCensusFatal(t *testing.T) {
	pathErr := func(errno syscall.Errno) error {
		return &fs.PathError{Op: "open", Path: "pkg", Err: errno}
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"outOfMemory", pathErr(syscall.ENOMEM), true},
		{"outOfFileDescriptors", pathErr(syscall.EMFILE), true},
		{"outOfSystemFileTable", pathErr(syscall.ENFILE), true},
		{"ioError", pathErr(syscall.EIO), true},
		{"permissionDenied", pathErr(syscall.EACCES), false},
		{"vanishedEntry", pathErr(syscall.ENOENT), false},
		{"otherError", pathErr(syscall.EINVAL), false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := censusFatal(tt.err); got != tt.want {
				t.Errorf("censusFatal(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// faultFS fails Open on configured paths, simulating transient filesystem
// failures the walk cannot distinguish from real ones.
type faultFS struct {
	fsys fs.FS
	fail map[string]error
}

func (f faultFS) Open(name string) (fs.File, error) {
	if err, ok := f.fail[name]; ok {
		return nil, err
	}
	return f.fsys.Open(name)
}

func faultErr(op, path string, errno syscall.Errno) error {
	return &fs.PathError{Op: op, Path: path, Err: errno}
}

func TestTreeHasLogosFS(t *testing.T) {
	root := scanTree(t, map[string]string{
		"pkg/x.lgo":  "package p\n",
		"other/y.go": "package o\n",
	}, nil)
	if found, err := treeHasLogosFS(os.DirFS(root)); !found || err != nil {
		t.Errorf("treeHasLogosFS = (%v, %v), want (true, nil)", found, err)
	}

	empty := scanTree(t, map[string]string{"y.go": "package o\n"}, nil)
	if found, err := treeHasLogosFS(os.DirFS(empty)); found || err != nil {
		t.Errorf("treeHasLogosFS = (%v, %v), want (false, nil)", found, err)
	}
}

func TestTreeHasLogosFSFaulty(t *testing.T) {
	root := scanTree(t, map[string]string{
		"pkg/x.lgo": "package p\n",
	}, nil)

	t.Run("resourceErrorFails", func(t *testing.T) {
		fsys := faultFS{
			fsys: os.DirFS(root),
			fail: map[string]error{
				"pkg": faultErr("open", "pkg", syscall.ENOMEM),
			},
		}
		found, err := treeHasLogosFS(fsys)
		if found {
			t.Error("treeHasLogosFS = true, want false (the walk missed the only .lgo file)")
		}
		if err == nil {
			t.Fatal("treeHasLogosFS err = nil, want the injected resource failure")
		}
		if !censusFatal(err) {
			t.Errorf("treeHasLogosFS err = %v, want a fatal census error", err)
		}
	})

	t.Run("routineErrorSkips", func(t *testing.T) {
		fsys := faultFS{
			fsys: os.DirFS(root),
			fail: map[string]error{
				"pkg": faultErr("open", "pkg", syscall.EACCES),
			},
		}
		found, err := treeHasLogosFS(fsys)
		if found {
			t.Error("treeHasLogosFS = true, want false (the subtree was skipped)")
		}
		if err != nil {
			t.Errorf("treeHasLogosFS err = %v, want nil (routine errors skip)", err)
		}
	})

	t.Run("foundBeatsFault", func(t *testing.T) {
		late := scanTree(t, map[string]string{
			"gone/z.go": "package g\n",
			"pkg/x.lgo": "package p\n",
		}, nil)
		fsys := faultFS{
			fsys: os.DirFS(late),
			fail: map[string]error{
				"gone": faultErr("open", "gone", syscall.EIO),
			},
		}
		found, err := treeHasLogosFS(fsys)
		if !found || err != nil {
			t.Errorf("treeHasLogosFS = (%v, %v), want (true, nil): a found answer is already correct", found, err)
		}
	})

	t.Run("invalidPathFails", func(t *testing.T) {
		fsys := faultFS{
			fsys: os.DirFS(root),
			fail: map[string]error{
				"pkg": &fs.PathError{Op: "open", Path: "pkg", Err: fs.ErrInvalid},
			},
		}
		found, err := treeHasLogosFS(fsys)
		if found || err == nil {
			t.Errorf("treeHasLogosFS = (%v, %v), want (false, err): a non-UTF-8 name can hide .lgo files", found, err)
		}
	})

	t.Run("nestedModuleProbeFaultFails", func(t *testing.T) {
		nested := scanTree(t, map[string]string{
			"inner/go.mod": "module example.com/inner\n",
			"inner/x.lgo":  "package i\n",
		}, nil)
		fsys := statFaultFS{
			fsys: faultFS{fsys: os.DirFS(nested), fail: map[string]error{}},
			fail: map[string]error{
				"inner/go.mod": faultErr("stat", "inner/go.mod", syscall.EIO),
			},
		}
		found, err := treeHasLogosFS(fsys)
		if found || err == nil {
			t.Errorf("treeHasLogosFS = (%v, %v), want (false, err): the go.mod probe can hide a nested module", found, err)
		}
	})
}

// statFaultFS fails Stat calls on configured paths.
type statFaultFS struct {
	fsys fs.FS
	fail map[string]error
}

func (f statFaultFS) Open(name string) (fs.File, error) {
	return f.fsys.Open(name)
}

func (f statFaultFS) Stat(name string) (fs.FileInfo, error) {
	if err, ok := f.fail[name]; ok {
		return nil, err
	}
	return fs.Stat(f.fsys, name)
}

func TestCensusErrFaulty(t *testing.T) {
	root := scanTree(t, map[string]string{"a.go": "package p\n"}, nil)
	old := censusTreeWalk
	censusTreeWalk = func(string) (bool, error) {
		return false, faultErr("open", "d", syscall.EIO)
	}
	defer func() { censusTreeWalk = old }()
	m := &Module{Root: root}
	if err := m.CensusErr(); err == nil {
		t.Fatal("CensusErr() = nil, want the injected census failure")
	} else if !censusFatal(err) {
		t.Errorf("CensusErr() = %v, want a fatal census error", err)
	}
}

func TestCensusErrClean(t *testing.T) {
	root := scanTree(t, map[string]string{"a.lgo": "package p\n"}, nil)
	m := ModuleAt(root)
	if err := m.CensusErr(); err != nil {
		t.Errorf("CensusErr() = %v, want nil on a clean walk", err)
	}
	if !m.Logos() {
		t.Error("Logos() = false, want true")
	}

	empty := scanTree(t, map[string]string{"a.go": "package p\n"}, nil)
	m = ModuleAt(empty)
	if err := m.CensusErr(); err != nil {
		t.Errorf("CensusErr() = %v, want nil when no .lgo file exists", err)
	}
	if m.Logos() {
		t.Error("Logos() = true, want false")
	}
}

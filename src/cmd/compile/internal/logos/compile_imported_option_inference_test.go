// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"internal/testenv"
	"os"
	"path/filepath"
	"testing"
)

func TestCompileImportedOptionInference(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	testenv.MustHaveExec(t)
	logosTool := wasmLogos(t)
	goTool := testenv.GoToolPath(t)
	src := t.TempDir()
	files := map[string]string{
		"go.mod": `module example.com/optioncapture

go 1.27.4
`,
		"pool/pool.lgo": `package pool

type Pool struct{}

func Open() (*Pool?, error) {
	return &Pool{}, nil
}

func Maybe(present bool) *Pool? {
	if present {
		return &Pool{}
	}
	return nil
}

func (p *Pool) Stop() error {
	return nil
}

func Count(present bool) int? {
	if present {
		return 42
	}
	return nil
}

func IsFortyTwo(v int?) bool {
	return (v ?? 0) == 42
}

func IsAbsent(v int?) bool {
	return v == nil
}

func Nested(state int) (int?)? {
	if state == 0 {
		return nil
	}
	var inner int?
	if state == 2 {
		inner = 42
	}
	return inner
}

func NestedValue(v (int?)?) int {
	if v == nil {
		return 0
	}
	var none int?
	inner := v ?? none
	if inner == nil {
		return 1
	}
	return inner ?? -1
}

func Cells() [](int?) {
	return [](int?){nil, 42}
}

func SumCells(cells [](int?)) int {
	return (cells[0] ?? 0) + (cells[1] ?? 0)
}

func (p *Pool) Optional(present bool) int? {
	return Count(present)
}

func VerifyMethod(value any) bool {
	f, ok := value.(func(bool) int?)
	if !ok {
		return false
	}
	return IsAbsent(f(false)) && IsFortyTwo(f(true))
}
`,
		"direct/direct_test.lgo": `package direct_test

import (
	"testing"

	"example.com/optioncapture/pool"
)

func TestDirect(t *testing.T) {
	p, err := pool.Open()
	if err != nil {
		t.Error(err)
		return
	}
	if p == nil {
		t.Error("pool is nil")
		return
	}
	if err := p.Stop(); err != nil {
		t.Error(err)
	}
}
`,
		"bare/bare_test.lgo": `package bare_test

import (
	"testing"

	"example.com/optioncapture/pool"
)

func TestBare(t *testing.T) {
	p := pool.Maybe(false)
	if p != nil {
		t.Fatal("absent pointer became present")
	}
	p = pool.Maybe(true)
	if p == nil {
		t.Fatal("present pointer became absent")
		return
	}
	if err := p.Stop(); err != nil {
		t.Error(err)
	}
}
`,
		"capture/capture_test.lgo": `package capture_test

import (
	"testing"

	"example.com/optioncapture/pool"
)

func TestCapture(t *testing.T) {
	p, err := pool.Open()
	if err != nil {
		t.Error(err)
		return
	}
	if p == nil {
		t.Error("pool is nil")
		return
	}
	defer func() {
		if err := p.Stop(); err != nil {
			t.Error(err)
		}
	}()
}
`,
		"local/local_test.lgo": `package local_test

import (
	"testing"

	"example.com/optioncapture/pool"
)

func open() (*pool.Pool?, error) {
	return &pool.Pool{}, nil
}

func TestLocal(t *testing.T) {
	p, err := open()
	if err != nil {
		t.Error(err)
		return
	}
	if p == nil {
		t.Error("pool is nil")
		return
	}
	defer func() {
		if err := p.Stop(); err != nil {
			t.Error(err)
		}
	}()
}
`,
		"nonnull/nonnull_test.lgo": `package nonnull_test

import (
	"testing"

	"example.com/optioncapture/pool"
)

func TestNonNull(t *testing.T) {
	p := &pool.Pool{}
	defer func() {
		if err := p.Stop(); err != nil {
			t.Error(err)
		}
	}()
}
`,
		"value/value_test.lgo": `package value_test

import (
	"testing"

	"example.com/optioncapture/pool"
)

func TestValue(t *testing.T) {
	v := pool.Count(true)
	if !pool.IsFortyTwo(v) {
		t.Error("present value changed")
	}
	v = pool.Count(false)
	if !pool.IsAbsent(v) {
		t.Error("absent value changed")
	}
}
`,
		"nested/nested_test.lgo": `package nested_test

import (
	"testing"

	"example.com/optioncapture/pool"
)

func TestNested(t *testing.T) {
	v := pool.Nested(0)
	if got := pool.NestedValue(v); got != 0 {
		t.Errorf("absent outer: got %d, want 0", got)
	}
	v = pool.Nested(1)
	if got := pool.NestedValue(v); got != 1 {
		t.Errorf("absent inner: got %d, want 1", got)
	}
	v = pool.Nested(2)
	if got := pool.NestedValue(v); got != 42 {
		t.Errorf("present inner: got %d, want 42", got)
	}
}
`,
		"signature/signature_test.lgo": `package signature_test

import (
	"testing"

	"example.com/optioncapture/pool"
)

func TestSignature(t *testing.T) {
	f := pool.Count
	if !pool.IsAbsent(f(false)) || !pool.IsFortyTwo(f(true)) {
		t.Error("function value changed option results")
	}
}
`,
		"container/container_test.lgo": `package container_test

import (
	"testing"

	"example.com/optioncapture/pool"
)

func TestContainer(t *testing.T) {
	values := pool.Cells()
	if got := pool.SumCells(values); got != 42 {
		t.Errorf("sum: got %d, want 42", got)
	}
}
`,
		"method/method_test.lgo": `package method_test

import (
	"testing"

	"example.com/optioncapture/pool"
)

func TestMethod(t *testing.T) {
	p := &pool.Pool{}
	if !pool.VerifyMethod(p.Optional) {
		t.Error("method value changed option results")
	}
}
`,
	}
	for name, text := range files {
		path := filepath.Join(src, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(t *testing.T, dir, tool string, args ...string) {
		t.Helper()
		cmd := testenv.Command(t, tool, args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(), "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOWORK=off")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %v: %v\n%s", tool, args, err, out)
		}
	}
	t.Run("native", func(t *testing.T) {
		run(t, src, logosTool, "test", "-buildvcs=false", "-mod=readonly", "-p=1", "-count=1", "./...")
	})
	t.Run("ejected", func(t *testing.T) {
		out := filepath.Join(t.TempDir(), "ejected")
		run(t, src, logosTool, "eject", "-o", out)
		run(t, out, goTool, "test", "-buildvcs=false", "-mod=readonly", "-p=1", "-count=1", "./...")
	})
}

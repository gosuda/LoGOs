// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"strconv"
	"strings"
	"testing"
)

func lowerArrayOp(t *testing.T, pre, arr, op string) string {
	t.Helper()
	src := "package main\n\n" + pre + "func f(a, b " + arr + ") " + arr + " {\n\treturn a " + op + " b\n}\n"
	return lowerSnippetSrc(t, "arraysimd.lgo", src)
}

func TestSimdArrayVectorScalarForms(t *testing.T) {
	n := strconv.FormatInt(4, 10)
	t.Run("fixed width takes vector form", func(t *testing.T) {
		simdSliceTarget(t, "amd64", 1)
		out := lowerArrayOp(t, "", "["+n+"]float32", "+")
		if !strings.Contains(out, "archsimd") {
			t.Errorf("float32 array misses the vector form:\n%s", out)
		}
	})
	t.Run("alias takes vector form", func(t *testing.T) {
		simdSliceTarget(t, "amd64", 1)
		out := lowerArrayOp(t, "type f32 = float32\n\n", "["+n+"]f32", "+")
		if !strings.Contains(out, "archsimd") {
			t.Errorf("alias array misses the vector form:\n%s", out)
		}
	})
	t.Run("byte takes vector form amd64", func(t *testing.T) {
		simdSliceTarget(t, "amd64", 1)
		out := lowerArrayOp(t, "", "[16]byte", "+")
		if !strings.Contains(out, "archsimd") {
			t.Errorf("byte array misses the vector form:\n%s", out)
		}
	})
	t.Run("byte takes vector form arm64", func(t *testing.T) {
		simdSliceTarget(t, "arm64", 1)
		out := lowerArrayOp(t, "", "[16]byte", "+")
		if !strings.Contains(out, "archsimd") {
			t.Errorf("byte array misses the vector form on arm64:\n%s", out)
		}
		if strings.Contains(out, "X86") {
			t.Errorf("arm64 byte array carries an X86 guard:\n%s", out)
		}
	})
	t.Run("rune takes vector form amd64", func(t *testing.T) {
		simdSliceTarget(t, "amd64", 1)
		out := lowerArrayOp(t, "", "[8]rune", "+")
		if !strings.Contains(out, "archsimd") {
			t.Errorf("rune array misses the vector form:\n%s", out)
		}
	})
	t.Run("rune takes vector form arm64", func(t *testing.T) {
		simdSliceTarget(t, "arm64", 1)
		out := lowerArrayOp(t, "", "[8]rune", "+")
		if !strings.Contains(out, "archsimd") {
			t.Errorf("rune array misses the vector form on arm64:\n%s", out)
		}
		if strings.Contains(out, "X86") {
			t.Errorf("arm64 rune array carries an X86 guard:\n%s", out)
		}
	})
	t.Run("int stays scalar", func(t *testing.T) {
		simdSliceTarget(t, "amd64", 1)
		out := lowerArrayOp(t, "", "["+n+"]int", "+")
		if strings.Contains(out, "archsimd") || strings.Contains(out, "_logos_simd") {
			t.Errorf("int array takes the vector form:\n%s", out)
		}
	})
	t.Run("uint stays scalar", func(t *testing.T) {
		simdSliceTarget(t, "amd64", 1)
		out := lowerArrayOp(t, "", "[4]uint", "+")
		if strings.Contains(out, "archsimd") || strings.Contains(out, "_logos_simd") {
			t.Errorf("uint array takes the vector form:\n%s", out)
		}
	})
	t.Run("uintptr stays scalar", func(t *testing.T) {
		simdSliceTarget(t, "amd64", 1)
		out := lowerArrayOp(t, "", "[2]uintptr", "+")
		if strings.Contains(out, "archsimd") || strings.Contains(out, "_logos_simd") {
			t.Errorf("uintptr array takes the vector form:\n%s", out)
		}
	})
	t.Run("defined type stays scalar", func(t *testing.T) {
		simdSliceTarget(t, "amd64", 1)
		out := lowerArrayOp(t, "type I int32\n\n", "["+n+"]I", "+")
		if strings.Contains(out, "archsimd") || strings.Contains(out, "_logos_simd") {
			t.Errorf("defined element type takes the vector form:\n%s", out)
		}
	})
	t.Run("no experiment scalar", func(t *testing.T) {
		scalarSliceTarget(t)
		out := lowerArrayOp(t, "", "["+n+"]float32", "+")
		if strings.Contains(out, "archsimd") || strings.Contains(out, "_logos_simd") {
			t.Errorf("experiment off takes the vector form:\n%s", out)
		}
		if !strings.Contains(out, "_logos_a[0] + _logos_b[0]") {
			t.Errorf("experiment off misses unrolled scalar code:\n%s", out)
		}
	})
}

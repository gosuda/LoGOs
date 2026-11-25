// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package json

import "testing"

func TestByteSliceOf(t *testing.T) {
	type namedBytes []byte
	type namedByte uint8

	checkByteSliceOf(t, []byte{'a', 'b', 'c'}[:2])
	checkByteSliceOf(t, namedBytes{'a', 'b', 'c'}[:2])
	checkByteSliceOf(t, []namedByte{'a', 'b', 'c'}[:2])

	if got, ok := ByteSliceOf(namedBytes(nil)); got != nil || !ok {
		t.Errorf("ByteSliceOf(nil named slice) = %v, %v; want nil, true", got, ok)
	}
}

func checkByteSliceOf[S ~[]E, E ~uint8](t *testing.T, src S) {
	t.Helper()
	got, ok := ByteSliceOf(src)
	if !ok || string(got) != "ab" || cap(got) != cap(src) {
		t.Fatalf("ByteSliceOf(%T) = %q (cap %d), %v; want %q (cap %d), true", src, got, cap(got), ok, "ab", cap(src))
	}
	got[0] = 'z'
	if src[0] != 'z' {
		t.Errorf("ByteSliceOf(%T) did not share the backing array", src)
	}
}

func TestByteSliceOfRejectsWithoutAllocating(t *testing.T) {
	for _, v := range []any{nil, []int8{1}, "abc", [1]byte{1}} {
		var got []byte
		var ok bool
		allocs := testing.AllocsPerRun(20, func() {
			got, ok = ByteSliceOf(v)
		})
		if got != nil || ok {
			t.Errorf("ByteSliceOf(%T) = %v, %v; want nil, false", v, got, ok)
		}
		if allocs != 0 {
			t.Errorf("ByteSliceOf(%T) allocated %v times; want zero", v, allocs)
		}
	}
}

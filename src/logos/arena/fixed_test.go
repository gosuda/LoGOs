// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package arena_test

import (
	"fmt"
	"logos/arena"
	"slices"
	"testing"
	"unsafe"
)

var (
	fixedSinkU *uint64
	fixedSinkB []byte
	fixedSinkI int
)

func fixedPanicText(t *testing.T, f func()) string {
	t.Helper()
	var msg string
	func() {
		defer func() {
			r := recover()
			if r == nil {
				t.Fatalf("expected a panic, got none")
			}
			msg = fmt.Sprint(r)
		}()
		f()
	}()
	return msg
}

func fixedBuffer(t *testing.T, size, align int) []byte {
	t.Helper()
	raw := make([]byte, size+align)
	base := uintptr(unsafe.Pointer(&raw[0]))
	off := (align - int(base%uintptr(align))) % align
	return raw[off : off+size]
}

func fixedMisaligned(t *testing.T, size int) []byte {
	t.Helper()
	raw := make([]byte, size+8)
	base := uintptr(unsafe.Pointer(&raw[0]))
	off := 0
	if base%8 == 0 {
		off = 1
	}
	return raw[off : off+size]
}

func TestFixedOpenUsed(t *testing.T) {
	if got := arena.FixedUsed(arena.Open()); got != 0 {
		t.Errorf("FixedUsed(heap) = %d, want 0", got)
	}
	r := arena.OpenFixed(make([]byte, 32))
	if got := arena.FixedUsed(r); got != 0 {
		t.Fatalf("FixedUsed fresh = %d, want 0", got)
	}
	s := arena.Make[byte](r, 7)
	if len(s) != 7 {
		t.Fatalf("Make[byte](r, 7) has len %d, want 7", len(s))
	}
	if got := arena.FixedUsed(r); got != 7 {
		t.Errorf("FixedUsed after 7 bytes = %d, want 7", got)
	}
	if p := arena.New[[8]byte](r); p == nil {
		t.Fatalf("New[[8]byte] returned nil")
	}
	if got := arena.FixedUsed(r); got != 15 {
		t.Errorf("FixedUsed after 7 + 8 bytes = %d, want 15", got)
	}
}

func TestFixedNewZeroed(t *testing.T) {
	buf := fixedBuffer(t, 16, 8)
	for i := range buf {
		buf[i] = 0xFF
	}
	r := arena.OpenFixed(buf)
	p := arena.New[uint64](r)
	if *p != 0 {
		t.Errorf("*p = %#x, want zeroed", *p)
	}
	q := arena.New[uint64](r)
	if p == q {
		t.Errorf("two New calls share one pointer %p", p)
	}
	if *q != 0 {
		t.Errorf("*q = %#x, want zeroed", *q)
	}
}

func TestFixedExhausted(t *testing.T) {
	t.Run("make", func(t *testing.T) {
		r := arena.OpenFixed(make([]byte, 10))
		_ = arena.Make[byte](r, 7)
		msg := fixedPanicText(t, func() { _ = arena.Make[byte](r, 4) })
		want := "arena: fixed region exhausted (want 4, have 3)"
		if msg != want {
			t.Errorf("got %q, want %q", msg, want)
		}
	})
	t.Run("new", func(t *testing.T) {
		r := arena.OpenFixed(make([]byte, 10))
		_ = arena.New[[8]byte](r)
		msg := fixedPanicText(t, func() { _ = arena.New[[8]byte](r) })
		want := "arena: fixed region exhausted (want 8, have 2)"
		if msg != want {
			t.Errorf("got %q, want %q", msg, want)
		}
	})
	t.Run("append", func(t *testing.T) {
		r := arena.OpenFixed(make([]byte, 10))
		s := arena.Make[byte](r, 6)
		msg := fixedPanicText(t, func() { _ = arena.Append(r, s, 1, 2, 3, 4, 5) })
		want := "arena: fixed region exhausted (want 11, have 4)"
		if msg != want {
			t.Errorf("got %q, want %q", msg, want)
		}
	})
	t.Run("empty", func(t *testing.T) {
		r := arena.OpenFixed(nil)
		msg := fixedPanicText(t, func() { _ = arena.Make[byte](r, 1) })
		want := "arena: fixed region exhausted (want 1, have 0)"
		if msg != want {
			t.Errorf("got %q, want %q", msg, want)
		}
	})
}

func TestFixedResetOverwrites(t *testing.T) {
	const X = 0xeeeeeeeeeeeeeeee
	const Y uint64 = 0xffffffffffffffff
	buf := fixedBuffer(t, 8, 8)
	r1 := arena.OpenFixed(buf)
	x := arena.New[uint64](r1)
	*x = X
	msg := fixedPanicText(t, func() { _ = arena.New[uint64](r1) })
	want := "arena: fixed region exhausted (want 8, have 0)"
	if msg != want {
		t.Fatalf("got %q, want %q", msg, want)
	}
	r2 := arena.OpenFixed(buf)
	y := arena.New[uint64](r2)
	*y = Y
	if *x != Y {
		t.Errorf("*x = %#x, want %#x: fresh handle did not reuse the buffer", *x, Y)
	}
	if *y != Y {
		t.Errorf("*y = %#x, want %#x", *y, Y)
	}
}

func TestFixedReallocReuse(t *testing.T) {
	r := arena.OpenFixed(make([]byte, 32))
	s0 := arena.Make[byte](r, 5)
	for i := range s0 {
		s0[i] = byte(i + 1)
	}
	s1 := arena.Append(r, s0, 6, 7, 8, 9, 10)
	if len(s1) != 10 {
		t.Fatalf("Append grew len to %d, want 10", len(s1))
	}
	if &s1[0] != &s0[0] {
		t.Errorf("last-block growth moved the backing array")
	}
	if got := arena.FixedUsed(r); got != 10 {
		t.Errorf("FixedUsed = %d, want 10", got)
	}
	if want := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}; !slices.Equal(s1, want) {
		t.Errorf("Append result = %v, want %v", s1, want)
	}
	a := arena.Make[byte](r, 2)
	a[0], a[1] = 0xA0, 0xA1
	b := arena.Append(r, s1, 99)
	if len(b) != 11 || b[10] != 99 {
		t.Fatalf("Append copy = %v, want ten bytes plus 99", b)
	}
	if !slices.Equal(b[:10], s1) {
		t.Errorf("Append copy lost prefix bytes: %v", b[:10])
	}
	if &b[0] == &s1[0] {
		t.Errorf("non-last growth did not copy to a fresh segment")
	}
	if got := arena.FixedUsed(r); got != 23 {
		t.Errorf("FixedUsed = %d, want 23", got)
	}
	extra := make([]byte, 10)
	msg := fixedPanicText(t, func() { _ = arena.Append(r, b, extra...) })
	want := "arena: fixed region exhausted (want 21, have 9)"
	if msg != want {
		t.Errorf("got %q, want %q", msg, want)
	}
}

func TestFixedAppendSpare(t *testing.T) {
	r := arena.OpenFixed(make([]byte, 32))
	full := arena.Make[byte](r, 8)
	s := full[:4]
	used := arena.FixedUsed(r)
	got := arena.Append(r, s, 1, 2)
	if &got[0] != &full[0] {
		t.Errorf("Append with spare capacity moved the backing array")
	}
	if want := []byte{0, 0, 0, 0, 1, 2}; !slices.Equal(got, want) {
		t.Errorf("Append(r, s, 1, 2) = %v, want %v", got, want)
	}
	if now := arena.FixedUsed(r); now != used {
		t.Errorf("FixedUsed moved from %d to %d on a spare-capacity append", used, now)
	}
	if none := arena.Append[byte](r, nil); none != nil {
		t.Errorf("Append(r, nil) = %v, want nil", none)
	}
}

func TestFixedAppendAdjacentInput(t *testing.T) {
	buf := []byte{7, 8, 0, 0, 0}
	r := arena.OpenFixed(buf[2:])
	got := arena.Append(r, buf[:2:2], 9)
	if len(got) != 3 || &got[0] != &buf[2] || got[0] != 7 || got[1] != 8 || got[2] != 9 {
		t.Fatalf("Append = %v; want [7 8 9] inside caller's region", got)
	}
	if used := arena.FixedUsed(r); used != 3 {
		t.Fatalf("FixedUsed = %d; want 3", used)
	}
}

func TestFixedHeapParity(t *testing.T) {
	r := arena.Open()
	p := arena.New[int](r)
	*p = 5
	if *p != 5 {
		t.Errorf("heap New wrote %d, want 5", *p)
	}
	s := arena.Make[int](r, 3)
	if !slices.Equal(s, make([]int, 3)) {
		t.Errorf("heap Make = %v, want %v", s, make([]int, 3))
	}
	got := arena.Append(r, []int{1}, 2)
	if !slices.Equal(got, []int{1, 2}) {
		t.Errorf("heap Append = %v, want [1 2]", got)
	}
}

func TestFixedZeroAlloc(t *testing.T) {
	r := arena.OpenFixed(make([]byte, 2048))
	if n := testing.AllocsPerRun(100, func() {
		fixedSinkU = arena.New[uint64](r)
	}); n != 0 {
		t.Errorf("New[uint64] allocated %v times, want 0", n)
	}
	r2 := arena.OpenFixed(make([]byte, 8192))
	if n := testing.AllocsPerRun(100, func() {
		fixedSinkB = arena.Make[byte](r2, 64)
	}); n != 0 {
		t.Errorf("Make[byte] allocated %v times, want 0", n)
	}
	r3 := arena.OpenFixed(make([]byte, 1024))
	fixedSinkB = nil
	if n := testing.AllocsPerRun(100, func() {
		fixedSinkB = arena.Append(r3, fixedSinkB, 1)
	}); n != 0 {
		t.Errorf("Append[byte] allocated %v times, want 0", n)
	}
	if n := testing.AllocsPerRun(100, func() {
		fixedSinkI = arena.FixedUsed(r)
	}); n != 0 {
		t.Errorf("FixedUsed allocated %v times, want 0", n)
	}
}

func TestFixedAlignment(t *testing.T) {
	buf := fixedMisaligned(t, 32)
	r := arena.OpenFixed(buf)
	p := arena.New[uint64](r)
	if uintptr(unsafe.Pointer(p))%8 != 0 {
		t.Errorf("New[uint64] = %p, want 8-byte alignment", p)
	}
	base := uintptr(unsafe.Pointer(&buf[0]))
	pad := int((8 - base%8) % 8)
	if got := arena.FixedUsed(r); got != pad+8 {
		t.Errorf("FixedUsed = %d, want %d", got, pad+8)
	}
}

func TestFixedMakeNegative(t *testing.T) {
	r := arena.OpenFixed(make([]byte, 8))
	n := -1
	heapMsg := fixedPanicText(t, func() { _ = make([]int, n) })
	fixedMsg := fixedPanicText(t, func() { _ = arena.Make[int](r, n) })
	if fixedMsg != heapMsg {
		t.Errorf("Make panic %q, want heap make panic %q", fixedMsg, heapMsg)
	}
}

func TestFixedZeroSize(t *testing.T) {
	r := arena.OpenFixed(make([]byte, 8))
	p := arena.New[struct{}](r)
	if p == nil {
		t.Fatalf("New[struct{}] returned nil")
	}
	m := arena.Make[struct{}](r, 100)
	if len(m) != 100 {
		t.Fatalf("Make[struct{}](r, 100) has len %d, want 100", len(m))
	}
	g := arena.Append(r, m, struct{}{})
	if len(g) != 101 {
		t.Errorf("Append zero-size has len %d, want 101", len(g))
	}
	if got := arena.FixedUsed(r); got != 0 {
		t.Errorf("FixedUsed = %d after zero-size ops, want 0", got)
	}
}

func TestFixedRejectsPointers(t *testing.T) {
	type entry struct {
		names [1]string
	}
	r := arena.OpenFixed(make([]byte, 64))
	for _, tc := range []struct {
		name string
		run  func()
	}{
		{"new", func() { arena.New[entry](r) }},
		{"make", func() { arena.Make[entry](r, 1) }},
		{"append", func() { arena.Append(r, []entry(nil), entry{}) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if msg := fixedPanicText(t, tc.run); msg != "arena: fixed region cannot hold pointers" {
				t.Fatalf("panic = %q, want pointer rejection", msg)
			}
			if got := arena.FixedUsed(r); got != 0 {
				t.Fatalf("rejected operation consumed %d bytes", got)
			}
		})
	}
}

func TestFixedEmptyBuffer(t *testing.T) {
	r := arena.OpenFixed(nil)
	if got := arena.FixedUsed(r); got != 0 {
		t.Errorf("FixedUsed(nil buffer) = %d, want 0", got)
	}
	if z := arena.Make[byte](r, 0); len(z) != 0 {
		t.Errorf("Make[byte](r, 0) has len %d, want 0", len(z))
	}
	msg := fixedPanicText(t, func() { _ = arena.Make[byte](r, 1) })
	want := "arena: fixed region exhausted (want 1, have 0)"
	if msg != want {
		t.Errorf("got %q, want %q", msg, want)
	}
}

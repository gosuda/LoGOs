// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sync_test

import (
	"sync"
	"sync/atomic"
	"testing"

	logosync "logos/sync"
)

type orderID int64

func mustPanic(t *testing.T, name string, f func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s did not panic", name)
		}
	}()
	f()
}

func TestAtomicOrderNames(t *testing.T) {
	cases := []struct {
		order logosync.Order
		want  string
	}{
		{logosync.Relaxed, "relaxed"},
		{logosync.Acquire, "acquire"},
		{logosync.Release, "release"},
		{logosync.AcqRel, "acq_rel"},
		{logosync.SeqCst, "seq_cst"},
		{logosync.Order(7), "invalid"},
	}
	for _, c := range cases {
		if got := c.order.String(); got != c.want {
			t.Fatalf("Order(%d).String() = %q, want %q", int(c.order), got, c.want)
		}
	}
}

func TestAtomicUint64Sequence(t *testing.T) {
	var got logosync.Atomic[uint64]
	var want atomic.Uint64
	got.Store(10, logosync.SeqCst)
	want.Store(10)
	if g := got.Load(logosync.Acquire); g != want.Load() {
		t.Fatalf("Load = %d, want %d", g, want.Load())
	}
	if g, w := got.Add(5, logosync.Relaxed), want.Add(5); g != w {
		t.Fatalf("Add = %d, want %d", g, w)
	}
	if g, w := got.Swap(7, logosync.SeqCst), want.Swap(7); g != w {
		t.Fatalf("Swap old = %d, want %d", g, w)
	}
	if g, w := got.CompareAndSwap(7, 9, logosync.SeqCst), want.CompareAndSwap(7, 9); g != w {
		t.Fatalf("CompareAndSwap = %v, want %v", g, w)
	}
	if g := got.CompareAndSwap(7, 1, logosync.SeqCst); g {
		t.Fatalf("CompareAndSwap with stale old = true, want false")
	}
	if g, w := got.And(0xF0, logosync.Relaxed), want.And(0xF0); g != w {
		t.Fatalf("And = %d, want %d", g, w)
	}
	if g, w := got.Or(0x0F, logosync.Relaxed), want.Or(0x0F); g != w {
		t.Fatalf("Or = %d, want %d", g, w)
	}
	if g := got.Load(logosync.SeqCst); g != want.Load() {
		t.Fatalf("final Load = %d, want %d", g, want.Load())
	}
}

func TestAtomicInt64SignedDelta(t *testing.T) {
	var got logosync.Atomic[int64]
	var want atomic.Int64
	got.Store(0, logosync.SeqCst)
	if g, w := got.Add(-3, logosync.Relaxed), want.Add(-3); g != w || g != -3 {
		t.Fatalf("Add(-3) = %d, want %d", g, w)
	}
	if g, w := got.Swap(-9, logosync.AcqRel), want.Swap(-9); g != w || g != -3 {
		t.Fatalf("Swap old = %d, want %d", g, w)
	}
	if g, w := got.CompareAndSwap(-9, 7, logosync.SeqCst), want.CompareAndSwap(-9, 7); g != w || !g {
		t.Fatalf("CompareAndSwap = %v, want %v", g, w)
	}
	if g := got.Load(logosync.Acquire); g != 7 {
		t.Fatalf("Load = %d, want 7", g)
	}
}

func TestAtomic32BitWrap(t *testing.T) {
	var gu logosync.Atomic[uint32]
	var wu atomic.Uint32
	gu.Store(0xFFFFFFFF, logosync.SeqCst)
	wu.Store(0xFFFFFFFF)
	if g, w := gu.Add(1, logosync.Relaxed), wu.Add(1); g != w || g != 0 {
		t.Fatalf("uint32 wrap Add = %d, want %d", g, w)
	}
	var gi logosync.Atomic[int32]
	var wi atomic.Int32
	gi.Store(-2147483648, logosync.SeqCst)
	wi.Store(-2147483648)
	if g, w := gi.Add(-1, logosync.Relaxed), wi.Add(-1); g != w {
		t.Fatalf("int32 Add = %d, want %d", g, w)
	}
	if g := gu.Load(logosync.SeqCst); g != wu.Load() {
		t.Fatalf("uint32 Load = %d, want %d", g, wu.Load())
	}
}

func TestAtomicNamedType(t *testing.T) {
	var got logosync.Atomic[orderID]
	var want atomic.Int64
	got.Store(orderID(41), logosync.SeqCst)
	want.Store(41)
	if g := got.Add(orderID(1), logosync.Relaxed); int64(g) != want.Add(1) {
		t.Fatalf("Add = %d, want %d", int64(g), want.Load())
	}
	if g := got.Load(logosync.Acquire); g != orderID(42) {
		t.Fatalf("Load = %d, want 42", int64(g))
	}
	if !got.CompareAndSwap(orderID(42), orderID(43), logosync.SeqCst) {
		t.Fatalf("CompareAndSwap = false, want true")
	}
}

func TestAtomicWordWidth(t *testing.T) {
	var gi logosync.Atomic[int]
	gi.Store(-1, logosync.SeqCst)
	if g := gi.Load(logosync.Acquire); g != -1 {
		t.Fatalf("int Load = %d, want -1", g)
	}
	var gu logosync.Atomic[uint]
	gu.Store(^uint(0), logosync.SeqCst)
	if g := gu.Load(logosync.Acquire); g != ^uint(0) {
		t.Fatalf("uint Load = %d, want max", g)
	}
	var gp logosync.Atomic[uintptr]
	gp.Store(uintptr(0xABCDEF), logosync.SeqCst)
	if g := gp.Load(logosync.Acquire); g != uintptr(0xABCDEF) {
		t.Fatalf("uintptr Load = %#x, want 0xabcdef", uint64(g))
	}
}

func TestAtomicOrderPanics(t *testing.T) {
	var u logosync.Atomic[uint64]
	cases := []struct {
		name string
		call func()
	}{
		{"Load Release", func() { _ = u.Load(logosync.Release) }},
		{"Load AcqRel", func() { _ = u.Load(logosync.AcqRel) }},
		{"Load invalid", func() { _ = u.Load(logosync.Order(9)) }},
		{"Store Acquire", func() { u.Store(1, logosync.Acquire) }},
		{"Store AcqRel", func() { u.Store(1, logosync.AcqRel) }},
		{"Store invalid", func() { u.Store(1, logosync.Order(-3)) }},
		{"Add invalid", func() { _ = u.Add(1, logosync.Order(9)) }},
		{"Swap invalid", func() { _ = u.Swap(1, logosync.Order(9)) }},
		{"CompareAndSwap invalid", func() { _ = u.CompareAndSwap(0, 1, logosync.Order(9)) }},
		{"And invalid", func() { _ = u.And(1, logosync.Order(9)) }},
		{"Or invalid", func() { _ = u.Or(1, logosync.Order(9)) }},
	}
	for _, c := range cases {
		mustPanic(t, c.name, c.call)
	}
}

func TestAtomicConcurrentAdds(t *testing.T) {
	var a logosync.Atomic[uint64]
	const writers = 8
	const perWriter = 1000
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			for range perWriter {
				a.Add(1, logosync.Relaxed)
			}
		})
	}
	wg.Wait()
	if got := a.Load(logosync.SeqCst); got != writers*perWriter {
		t.Fatalf("Load after %d adds = %d, want %d", writers*perWriter, got, writers*perWriter)
	}
}

func TestAtomicNoAllocs(t *testing.T) {
	var a logosync.Atomic[uint64]
	a.Store(1, logosync.SeqCst)
	n := testing.AllocsPerRun(200, func() {
		a.Store(2, logosync.Relaxed)
		_ = a.Load(logosync.Acquire)
		_ = a.Add(1, logosync.Relaxed)
		_ = a.Swap(3, logosync.SeqCst)
		_ = a.CompareAndSwap(3, 4, logosync.SeqCst)
		_ = a.And(0xFF, logosync.Relaxed)
		_ = a.Or(0xF0, logosync.Relaxed)
	})
	if n != 0 {
		t.Fatalf("atomic ops allocated %v per run, want 0", n)
	}
}

func FuzzAtomicUint64Ops(f *testing.F) {
	f.Add([]byte{0, 10, 1, 0, 0, 0, 0, 0, 0, 0, 5})
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		var got logosync.Atomic[uint64]
		var want atomic.Uint64
		for len(data) > 0 {
			n := len(data)
			if n > 9 {
				n = 9
			}
			chunk := data[:n]
			data = data[n:]
			var v uint64
			for _, b := range chunk[1:] {
				v = v<<8 | uint64(b)
			}
			switch chunk[0] % 7 {
			case 0:
				got.Store(v, logosync.SeqCst)
				want.Store(v)
			case 1:
				if g, w := got.Add(v, logosync.Relaxed), want.Add(v); g != w {
					t.Fatalf("Add(%d) = %d, want %d", v, g, w)
				}
			case 2:
				if g, w := got.Swap(v, logosync.SeqCst), want.Swap(v); g != w {
					t.Fatalf("Swap(%d) old = %d, want %d", v, g, w)
				}
			case 3:
				if g, w := got.CompareAndSwap(v, v+1, logosync.SeqCst), want.CompareAndSwap(v, v+1); g != w {
					t.Fatalf("CompareAndSwap(%d) = %v, want %v", v, g, w)
				}
			case 4:
				if g, w := got.And(v, logosync.Relaxed), want.And(v); g != w {
					t.Fatalf("And(%d) = %d, want %d", v, g, w)
				}
			case 5:
				if g, w := got.Or(v, logosync.Relaxed), want.Or(v); g != w {
					t.Fatalf("Or(%d) = %d, want %d", v, g, w)
				}
			default:
				if g, w := got.Load(logosync.Acquire), want.Load(); g != w {
					t.Fatalf("Load = %d, want %d", g, w)
				}
			}
		}
		if g, w := got.Load(logosync.SeqCst), want.Load(); g != w {
			t.Fatalf("final Load = %d, want %d", g, w)
		}
	})
}

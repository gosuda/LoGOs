// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rt

import (
	"runtime"
	"testing"
	"unsafe"
)

type viewasHeader struct {
	Magic   uint32
	Version uint32
	Size    uint64
}

func TestViewAsDirect(t *testing.T) {
	b := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	h := ViewAs[viewasHeader](b)
	if h.Magic != 67305985 || h.Version != 134678021 || h.Size != 1157159078456920585 {
		t.Fatalf("ViewAs = %+v, want {67305985 134678021 1157159078456920585}", *h)
	}
}

func TestViewAsOddOffset(t *testing.T) {
	b := make([]byte, 17)
	for i := range b {
		b[i] = byte(i + 1)
	}
	h := ViewAs[viewasHeader](b[1:])
	if h.Magic != 84148994 || h.Version != 151521030 || h.Size != 1229499251294997258 {
		t.Fatalf("odd-offset ViewAs = %+v", *h)
	}
}

func TestViewAsShortPanics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("ViewAs did not panic on short input")
		}
		msg, ok := r.(string)
		if !ok || msg != "logos: ViewAs: need 16 bytes, have 8" {
			t.Fatalf("ViewAs panic = %v, want %q", r, "logos: ViewAs: need 16 bytes, have 8")
		}
	}()
	_ = ViewAs[viewasHeader]([]byte{1, 2, 3, 4, 5, 6, 7, 8})
}

func TestViewAsCopyPath(t *testing.T) {
	b := make([]byte, 17)
	for i := range b {
		b[i] = byte(i + 1)
	}
	h := viewAsCopy[viewasHeader](b[1:])
	if h.Magic != 84148994 || h.Version != 151521030 || h.Size != 1229499251294997258 {
		t.Fatalf("copy-path ViewAs = %+v", *h)
	}
	for i := range b {
		b[i] = 0
	}
	if h.Magic != 84148994 {
		t.Fatalf("copy-path view aliases source: %+v", *h)
	}
}

// SAFETY: the unsafe pointers below are read-only address probes. They are
// converted to uintptr and compared immediately; no pointer is retained,
// dereferenced, or used after the backing slice is dropped.
func TestViewAsCopyAlignedIndependent(t *testing.T) {
	b := make([]byte, 19)
	for i := range b {
		b[i] = byte(i + 1)
	}
	off := b[1:]
	h := viewAsCopy[viewasHeader](off)
	if uintptr(unsafe.Pointer(h))%unsafe.Alignof(viewasHeader{}) != 0 {
		t.Fatalf("copy-path ViewAs misaligned: %p", h)
	}
	start := uintptr(unsafe.Pointer(&off[0]))
	end := start + uintptr(len(off))
	p := uintptr(unsafe.Pointer(h))
	if p >= start && p < end+uintptr(unsafe.Sizeof(viewasHeader{})) {
		t.Fatalf("copy-path ViewAs aliases its input: view %p inside [%x, %x)", h, start, end)
	}
	if d := ViewAs[viewasHeader](off); *h != *d {
		t.Fatalf("copy-path ViewAs = %+v, direct path = %+v", *h, *d)
	}
	off = nil
	b = nil
	runtime.GC()
	if h.Magic != 84148994 || h.Version != 151521030 || h.Size != 1229499251294997258 {
		t.Fatalf("copy-path ViewAs invalid after input dropped: %+v", *h)
	}
	runtime.KeepAlive(h)
}

func TestViewAsKeepsAlive(t *testing.T) {
	v := freshViewas()
	runtime.GC()
	g := make([]byte, 1<<20)
	for i := range g {
		g[i] = 0xFF
	}
	runtime.GC()
	runtime.KeepAlive(g)
	if v.Magic != 336794129 || v.Version != 404166165 || v.Size != 2314601843866147353 {
		t.Fatalf("view after GC = %+v", *v)
	}
}

func freshViewas() *viewasHeader {
	b := []byte{17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	return ViewAs[viewasHeader](b)
}

func TestViewAsPanicMentionsSizes(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("ViewAs did not panic on nil input")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("ViewAs panic type = %T, want string", r)
		}
		if msg != "logos: ViewAs: need 16 bytes, have 0" {
			t.Fatalf("ViewAs panic = %q, want %q", msg, "logos: ViewAs: need 16 bytes, have 0")
		}
	}()
	_ = ViewAs[viewasHeader](nil)
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package arena

import "unsafe"

// fixedRef is the option form of *fixedState: nil is absent (the growable
// heap mode) and non-nil is present (the fixed-buffer mode opened by
// OpenFixed). The named type keeps the field opaque to importing .lgo
// code, so Region{} stays a legal empty literal under
// [correctness::nonnil_uninitialized]; a bare *fixedState field would
// make the zero Region illegal.
type fixedRef *fixedState

type fixedState struct {
	buf  []byte
	used int
}

// OpenFixed opens a fixed region over buf and returns its handle.
//
// New, Make, and Append with the handle bump through buf. A request that
// does not fit panics with arena: fixed region exhausted (want N, have M)
// instead of growing. Requests for zero-size types and empty appends never
// consume the buffer. A nonempty allocation of a pointer-bearing type
// panics. FixedUsed reports the bytes consumed so far. The handle keeps
// buf alive. A fixed handle is not safe for concurrent use; open one
// handle per buffer.
func OpenFixed(buf []byte) Region {
	return Region{fixed: fixedRef(&fixedState{buf: buf})}
}

// FixedUsed reports the bytes of the fixed region consumed so far. It
// reports 0 for a heap region.
func FixedUsed(r Region) int {
	if r.fixed == nil {
		return 0
	}
	return r.fixed.used
}

func fixedAlignUpAddr(x, align uintptr) uintptr {
	return (x + align - 1) &^ (align - 1)
}

func fixedExhausted(want, have int) string {
	out := make([]byte, 0, 64)
	out = append(out, "arena: fixed region exhausted (want "...)
	out = appendFixedUint(out, uint64(want))
	out = append(out, ", have "...)
	out = appendFixedUint(out, uint64(have))
	out = append(out, ')')
	return string(out)
}

func appendFixedUint(out []byte, v uint64) []byte {
	var digits [20]byte
	i := len(digits)
	for v > 9 {
		i--
		digits[i] = byte(v%10) + '0'
		v /= 10
	}
	i--
	digits[i] = byte(v) + '0'
	return append(out, digits[i:]...)
}

func fixedCheckElement[T any]() {
	if fixedElementHasPointers[T]() {
		panic("arena: fixed region cannot hold pointers")
	}
}

func fixedNew[T any](r Region) *T {
	var zero T
	if unsafe.Sizeof(zero) == 0 {
		return new(T)
	}
	fixedCheckElement[T]()
	size := int(unsafe.Sizeof(zero))
	align := int(unsafe.Alignof(zero))
	st := r.fixed
	base := uintptr(unsafe.Pointer(unsafe.SliceData(st.buf)))
	at := int(fixedAlignUpAddr(base+uintptr(st.used), uintptr(align)) - base)
	if at > len(st.buf) || size > len(st.buf)-at {
		panic(fixedExhausted(size, len(st.buf)-st.used))
	}
	st.used = at + size
	p := (*T)(unsafe.Pointer(&st.buf[at]))
	clear(st.buf[at:st.used])
	return p
}

func fixedMake[T any](r Region, n int) []T {
	if n < 0 {
		return make([]T, n)
	}
	var zero T
	if unsafe.Sizeof(zero) == 0 {
		return make([]T, n)
	}
	size := int(unsafe.Sizeof(zero))
	st := r.fixed
	if n == 0 {
		return unsafe.Slice((*T)(unsafe.Pointer(unsafe.SliceData(st.buf[st.used:]))), 0)
	}
	fixedCheckElement[T]()
	want := n * size
	if want/n != size {
		want = int(^uint(0) >> 1)
	}
	align := int(unsafe.Alignof(zero))
	base := uintptr(unsafe.Pointer(unsafe.SliceData(st.buf)))
	at := int(fixedAlignUpAddr(base+uintptr(st.used), uintptr(align)) - base)
	if at > len(st.buf) || want > len(st.buf)-at {
		panic(fixedExhausted(want, len(st.buf)-st.used))
	}
	st.used = at + want
	out := unsafe.Slice((*T)(unsafe.Pointer(&st.buf[at])), n)
	clear(st.buf[at:st.used])
	return out
}

func fixedAppend[T any](r Region, s []T, v []T) []T {
	if len(v) == 0 {
		return s
	}
	var zero T
	if unsafe.Sizeof(zero) == 0 {
		return append(s, v...)
	}
	fixedCheckElement[T]()
	size := int(unsafe.Sizeof(zero))
	align := int(unsafe.Alignof(zero))
	if cap(s)-len(s) >= len(v) {
		return append(s, v...)
	}
	st := r.fixed
	base := uintptr(unsafe.Pointer(unsafe.SliceData(st.buf)))
	front := base + uintptr(st.used)
	last := false
	if len(s) > 0 {
		start := uintptr(unsafe.Pointer(&s[0]))
		end := start + uintptr(len(s))*uintptr(size)
		last = start >= base && end == front && front%uintptr(align) == 0
	}
	if last {
		add := len(v) * size
		if add <= len(st.buf)-st.used {
			out := unsafe.Slice(&s[0], len(s)+len(v))
			st.used += add
			copy(out[len(s):], v)
			return out
		}
	}
	total := len(s) + len(v)
	want := total * size
	if want/total != size {
		want = int(^uint(0) >> 1)
	}
	at := int(fixedAlignUpAddr(base+uintptr(st.used), uintptr(align)) - base)
	if at > len(st.buf) || want > len(st.buf)-at {
		panic(fixedExhausted(want, len(st.buf)-st.used))
	}
	// Preserve v when it aliases the fresh segment: the s copy below
	// would otherwise overwrite v before it is copied. Each copy is
	// memmove, so only the second source needs a temporary.
	if len(s) > 0 {
		dst := base + uintptr(at)
		sBytes := uintptr(len(s)) * uintptr(size)
		vPtr := uintptr(unsafe.Pointer(unsafe.SliceData(v)))
		vBytes := uintptr(len(v)) * uintptr(size)
		if vPtr < dst+sBytes && dst < vPtr+vBytes {
			tmp := make([]T, len(v))
			copy(tmp, v)
			v = tmp
		}
	}
	st.used = at + want
	out := unsafe.Slice((*T)(unsafe.Pointer(&st.buf[at])), total)
	copy(out, s)
	copy(out[len(s):], v)
	return out
}

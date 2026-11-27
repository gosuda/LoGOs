// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package sync

import (
	"sync/atomic"
	"unsafe"
)

// Ordering vocabulary follows Zig lib/std/atomic.zig Value(T) at revision
// c7826aa7a0 (master, 2026-11-16): every operation spells its ordering.
// The lowering differs: Go offers only sequentially consistent atomics,
// so each spelling below executes through sync/atomic while the Order
// argument records the intent for readers and weaker future backends.

// Order names the memory ordering of one atomic operation.
// Relaxed matches Zig monotonic. Zig unordered has no spelling here;
// every operation below executes sequentially consistent.
type Order int

const (
	// Relaxed orders only the atomic itself.
	Relaxed Order = iota
	// Acquire pairs a load with a releasing store.
	Acquire
	// Release pairs a store with an acquiring load.
	Release
	// AcqRel pairs a read-modify-write with both sides.
	AcqRel
	// SeqCst is the total order. Use it unless a weaker one is proven.
	SeqCst
)

// String names the order for diagnostics.
func (o Order) String() string {
	switch o {
	case Relaxed:
		return "relaxed"
	case Acquire:
		return "acquire"
	case Release:
		return "release"
	case AcqRel:
		return "acq_rel"
	case SeqCst:
		return "seq_cst"
	default:
		return "invalid"
	}
}

// Integer bounds the values an Atomic holds to the widths sync/atomic
// implements: 32 and 64 bit integers plus the platform word. 8 and 16 bit
// integers stay out: backing them would widen the storage silently, so
// naming one fails to compile against this constraint instead.
type Integer interface {
	~int | ~int32 | ~int64 | ~uint | ~uint32 | ~uint64 | ~uintptr
}

// Atomic is an integer atomic whose every operation spells its ordering.
// The zero value is ready to use. Do not copy an Atomic after first use:
// copies share no state check and race exactly like copying sync.Mutex.
type Atomic[T Integer] struct {
	s32 atomic.Int32
	u32 atomic.Uint32
	s64 atomic.Int64
	u64 atomic.Uint64
}

func signed[T Integer]() bool {
	var zero T
	return zero-1 < zero
}

func width[T Integer]() uintptr {
	var zero T
	return unsafe.Sizeof(zero)
}

func (o Order) checkLoad() {
	switch o {
	case Relaxed, Acquire, SeqCst:
		return
	default:
		panic("logos/sync: Atomic.Load order " + o.String() + " is not a load ordering")
	}
}
func (o Order) checkStore() {
	switch o {
	case Relaxed, Release, SeqCst:
		return
	default:
		panic("logos/sync: Atomic.Store order " + o.String() + " is not a store ordering")
	}
}

func (o Order) checkUpdate() {
	switch o {
	case Relaxed, Acquire, Release, AcqRel, SeqCst:
		return
	default:
		panic("logos/sync: atomic update order " + o.String() + " is not an ordering")
	}
}

// Load reports the value. It accepts Relaxed, Acquire, and SeqCst.
func (a *Atomic[T]) Load(order Order) T {
	order.checkLoad()
	if signed[T]() {
		if width[T]() == 8 {
			return T(a.s64.Load())
		}
		return T(a.s32.Load())
	}
	if width[T]() == 8 {
		return T(a.u64.Load())
	}
	return T(a.u32.Load())
}

// Store publishes v. It accepts Relaxed, Release, and SeqCst.
func (a *Atomic[T]) Store(v T, order Order) {
	order.checkStore()
	if signed[T]() {
		if width[T]() == 8 {
			a.s64.Store(int64(v))
			return
		}
		a.s32.Store(int32(v))
		return
	}
	if width[T]() == 8 {
		a.u64.Store(uint64(v))
		return
	}
	a.u32.Store(uint32(v))
}

// Add adds delta and reports the new value, like sync/atomic.
// It accepts any Order.
func (a *Atomic[T]) Add(delta T, order Order) T {
	order.checkUpdate()
	if signed[T]() {
		if width[T]() == 8 {
			return T(a.s64.Add(int64(delta)))
		}
		return T(a.s32.Add(int32(delta)))
	}
	if width[T]() == 8 {
		return T(a.u64.Add(uint64(delta)))
	}
	return T(a.u32.Add(uint32(delta)))
}

// Swap stores v and reports the previous value. It accepts any Order.
func (a *Atomic[T]) Swap(v T, order Order) T {
	order.checkUpdate()
	if signed[T]() {
		if width[T]() == 8 {
			return T(a.s64.Swap(int64(v)))
		}
		return T(a.s32.Swap(int32(v)))
	}
	if width[T]() == 8 {
		return T(a.u64.Swap(uint64(v)))
	}
	return T(a.u32.Swap(uint32(v)))
}

// CompareAndSwap stores next when the value equals old and reports
// whether it did. It accepts any Order.
func (a *Atomic[T]) CompareAndSwap(old T, next T, order Order) bool {
	order.checkUpdate()
	if signed[T]() {
		if width[T]() == 8 {
			return a.s64.CompareAndSwap(int64(old), int64(next))
		}
		return a.s32.CompareAndSwap(int32(old), int32(next))
	}
	if width[T]() == 8 {
		return a.u64.CompareAndSwap(uint64(old), uint64(next))
	}
	return a.u32.CompareAndSwap(uint32(old), uint32(next))
}

// And clears every bit absent from v and reports the previous value,
// like sync/atomic. It accepts any Order.
func (a *Atomic[T]) And(v T, order Order) T {
	order.checkUpdate()
	if signed[T]() {
		if width[T]() == 8 {
			return T(a.s64.And(int64(v)))
		}
		return T(a.s32.And(int32(v)))
	}
	if width[T]() == 8 {
		return T(a.u64.And(uint64(v)))
	}
	return T(a.u32.And(uint32(v)))
}

// Or sets every bit present in v and reports the previous value,
// like sync/atomic. It accepts any Order.
func (a *Atomic[T]) Or(v T, order Order) T {
	order.checkUpdate()
	if signed[T]() {
		if width[T]() == 8 {
			return T(a.s64.Or(int64(v)))
		}
		return T(a.s32.Or(int32(v)))
	}
	if width[T]() == 8 {
		return T(a.u64.Or(uint64(v)))
	}
	return T(a.u32.Or(uint32(v)))
}

// errorcheck -0 --pedantic

// portability::atomic_plain_int64 (M9-S9): a sync/atomic 64-bit call on the
// address of a plain int64 or uint64 warns, because a 32-bit target cannot
// promise 8-byte alignment for such a value. The autofix replaces the
// declared type with atomic.Int64 or atomic.Uint64.

package p

import (
	"sync"
	"sync/atomic"
)

var plain int64 // ERROR "\[portability::atomic_plain_int64\]"

var plainU uint64 // ERROR "\[portability::atomic_plain_int64\]"

var safe atomic.Int64

var small int32

type box struct {
	plain int64 // ERROR "\[portability::atomic_plain_int64\]"
	small int32
}

func reads() int64 {
	return atomic.LoadInt64(&plain)
}

func adds() {
	atomic.AddUint64(&plainU, 1)
}

func swaps() {
	atomic.CompareAndSwapInt64(&plain, 0, 1)
}

func safeRead() int64 {
	return safe.Load()
}

func smallRead() int32 {
	return atomic.LoadInt32(&small)
}

func boxRead(b *box) int64 {
	return atomic.LoadInt64(&b.plain)
}

func mutexes() {
	var mu sync.Mutex
	mu.Lock()
	mu.Unlock()
}

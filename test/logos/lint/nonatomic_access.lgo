// errorcheck

// concurrency::nonatomic_access (M9-S9): a field marked //logos:atomic is
// read or written only through sync/atomic. A plain read, a plain write, or
// an address that leaves an atomic call reports; the address of the field
// inside a sync/atomic call is the blessed form.

package p

import "sync/atomic"

type counter struct {
	total int64
	small int32

	//logos:atomic
	hits int64

	//logos:atomic
	drops uint64
}

func passthrough(*int64) {}

func loadOK(c *counter) int64 {
	return atomic.LoadInt64(&c.hits)
}

func addOK(c *counter) {
	atomic.AddUint64(&c.drops, 1)
}

func smallOK(c *counter) int32 {
	return atomic.LoadInt32(&c.small)
}

func badRead(c *counter) int64 {
	return c.hits // ERROR "\[concurrency::nonatomic_access\]"
}

func badWrite(c *counter) {
	c.hits = 1 // ERROR "\[concurrency::nonatomic_access\]"
}

func badInc(c *counter) {
	c.hits++ // ERROR "\[concurrency::nonatomic_access\]"
}

func badSwap(c *counter) uint64 {
	return atomic.SwapUint64(&c.drops, 2) + c.drops // ERROR "\[concurrency::nonatomic_access\]"
}

func badLeak(c *counter) {
	passthrough(&c.hits) // ERROR "\[concurrency::nonatomic_access\]"
}

//logos:atomic
var gtotal int64

func badGlobal() {
	gtotal = 5 // ERROR "\[concurrency::nonatomic_access\]"
}

func okGlobal() {
	atomic.StoreInt64(&gtotal, 5)
}

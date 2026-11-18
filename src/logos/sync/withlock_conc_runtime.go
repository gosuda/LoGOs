// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build logosconc && !logosejected

package sync

import (
	"unsafe"
	"weak"

	"logos/internal/conc"
)

// WithLock is the monitored form that `logos conc` builds: it has the
// contract of the unmonitored form and tells logos/internal/conc which
// values the goroutine holds while it takes this one.
func (l *Locked[T]) WithLock(f func(*T)) {
	key := unsafe.Pointer(&l.mu)
	if conc.Acquire(key) {
		observed := weak.Make(l)
		conc.Track(key, func() bool { return observed.Value() != nil })
	}
	l.mu.Lock()
	conc.Hold(key)
	defer func() {
		l.mu.Unlock()
		conc.Release(key)
	}()
	f(&l.v)
}

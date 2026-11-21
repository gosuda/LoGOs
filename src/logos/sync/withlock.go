// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !logosconc

package sync

// WithLock holds Lock, defers Unlock, then calls f with the value's
// address. The deferred Unlock runs during unwinding, so a panic in f
// unlocks the mutex before propagating (#176). f must neither return
// nor store its argument; .lgo callers get that rule from the M9 lint,
// which rejects escaping the pointer.
func (l *Locked[T]) WithLock(f func(*T)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f(&l.v)
}

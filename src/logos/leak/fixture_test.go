// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package leak_test

import "testing"

// Manual-proof fixture for per-test leak detection: it leaks exactly one
// goroutine blocked on a channel that never closes.
func TestLeakFixture(t *testing.T) {
	ch := make(chan struct{})
	go func() {
		<-ch
	}()
}

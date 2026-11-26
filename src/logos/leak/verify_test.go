// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package leak

import (
	"testing"
)

// The deferred Check must actually verify: a test that grows the live
// set has to fail. Planted fault: the documented form defers Check
// itself, which samples at exit and discards the verifier, so a leak
// passes silently.
func TestVerifyFailsOnGrowth(t *testing.T) {
	before := snapshot{goroutines: 5, fds: 3, timers: 0, fdsOK: true}
	calls := 0
	take := func() snapshot {
		calls++
		return snapshot{goroutines: 6, fds: 3, timers: 0, fdsOK: true}
	}
	failed := 0
	verify(before, func(format string, args ...any) { failed++ }, take, func() {})
	if failed != 1 {
		t.Fatalf("verify failures = %d, want 1", failed)
	}
	if calls != 1+quiesceTries {
		t.Fatalf("verify samples = %d, want %d", calls, 1+quiesceTries)
	}
}

func TestVerifyPassesWhenSettled(t *testing.T) {
	before := snapshot{goroutines: 5, fds: 3, timers: 0, fdsOK: true}
	failed := 0
	verify(before, func(format string, args ...any) { failed++ }, func() snapshot {
		return before
	}, func() { t.Fatal("quiesce sleep ran for a settled test") })
	if failed != 0 {
		t.Fatalf("verify failures = %d, want 0", failed)
	}
}

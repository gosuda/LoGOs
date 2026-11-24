// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package leak checks per-test resource cleanup for goroutines, file
// descriptors, and timers. It is the LoGOs analogue of Zig's per-test
// allocator accounting (lib/std/testing.zig): where Zig fails a test
// that leaks bytes, leak fails a test whose goroutine, fd, or timer
// counts grow between the start and the end of the test.
//
// Use it as the first line of a test:
//
//	func TestTopicDrops(t *testing.T) {
//		defer leak.Check(t)()
//	}
//
// Check samples the three counts when it runs, and the deferred call
// fails the test unless every count is back at its sample. A test that
// leaks one goroutine blocked on a never-closed channel fails with
// "leak: 1 goroutine, 0 fds, 0 timers still live".
//
// A check that sees growth waits out a bounded quiescence window and
// samples again before failing, so a goroutine that is already winding
// down does not fail the test. Only growth fails: a test that ends
// with fewer live goroutines or fds than it started passes.
//
// File descriptors are counted from the process fd table, trying
// /proc/self/fd and then /dev/fd. Where neither exists the fd column
// is skipped. The timer column is reserved and reads 0. The Go runtime
// keeps timers in per-P heaps with no public census, so no library can
// count them without runtime support; a timer leak that parks a
// goroutine is still caught through the goroutine column.
//
// Check is for tests that own their process state; do not use it in
// parallel tests. It is pure standard library with no lowering
// dependence, so ejected code behaves identically.
package leak

import (
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"
)

const (
	quiesceTries = 10
	quiesceWait  = 10 * time.Millisecond
)

type snapshot struct {
	goroutines int
	fds        int
	timers     int
	fdsOK      bool
}

type delta struct {
	goroutines int
	fds        int
	timers     int
	fdsOK      bool
}

// Check arranges a leak assertion for the calling test. Call it first
// and defer the result; the deferred call fails the test unless the
// goroutine, fd, and timer counts return to their sampled values.
func Check(t *testing.T) func() {
	t.Helper()
	before := sample()
	return func() {
		t.Helper()
		verify(before, t.Errorf, sample, sleepQuiesce)
	}
}

func sleepQuiesce() {
	time.Sleep(quiesceWait)
}

func sample() snapshot {
	fds, ok := countFDs()
	return snapshot{goroutines: runtime.NumGoroutine(), fds: fds, timers: 0, fdsOK: ok}
}

func countFDs() (int, bool) {
	if names, err := os.ReadDir("/proc/self/fd"); err == nil {
		return len(names), true
	}
	if names, err := os.ReadDir("/dev/fd"); err == nil {
		return len(names), true
	}
	return 0, false
}

func diff(before, after snapshot) delta {
	d := delta{goroutines: after.goroutines - before.goroutines, timers: after.timers - before.timers}
	if before.fdsOK && after.fdsOK {
		d.fds = after.fds - before.fds
		d.fdsOK = true
	}
	return d
}

func (d delta) leaked() bool {
	return d.goroutines > 0 || d.timers > 0 || (d.fdsOK && d.fds > 0)
}

func (d delta) String() string {
	return "leak: " + plural(d.goroutines, "goroutine") + ", " + plural(d.fds, "fd") + ", " + plural(d.timers, "timer") + " still live"
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + one + "s"
}

func verify(before snapshot, fail func(string, ...any), take func() snapshot, sleep func()) {
	d := diff(before, take())
	for i := 0; i < quiesceTries && d.leaked(); i++ {
		sleep()
		d = diff(before, take())
	}
	if d.leaked() {
		fail("%s", d.String())
	}
}

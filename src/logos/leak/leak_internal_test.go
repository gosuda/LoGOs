// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package leak

import (
	"fmt"
	"os"
	"runtime"
	"testing"
)

func TestDiff(t *testing.T) {
	base := snapshot{goroutines: 10, fds: 7, timers: 0, fdsOK: true}
	rows := []struct {
		name   string
		before snapshot
		after  snapshot
		want   delta
		leak   bool
	}{
		{"zero", base, base, delta{fdsOK: true}, false},
		{"goroutine growth", base, snapshot{goroutines: 11, fds: 7, fdsOK: true}, delta{goroutines: 1, fdsOK: true}, true},
		{"goroutine shrink passes", base, snapshot{goroutines: 9, fds: 7, fdsOK: true}, delta{goroutines: -1, fdsOK: true}, false},
		{"fd growth", base, snapshot{goroutines: 10, fds: 8, fdsOK: true}, delta{fds: 1, fdsOK: true}, true},
		{"fd shrink passes", base, snapshot{goroutines: 10, fds: 6, fdsOK: true}, delta{fds: -1, fdsOK: true}, false},
		{"timer growth", base, snapshot{goroutines: 10, fds: 7, timers: 1, fdsOK: true}, delta{timers: 1, fdsOK: true}, true},
		{"fd blind before", snapshot{goroutines: 10, fdsOK: false}, snapshot{goroutines: 10, fds: 5, fdsOK: true}, delta{}, false},
		{"fd blind after", base, snapshot{goroutines: 10, fds: 7, fdsOK: false}, delta{}, false},
		{"growth while blind", snapshot{goroutines: 10, fdsOK: false}, snapshot{goroutines: 11, fds: 5, fdsOK: true}, delta{goroutines: 1}, true},
	}
	for _, r := range rows {
		got := diff(r.before, r.after)
		if got != r.want || got.leaked() != r.leak {
			t.Errorf("%s: diff = %+v leaked %v, want %+v leaked %v", r.name, got, got.leaked(), r.want, r.leak)
		}
	}
}

func TestMessage(t *testing.T) {
	rows := []struct {
		name string
		d    delta
		want string
	}{
		{"proof string", delta{goroutines: 1, fdsOK: true}, "leak: 1 goroutine, 0 fds, 0 timers still live"},
		{"plurals", delta{goroutines: 2, fds: 1, fdsOK: true}, "leak: 2 goroutines, 1 fd, 0 timers still live"},
		{"timer singular", delta{timers: 1, fdsOK: true}, "leak: 0 goroutines, 0 fds, 1 timer still live"},
		{"negative count", delta{goroutines: -1, fdsOK: true}, "leak: -1 goroutines, 0 fds, 0 timers still live"},
	}
	for _, r := range rows {
		if got := r.d.String(); got != r.want {
			t.Errorf("%s: message = %q, want %q", r.name, got, r.want)
		}
	}
}

func TestVerifyPersistentGrowth(t *testing.T) {
	before := snapshot{goroutines: 5, fds: 4, fdsOK: true}
	stuck := snapshot{goroutines: 6, fds: 4, fdsOK: true}
	var takes, sleeps int
	take := func() snapshot {
		takes++
		return stuck
	}
	sleep := func() { sleeps++ }
	var msgs []string
	fail := func(format string, args ...any) {
		msgs = append(msgs, fmt.Sprintf(format, args...))
	}
	verify(before, fail, take, sleep)
	if len(msgs) != 1 || msgs[0] != "leak: 1 goroutine, 0 fds, 0 timers still live" {
		t.Errorf("fail messages = %q, want one proof string", msgs)
	}
	if takes != 1+quiesceTries || sleeps != quiesceTries {
		t.Errorf("takes = %d sleeps = %d, want %d takes and %d sleeps", takes, sleeps, 1+quiesceTries, quiesceTries)
	}
}

func TestVerifySettlesOnRetry(t *testing.T) {
	before := snapshot{goroutines: 5, fds: 4, fdsOK: true}
	stuck := snapshot{goroutines: 6, fds: 4, fdsOK: true}
	n := 0
	take := func() snapshot {
		n++
		if n < 3 {
			return stuck
		}
		return before
	}
	sleeps := 0
	failed := false
	verify(before, func(string, ...any) { failed = true }, take, func() { sleeps++ })
	if failed {
		t.Errorf("verify failed after the counts settled")
	}
	if n != 3 || sleeps != 2 {
		t.Errorf("takes = %d sleeps = %d, want 3 takes and 2 sleeps", n, sleeps)
	}
}

func TestVerifyIgnoresShrink(t *testing.T) {
	before := snapshot{goroutines: 5, fds: 4, fdsOK: true}
	fewer := snapshot{goroutines: 4, fds: 3, fdsOK: true}
	takes := 0
	failed := false
	verify(before, func(string, ...any) { failed = true }, func() snapshot {
		takes++
		return fewer
	}, func() { t.Errorf("sleep ran on a shrinking sample") })
	if failed {
		t.Errorf("verify failed on shrinking counts")
	}
	if takes != 1 {
		t.Errorf("takes = %d, want 1", takes)
	}
}

func TestSampleMatchesRuntime(t *testing.T) {
	got := sample()
	if want := runtime.NumGoroutine(); got.goroutines != want {
		t.Errorf("goroutines = %d, want NumGoroutine %d", got.goroutines, want)
	}
	if got.timers != 0 {
		t.Errorf("timers = %d, want 0 (reserved column)", got.timers)
	}
	names, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		names, err = os.ReadDir("/dev/fd")
	}
	if err != nil {
		if got.fdsOK {
			t.Errorf("fdsOK = true with no readable fd table")
		}
		return
	}
	if !got.fdsOK || got.fds != len(names) {
		t.Errorf("fds = %d ok %v, want %d ok true", got.fds, got.fdsOK, len(names))
	}
}

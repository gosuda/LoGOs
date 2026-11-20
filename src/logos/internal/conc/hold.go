// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conc

import (
	"os"
	"sync"
	"time"
)

// HoldEnv names the Go duration for which a goroutine may hold a lock
// that another goroutine waits for before the monitor reports it. The
// `logos conc` command sets it from its -hold flag. When it is empty or
// not a positive duration, the duration is DefaultHold.
const (
	HoldEnv     = "LOGOSCONC_HOLD"
	DefaultHold = 100 * time.Millisecond
)

var holdLimit = sync.OnceValue(func() time.Duration {
	return durationSetting(HoldEnv, DefaultHold)
})

func durationSetting(env string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(os.Getenv(env))
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// lockHold is one hold of a lock, from Hold to Release.
type lockHold struct {
	site  uintptr
	since time.Time
	// waiter is the latest Acquire call of another goroutine during this
	// hold, or 0.
	waiter uintptr
}

// pendingWaiter is one goroutine that has called Acquire for a lock and
// has not held it since. The monitor keeps waiters outside the current
// hold, so a waiter that stays queued across a change of holder still
// counts as waiting on the new hold.
type pendingWaiter struct {
	g  uint64
	pc uintptr
}

// The monitor has no timer for a hold. It looks at the age of a hold
// when another goroutine comes for the lock and when the hold ends, so
// it reports a long hold at the first of these two events after the
// limit.

// waiterArrived records that goroutine g comes for the lock at pc. The
// holder is another goroutine: a second WithLock call of the holder on
// one value never returns, and the linter rejects it. The caller holds
// m.mu.
func (m *monitor) waiterArrived(g uint64, key uintptr, pc uintptr) {
	ws := m.pending[key]
	seen := false
	for i := range ws {
		if ws[i].g == g {
			ws[i].pc = pc
			seen = true
			break
		}
	}
	if !seen {
		m.pending[key] = append(ws, pendingWaiter{g: g, pc: pc})
	}
	hold, held := m.holding[key]
	if !held {
		return
	}
	hold.waiter = pc
	m.holding[key] = hold
	m.reportIfLong(hold)
}

// holdStarted opens a hold on the lock for goroutine g. Its wait is
// over; every other outstanding waiter still queues behind it, so the
// earliest is the waiter of the new hold. The caller holds m.mu.
func (m *monitor) holdStarted(g uint64, key uintptr, pc uintptr) {
	hold := lockHold{site: pc, since: time.Now()}
	ws := m.pending[key]
	for i := range ws {
		if ws[i].g == g {
			ws = append(ws[:i], ws[i+1:]...)
			break
		}
	}
	if len(ws) == 0 {
		delete(m.pending, key)
	} else {
		m.pending[key] = ws
		hold.waiter = ws[0].pc
	}
	m.holding[key] = hold
}

// holdEnded forgets the hold of the lock. Outstanding waiters stay
// recorded: they queue behind the next hold as well. The caller holds
// m.mu.
func (m *monitor) holdEnded(key uintptr) {
	hold, held := m.holding[key]
	if !held {
		return
	}
	delete(m.holding, key)
	m.reportIfLong(hold)
}

func (m *monitor) reportIfLong(hold lockHold) {
	age := time.Since(hold.since)
	if hold.waiter == 0 || age < holdLimit() {
		return
	}
	key := keyOf(hold.site)
	if _, done := m.longHolds[key]; done {
		return
	}
	m.longHolds[key] = struct{}{}
	write(finding{
		rule:    "conc::lock_held_long",
		message: "a goroutine held a Locked value for " + age.Round(time.Millisecond).String() + " while another goroutine waited for it; the limit is " + holdLimit().String(),
		positions: []position{
			at(hold.site, "this WithLock call holds the value"),
			at(hold.waiter, "another goroutine waits for the value here"),
		},
		hint: "move the slow call out of the WithLock callback: copy what the call needs, leave the callback, then call; or keep the value in logos/sync.Hot when readers must not wait",
	})
}

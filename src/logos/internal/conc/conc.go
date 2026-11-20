// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package conc is the run-time monitor behind `logos conc`. The loGo/s
// support packages call it only in a build with the logosconc tag.
//
// The monitor calls no user code and takes no user lock. Its memory is
// bounded: one entry per goroutine that holds a lock, one entry per lock
// that is held, one entry per lock that is still reachable, at most
// maxOrderEdges lock pairs, one entry per group that is not joined, one
// entry per running group child, and one entry per reported position.
//
// A lock is known by its address. An address is reused after the garbage
// collector frees a lock, so each lock comes with a liveness probe, and
// the monitor forgets every pair of a lock whose probe reports false.
package conc

import (
	"runtime"
	"sync"
	"unsafe"
)

//go:linkname goroutineID runtime.logosGoid
func goroutineID() uint64

const (
	maxOrderEdges  = 1 << 16
	firstSweepSize = 1 << 10

	// lockCallerSkip names the frame that called the support function
	// which called the hook: runtime.Callers, callerPC, the hook, the
	// support function, then the caller. A spawn has one more support
	// function between the hook and the caller, and the `go group`
	// statement has the Group constructor there.
	lockCallerSkip  = 4
	spawnCallerSkip = 5
	groupCallerSkip = 5
)

type heldLock struct {
	key uintptr
	pc  uintptr
}

type lockPair struct{ first, second uintptr }

type pairSite struct {
	goroutine         uint64
	firstPC, secondPC uintptr
}

type monitor struct {
	mu        sync.Mutex
	held      map[uint64][]heldLock
	holding   map[uintptr]lockHold
	pending   map[uintptr][]pendingWaiter
	order     map[lockPair][]pairSite
	alive     map[uintptr]func() bool
	sweepAt   int
	reported  map[[4]uintptr]struct{}
	longHolds map[statementKey]struct{}
}

var locks = monitor{
	held:      map[uint64][]heldLock{},
	holding:   map[uintptr]lockHold{},
	pending:   map[uintptr][]pendingWaiter{},
	order:     map[lockPair][]pairSite{},
	alive:     map[uintptr]func() bool{},
	sweepAt:   firstSweepSize,
	reported:  map[[4]uintptr]struct{}{},
	longHolds: map[statementKey]struct{}{},
}

// Acquire records that the calling goroutine is about to take lock while
// it holds the locks of earlier Hold calls. It reports a lock order
// inversion when another goroutine took the same two locks in the other
// order, and a long hold when another goroutine has held lock for the
// hold limit. The result is true when the monitor has no liveness probe
// for lock; the caller then calls Track.
func Acquire(lock unsafe.Pointer) (untracked bool) {
	key := uintptr(lock)
	Shake()
	pc := callerPC(lockCallerSkip)
	g := goroutineID()
	locks.mu.Lock()
	defer locks.mu.Unlock()
	locks.forgetIfFreed(key)
	locks.waiterArrived(g, key, pc)
	_, tracked := locks.alive[key]
	for _, outer := range locks.held[g] {
		if outer.key == key {
			continue
		}
		pair := lockPair{outer.key, key}
		here := pairSite{goroutine: g, firstPC: outer.pc, secondPC: pc}
		witnesses, known := locks.order[pair]
		if !known {
			if len(locks.order) < maxOrderEdges {
				locks.order[pair] = []pairSite{here}
			}
		} else if len(witnesses) == 1 && witnesses[0].goroutine != g {
			locks.order[pair] = append(witnesses, here)
		}
		if others, inverted := locks.order[lockPair{key, outer.key}]; inverted {
			for _, other := range others {
				if other.goroutine != g {
					locks.reportInversion(here, other)
					break
				}
			}
		}
	}
	return !tracked
}

// Track gives the monitor the liveness probe of a lock that Acquire
// reported as untracked. The probe reports false once the collector has
// freed the lock.
func Track(lock unsafe.Pointer, alive func() bool) {
	key := uintptr(lock)
	locks.mu.Lock()
	defer locks.mu.Unlock()
	locks.alive[key] = alive
	if len(locks.alive) >= locks.sweepAt {
		locks.sweep()
	}
}

// Hold records that the calling goroutine now holds lock.
func Hold(lock unsafe.Pointer) {
	key := uintptr(lock)
	pc := callerPC(lockCallerSkip)
	g := goroutineID()
	locks.mu.Lock()
	defer locks.mu.Unlock()
	locks.held[g] = append(locks.held[g], heldLock{key: key, pc: pc})
	locks.holdStarted(g, key, pc)
}

// Release records that the calling goroutine no longer holds lock. It
// reports a long hold when another goroutine waited for lock.
func Release(lock unsafe.Pointer) {
	key := uintptr(lock)
	g := goroutineID()
	locks.mu.Lock()
	defer locks.mu.Unlock()
	locks.holdEnded(key)
	stack := locks.held[g]
	for i := len(stack) - 1; i >= 0; i-- {
		if stack[i].key != key {
			continue
		}
		stack = append(stack[:i], stack[i+1:]...)
		break
	}
	if len(stack) == 0 {
		delete(locks.held, g)
		return
	}
	locks.held[g] = stack
}

func (m *monitor) forgetIfFreed(key uintptr) {
	alive, tracked := m.alive[key]
	if !tracked || alive() {
		return
	}
	m.forget(key)
}

func (m *monitor) forget(key uintptr) {
	delete(m.alive, key)
	delete(m.holding, key)
	delete(m.pending, key)
	for pair := range m.order {
		if pair.first == key || pair.second == key {
			delete(m.order, pair)
		}
	}
}

func (m *monitor) sweep() {
	for key, alive := range m.alive {
		if !alive() {
			m.forget(key)
		}
	}
	m.sweepAt = max(firstSweepSize, 2*len(m.alive))
}

func (m *monitor) reportInversion(here, other pairSite) {
	a := [2]uintptr{here.firstPC, here.secondPC}
	b := [2]uintptr{other.firstPC, other.secondPC}
	if b[0] < a[0] || (b[0] == a[0] && b[1] < a[1]) {
		a, b = b, a
	}
	seen := [4]uintptr{a[0], a[1], b[0], b[1]}
	if _, done := m.reported[seen]; done {
		return
	}
	m.reported[seen] = struct{}{}
	write(finding{
		rule:    "conc::lock_order_inversion",
		message: "two goroutines take the same two Locked values in opposite orders; when both run at once, each waits for the other",
		positions: []position{
			at(here.secondPC, "this goroutine takes the second value here while it holds the first"),
			at(here.firstPC, "this goroutine took the first value here"),
			at(other.secondPC, "another goroutine took the values in the other order: second here"),
			at(other.firstPC, "another goroutine took the values in the other order: first here"),
		},
		hint: "take the two values in one order everywhere, or keep both in one Locked value",
	})
}

func callerPC(skip int) uintptr {
	var pcs [1]uintptr
	if runtime.Callers(skip, pcs[:]) == 0 {
		return 0
	}
	return pcs[0]
}

func at(pc uintptr, role string) position {
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	return position{file: frame.File, line: frame.Line, role: role}
}

type statementKey struct {
	file string
	line int
}

var positionsByPC sync.Map

func keyOf(pc uintptr) statementKey {
	if v, ok := positionsByPC.Load(pc); ok {
		return v.(statementKey)
	}
	frame, _ := runtime.CallersFrames([]uintptr{pc}).Next()
	key := statementKey{file: frame.File, line: frame.Line}
	actual, _ := positionsByPC.LoadOrStore(pc, key)
	return actual.(statementKey)
}

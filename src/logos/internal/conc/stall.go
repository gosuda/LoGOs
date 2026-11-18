// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conc

import (
	"os"
	"sync"
	"time"
	"unsafe"
)

// StallEnv names the Go duration that a group child may run after the
// cancellation of its group before the monitor reports it. The
// `logos conc` command sets it from its -stall flag. When it is empty
// or not a positive duration, the duration is DefaultStall.
const (
	StallEnv     = "LOGOSCONC_STALL"
	DefaultStall = 5 * time.Second
)

type groupChild struct {
	site    uintptr
	started time.Time
}

// group is the record of one group. cancelledAt is zero until the
// context of the group is cancelled. watchdog is nil when no timer
// waits to run reportStalled.
type group struct {
	children    map[uint64]groupChild
	cancelledAt time.Time
	watchdog    *time.Timer
}

type stalledChild struct {
	goroutine uint64
	site      uintptr
}

var groups = struct {
	mu       sync.Mutex
	open     map[uintptr]*group
	reported map[uintptr]struct{}
}{
	open:     map[uintptr]*group{},
	reported: map[uintptr]struct{}{},
}

var stallAfter = sync.OnceValue(func() time.Duration {
	d, err := time.ParseDuration(os.Getenv(StallEnv))
	if err != nil || d <= 0 {
		return DefaultStall
	}
	return d
})

// GroupCreated records a group, known by its address until GroupJoined.
func GroupCreated(key unsafe.Pointer) {
	groups.mu.Lock()
	defer groups.mu.Unlock()
	groups.open[uintptr(key)] = &group{children: map[uint64]groupChild{}}
}

// GroupCancelled records that the context of the group is cancelled.
// Only the first call for a group counts.
func GroupCancelled(key unsafe.Pointer) {
	groups.mu.Lock()
	defer groups.mu.Unlock()
	g := groups.open[uintptr(key)]
	if !g.cancelledAt.IsZero() {
		return
	}
	g.cancelledAt = time.Now()
	if len(g.children) > 0 {
		g.watchdog = time.AfterFunc(stallAfter(), g.reportStalled)
	}
}

// SpawnSite returns the statement that called the Group method which
// called the caller of SpawnSite.
func SpawnSite() uintptr {
	return callerPC(spawnCallerSkip)
}

// ChildStarted records that the calling goroutine runs a child of the
// group, started at site.
func ChildStarted(key unsafe.Pointer, site uintptr) {
	id := goroutineID()
	groups.mu.Lock()
	defer groups.mu.Unlock()
	g := groups.open[uintptr(key)]
	g.children[id] = groupChild{site: site, started: time.Now()}
	if !g.cancelledAt.IsZero() && g.watchdog == nil {
		g.watchdog = time.AfterFunc(stallAfter(), g.reportStalled)
	}
}

// ChildDone records that the child of the calling goroutine returned.
func ChildDone(key unsafe.Pointer) {
	id := goroutineID()
	groups.mu.Lock()
	defer groups.mu.Unlock()
	delete(groups.open[uintptr(key)].children, id)
}

// GroupJoined records that every child of the group has returned.
func GroupJoined(key unsafe.Pointer) {
	groups.mu.Lock()
	defer groups.mu.Unlock()
	g := groups.open[uintptr(key)]
	delete(groups.open, uintptr(key))
	if g.watchdog != nil {
		g.watchdog.Stop()
	}
}

func (g *group) reportStalled() {
	stalled := g.unreportedStalled()
	if len(stalled) == 0 {
		return
	}
	dump := allStacks()
	for _, child := range stalled {
		write(child.finding(dump))
	}
}

// unreportedStalled returns the children whose stall duration has
// ended, one per spawn statement that has no finding yet. It arms the
// watchdog for the child whose duration ends next.
func (g *group) unreportedStalled() []stalledChild {
	groups.mu.Lock()
	defer groups.mu.Unlock()
	g.watchdog = nil
	now := time.Now()
	var next time.Time
	var stalled []stalledChild
	for id, child := range g.children {
		due := g.stallEnd(child)
		if due.After(now) {
			if next.IsZero() || due.Before(next) {
				next = due
			}
			continue
		}
		if _, done := groups.reported[child.site]; done {
			continue
		}
		groups.reported[child.site] = struct{}{}
		stalled = append(stalled, stalledChild{goroutine: id, site: child.site})
	}
	if !next.IsZero() {
		g.watchdog = time.AfterFunc(next.Sub(now), g.reportStalled)
	}
	return stalled
}

// stallEnd is the time at which child has run for the stall duration
// in the cancelled group.
func (g *group) stallEnd(child groupChild) time.Time {
	if child.started.After(g.cancelledAt) {
		return child.started.Add(stallAfter())
	}
	return g.cancelledAt.Add(stallAfter())
}

func (c stalledChild) finding(dump string) finding {
	positions := []position{at(c.site, "the group started this child here")}
	for stack := range stacksOf(dump) {
		if stack.id != c.goroutine {
			continue
		}
		if where, found := stack.innermost(outsideToolchain); found {
			where.role = "the child is here now, in the state: " + stack.state
			positions = []position{where, positions[0]}
		}
		break
	}
	return finding{
		rule:      "conc::group_child_stalled",
		message:   "a group child did not return within " + stallAfter().String() + " after its group was cancelled; the group cannot join before the child returns",
		positions: positions,
		hint:      "make the child return when its context is done: receive from the Done channel of the context in a select, or give the context to the call that blocks",
	}
}

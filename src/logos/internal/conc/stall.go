// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conc

import (
	"strconv"
	"sync"
	"time"
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
	site uintptr
	// started is zero for a child that started before the cancellation
	// of its group.
	started time.Time
}

// Group is the record of one group. The group holds it, so the monitor
// has no table of groups, and the children of two groups do not wait
// for one lock. The record holds whether the group bounds the children
// that run at one time, and the `go group` statement that built it.
type Group struct {
	mu       sync.Mutex
	children map[uint64]groupChild
	// cancelledAt is zero until the context of the group is cancelled.
	cancelledAt time.Time
	// watchdog is nil when no timer waits to run reportStalled.
	watchdog *time.Timer
	// bounded says that the children that run at one time have a bound:
	// the limit of the group, or a bound that its owner keeps. A bounded
	// group is never reported as unbounded. It does not change after the
	// first child starts.
	bounded bool
	// statement is the `go group` statement that built the record.
	statement uintptr
	// unboundedAt is the running-children count at which the monitor
	// reports the group. The tests lower it.
	unboundedAt int
}

type stalledChild struct {
	goroutine uint64
	site      uintptr
}

var stalls = struct {
	mu       sync.Mutex
	reported map[uintptr]struct{}
}{
	reported: map[uintptr]struct{}{},
}

// unboundedAt is the number of children that run at one time in a
// group without a limit when the monitor reports the group. The
// monitor takes no flag and no environment variable for it; only the
// tests change it. NewGroup reads it once, so no child reads a shared
// variable.
var unboundedAt = 10000

// unbounded holds the `go group` statements that have a finding. One
// statement is reported one time in one process.
var unbounded = struct {
	mu       sync.Mutex
	reported map[uintptr]struct{}
}{
	reported: map[uintptr]struct{}{},
}

var stallAfter = sync.OnceValue(func() time.Duration {
	return durationSetting(StallEnv, DefaultStall)
})

// NewGroup returns the record of a new group. A limit above zero
// bounds the children that run at one time, so the monitor never
// reports that group as unbounded.
func NewGroup(limit int) *Group {
	return &Group{
		children:    map[uint64]groupChild{},
		bounded:     limit > 0,
		statement:   callerPC(groupCallerSkip),
		unboundedAt: unboundedAt,
	}
}

// Bounded records that the owner of the group bounds the children that
// run at one time, although the group has no limit. The owner calls it
// before it starts a child.
func (g *Group) Bounded() {
	g.bounded = true
}

// Cancelled records that the context of the group is cancelled. Only
// the first call counts.
func (g *Group) Cancelled() {
	g.mu.Lock()
	defer g.mu.Unlock()
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
// group, started at site. In a group without a bound it also reports
// the group statement when the running children reach the count of the
// monitor. It reports at that count only, so a larger group takes no
// lock of the monitor for a later child.
func (g *Group) ChildStarted(site uintptr) {
	running := g.admit(goroutineID(), site)
	if g.bounded || running != g.unboundedAt || !firstUnboundedAt(g.statement) {
		return
	}
	write(g.unboundedFinding(running, site))
}

// admit records the child and returns the number of children that run.
func (g *Group) admit(id uint64, site uintptr) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	child := groupChild{site: site}
	if !g.cancelledAt.IsZero() {
		child.started = time.Now()
		if g.watchdog == nil {
			g.watchdog = time.AfterFunc(stallAfter(), g.reportStalled)
		}
	}
	g.children[id] = child
	return len(g.children)
}

// ChildDone records that the child of the calling goroutine returned.
func (g *Group) ChildDone() {
	id := goroutineID()
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.children, id)
}

// firstUnboundedAt reports whether the group statement has no finding
// yet, and records it.
func firstUnboundedAt(statement uintptr) bool {
	unbounded.mu.Lock()
	defer unbounded.mu.Unlock()
	if _, done := unbounded.reported[statement]; done {
		return false
	}
	unbounded.reported[statement] = struct{}{}
	return true
}

// unboundedFinding is the finding for a group without a limit whose
// running children reached the count of the monitor.
func (g *Group) unboundedFinding(count int, site uintptr) finding {
	return finding{
		rule:    "conc::group_unbounded",
		message: "a group without a limit runs " + strconv.Itoa(count) + " children at one time; the count follows the work the program gives the group",
		positions: []position{
			at(g.statement, "this go group has no limit"),
			at(site, "the child that made the count reach "+strconv.Itoa(count)+" started here"),
		},
		hint: "give the group a limit: go group (ctx, limit = N)",
	}
}

// Joined records that every child of the group has returned.
func (g *Group) Joined() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.watchdog != nil {
		g.watchdog.Stop()
	}
}

func (g *Group) reportStalled() {
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
func (g *Group) unreportedStalled() []stalledChild {
	g.mu.Lock()
	defer g.mu.Unlock()
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
		if firstStallAt(child.site) {
			stalled = append(stalled, stalledChild{goroutine: id, site: child.site})
		}
	}
	if !next.IsZero() {
		g.watchdog = time.AfterFunc(next.Sub(now), g.reportStalled)
	}
	return stalled
}

func firstStallAt(site uintptr) bool {
	stalls.mu.Lock()
	defer stalls.mu.Unlock()
	if _, done := stalls.reported[site]; done {
		return false
	}
	stalls.reported[site] = struct{}{}
	return true
}

// stallEnd is the time at which child has run for the stall duration
// in the cancelled group.
func (g *Group) stallEnd(child groupChild) time.Time {
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

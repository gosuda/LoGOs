// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conc_test

import (
	"encoding/json"
	"logos/internal/conc"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
	"unsafe"
)

var sinkPath string

const (
	stall = 50 * time.Millisecond
	hold  = 50 * time.Millisecond
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "logosconc")
	if err != nil {
		panic(err)
	}
	sinkPath = filepath.Join(dir, "findings.jsonl")
	if err := os.WriteFile(sinkPath, nil, 0o600); err != nil {
		panic(err)
	}
	os.Setenv(conc.SinkEnv, sinkPath)
	os.Setenv(conc.StallEnv, stall.String())
	os.Setenv(conc.HoldEnv, hold.String())
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type reported struct {
	Rule      string `json:"rule"`
	Message   string `json:"message"`
	Positions []struct {
		File string `json:"file"`
		Line int    `json:"line"`
		Role string `json:"role"`
	} `json:"positions"`
	Fix struct {
		Hint string `json:"hint"`
	} `json:"fix"`
	Pid int `json:"pid"`
}

// findingsAt returns the findings of this process that name one of the
// given lines of this file. The monitor reports a pair of positions once
// per process, so the result does not depend on how often a test ran.
func findingsAt(t *testing.T, lines ...int) []reported {
	t.Helper()
	data, err := os.ReadFile(sinkPath)
	if err != nil {
		t.Fatal(err)
	}
	var out []reported
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		if len(line)+1 > conc.MaxLine {
			t.Fatalf("finding line holds %d bytes, limit %d", len(line)+1, conc.MaxLine)
		}
		var r reported
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("finding line is not JSON: %v\n%s", err, line)
		}
		for _, p := range r.Positions {
			if filepath.Base(p.File) == "conc_test.go" && contains(lines, p.Line) {
				out = append(out, r)
				break
			}
		}
	}
	return out
}

func contains(lines []int, line int) bool {
	for _, l := range lines {
		if l == line {
			return true
		}
	}
	return false
}

func nextLine() int {
	_, _, line, _ := runtime.Caller(1)
	return line + 1
}

type probe struct{ freed bool }

func (p *probe) alive() bool { return !p.freed }

// testLocks keeps every lock of these tests reachable. A probe of a
// test says that its lock is alive, so the collector must not give the
// address of that lock to the lock of a later test.
var testLocks []*int

func twoLocks() (a, b unsafe.Pointer) {
	first, second := new(int), new(int)
	testLocks = append(testLocks, first, second)
	return unsafe.Pointer(first), unsafe.Pointer(second)
}

// take stands where logos/sync.Locked.WithLock stands: the monitor
// reports the line that calls take.
func take(lock unsafe.Pointer, p *probe, inner func()) {
	if conc.Acquire(lock) {
		conc.Track(lock, p.alive)
	}
	conc.Hold(lock)
	defer conc.Release(lock)
	inner()
}

func inGoroutine(body func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		body()
	}()
	<-done
}

func TestOppositeOrdersInTwoGoroutinesAreReportedOnce(t *testing.T) {
	pa, pb := new(probe), new(probe)
	ka, kb := twoLocks()

	var lineAB, lineBA int
	aThenB := func() {
		lineAB = nextLine()
		take(ka, pa, func() { take(kb, pb, func() {}) })
	}
	bThenA := func() {
		lineBA = nextLine()
		take(kb, pb, func() { take(ka, pa, func() {}) })
	}
	inGoroutine(aThenB)
	inGoroutine(bThenA)
	inGoroutine(aThenB)
	inGoroutine(bThenA)

	got := findingsAt(t, lineAB, lineBA)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Rule != "conc::lock_order_inversion" {
		t.Errorf("rule = %q", f.Rule)
	}
	if f.Pid != os.Getpid() {
		t.Errorf("pid = %d, want %d", f.Pid, os.Getpid())
	}
	if f.Fix.Hint == "" {
		t.Error("the finding has no fix hint")
	}
	if len(f.Positions) != 4 {
		t.Fatalf("got %d positions, want 4", len(f.Positions))
	}
	named := map[int]int{}
	for _, p := range f.Positions {
		named[p.Line]++
	}
	if named[lineAB] != 2 || named[lineBA] != 2 {
		t.Errorf("positions name lines %v, want lines %d and %d twice each", named, lineAB, lineBA)
	}
}

func TestBothOrdersInOneGoroutineAreSilent(t *testing.T) {
	pa, pb := new(probe), new(probe)
	ka, kb := twoLocks()
	var first, second int
	inGoroutine(func() {
		first = nextLine()
		take(ka, pa, func() { take(kb, pb, func() {}) })
		second = nextLine()
		take(kb, pb, func() { take(ka, pa, func() {}) })
	})
	if got := findingsAt(t, first, second); len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestOneOrderInTwoGoroutinesIsSilent(t *testing.T) {
	pa, pb := new(probe), new(probe)
	ka, kb := twoLocks()
	var line int
	aThenB := func() {
		line = nextLine()
		take(ka, pa, func() { take(kb, pb, func() {}) })
	}
	inGoroutine(aThenB)
	inGoroutine(aThenB)
	if got := findingsAt(t, line); len(got) != 0 {
		t.Fatalf("got %d findings, want 0: %+v", len(got), got)
	}
}

func TestAFreedLockTakesItsPairsWithIt(t *testing.T) {
	old, pb := new(probe), new(probe)
	ka, kb := twoLocks()
	var stale, second, third int
	inGoroutine(func() {
		stale = nextLine()
		take(ka, old, func() { take(kb, pb, func() {}) })
	})

	old.freed = true
	reused := new(probe)
	inGoroutine(func() {
		second = nextLine()
		take(kb, pb, func() { take(ka, reused, func() {}) })
	})
	if got := findingsAt(t, stale); len(got) != 0 {
		t.Fatalf("a new lock at a freed address inherited a pair: %+v", got)
	}

	inGoroutine(func() {
		third = nextLine()
		take(ka, reused, func() { take(kb, pb, func() {}) })
	})
	if got := findingsAt(t, third); len(got) != 1 {
		t.Fatalf("the lock at the reused address lost its own pair with line %d: got %d findings, want 1", second, len(got))
	}
}

// group stands where logos/rt.Group stands: the monitor reports the
// line that calls spawn.
type group struct {
	record   *conc.Group
	children sync.WaitGroup
}

func newGroup(limit int) *group {
	return &group{record: newRecord(limit)}
}

// newRecord stands where monitorCreated stands: the record holds the
// line that calls newGroup.
func newRecord(limit int) *conc.Group { return conc.NewGroup(limit) }

func (g *group) cancelled() { g.record.Cancelled() }

func (g *group) spawn(body func()) {
	site := g.site()
	g.children.Go(func() {
		g.record.ChildStarted(site)
		defer g.record.ChildDone()
		body()
	})
}

func (g *group) site() uintptr { return conc.SpawnSite() }

func (g *group) join() {
	g.children.Wait()
	g.record.Joined()
}

func TestGroupChildThatOutlivesCancellationIsReportedOncePerSpawnStatement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newGroup(0)
		release := make(chan struct{})
		var line int
		for range 3 {
			line = nextLine()
			g.spawn(func() { <-release })
		}
		synctest.Wait()
		g.cancelled()
		time.Sleep(stall)
		synctest.Wait()

		got := findingsAt(t, line)
		if len(got) != 1 {
			t.Fatalf("got %d findings %v after the stall duration, want 1: %+v", len(got), stall, got)
		}
		f := got[0]
		if f.Rule != "conc::group_child_stalled" || !strings.Contains(f.Message, "within "+stall.String()+" after") || f.Fix.Hint == "" || f.Pid != os.Getpid() {
			t.Errorf("finding = %+v", f)
		}
		// This file is below the toolchain sources, so the finding has no
		// frame of the child and names the spawn statement only.
		if len(f.Positions) != 1 || f.Positions[0].Line != line {
			t.Errorf("positions = %+v, want line %d only", f.Positions, line)
		}
		close(release)
		g.join()
	})
}

func TestGroupChildThatReturnsBeforeItsStallDurationEndsIsSilent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newGroup(0)
		cancelled := make(chan struct{})
		early := nextLine()
		g.spawn(func() { <-cancelled; time.Sleep(stall - time.Millisecond) })
		synctest.Wait()
		g.cancelled()
		close(cancelled)
		time.Sleep(stall / 2)
		late := nextLine()
		g.spawn(func() { time.Sleep(stall - time.Millisecond) })
		time.Sleep(10 * stall)
		synctest.Wait()

		if got := findingsAt(t, early, late); len(got) != 0 {
			t.Errorf("two children that returned 1ms before the end of their durations gave %+v", got)
		}
		g.join()
	})
}

func TestGroupChildThatStartsAfterCancellationIsWatchedFromItsStart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newGroup(0)
		g.cancelled()
		time.Sleep(3 * stall)
		release := make(chan struct{})
		line := nextLine()
		g.spawn(func() { <-release })
		synctest.Wait()
		time.Sleep(stall)
		synctest.Wait()

		if got := findingsAt(t, line); len(got) != 1 {
			t.Errorf("got %d findings %v after the child started in a cancelled group, want 1: %+v", len(got), stall, got)
		}
		close(release)
		g.join()
	})
}

func TestYoungerGroupChildIsReportedWhenItsOwnStallDurationEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newGroup(0)
		release := make(chan struct{})
		older := nextLine()
		g.spawn(func() { <-release })
		synctest.Wait()
		g.cancelled()
		time.Sleep(stall / 2)
		younger := nextLine()
		g.spawn(func() { <-release })
		synctest.Wait()
		time.Sleep(stall)
		synctest.Wait()

		if a, b := findingsAt(t, older), findingsAt(t, younger); len(a) != 1 || len(b) != 1 {
			t.Errorf("the older child gave %d findings and the younger child gave %d, want 1 and 1", len(a), len(b))
		}
		close(release)
		g.join()
	})
}

func TestUnboundedGroupIsReportedWhenTheCountReachesTheLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := conc.SetUnboundedAt(4)
		defer conc.SetUnboundedAt(old)
		release := make(chan struct{})
		var statement int
		statement = nextLine()
		g := newGroup(0)
		var spawn int
		for range 4 {
			spawn = nextLine()
			g.spawn(func() { <-release })
		}
		synctest.Wait()

		got := findingsAt(t, statement, spawn)
		if len(got) != 1 {
			t.Fatalf("got %d findings at the limit of 4 children, want 1: %+v", len(got), got)
		}
		f := got[0]
		if f.Rule != "conc::group_unbounded" || !strings.Contains(f.Message, "4 children") || f.Fix.Hint == "" || f.Pid != os.Getpid() {
			t.Errorf("finding = %+v", f)
		}
		if len(f.Positions) != 2 || f.Positions[0].Line != statement || f.Positions[1].Line != spawn {
			t.Errorf("positions = %+v, want the group statement %d first and the spawn %d second", f.Positions, statement, spawn)
		}
		close(release)
		g.join()
	})
}

func TestUnboundedGroupBelowTheLimitIsSilent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := conc.SetUnboundedAt(4)
		defer conc.SetUnboundedAt(old)
		release := make(chan struct{})
		var statement int
		statement = nextLine()
		g := newGroup(0)
		var spawn int
		for range 3 {
			spawn = nextLine()
			g.spawn(func() { <-release })
		}
		synctest.Wait()

		if got := findingsAt(t, statement, spawn); len(got) != 0 {
			t.Errorf("3 children in a group whose limit is 4 gave %+v", got)
		}
		close(release)
		g.join()
	})
}

func TestLimitedGroupIsNeverReportedAsUnbounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := conc.SetUnboundedAt(2)
		defer conc.SetUnboundedAt(old)
		release := make(chan struct{})
		var statement int
		statement = nextLine()
		g := newGroup(1)
		var spawn int
		for range 6 {
			spawn = nextLine()
			g.spawn(func() { <-release })
		}
		synctest.Wait()

		if got := findingsAt(t, statement, spawn); len(got) != 0 {
			t.Errorf("a group with a limit gave %+v", got)
		}
		close(release)
		g.join()
	})
}

func TestGroupThatItsOwnerBoundsIsNeverReportedAsUnbounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := conc.SetUnboundedAt(2)
		defer conc.SetUnboundedAt(old)
		release := make(chan struct{})
		statement := nextLine()
		g := newGroup(0)
		g.record.Bounded()
		var spawn int
		for range 6 {
			spawn = nextLine()
			g.spawn(func() { <-release })
		}
		synctest.Wait()

		if got := findingsAt(t, statement, spawn); len(got) != 0 {
			t.Errorf("a group that its owner bounds gave %+v", got)
		}
		close(release)
		g.join()
	})
}

func TestUnboundedGroupIsReportedOncePerStatement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		old := conc.SetUnboundedAt(2)
		defer conc.SetUnboundedAt(old)
		release := make(chan struct{})
		var statement int
		statement = nextLine()
		groups := []*group{newGroup(0), newGroup(0)}
		for _, g := range groups {
			g.spawn(func() { <-release })
			g.spawn(func() { <-release })
		}
		synctest.Wait()

		if got := findingsAt(t, statement); len(got) != 1 {
			t.Fatalf("two groups from one statement gave %d findings, want 1: %+v", len(got), got)
		}
		close(release)
		for _, g := range groups {
			g.join()
		}
	})
}

func TestChildrenThatReturnKeepTheCountBelowTheLimit(t *testing.T) {
	old := conc.SetUnboundedAt(2)
	defer conc.SetUnboundedAt(old)
	var statement int
	statement = nextLine()
	g := newGroup(0)
	for range 6 {
		g.spawn(func() {})
		g.join()
	}

	if got := findingsAt(t, statement); len(got) != 0 {
		t.Errorf("children that return one at a time gave %+v", got)
	}
	g.join()
}

// leakFindings runs check until the monitor has a finding at line, and
// returns the findings at line.
func leakFindings(t *testing.T, line int, check func()) []reported {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); runtime.Gosched() {
		check()
		if got := findingsAt(t, line); len(got) > 0 {
			return got
		}
	}
	t.Fatalf("no finding at line %d in 30s", line)
	return nil
}

func linesOf(f reported) []int {
	var lines []int
	for _, p := range f.Positions {
		lines = append(lines, p.Line)
	}
	return lines
}

func TestLeakedGoroutineIsReportedOnceWithItsWaitAndItsStart(t *testing.T) {
	var starts int
	for range 2 {
		unreachable := make(chan int)
		starts = nextLine()
		go func() {
			<-unreachable
		}()
	}
	waits := starts + 1
	leakFindings(t, waits, conc.TestLeakWatch.Report)
	conc.TestLeakWatch.Report()

	got := findingsAt(t, waits)
	if len(got) != 1 {
		t.Fatalf("two goroutines that leak at one statement gave %d findings, want 1: %+v", len(got), got)
	}
	f := got[0]
	if f.Rule != "conc::goroutine_leak" || f.Positions[0].Role != "the goroutine waits here, in runtime.chanrecv1" || f.Fix.Hint == "" {
		t.Errorf("finding = %+v", f)
	}
	if lines := linesOf(f); len(lines) != 2 || lines[0] != waits || lines[1] != starts {
		t.Errorf("positions at lines %v, want the wait at %d and then the start at %d", lines, waits, starts)
	}
}

func TestWaitingGoroutineThatAnotherCanWakeIsNotALeak(t *testing.T) {
	reachable := make(chan int)
	starts := nextLine()
	go func() {
		<-reachable
	}()
	for range 20 {
		runtime.Gosched()
		conc.TestLeakWatch.Report()
	}
	if got := findingsAt(t, starts, starts+1); len(got) != 0 {
		t.Errorf("a goroutine that waits on a channel of this test gave %+v", got)
	}
	reachable <- 1
}

// waitLine is the line of the receive in waitOn.
var waitLine = nextLine() + 2

func waitOn(c chan int) {
	<-c
}

func TestLeakNamesTheInnermostWaitAndTheOutermostFunctionOfTheUser(t *testing.T) {
	unreachable := make(chan int)
	starts := nextLine()
	go func() {
		waitOn(unreachable)
	}()
	got := leakFindings(t, waitLine, conc.TestLeakWatch.Report)
	if lines := linesOf(got[0]); len(got) != 1 || len(lines) != 2 || lines[0] != waitLine || lines[1] != starts {
		t.Errorf("got %d findings with positions at lines %v, want 1 with the wait at %d and then the function at %d", len(got), lines, waitLine, starts)
	}
}

func TestLeakWatchChecksAfterACollection(t *testing.T) {
	unreachable := make(chan int)
	starts := nextLine()
	go func() {
		<-unreachable
	}()
	conc.TestLeakWatch.CheckAfterEachCollection()
	leakFindings(t, starts+1, runtime.GC)
}

// lockedValue stands where a logos/sync.Locked value stands. Its mutex
// is a channel, so a goroutine that waits for it in a synctest bubble is
// durably blocked.
type lockedValue struct {
	key   unsafe.Pointer
	mutex chan struct{}
}

func newLockedValue() *lockedValue {
	key, _ := twoLocks()
	return &lockedValue{key: key, mutex: make(chan struct{}, 1)}
}

// with stands where WithLock stands: the monitor reports the line that
// calls with.
func (v *lockedValue) with(inner func()) {
	if conc.Acquire(v.key) {
		conc.Track(v.key, func() bool { return true })
	}
	v.mutex <- struct{}{}
	conc.Hold(v.key)
	defer func() {
		conc.Release(v.key)
		<-v.mutex
	}()
	inner()
}

func TestLongHoldIsReportedWhenAWaiterComesAfterTheLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newLockedValue()
		var both sync.WaitGroup
		holds := nextLine() + 1
		both.Go(func() {
			v.with(func() { time.Sleep(3 * hold) })
		})
		synctest.Wait()
		time.Sleep(2 * hold)
		waits := nextLine() + 1
		both.Go(func() {
			v.with(func() {})
		})
		synctest.Wait()

		got := findingsAt(t, holds)
		if len(got) != 1 {
			t.Fatalf("got %d findings while the value is still held, want 1: %+v", len(got), got)
		}
		f := got[0]
		if f.Rule != "conc::lock_held_long" || !strings.Contains(f.Message, "for "+(2*hold).String()+" while") || !strings.Contains(f.Message, "the limit is "+hold.String()) || f.Fix.Hint == "" {
			t.Errorf("finding = %+v", f)
		}
		if lines := linesOf(f); len(lines) != 2 || lines[0] != holds || lines[1] != waits {
			t.Errorf("positions at lines %v, want the holder at %d and then the waiter at %d", lines, holds, waits)
		}
		both.Wait()
		if got := findingsAt(t, holds); len(got) != 1 {
			t.Errorf("got %d findings after the hold ended, want the 1 of the waiter's arrival: %+v", len(got), got)
		}
	})
}

func TestLongHoldIsReportedAtItsEndWhenTheWaiterCameBeforeTheLimit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		v := newLockedValue()
		var both sync.WaitGroup
		holds := nextLine() + 1
		both.Go(func() {
			v.with(func() { time.Sleep(3 * hold) })
		})
		synctest.Wait()
		time.Sleep(hold / 2)
		waits := nextLine() + 1
		both.Go(func() {
			v.with(func() {})
		})
		both.Wait()

		got := findingsAt(t, holds)
		if len(got) != 1 {
			t.Fatalf("got %d findings after the hold of %v ended, want 1: %+v", len(got), 3*hold, got)
		}
		if lines := linesOf(got[0]); len(lines) != 2 || lines[0] != holds || lines[1] != waits {
			t.Errorf("positions at lines %v, want the holder at %d and then the waiter at %d", lines, holds, waits)
		}
	})
}

func TestHoldWithoutAWaiterAndShortHoldAreSilent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		alone := newLockedValue()
		longAlone := nextLine()
		alone.with(func() { time.Sleep(3 * hold) })

		v := newLockedValue()
		var both sync.WaitGroup
		short := nextLine() + 1
		both.Go(func() {
			v.with(func() { time.Sleep(hold - time.Millisecond) })
		})
		synctest.Wait()
		waits := nextLine() + 1
		both.Go(func() {
			v.with(func() {})
		})
		both.Wait()

		if got := findingsAt(t, longAlone, short, waits); len(got) != 0 {
			t.Errorf("a long hold that nobody waited for and a hold 1ms below the limit gave %+v", got)
		}
	})
}

func TestSecondCancellationDoesNotRestartTheStallDuration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newGroup(0)
		release := make(chan struct{})
		line := nextLine()
		g.spawn(func() { <-release })
		synctest.Wait()
		g.cancelled()
		time.Sleep(stall / 2)
		g.cancelled()
		time.Sleep(stall / 2)
		synctest.Wait()

		if got := findingsAt(t, line); len(got) != 1 {
			t.Errorf("got %d findings %v after the first cancellation, want 1: %+v", len(got), stall, got)
		}
		close(release)
		g.join()
	})
}

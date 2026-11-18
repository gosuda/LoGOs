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

const stall = 50 * time.Millisecond

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
	var a, b int
	pa, pb := new(probe), new(probe)
	ka, kb := unsafe.Pointer(&a), unsafe.Pointer(&b)

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
	var a, b int
	pa, pb := new(probe), new(probe)
	ka, kb := unsafe.Pointer(&a), unsafe.Pointer(&b)
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
	var a, b int
	pa, pb := new(probe), new(probe)
	ka, kb := unsafe.Pointer(&a), unsafe.Pointer(&b)
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
	var a, b int
	old, pb := new(probe), new(probe)
	ka, kb := unsafe.Pointer(&a), unsafe.Pointer(&b)
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
	key       unsafe.Pointer
	cancelled func()
	children  sync.WaitGroup
}

func newGroup() *group {
	g := new(group)
	g.key = unsafe.Pointer(g)
	g.cancelled = conc.GroupCreated(g.key)
	return g
}

func (g *group) spawn(body func()) {
	site := g.site()
	g.children.Go(func() {
		conc.ChildStarted(g.key, site)
		defer conc.ChildDone(g.key)
		body()
	})
}

func (g *group) site() uintptr { return conc.SpawnSite() }

func (g *group) join() {
	g.children.Wait()
	conc.GroupJoined(g.key)
}

func TestGroupChildThatOutlivesCancellationIsReportedOncePerSpawnStatement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		g := newGroup()
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
		g := newGroup()
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
		g := newGroup()
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
		g := newGroup()
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

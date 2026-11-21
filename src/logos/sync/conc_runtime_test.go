// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build logosconc

package sync_test

import (
	"encoding/json"
	"logos/sync"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

var monitorSink string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "logosconc")
	if err != nil {
		panic(err)
	}
	monitorSink = filepath.Join(dir, "findings.jsonl")
	if err := os.WriteFile(monitorSink, nil, 0o600); err != nil {
		panic(err)
	}
	os.Setenv("LOGOSCONC_SINK", monitorSink)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type account struct{ balance int }

var packageLevel sync.Locked[account]

type monitorFinding struct {
	Rule      string `json:"rule"`
	Positions []struct {
		File string `json:"file"`
		Line int    `json:"line"`
	} `json:"positions"`
}

// findingsNaming returns the findings of this process that name one of
// the given lines of this file. The monitor reports a pair of positions
// once per process, so the result does not depend on -count.
func findingsNaming(t *testing.T, lines ...int) []monitorFinding {
	t.Helper()
	data, err := os.ReadFile(monitorSink)
	if err != nil {
		t.Fatal(err)
	}
	var out []monitorFinding
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var f monitorFinding
		if err := json.Unmarshal([]byte(line), &f); err != nil {
			t.Fatalf("finding line is not JSON: %v\n%s", err, line)
		}
		for _, p := range f.Positions {
			named := false
			for _, l := range lines {
				named = named || l == p.Line
			}
			if named && filepath.Base(p.File) == "conc_runtime_test.go" {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

func lineBelow() int {
	_, _, line, _ := runtime.Caller(1)
	return line + 1
}

func runAndWait(body func()) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		body()
	}()
	<-done
}

func TestMonitoredWithLockIsSilentForOneOrder(t *testing.T) {
	var first, second sync.Locked[account]
	var outer, inner int
	nested := func() {
		outer = lineBelow()
		first.WithLock(func(a *account) {
			inner = lineBelow()
			second.WithLock(func(b *account) { b.balance = a.balance + 1 })
		})
	}
	runAndWait(nested)
	runAndWait(nested)
	if got := findingsNaming(t, outer, inner); len(got) != 0 {
		t.Fatalf("one order in two goroutines gave findings: %+v", got)
	}
}

func TestMonitoredWithLockHoldsNothingAfterItReturns(t *testing.T) {
	var first, second sync.Locked[account]
	var lines [4]int
	runAndWait(func() {
		lines[0] = lineBelow()
		first.WithLock(func(a *account) { a.balance++ })
		func() {
			defer func() { recover() }()
			lines[1] = lineBelow()
			first.WithLock(func(*account) { panic("stop") })
		}()
		lines[2] = lineBelow()
		second.WithLock(func(b *account) { b.balance++ })
	})
	runAndWait(func() {
		second.WithLock(func(b *account) { b.balance++ })
		lines[3] = lineBelow()
		first.WithLock(func(a *account) { a.balance++ })
	})
	if got := findingsNaming(t, lines[:]...); len(got) != 0 {
		t.Fatalf("locks taken one after the other gave findings: %+v", got)
	}
}

func TestMonitoredWithLockReportsOppositeOrders(t *testing.T) {
	var local sync.Locked[account]
	var lines [4]int
	runAndWait(func() {
		lines[0] = lineBelow()
		packageLevel.WithLock(func(p *account) {
			lines[1] = lineBelow()
			local.WithLock(func(l *account) { l.balance = p.balance + 1 })
		})
	})
	runAndWait(func() {
		lines[2] = lineBelow()
		local.WithLock(func(l *account) {
			lines[3] = lineBelow()
			packageLevel.WithLock(func(p *account) { p.balance = l.balance + 1 })
		})
	})

	got := findingsNaming(t, lines[:]...)
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1: %+v", len(got), got)
	}
	if got[0].Rule != "conc::lock_order_inversion" {
		t.Fatalf("rule = %q", got[0].Rule)
	}
	named := map[int]bool{}
	for _, p := range got[0].Positions {
		named[p.Line] = true
	}
	for _, line := range lines {
		if !named[line] {
			t.Errorf("no position names line %d; positions: %+v", line, got[0].Positions)
		}
	}
}

func TestMonitoredWithLockKeepsTheContract(t *testing.T) {
	var guarded sync.Locked[account]
	guarded.WithLock(func(a *account) { a.balance = 7 })
	func() {
		defer func() { recover() }()
		guarded.WithLock(func(*account) { panic("stop") })
	}()
	balance := 0
	guarded.WithLock(func(a *account) { balance = a.balance })
	if balance != 7 {
		t.Fatalf("balance = %d after a panic in a later callback, want 7", balance)
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conc

import (
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The leak watch runs a collection that finds leaked goroutines. A
// short program gets timedLeakChecks checks, the first one after
// firstLeakCheck and each next one after twice the wait. After them the
// watch has no timer: a timer keeps a deadlocked program alive, and the
// runtime must still end such a program. Each later check follows a
// collection of the program, leakCheckSpacing or more after the check
// before it.
//
// A check reads the stacks of the leaked goroutines only, so its cost
// does not grow with the number of goroutines that run.
const (
	firstLeakCheck   = 100 * time.Millisecond
	timedLeakChecks  = 6
	leakCheckSpacing = time.Minute
)

type leakWatch struct {
	// written reports whether the user wrote the source file.
	written func(file string) bool
	spacing time.Duration
}

// leakSites holds the two positions of a leak finding.
type leakSites [2]position

// collectionMark is large enough to get its own heap object, so the
// collector frees it at the first collection after its last use.
type collectionMark struct{ _ [4]uintptr }

var leaks = struct {
	mu       sync.Mutex
	reported map[leakSites]struct{}
}{
	reported: map[leakSites]struct{}{},
}

func init() {
	go leakWatch{written: outsideToolchain, spacing: leakCheckSpacing}.run()
}

func (w leakWatch) run() {
	start := time.Now()
	wait := firstLeakCheck
	for range timedLeakChecks {
		time.Sleep(time.Until(start.Add(wait)))
		w.report()
		wait *= 2
	}
	w.checkAfterCollection(time.Now())
}

// checkAfterCollection runs one check after the next collection that
// ends w.spacing or more after last, and then waits for the collection
// after that check.
func (w leakWatch) checkAfterCollection(last time.Time) {
	runtime.AddCleanup(new(collectionMark), func(last time.Time) {
		if time.Since(last) < w.spacing {
			w.checkAfterCollection(last)
			return
		}
		go func() {
			w.report()
			w.checkAfterCollection(time.Now())
		}()
	}, last)
}

// report asks the runtime which goroutines wait on something that no
// goroutine that can run reaches, and writes one finding for each pair
// of positions that has none yet. The profile has one line of the form
// `<count> @ <pc> <pc> ...` for each stack of leaked goroutines.
func (w leakWatch) report() {
	var profile strings.Builder
	if err := pprof.Lookup("goroutineleak").WriteTo(&profile, 1); err != nil {
		return
	}
	for line := range strings.SplitSeq(profile.String(), "\n") {
		_, stack, isStack := strings.Cut(line, " @ ")
		if !isStack {
			continue
		}
		sites, found := w.leakSites(stack)
		if !found || !firstLeakAt(sites) {
			continue
		}
		write(finding{
			rule:      "conc::goroutine_leak",
			message:   "a goroutine waits, and no goroutine that can run reaches what it waits for; it never ends",
			positions: sites[:],
			hint:      "give the wait an end: let the owner close the channel or release the lock, or wait on a context in the same select",
		})
	}
}

// leakSites reads the program counters of one leaked stack, innermost
// first. It returns where the user's code waits, with the call that
// waits, and where the outermost function of the user starts: that
// function is what the goroutine was started to run.
func (w leakWatch) leakSites(stack string) (sites leakSites, found bool) {
	var pcs []uintptr
	for text := range strings.FieldsSeq(stack) {
		pc, err := strconv.ParseUint(text, 0, 64)
		if err != nil {
			return leakSites{}, false
		}
		pcs = append(pcs, uintptr(pc))
	}
	waitsIn := ""
	frames := runtime.CallersFrames(pcs)
	for more := len(pcs) > 0; more; {
		var frame runtime.Frame
		frame, more = frames.Next()
		if !w.written(frame.File) {
			waitsIn = frame.Function
			continue
		}
		if !found {
			sites[0] = position{file: frame.File, line: frame.Line, role: "the goroutine waits here, in " + waitsIn}
			found = true
		}
		if frame.Func != nil {
			file, line := frame.Func.FileLine(frame.Func.Entry())
			sites[1] = position{file: file, line: line, role: "the goroutine runs the function that starts here"}
		}
	}
	return sites, found && sites[1] != position{}
}

func firstLeakAt(sites leakSites) bool {
	leaks.mu.Lock()
	defer leaks.mu.Unlock()
	if _, done := leaks.reported[sites]; done {
		return false
	}
	leaks.reported[sites] = struct{}{}
	return true
}

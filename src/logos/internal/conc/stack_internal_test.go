// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conc

import (
	"strings"
	"testing"
)

const stackDump = `goroutine 71 [running]:
main.busy(...)
	/home/dev/jobs/busy.lgo:9 +0x1d
created by main.main in goroutine 1
	/home/dev/jobs/main.lgo:40 +0x5a

goroutine 7 [chan receive, 2 minutes]:
runtime.gopark(0x0?, 0x0?, 0x0?, 0x0?, 0x0?)
	/usr/lib/logos/src/runtime/proc.go:461 +0xce
runtime.chanrecv(0xc000022070, 0x0, 0x1)
	/usr/lib/logos/src/runtime/chan.go:667 +0x445
example.com/jobs/queue.(*Queue).Next(...)
	/home/dev/jobs/queue/queue.lgo:31
main.worker({0x4f2a38?, 0xc000012230?})
	/home/dev/jobs/main.lgo:22 +0x45
logos/rt.monitored.func1({0x4f2a38, 0xc000012230})
	/usr/lib/logos/src/logos/rt/group_conc_runtime.go:34 +0x8b
created by logos/rt.(*Group).Go in goroutine 1
	/usr/lib/logos/src/logos/rt/group.go:78 +0x12

goroutine 9 [sleep]:
time.Sleep(0x3b9aca00)
	/usr/lib/logos/src/runtime/time.go:363 +0x165
created by main.main in goroutine 1
	/home/dev/jobs/main.lgo:41 +0x6c
`

func TestStacksOfReadsEachGoroutineOfADump(t *testing.T) {
	outside := func(file string) bool { return !strings.HasPrefix(file, "/usr/lib/logos/src/") }
	nothing := func(string) bool { return false }
	none := position{}
	tests := []struct {
		name      string
		id        uint64
		written   func(string) bool
		state     string
		innermost position
	}{
		{"an inlined frame of the user below two runtime frames", 7, outside, "chan receive", position{file: "/home/dev/jobs/queue/queue.lgo", line: 31}},
		{"the first goroutine of the dump, whose id starts another id", 71, outside, "running", position{file: "/home/dev/jobs/busy.lgo", line: 9}},
		{"toolchain frames only; the frame that started it is not a frame of it", 9, outside, "sleep", none},
		{"a binary that records no source directory", 7, nothing, "chan receive", none},
	}
	for _, tt := range tests {
		found := false
		for stack := range stacksOf(stackDump) {
			if stack.id != tt.id {
				continue
			}
			found = true
			if innermost, _ := stack.innermost(tt.written); stack.state != tt.state || innermost != tt.innermost {
				t.Errorf("%s: got state %q innermost %+v; want state %q innermost %+v", tt.name, stack.state, innermost, tt.state, tt.innermost)
			}
		}
		if !found {
			t.Errorf("%s: the dump gave no goroutine %d", tt.name, tt.id)
		}
	}
	ids := 0
	for range stacksOf(stackDump) {
		ids++
	}
	if ids != 3 {
		t.Errorf("the dump gave %d goroutines, want 3", ids)
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conc

import (
	"testing"
	"unsafe"
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
	/home/dev/jobs/not-a-frame.lgo:1 +0x12

goroutine 9 [sleep]:
time.Sleep(0x3b9aca00)
	/usr/lib/logos/src/runtime/time.go:363 +0x165
created by main.main in goroutine 1
	/home/dev/jobs/main.lgo:41 +0x6c
`

func TestBlockedFrameIsTheInnermostFrameOutsideTheToolchain(t *testing.T) {
	const toolchain = "/usr/lib/logos/src/"
	tests := []struct {
		name      string
		id        uint64
		toolchain string
		state     string
		file      string
		line      int
		found     bool
	}{
		{"an inlined frame of the user below two runtime frames", 7, toolchain, "chan receive", "/home/dev/jobs/queue/queue.lgo", 31, true},
		{"the first goroutine of the dump, whose id starts another id", 71, toolchain, "running", "/home/dev/jobs/busy.lgo", 9, true},
		{"a goroutine with toolchain frames only", 9, toolchain, "sleep", "", 0, false},
		{"a goroutine that the dump does not hold", 1, toolchain, "", "", 0, false},
		{"a binary that records no source directory", 7, "", "chan receive", "", 0, false},
	}
	for _, tt := range tests {
		state, where, found := blockedFrame(stackDump, tt.id, tt.toolchain)
		if state != tt.state || where.file != tt.file || where.line != tt.line || found != tt.found {
			t.Errorf("%s: got state %q at %s:%d found=%v; want state %q at %s:%d found=%v",
				tt.name, state, where.file, where.line, found, tt.state, tt.file, tt.line, tt.found)
		}
	}
}

func TestJoinedGroupLeavesNoEntry(t *testing.T) {
	var cell int
	key := unsafe.Pointer(&cell)
	open := func() int {
		groups.mu.Lock()
		defer groups.mu.Unlock()
		return len(groups.open)
	}
	before := open()
	GroupCreated(key)
	ChildStarted(key, 0)
	ChildDone(key)
	if got := open(); got != before+1 {
		t.Fatalf("an open group gave %d entries, want %d", got, before+1)
	}
	GroupJoined(key)
	if got := open(); got != before {
		t.Fatalf("a joined group left %d entries, want %d", got, before)
	}
}

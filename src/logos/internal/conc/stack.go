// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conc

import (
	"iter"
	"runtime"
	"strconv"
	"strings"
)

// maxStackDump bounds the text of one dump of all goroutines, and with
// it the time for which the dump stops the program. A goroutine that
// the bounded dump does not hold gives a finding without its frame.
const maxStackDump = 8 << 20

// toolchainSources is the directory prefix of the source files of the
// toolchain, as this binary records them. It is empty when the binary
// records no directory, as in a -trimpath build; the monitor then
// cannot tell a toolchain file from a file of the user.
var toolchainSources = func() string {
	const self = "logos/internal/conc/stack.go"
	_, file, _, _ := runtime.Caller(0)
	root, found := strings.CutSuffix(file, self)
	if !found {
		return ""
	}
	return root
}()

// outsideToolchain reports whether the user wrote file. With an empty
// toolchainSources no file counts as written by the user.
func outsideToolchain(file string) bool {
	return !strings.HasPrefix(file, toolchainSources)
}

// goroutineStack is the text of one goroutine in a dump of the runtime.
type goroutineStack struct {
	id uint64
	// state is the wait state without the wait time, as "chan receive".
	state string
	// frames holds two lines per frame, innermost first: the function,
	// then a tab and the position. The frame that started the goroutine
	// is not part of it.
	frames string
}

func allStacks() string {
	for size := 1 << 16; ; size *= 2 {
		buf := make([]byte, size)
		if n := runtime.Stack(buf, true); n < size || size >= maxStackDump {
			return string(buf[:n])
		}
	}
}

// stacksOf reads a dump of all goroutines, in the form of runtime.Stack.
func stacksOf(dump string) iter.Seq[goroutineStack] {
	return func(yield func(goroutineStack) bool) {
		for block := range strings.SplitSeq(dump, "\n\n") {
			head, body, _ := strings.Cut(block, "\n")
			number, isGoroutine := strings.CutPrefix(head, "goroutine ")
			if !isGoroutine {
				continue
			}
			number, rest, _ := strings.Cut(number, " ")
			id, err := strconv.ParseUint(number, 10, 64)
			if err != nil {
				continue
			}
			_, state, _ := strings.Cut(rest, "[")
			state, _, _ = strings.Cut(state, "]")
			state, _, _ = strings.Cut(state, ",")
			frames, _, _ := strings.Cut(body, "\ncreated by ")
			if !yield(goroutineStack{id: id, state: state, frames: frames}) {
				return
			}
		}
	}
}

// innermost returns the innermost frame of the goroutine whose file the
// user wrote.
func (s goroutineStack) innermost(written func(file string) bool) (position, bool) {
	for line := range strings.SplitSeq(s.frames, "\n") {
		file, isPosition := strings.CutPrefix(line, "\t")
		if !isPosition {
			continue
		}
		file, _, _ = strings.Cut(file, " +0x")
		colon := strings.LastIndexByte(file, ':')
		if colon < 0 || !written(file[:colon]) {
			continue
		}
		number, err := strconv.Atoi(file[colon+1:])
		if err != nil {
			continue
		}
		return position{file: file[:colon], line: number}, true
	}
	return position{}, false
}

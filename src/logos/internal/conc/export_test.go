// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conc

import (
	"strings"
	"time"
)

// TestLeakWatch is a leak watch for the tests of this package. The
// tests are files of the toolchain, so for this watch the test files
// are the files that the user wrote.
var TestLeakWatch = leakWatch{written: func(file string) bool { return strings.HasSuffix(file, "_test.go") }}

func (w leakWatch) Report() { w.report() }

// CheckAfterEachCollection starts the part of the watch that follows
// the collections of the program, with no spacing between checks.
func (w leakWatch) CheckAfterEachCollection() { w.checkAfterCollection(time.Now()) }

// SetUnboundedAt sets the running-children count that reports a group
// without a limit, and returns the count before the call.
func SetUnboundedAt(count int) int {
	old := unboundedAt
	unboundedAt = count
	return old
}

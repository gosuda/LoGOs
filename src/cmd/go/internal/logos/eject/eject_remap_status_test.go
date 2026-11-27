// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"cmd/go/internal/base"
	"testing"
)

func TestRejectedDataRemapDoesNotSetCommandStatus(t *testing.T) {
	const input = `package fixture

import "net/http"
import "comptime"

func enable(s *http.Server) { s.EnableHTTP2ExtendedConnect() }
`
	before := base.GetExitStatus()
	out, uses, ok := remapEjectedFile([]byte(input), "", nil)
	if ok || uses || out != nil {
		t.Fatalf("overlapping fixture reported partial success: ok=%v uses=%v output=%q", ok, uses, out)
	}
	if after := base.GetExitStatus(); after != before {
		t.Errorf("rejected data remap changed command status: got %d, want %d", after, before)
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import "testing"

func TestH2ExtendedConnectOverlapFailsRemap(t *testing.T) {
	const in = `package main

import "net/http"
import "comptime"

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	// An overlap must fail the remap so the caller preserves the
	// original bytes; reporting success writes a partial rewrite.
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if ok || out != nil {
		t.Errorf("overlapping remap reported success (ok=%v out=%q); copyDataFile would write a partial rewrite", ok, out)
	}
}

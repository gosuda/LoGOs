// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build logosejected

package rt_test

import (
	"testing"

	"logos/rt"
)

func TestEjectedFormatTraceEmpty(t *testing.T) {
	if got := rt.FormatTrace(nil); got != "" {
		t.Fatalf("FormatTrace(nil) = %q, want empty", got)
	}
	if got := rt.FormatTrace([]rt.Site{{File: "a.go", Line: 1}}); got != "" {
		t.Fatalf("FormatTrace(nonempty) = %q, want empty", got)
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.
package config

import "testing"

func TestArchLevelsDefaultUsesV82(t *testing.T) {
	amd64, arm64 := ArchLevels("")
	if amd64 != "v3" || arm64 != "v8.2" {
		t.Fatalf("ArchLevels(%q) = (%q, %q), want (v3, v8.2)", "", amd64, arm64)
	}
	amd64, arm64 = ArchLevels("deterministic")
	if amd64 != "v3" || arm64 != "v8.2" {
		t.Fatalf("ArchLevels(%q) = (%q, %q), want (v3, v8.2)", "deterministic", amd64, arm64)
	}
	amd64, arm64 = ArchLevels("baseline")
	if amd64 != "v1" || arm64 != "v8.0" {
		t.Fatalf("ArchLevels(%q) = (%q, %q), want (v1, v8.0)", "baseline", amd64, arm64)
	}
}

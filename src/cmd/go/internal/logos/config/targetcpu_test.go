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
func TestArchLevelsNativeUsesHostLevel(t *testing.T) {
	amd64, arm64 := ArchLevels("native")
	if amd64 != nativeAMD64() || arm64 != nativeARM64() {
		t.Fatalf("ArchLevels(%q) = (%q, %q), want host levels (%q, %q)", "native", amd64, arm64, nativeAMD64(), nativeARM64())
	}
	host := false
	for _, level := range []string{"v1", "v3", "v4"} {
		host = host || amd64 == level
	}
	if !host {
		t.Fatalf("ArchLevels(%q) amd64 = %q, want a host level", "native", amd64)
	}
	host = false
	for _, level := range []string{"v8.0", "v8.1"} {
		host = host || arm64 == level
	}
	if !host {
		t.Fatalf("ArchLevels(%q) arm64 = %q, want a host level", "native", arm64)
	}
}

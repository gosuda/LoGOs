// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime_test

import (
	"internal/goarch"
	"runtime"
	"testing"
)

func TestLogosRuntimeNoNewPadding(t *testing.T) {
	ptr := uintptr(goarch.PtrSize)
	if runtime.LogosMFrameSize != 4*ptr {
		t.Fatalf("fastCgoFrame = %d, want %d", runtime.LogosMFrameSize, 4*ptr)
	}
	if runtime.LogosMStampSize != 8 {
		t.Fatalf("fastCallEnterNano = %d, want 8", runtime.LogosMStampSize)
	}
	if runtime.LogosMFlagSize != 1 {
		t.Fatalf("inFastCall = %d, want 1", runtime.LogosMFlagSize)
	}
	if runtime.LogosGArenaSize != ptr {
		t.Fatalf("logosArena = %d, want %d", runtime.LogosGArenaSize, ptr)
	}
	if runtime.LogosGRetOKSize != 1 {
		t.Fatalf("logosArenaRetOK = %d, want 1", runtime.LogosGRetOKSize)
	}
	if runtime.LogosGRegionsSize != ptr {
		t.Fatalf("logosRegions = %d, want %d", runtime.LogosGRegionsSize, ptr)
	}
	if runtime.LogosGFreeSize != ptr {
		t.Fatalf("logosRegionFree = %d, want %d", runtime.LogosGFreeSize, ptr)
	}
	if runtime.LogosGSiteSize != 8*ptr {
		t.Fatalf("logosErrSite = %d, want %d", runtime.LogosGSiteSize, 8*ptr)
	}
	if runtime.LogosGGroupSize != 4 {
		t.Fatalf("logosGroup = %d, want 4", runtime.LogosGGroupSize)
	}
	if runtime.LogosGLastSize != ptr {
		t.Fatalf("logosErrLast = %d, want %d", runtime.LogosGLastSize, ptr)
	}
	if runtime.LogosPPoolSize != ptr {
		t.Fatalf("logosArenaPool = %d, want %d", runtime.LogosPPoolSize, ptr)
	}
	if runtime.LogosPNSize != 4 {
		t.Fatalf("logosArenaPoolN = %d, want 4", runtime.LogosPNSize)
	}
	if runtime.LogosPRegionSize != ptr {
		t.Fatalf("logosRegionPool = %d, want %d", runtime.LogosPRegionSize, ptr)
	}
	if runtime.LogosGAlign != ptr {
		t.Fatalf("Alignof(g) = %d, want %d", runtime.LogosGAlign, ptr)
	}
	if runtime.LogosMAlign != ptr {
		t.Fatalf("Alignof(m) = %d, want %d", runtime.LogosMAlign, ptr)
	}
	if runtime.LogosPAlign != ptr {
		t.Fatalf("Alignof(p) = %d, want %d", runtime.LogosPAlign, ptr)
	}
	var wantMTail, wantGTail, wantGMid, wantPTail uintptr
	switch runtime.GOARCH {
	case "amd64", "arm64":
		wantMTail = 4*ptr + 8
		wantGTail = 10 * ptr
		wantGMid = 4 * ptr
		wantPTail = 3 * ptr
	case "wasm":
		wantMTail = 4*ptr + 8
		wantGTail = 10 * ptr
		wantGMid = 4 * ptr
		wantPTail = 3 * ptr
	default:
		t.Skipf("unsupported arch %s", runtime.GOARCH)
	}
	if d := runtime.LogosMStampOff - runtime.LogosMFrameOff; d != 4*ptr {
		t.Fatalf("m stamp-frame gap = %d, want %d", d, 4*ptr)
	}
	if tail := runtime.LogosMSize - runtime.LogosMFrameOff; tail != wantMTail {
		t.Fatalf("m tail = %d, want %d", tail, wantMTail)
	}
	if d := runtime.LogosGGroupOff - runtime.LogosGSiteOff; d != 8*ptr {
		t.Fatalf("g group-site gap = %d, want %d", d, 8*ptr)
	}
	if d := runtime.LogosGLastOff - runtime.LogosGGroupOff; d != ptr {
		t.Fatalf("g last-group gap = %d, want %d", d, ptr)
	}
	if tail := runtime.LogosGSize - runtime.LogosGSiteOff; tail != wantGTail {
		t.Fatalf("g tail = %d, want %d", tail, wantGTail)
	}
	if d := runtime.LogosGRetOKOff - runtime.LogosGArenaOff; d != ptr {
		t.Fatalf("g retok-arena gap = %d, want %d", d, ptr)
	}
	if d := runtime.LogosGRegionsOff - runtime.LogosGRetOKOff; d != ptr {
		t.Fatalf("g regions-retok gap = %d, want %d", d, ptr)
	}
	if d := runtime.LogosGFreeOff - runtime.LogosGRegionsOff; d != ptr {
		t.Fatalf("g free-regions gap = %d, want %d", d, ptr)
	}
	if run := runtime.LogosGFreeOff + ptr - runtime.LogosGArenaOff; run != wantGMid {
		t.Fatalf("g mid run = %d, want %d", run, wantGMid)
	}
	if d := runtime.LogosPNOff - runtime.LogosPPoolOff; d != ptr {
		t.Fatalf("p n-pool gap = %d, want %d", d, ptr)
	}
	if d := runtime.LogosPRegionOff - runtime.LogosPNOff; d != ptr {
		t.Fatalf("p region-n gap = %d, want %d", d, ptr)
	}
	if run := runtime.LogosPRegionOff + ptr - runtime.LogosPPoolOff; run != wantPTail {
		t.Fatalf("p run = %d, want %d", run, wantPTail)
	}
}

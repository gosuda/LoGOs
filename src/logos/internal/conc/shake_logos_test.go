// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conc_test

import (
	"testing"

	"logos/internal/conc"
)

func TestShakeYieldsNothingWhenTheSeedIsZero(t *testing.T) {
	for _, goid := range []uint64{0, 1, 2, 7, 100, 1 << 32} {
		for n := uint64(1); n <= 16; n++ {
			if got := conc.ShakeYields(0, goid, n); got != 0 {
				t.Fatalf("ShakeYields(0, %d, %d) = %d, want 0", goid, n, got)
			}
		}
	}
}

func TestShakeYieldsStayWithinTheirBound(t *testing.T) {
	for _, seed := range []uint64{1, 2, 7, 12345, 1<<64 - 1} {
		for _, goid := range []uint64{0, 1, 2, 7, 100, 1 << 32} {
			for n := uint64(1); n <= 16; n++ {
				got := conc.ShakeYields(seed, goid, n)
				if got < 1 || got > 4 {
					t.Fatalf("ShakeYields(%d, %d, %d) = %d, want 1 to 4", seed, goid, n, got)
				}
			}
		}
	}
}

func TestShakeStreamReplaysOneSeed(t *testing.T) {
	for _, seed := range []uint64{1, 7, 12345} {
		for _, goid := range []uint64{0, 1, 2, 99} {
			for n := uint64(1); n <= 16; n++ {
				first := conc.ShakeYields(seed, goid, n)
				second := conc.ShakeYields(seed, goid, n)
				if first != second {
					t.Fatalf("ShakeYields(%d, %d, %d) gave %d then %d", seed, goid, n, first, second)
				}
			}
		}
	}
}

func TestShakeStreamDiffersAcrossSeeds(t *testing.T) {
	differ := false
outer:
	for _, goid := range []uint64{0, 1, 2, 7, 100} {
		for n := uint64(1); n <= 32; n++ {
			if conc.ShakeYields(1, goid, n) != conc.ShakeYields(2, goid, n) {
				differ = true
				break outer
			}
		}
	}
	if !differ {
		t.Fatal("seeds 1 and 2 gave the same yields for every probed goroutine and count")
	}
}

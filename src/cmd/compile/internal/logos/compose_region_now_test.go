// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import "testing"

// A region without nested hosts must not copy the file-wide edit list:
// loop lowering composes one region per unrolled const-for iteration, so
// copying fl.splices every time is O(iterations x edits).
func TestComposeRegionNowSkipsFileWideCopy(t *testing.T) {
	src := []byte("0123456789ABCDEF")
	fl := &fileLower{
		srcMap: origBuf(src),
		splices: []splice{
			{start: 0, end: 1, text: "Q"},
			{start: 10, end: 12, text: "W"},
		},
	}
	r := &cfRegion{
		start:   4,
		end:     8,
		splices: []splice{{start: 4, end: 6, text: "XY"}},
	}
	before := &fl.splices[0]
	got := fl.composeRegionNow(r)
	if got.s != "XY67" {
		t.Fatalf("composeRegionNow = %q, want %q", got.s, "XY67")
	}
	if len(fl.splices) != 2 {
		t.Fatalf("len(fl.splices) = %d, want 2 (untouched)", len(fl.splices))
	}
	if &fl.splices[0] != before {
		t.Fatalf("composeRegionNow copied the file-wide edit list for a region with no nested hosts")
	}
}

// A region that does hold a nested host still consumes exactly that host.
func TestComposeRegionNowConsumesNestedHost(t *testing.T) {
	src := []byte("0123456789ABCDEF")
	h := &cfHost{head: "H", tail: "T"}
	fl := &fileLower{
		srcMap:  origBuf(src),
		splices: []splice{{start: 5, end: 7, host: h}},
	}
	r := &cfRegion{start: 4, end: 8}
	got := fl.composeRegionNow(r)
	if got.s != "4HT7" {
		t.Fatalf("composeRegionNow = %q, want %q", got.s, "4HT7")
	}
	if len(fl.splices) != 0 {
		t.Fatalf("len(fl.splices) = %d, want 0 (nested host consumed)", len(fl.splices))
	}
	if !h.retired {
		t.Fatalf("nested host was not retired")
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"slices"
	"testing"
)

// TestSupportCandidatesInverse requires supportCandidates to be the exact
// inverse of supportRelPath: every toolchain package must round-trip
// through its support image back to itself, and every returned candidate
// must forward-map to the image it was looked up from.
func TestSupportCandidatesInverse(t *testing.T) {
	paths := []string{
		"logos/rt",
		"logos/rt/json",
		"logos/arena",
		"logos/sync",
		"logos/sync/mutex",
		"logos/json",
		"logos/net/http",
		"logos/net/uri",
		"logos/encoding/base64",
	}
	for _, path := range paths {
		rel, ok := supportRelPath(path)
		if !ok {
			t.Fatalf("supportRelPath(%q) = not ok", path)
		}
		image := rtModulePath
		if rel != "" {
			image += "/" + rel
		}
		got := supportCandidates(image)
		if !slices.Contains(got, path) {
			t.Errorf("supportCandidates(%q) = %q, want it to contain %q", image, got, path)
		}
		for _, cand := range got {
			crel, ok := supportRelPath(cand)
			if !ok {
				t.Errorf("supportCandidates(%q) returned %q outside logos/", image, cand)
				continue
			}
			rebuilt := rtModulePath
			if crel != "" {
				rebuilt += "/" + crel
			}
			if rebuilt != image {
				t.Errorf("supportCandidates(%q) returned %q mapping to %q", image, cand, rebuilt)
			}
		}
	}
	if got := supportCandidates(rtModulePath + "/internal/jsonscan"); got != nil {
		t.Errorf("supportCandidates internal = %q, want nil", got)
	}
}

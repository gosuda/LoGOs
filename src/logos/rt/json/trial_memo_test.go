// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package json

import (
	"strconv"
	"testing"
)

func TestUnionTrialMemoMiss(t *testing.T) {
	hit, out := LookupUnionTrial("trial-memo-test-miss", []byte(`{"a":1}`))
	if hit {
		t.Fatalf("unknown key must miss, got %+v", out)
	}
	if out.Kind != UnionTrialMiss || out.Winner != -1 {
		t.Fatalf("miss must report Miss/-1, got %+v", out)
	}
}

func TestUnionTrialMemoHit(t *testing.T) {
	raw := []byte(`{"a":2}`)
	StoreUnionTrial("trial-memo-test-hit", raw, UnionTrialOutcome{Kind: UnionTrialSingle, Winner: 2})
	hit, out := LookupUnionTrial("trial-memo-test-hit", raw)
	if !hit {
		t.Fatalf("stored outcome must hit")
	}
	if out.Kind != UnionTrialSingle || out.Winner != 2 || out.Matched != 0 {
		t.Fatalf("stored outcome must round-trip, got %+v", out)
	}
}

func TestUnionTrialMemoCapKeepsRecent(t *testing.T) {
	const id = "trial-memo-test-cap"
	for i := range 1100 {
		raw := []byte(`{"n":` + strconv.Itoa(i) + `}`)
		StoreUnionTrial(id, raw, UnionTrialOutcome{Kind: UnionTrialNoMatch, Winner: -1})
	}
	if hit, _ := LookupUnionTrial(id, []byte(`{"n":0}`)); hit {
		t.Fatalf("entries past the cap must be evicted")
	}
	hit, out := LookupUnionTrial(id, []byte(`{"n":1099}`))
	if !hit || out.Kind != UnionTrialNoMatch {
		t.Fatalf("recent entries must survive the cap reset, got %v %+v", hit, out)
	}
}

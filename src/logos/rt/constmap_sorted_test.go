// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rt_test

import (
	"logos/rt"
	"slices"
	"testing"
)

type sortedPair struct{ A, B int }

func sortedKeys[K comparable, V any](m map[K]V) []K {
	var keys []K
	for k := range rt.SortedAll(m) {
		keys = append(keys, k)
	}
	return keys
}

func wantKeys[K comparable](t *testing.T, name string, got, want []K) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s: keys %v, want %v", name, got, want)
	}
}

func TestSortedAllOrdersEveryKeyKind(t *testing.T) {
	wantKeys(t, "string", sortedKeys(map[string]int{"b": 0, "a": 0, "ab": 0, "": 0, "B": 0}), []string{"", "B", "a", "ab", "b"})
	wantKeys(t, "int", sortedKeys(map[int]int{3: 0, -1: 0, 10: 0, -20: 0, 0: 0}), []int{-20, -1, 0, 3, 10})
	wantKeys(t, "int8", sortedKeys(map[int8]int{100: 0, -128: 0, 1: 0}), []int8{-128, 1, 100})
	wantKeys(t, "uint8", sortedKeys(map[uint8]int{200: 0, 3: 0, 100: 0}), []uint8{3, 100, 200})
	wantKeys(t, "float64", sortedKeys(map[float64]int{2.5: 0, -1.5: 0, 10: 0, 0.25: 0, -100: 0}), []float64{-100, -1.5, 0.25, 2.5, 10})
	wantKeys(t, "bool", sortedKeys(map[bool]int{true: 0, false: 0}), []bool{false, true})
	wantKeys(t, "complex128", sortedKeys(map[complex128]int{1 + 2i: 0, 1 + 1i: 0, 5i: 0, -1: 0}), []complex128{-1, 5i, 1 + 1i, 1 + 2i})
	wantKeys(t, "struct", sortedKeys(map[sortedPair]int{{2, 1}: 0, {1, 5}: 0, {1, 2}: 0, {3, 0}: 0}), []sortedPair{{1, 2}, {1, 5}, {2, 1}, {3, 0}})
	wantKeys(t, "array", sortedKeys(map[[2]int]int{{2, 1}: 0, {1, 5}: 0, {1, 2}: 0}), [][2]int{{1, 2}, {1, 5}, {2, 1}})
}

func TestSortedAllYieldsValuesWithKeys(t *testing.T) {
	m := map[string]int{"c": 3, "a": 1, "b": 2}
	var got []int
	for k, v := range rt.SortedAll(m) {
		if m[k] != v {
			t.Fatalf("key %q yielded value %d, want %d", k, v, m[k])
		}
		got = append(got, v)
	}
	wantKeys(t, "values", got, []int{1, 2, 3})
}

func TestSortedAllStopsWhenYieldReturnsFalse(t *testing.T) {
	count := 0
	for range rt.SortedAll(map[int]int{1: 0, 2: 0, 3: 0}) {
		count++
		break
	}
	if count != 1 {
		t.Fatalf("visited %d entries after break, want 1", count)
	}
}

func TestSortedAllSkipsKeysDeletedDuringTheRange(t *testing.T) {
	m := map[int]int{1: 0, 2: 0, 3: 0}
	var got []int
	for k := range rt.SortedAll(m) {
		got = append(got, k)
		delete(m, 3)
	}
	wantKeys(t, "after delete", got, []int{1, 2})
}

func TestSortedAllOfNilMapVisitsNothing(t *testing.T) {
	var m map[string]int
	if keys := sortedKeys(m); len(keys) != 0 {
		t.Fatalf("nil map visited %v", keys)
	}
}

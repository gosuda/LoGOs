// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package ascii provides ASCII-only case-insensitive primitives for LoGOs.
//
// The functions fold A to Z to a to z and leave all other bytes unchanged,
// including bytes above 127. They allocate nothing and use no tables.
// Ported from Zig lib/std/ascii.zig at revision c7826aa7a096270e7b8a1c7b8b4343650950f30 under MIT.
package ascii

// EqualFold reports whether a and b have equal length and equal bytes under ASCII folding.
// It folds only A to Z. It differs from strings.EqualFold on non-ASCII input.
// strings.EqualFold folds Unicode characters such as U+212A KELVIN SIGN to k, EqualFold does not.
func EqualFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		if lower(a[i]) != lower(b[i]) {
			return false
		}
	}
	return true
}

// HasPrefixFold reports whether s starts with prefix under ASCII folding.
// An empty prefix matches any s. A prefix longer than s never matches.
func HasPrefixFold(s, prefix string) bool {
	if len(prefix) > len(s) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		if lower(s[i]) != lower(prefix[i]) {
			return false
		}
	}
	return true
}

// CompareFold compares a and b under ASCII folding and returns -1, 0, or 1.
// It compares folded bytes in order, then orders shorter length before longer length.
func CompareFold(a, b string) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		la := lower(a[i])
		lb := lower(b[i])
		if la < lb {
			return -1
		}
		if la > lb {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

func lower(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
}

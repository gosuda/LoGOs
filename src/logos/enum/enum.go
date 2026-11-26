// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package enum provides dense indexes over caller-supplied enum members.
//
// A table is a caller-sized slice addressed by Index: make one with
// make([]V, enum.Count(members)) and read it with
// table[enum.Index(members, key)]. Positions follow the order of
// members, so a non-dense enum remaps to dense positions through that
// order. An unknown key reports -1 like slices.Index, so drift between
// an enum and its table fails fast instead of aliasing another entry.
//
// The indexing rule is a translation of the rule in Zig's
// lib/std/enums.zig (EnumIndexer with EnumArray, EnumMap and EnumSet)
// at revision c7826aa7a096270e4de8a1c7b8b4343650950f30, which is MIT
// licensed. Zig reads the member list from the type at compile time;
// Go cannot, and LoGOs comptime ranges only over struct fields, so the
// list Zig infers travels here as an explicit argument. Index and
// Count keep the names of the port task; the members parameter is the
// one difference from its shape.
package enum

// Index returns the dense position of e in members, or -1 when e is
// absent. Positions follow the order of members.
func Index[E comparable](members []E, e E) int {
	for i, m := range members {
		if m == e {
			return i
		}
	}
	return -1
}

// Count returns the number of dense positions in members.
func Count[E comparable](members []E) int {
	return len(members)
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package enum_test

import (
	"logos/enum"
	"slices"
	"testing"
)

type color int

const (
	red color = iota
	green
	blue
)

var colors = []color{red, green, blue}

type sparse int

const (
	sA sparse = 4
	sB sparse = 6
	sC sparse = 2
)

var sparsers = []sparse{sA, sB, sC}

type opcode int

const (
	opText opcode = iota
	opBinary
	opClose = 0x8
	opPing  = 0x9
	opPong  = 0xa
)

var opcodes = []opcode{opText, opBinary, opClose, opPing, opPong}

func TestIndexDense(t *testing.T) {
	for _, c := range colors {
		if got, want := enum.Index(colors, c), slices.Index(colors, c); got != want {
			t.Errorf("Index(colors, %d) = %d, want %d", c, got, want)
		}
	}
	if got := enum.Index(colors, color(9)); got != -1 {
		t.Errorf("Index(colors, 9) = %d, want -1", got)
	}
	if got := enum.Index[color](nil, red); got != -1 {
		t.Errorf("Index(nil, red) = %d, want -1", got)
	}
}

func TestIndexSparse(t *testing.T) {
	for _, s := range sparsers {
		if got, want := enum.Index(sparsers, s), slices.Index(sparsers, s); got != want {
			t.Errorf("Index(sparsers, %d) = %d, want %d", s, got, want)
		}
	}
	for _, hole := range []sparse{0, 1, 3, 5, 7, -1} {
		if got := enum.Index(sparsers, hole); got != -1 {
			t.Errorf("Index(sparsers, %d) = %d, want -1", hole, got)
		}
	}
}

func TestIndexOpcodes(t *testing.T) {
	for _, op := range opcodes {
		if got, want := enum.Index(opcodes, op), slices.Index(opcodes, op); got != want {
			t.Errorf("Index(opcodes, %#x) = %d, want %d", int(op), got, want)
		}
	}
	if got := enum.Index(opcodes, opcode(0x3)); got != -1 {
		t.Errorf("Index(opcodes, 0x3) = %d, want -1", got)
	}
	if got := enum.Index(opcodes, opPong); got != 4 {
		t.Errorf("Index(opcodes, opPong) = %d, want 4", got)
	}
}

func TestCount(t *testing.T) {
	if got := enum.Count(colors); got != len(colors) {
		t.Errorf("Count(colors) = %d, want %d", got, len(colors))
	}
	if got := enum.Count(sparsers); got != len(sparsers) {
		t.Errorf("Count(sparsers) = %d, want %d", got, len(sparsers))
	}
	if got := enum.Count(opcodes); got != len(opcodes) {
		t.Errorf("Count(opcodes) = %d, want %d", got, len(opcodes))
	}
	if got := enum.Count[color](nil); got != 0 {
		t.Errorf("Count(nil) = %d, want 0", got)
	}
}

func TestTableRoundTrip(t *testing.T) {
	table := make([]uint8, enum.Count(colors))
	stored := map[color]uint8{green: 0xff, blue: 0x80}
	for k, v := range stored {
		table[enum.Index(colors, k)] = v
	}
	for _, k := range colors {
		if got, want := table[enum.Index(colors, k)], stored[k]; got != want {
			t.Errorf("table[Index(colors, %d)] = %#x, want %#x", k, got, want)
		}
	}
}

func TestSetAlgebra(t *testing.T) {
	at := func(m uint64, k sparse) bool {
		return m&(1<<uint(enum.Index(sparsers, k))) != 0
	}
	bits := func(ks ...sparse) uint64 {
		var m uint64
		for _, k := range ks {
			m |= 1 << uint(enum.Index(sparsers, k))
		}
		return m
	}
	full := uint64(1<<uint(enum.Count(sparsers))) - 1
	if full != 0b111 {
		t.Fatalf("full = %b, want 111", full)
	}
	black, white := bits(sA, sC), bits(sB)
	if black|white != full || black&white != 0 {
		t.Errorf("black|white = %b black&white = %b, want 111 and 0", black|white, black&white)
	}
	if black&full != black {
		t.Errorf("black = %b is not a subset of full", black)
	}
	if white&black == white && white != 0 {
		t.Errorf("white = %b should not be a superset of black = %b", white, black)
	}
	comp := ^black & full
	if comp != white {
		t.Errorf("complement(black) = %b, want %b", comp, white)
	}
	inBlack := map[sparse]bool{sA: true, sC: true}
	for _, k := range sparsers {
		if got, want := at(black, k), inBlack[k]; got != want {
			t.Errorf("at(black, %d) = %v, want %v", k, got, want)
		}
		if got, want := at(comp, k), !inBlack[k]; got != want {
			t.Errorf("at(complement, %d) = %v, want %v", k, got, want)
		}
	}
}

func TestIndexNoAlloc(t *testing.T) {
	if n := testing.AllocsPerRun(200, func() {
		_ = enum.Index(opcodes, opPing)
		_ = enum.Count(opcodes)
	}); n != 0 {
		t.Errorf("Index/Count allocated %v times, want 0", n)
	}
}

func FuzzIndexSparse(f *testing.F) {
	for _, s := range []sparse{sA, sB, sC, 0, 1, 3, 5, 7, -1} {
		f.Add(int(s))
	}
	f.Fuzz(func(t *testing.T, v int) {
		e := sparse(v)
		if got, want := enum.Index(sparsers, e), slices.Index(sparsers, e); got != want {
			t.Errorf("Index(sparsers, %d) = %d, want %d", v, got, want)
		}
	})
}

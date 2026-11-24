// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"testing"
	"testing/iotest"
)

func TestH3VarintWire(t *testing.T) {
	cases := []struct {
		name  string
		wire  string
		value uint64
	}{
		{"rfc-one-byte", "\x25", 37},
		{"rfc-two-byte", "\x7b\xbd", 15293},
		{"rfc-four-byte", "\x9d\x7f\x3e\x7d", 494878333},
		{"rfc-eight-byte", "\xc2\x19\x7c\x5e\xff\x14\xe8\x8c", 151288809941952652},
		{"zero", "\x00", 0},
		{"max-one-byte", "\x3f", 63},
		{"min-two-byte", "\x40\x40", 64},
		{"max-two-byte", "\x7f\xff", 16383},
		{"min-four-byte", "\x80\x00\x40\x00", 16384},
		{"max-four-byte", "\xbf\xff\xff\xff", 1073741823},
		{"min-eight-byte", "\xc0\x00\x00\x00\x40\x00\x00\x00", 1073741824},
		{"max-eight-byte", "\xff\xff\xff\xff\xff\xff\xff\xff", 4611686018427387903},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wire := []byte(tc.wire + "\x16")
			got, n, err := parseH3Varint(wire)
			if err != nil || got != tc.value || n != len(tc.wire) {
				t.Fatalf("parse: got (%d, %d, %v), want (%d, %d, nil)", got, n, err, tc.value, len(tc.wire))
			}
			r := bytes.NewReader(wire)
			got, err = readH3Varint(r)
			if err != nil || got != tc.value {
				t.Fatalf("read: got (%d, %v), want (%d, nil)", got, err, tc.value)
			}
			if next, err := r.ReadByte(); next != 0x16 || err != nil {
				t.Fatalf("after read: got (%#x, %v), want (0x16, nil)", next, err)
			}
			want := "pre" + tc.wire
			if got := appendH3Varint([]byte("pre"), tc.value); string(got) != want {
				t.Fatalf("append: got % x, want % x", got, want)
			}
		})
	}
}

func TestH3VarintNonMinimal(t *testing.T) {
	for _, encoding := range []string{"\x40\x25", "\x80\x00\x00\x25", "\xc0\x00\x00\x00\x00\x00\x00\x25"} {
		wire := []byte(encoding)
		if got, n, err := parseH3Varint(wire); got != 37 || n != len(wire) || err != nil {
			t.Fatalf("parse % x: got (%d, %d, %v), want (37, %d, nil)", wire, got, n, err, len(wire))
		}
		if got, err := readH3Varint(bytes.NewReader(wire)); got != 37 || err != nil {
			t.Fatalf("read % x: got (%d, %v), want (37, nil)", wire, got, err)
		}
	}
}

func TestH3VarintTruncation(t *testing.T) {
	for _, encoding := range []string{"\x25", "\x7b\xbd", "\x9d\x7f\x3e\x7d", "\xc2\x19\x7c\x5e\xff\x14\xe8\x8c"} {
		wire := []byte(encoding)
		for cut := range len(wire) {
			want := io.ErrUnexpectedEOF
			if cut == 0 {
				want = io.EOF
			}
			if got, n, err := parseH3Varint(wire[:cut]); got != 0 || n != 0 || !errors.Is(err, want) {
				t.Fatalf("parse % x at %d: got (%d, %d, %v), want (0, 0, %v)", wire, cut, got, n, err, want)
			}
			if got, err := readH3Varint(bytes.NewReader(wire[:cut])); got != 0 || !errors.Is(err, want) {
				t.Fatalf("read % x at %d: got (%d, %v), want (0, %v)", wire, cut, got, err, want)
			}
		}
	}
}

func TestH3ReadVarintPreservesReaderError(t *testing.T) {
	want := errors.New("stream failed")
	for _, prefix := range []string{"", "\x80\x01"} {
		r := bufio.NewReader(io.MultiReader(bytes.NewReader([]byte(prefix)), iotest.ErrReader(want)))
		if _, err := readH3Varint(r); !errors.Is(err, want) {
			t.Fatalf("read after % x: got %v, want %v", prefix, err, want)
		}
	}
}

func TestH3AppendVarintRejectsOverflow(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("append accepted a value of 2^62")
		}
	}()
	appendH3Varint(nil, 1<<62)
}

func TestH3VarintNoAllocations(t *testing.T) {
	var dst [8]byte
	var r bytes.Reader
	allocs := testing.AllocsPerRun(100, func() {
		for _, value := range [...]uint64{37, 15293, 494878333, 151288809941952652} {
			wire := appendH3Varint(dst[:0], value)
			got, n, err := parseH3Varint(wire)
			if err != nil || got != value || n != len(wire) {
				t.Fatalf("parse: got (%d, %d, %v) for %d", got, n, err, value)
			}
			r.Reset(wire)
			got, err = readH3Varint(&r)
			if err != nil || got != value || r.Len() != 0 {
				t.Fatalf("read: got (%d, %v) for %d with %d bytes left", got, err, value, r.Len())
			}
		}
	})
	if allocs != 0 {
		t.Fatalf("varint operations allocated %v times per run, want 0", allocs)
	}
}

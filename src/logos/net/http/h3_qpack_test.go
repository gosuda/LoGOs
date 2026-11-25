// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
	"unsafe"
)

func TestH3QPACKDecodeWire(t *testing.T) {
	cases := []struct {
		name string
		wire string
		want []h3Field
	}{
		{"empty section", "0000", nil},
		{"RFC 9204 B.1", "0000510b2f696e6465782e68746d6c", []h3Field{{name: ":path", value: "/index.html"}}},
		{"RFC 7541 C.4.1 Huffman value", "0000d1d7c1508cf1e3c2e5f23a6ba0ab90f4ff", []h3Field{
			{name: ":method", value: "GET"}, {name: ":scheme", value: "https"},
			{name: ":path", value: "/"}, {name: ":authority", value: "www.example.com"},
		}},
		{"RFC 7541 C.4.3 Huffman name and value", "00003f0125a849e95ba97d7f8925a849e95bb8e8b4bf", []h3Field{
			{name: "custom-key", value: "custom-value", sensitive: true},
		}},
		{"static index 98", "0000ff23", []h3Field{{name: "x-frame-options", value: "sameorigin"}}},
		{"sensitive extended name index", "00007f45036b6579", []h3Field{{name: "authorization", value: "key", sensitive: true}}},
		{"positive Base with static field", "0001d1", []h3Field{{name: ":method", value: "GET"}}},
		{"largest QPACK Base", "007f80ffffffffffffff3fd1", []h3Field{{name: ":method", value: "GET"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block, err := hex.DecodeString(tc.wire)
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeH3Fields(nil, block, 1024)
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("decode = (%+v, %v), want %+v", got, err, tc.want)
			}
		})
	}
}

func TestH3QPACKRejectsMalformedWire(t *testing.T) {
	cases := []struct {
		name string
		wire string
		want error
	}{
		{"no Required Insert Count", "", io.ErrUnexpectedEOF},
		{"no Delta Base", "00", io.ErrUnexpectedEOF},
		{"nonzero Required Insert Count", "0100d1", errH3QPACK},
		{"negative Base", "0080d1", errH3QPACK},
		{"truncated Delta Base", "007f80", io.ErrUnexpectedEOF},
		{"Delta Base overflow", "007f81ffffffffffffff3f", errH3QPACK},
		{"overlong Delta Base", "007f80808080808080808000", errH3QPACK},
		{"dynamic index", "000080", errH3QPACK},
		{"dynamic name index", "00004000", errH3QPACK},
		{"post-Base index", "000010", errH3QPACK},
		{"post-Base name index", "00000800", errH3QPACK},
		{"static index 99", "0000ff24", errH3QPACK},
		{"static name index 99", "00005f5400", errH3QPACK},
		{"truncated static index", "0000ff", io.ErrUnexpectedEOF},
		{"truncated name length", "000027", io.ErrUnexpectedEOF},
		{"truncated name", "0000236162", io.ErrUnexpectedEOF},
		{"missing value", "000050", io.ErrUnexpectedEOF},
		{"truncated value length", "0000507f", io.ErrUnexpectedEOF},
		{"truncated value", "000050036162", io.ErrUnexpectedEOF},
		{"largest literal length exceeds block", "0000507f80ffffffffffffff3f", io.ErrUnexpectedEOF},
		{"truncated Huffman value", "0000508261", io.ErrUnexpectedEOF},
		{"Huffman invalid padding", "0000508106", errH3QPACK},
		{"Huffman excess padding", "00005081ff", errH3QPACK},
		{"Huffman EOS in value", "00005084ffffffff", errH3QPACK},
		{"Huffman EOS in name", "00002cffffffff00", errH3QPACK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			block, err := hex.DecodeString(tc.wire)
			if err != nil {
				t.Fatal(err)
			}
			got, err := decodeH3Fields(nil, block, 1024)
			if len(got) != 0 || !errors.Is(err, tc.want) || !errors.Is(err, errH3QPACK) {
				t.Fatalf("decode = (%+v, %v), want QPACK failure %v", got, err, tc.want)
			}
		})
	}
}

func TestH3QPACKFieldSectionLimit(t *testing.T) {
	block := []byte{0, 0, 0x21, 'x', 0x83, 0, 0, 0x0f, 0xd1}
	want := []h3Field{{name: "x", value: "0000"}, {name: ":method", value: "GET"}}
	for _, limit := range []uint64{0, 31, 32, 33, 36, 37, 78, 79, 80} {
		got, err := decodeH3Fields(nil, block, limit)
		if limit < 79 {
			if len(got) != 0 || !errors.Is(err, errH3FieldSectionTooLarge) {
				t.Fatalf("decode at limit %d = (%+v, %v), want field section too large", limit, got, err)
			}
			continue
		}
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("decode at limit %d = (%+v, %v), want %+v", limit, got, err, want)
		}
	}
	if got, err := decodeH3Fields(nil, block[:2], 0); len(got) != 0 || err != nil {
		t.Fatalf("empty section at zero limit = (%+v, %v)", got, err)
	}
}

func TestH3QPACKLongHuffmanValue(t *testing.T) {
	block := append([]byte{0, 0, 0x50, 0xd5}, make([]byte, 85)...)
	got, err := decodeH3Fields(nil, block, 178)
	want := []h3Field{{name: ":authority", value: strings.Repeat("0", 136)}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("decode = (%+v, %v), want %+v", got, err, want)
	}
	if _, err := decodeH3Fields(nil, block, 177); !errors.Is(err, errH3FieldSectionTooLarge) {
		t.Fatalf("decode below expanded limit = %v, want field section too large", err)
	}
}

func TestH3QPACKBorrowsRawLiterals(t *testing.T) {
	block := []byte{0, 0, 0x23, 'x', '-', 'a', 0x02, 'h', 'i', 0x51, 0x02, '/', 'z', 0x50, 0x81, 0x07}
	got, err := decodeH3Fields(nil, block, 1024)
	want := []h3Field{{name: "x-a", value: "hi"}, {name: ":path", value: "/z"}, {name: ":authority", value: "0"}}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("decode = (%+v, %v), want %+v", got, err, want)
	}
	if unsafe.StringData(got[0].name) != &block[3] || unsafe.StringData(got[0].value) != &block[7] || unsafe.StringData(got[1].value) != &block[11] {
		t.Fatal("raw literal strings do not borrow from the encoded block")
	}
	if unsafe.StringData(got[2].value) == &block[15] {
		t.Fatal("Huffman output aliases encoded bytes")
	}
}

func TestH3QPACKDecodeReusesDestinationWithoutAllocating(t *testing.T) {
	block := []byte{0, 0, 0xd1, 0x21, 'x', 0x01, 'y'}
	var storage [3]h3Field
	storage[0] = h3Field{name: "prefix"}
	allocs := testing.AllocsPerRun(100, func() {
		got, err := decodeH3Fields(storage[:1], block, 76)
		if err != nil || len(got) != len(storage) || &got[0] != &storage[0] || got[0].name != "prefix" ||
			got[1].name != ":method" || got[1].value != "GET" || got[2].name != "x" || got[2].value != "y" {
			t.Fatalf("decode into reusable destination = (%+v, %v)", got, err)
		}
	})
	if allocs != 0 {
		t.Fatalf("indexed and raw decode allocated %g times per run", allocs)
	}
}

func TestH3QPACKEncodeWire(t *testing.T) {
	fields := []h3Field{
		{name: ":status", value: "500"},
		{name: ":path", value: "/index.html"},
		{name: "cookie", sensitive: true},
		{name: "x-k", value: "value", sensitive: true},
	}
	want, err := hex.DecodeString("ab0000ff08510b2f696e6465782e68746d6c750033782d6b0576616c7565")
	if err != nil {
		t.Fatal(err)
	}
	var storage [64]byte
	storage[0] = 0xab
	got, err := appendH3Fields(storage[:1], fields)
	if err != nil || !bytes.Equal(got, want) || &got[0] != &storage[0] {
		t.Fatalf("encode = (%x, %v), want %x in reusable destination", got, err, want)
	}
}

func TestH3QPACKRoundTrip(t *testing.T) {
	fields := []h3Field{
		{name: ":method", value: "GET", sensitive: true},
		{name: "server", value: strings.Repeat("s", 127)},
		{name: "x-seven", value: strings.Repeat("v", 257)},
		{name: "X-\x00-\xff", value: "\x00\r\n"},
		{},
	}
	block, err := appendH3Fields(nil, fields)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeH3Fields(nil, block, 1024)
	if err != nil || !slices.Equal(got, fields) {
		t.Fatalf("round trip = (%+v, %v), want %+v", got, err, fields)
	}
}

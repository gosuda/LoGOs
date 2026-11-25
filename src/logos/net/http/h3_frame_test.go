// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestH3FrameHeaderWire(t *testing.T) {
	cases := []struct {
		name string
		wire string
		want h3FrameHeader
	}{
		{"settings", "\x04\x06", h3FrameHeader{kind: 4, length: 6}},
		{"large-kind", "\xc1\x23\x45\x67\x89\xab\xcd\xef\x00", h3FrameHeader{kind: 0x0123456789abcdef}},
		{"max-length", "\x01\xff\xff\xff\xff\xff\xff\xff\xff", h3FrameHeader{kind: 1, length: 4611686018427387903}},
		{"mixed-widths", "\x44\x03\x92\x34\x56\x78", h3FrameHeader{kind: 0x403, length: 0x12345678}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := "pre" + tc.wire
			got := appendH3FrameHeader([]byte("pre"), tc.want.kind, tc.want.length)
			if string(got) != want {
				t.Fatalf("append: got % x, want % x", got, want)
			}
			r := bytes.NewReader([]byte(tc.wire + "\x16"))
			header, err := readH3FrameHeader(r)
			if err != nil || header != tc.want {
				t.Fatalf("read: got (%+v, %v), want (%+v, nil)", header, err, tc.want)
			}
			if next, err := r.ReadByte(); next != 0x16 || err != nil {
				t.Fatalf("after header: got (%#x, %v), want (0x16, nil)", next, err)
			}
		})
	}
}

func TestH3FrameHeaderNonMinimal(t *testing.T) {
	r := bytes.NewReader([]byte("\x40\x04\x80\x00\x00\x00"))
	want := h3FrameHeader{kind: 4}
	if got, err := readH3FrameHeader(r); err != nil || got != want || r.Len() != 0 {
		t.Fatalf("read non-minimal header: got (%+v, %v) with %d bytes left, want (%+v, nil) with no bytes left", got, err, r.Len(), want)
	}
}

func TestH3FrameHeaderTruncation(t *testing.T) {
	const kind = "\xc0\x00\x00\x00\x40\x00\x00\x00"
	wire := []byte(kind + "\x80\x00\x40\x00")
	for cut := range len(wire) {
		want := io.ErrUnexpectedEOF
		if cut == 0 {
			want = io.EOF
		}
		got, err := readH3FrameHeader(bytes.NewReader(wire[:cut]))
		if got != (h3FrameHeader{}) || !errors.Is(err, want) {
			t.Fatalf("cut at %d: got (%+v, %v), want zero header and %v", cut, got, err, want)
		}
	}
}

func TestH3FrameHeaderNoAllocations(t *testing.T) {
	var dst [16]byte
	var r bytes.Reader
	allocs := testing.AllocsPerRun(100, func() {
		want := h3FrameHeader{kind: 1073741824, length: 4611686018427387903}
		r.Reset(appendH3FrameHeader(dst[:0], want.kind, want.length))
		got, err := readH3FrameHeader(&r)
		if err != nil || got != want || r.Len() != 0 {
			t.Fatalf("header: got (%+v, %v) with %d bytes left, want (%+v, nil) with no bytes left", got, err, r.Len(), want)
		}
	})
	if allocs != 0 {
		t.Fatalf("frame header operations allocated %v times per run, want 0", allocs)
	}
}

func TestH3DecodeSettingsValues(t *testing.T) {
	defaults := h3Settings{maxFieldSectionSize: ^uint64(0)}
	cases := []struct {
		name    string
		payload string
		want    h3Settings
	}{
		{"defaults", "", defaults},
		{"explicit-zero", "\x06\x00\x08\x00\x33\x00", h3Settings{}},
		{"unknown-and-grease", "\x00\x02\x21\x07\x40\x46\x05\xff\xff\xff\xff\xff\xff\xff\xff\x01\x08\x01", h3Settings{maxFieldSectionSize: ^uint64(0), enableConnectProtocol: true}},
		{
			name:    "all-fields",
			payload: "\x06\xff\xff\xff\xff\xff\xff\xff\xff\x01\x50\x00\x07\x40\x64\x08\x01\x33\x01",
			want: h3Settings{
				maxFieldSectionSize:   4611686018427387903,
				qpackMaxTableCapacity: 4096,
				qpackBlockedStreams:   100,
				enableConnectProtocol: true,
				enableDatagram:        true,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeH3Settings([]byte(tc.payload))
			if err != nil || got != tc.want {
				t.Fatalf("got (%+v, %v), want (%+v, nil)", got, err, tc.want)
			}
		})
	}
}

func TestH3DecodeSettingsMalformed(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{"duplicate-known", "\x06\x01\x40\x06\x02"},
		{"duplicate-unknown", "\x40\x99\x01\x80\x00\x00\x99\x02"},
		{"forbidden-id-2", "\x02\x01"},
		{"forbidden-id-3", "\x03\x01"},
		{"forbidden-id-4", "\x04\x01"},
		{"forbidden-id-5", "\x05\x01"},
		{"invalid-connect", "\x08\x02"},
		{"invalid-datagram", "\x33\x02"},
		{"truncated-id", "\x06\x01\xbf"},
		{"missing-value", "\x06"},
		{"truncated-value", "\x06\xc7\xff"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := decodeH3Settings([]byte(tc.payload))
			if got != (h3Settings{}) || !errors.Is(err, errH3SettingsMalformed) || errors.Is(err, errH3SettingsTooLarge) {
				t.Fatalf("got (%+v, %v), want zero settings and only the malformed error", got, err)
			}
		})
	}
}

func TestH3DecodeSettingsPayloadLimit(t *testing.T) {
	payload := make([]byte, 0, 8193)
	for i := range uint16(2048) {
		payload = binary.BigEndian.AppendUint16(payload, 0x4040+i)
		payload = append(payload, 0x40, 0x01)
	}
	want := h3Settings{maxFieldSectionSize: ^uint64(0)}
	if got, err := decodeH3Settings(payload); err != nil || got != want {
		t.Fatalf("8192 bytes: got (%+v, %v), want (%+v, nil)", got, err, want)
	}
	got, err := decodeH3Settings(append(payload, 0x00))
	if got != (h3Settings{}) || !errors.Is(err, errH3SettingsTooLarge) || errors.Is(err, errH3SettingsMalformed) {
		t.Fatalf("8193 bytes: got (%+v, %v), want zero settings and only the too-large error", got, err)
	}
}

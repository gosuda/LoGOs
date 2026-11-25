// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"bufio"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"testing/iotest"
)

func TestH3RequestStreamInitialErrors(t *testing.T) {
	const validHead = "\x01\x12\x00\x00\xd1\xd7\x50\x0bexample.com\xc1"
	for _, tc := range []struct {
		name string
		wire string
		tail error
		want error
		code uint64
	}{
		{name: "FIN before HEADERS", want: errH3MissingRequestHeaders},
		{name: "FIN after extension", wire: "\x21\x03ext", want: errH3MissingRequestHeaders},
		{name: "DATA before HEADERS", wire: "\x00\x00", want: errH3FrameUnexpected, code: 0x105},
		{name: "SETTINGS before HEADERS", wire: "\x04\x00", want: errH3FrameUnexpected, code: 0x105},
		{name: "partial frame type", wire: "\x40", want: errH3FrameTruncated, code: 0x106},
		{name: "partial frame length", wire: "\x01\x40", want: errH3FrameTruncated, code: 0x106},
		{name: "partial extension", wire: "\x21\x02x", want: errH3FrameTruncated, code: 0x106},
		{name: "partial field section", wire: "\x01\x03\x00\x00", want: errH3FrameTruncated, code: 0x106},
		{name: "read deadline during field section", wire: "\x01\x03\x00\x00", tail: os.ErrDeadlineExceeded, want: os.ErrDeadlineExceeded},
		{name: "invalid QPACK field section", wire: "\x01\x02\x01\x00", want: errH3QPACK, code: 0x200},
		{name: "malformed request", wire: "\x01\x02\x00\x00", want: errH3MalformedMessage},
		{name: "oversized compressed section", wire: "\x01\xff\xff\xff\xff\xff\xff\xff\xff", want: errH3FieldSectionTooLarge},
		{name: "oversized decoded section", wire: validHead, want: errH3FieldSectionTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var wire io.Reader = strings.NewReader(tc.wire)
			if tc.tail != nil {
				wire = io.MultiReader(wire, iotest.ErrReader(tc.tail))
			}
			cfg := h3RequestStreamConfig{maxHeaderBytes: 1024, maxFieldSectionSize: 16}
			req, err := readH3RequestHead(bufio.NewReader(wire), cfg)
			if req != nil || !errors.Is(err, tc.want) {
				t.Fatalf("read request = (%v, %v), want (nil, %v)", req, err, tc.want)
			}
			if code, ok := h3RequestConnectionError(err); code != tc.code || ok != (tc.code != 0) {
				t.Errorf("connection error = (%#x, %t), want (%#x, %t)", code, ok, tc.code, tc.code != 0)
			}
		})
	}
}

func TestH3RequestStreamHeaderBlockSurvivesBodyReads(t *testing.T) {
	const initial = "\x21\x03ext\x01\x28\x00\x00\xd1\xd7\x50\x0bexample.com\x51\x07/borrow\x26x-note\x06stable"
	payload := strings.Repeat("z", 128)
	reader := bufio.NewReaderSize(strings.NewReader(initial+"\x00\x40\x80"+payload), 64)
	cfg := h3RequestStreamConfig{maxHeaderBytes: 1024, maxFieldSectionSize: 1024}
	req, err := readH3RequestHead(reader, cfg)
	if err != nil {
		t.Fatal(err)
	}

	body := newH3RequestBodyReader(reader, req.Trailer, h3RequestBodyLimits{contentLength: req.ContentLength})
	var chunk [8]byte
	for offset := 0; offset < len(payload); offset += len(chunk) {
		if _, err := io.ReadFull(body, chunk[:]); err != nil {
			t.Fatalf("read body at offset %d: %v", offset, err)
		}
		if got, want := string(chunk[:]), payload[offset:offset+len(chunk)]; got != want {
			t.Fatalf("body at offset %d = %q, want %q", offset, got, want)
		}
	}
	if n, err := body.Read(chunk[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("read after DATA = (%d, %v), want (0, EOF)", n, err)
	}
	if got := req.Host; got != "example.com" {
		t.Errorf("request host after body reads = %q, want example.com", got)
	}
	if got := req.RequestURI; got != "/borrow" {
		t.Errorf("request URI after body reads = %q, want /borrow", got)
	}
	if got := req.Header.Get("X-Note"); got != "stable" {
		t.Errorf("header after body reads = %q, want stable", got)
	}
}

func TestH3RequestStreamBodyReturnsDataAndReadError(t *testing.T) {
	const payload = "12345678901234567890"
	wire := io.MultiReader(strings.NewReader("\x00\x14"), h3RequestDataAndError(payload))
	reader := bufio.NewReaderSize(wire, 16)
	body := &h3RequestStreamBody{
		reader: newH3RequestBodyReader(reader, nil, h3RequestBodyLimits{contentLength: -1}),
	}
	var buf [len(payload)]byte
	n, err := body.Read(buf[:])
	if got := string(buf[:n]); got != payload || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Errorf("read = (%q, %v), want (%q, deadline exceeded)", got, err, payload)
	}
}

type h3RequestDataAndError string

func (s h3RequestDataAndError) Read(p []byte) (int, error) {
	return copy(p, s), os.ErrDeadlineExceeded
}

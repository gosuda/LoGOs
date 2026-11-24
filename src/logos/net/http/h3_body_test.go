// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"bufio"
	"errors"
	"io"
	"math"
	stdhttp "net/http"
	"slices"
	"strings"
	"testing"
	"testing/iotest"
)

const h3BodyTestChecksumBlock = "\x00\x00\x27\x03x-checksum\x02ok"

func TestH3RequestBodyStreamsData(t *testing.T) {
	wire := "\x00\x03abc\x21\x04skip\x00\x00\x00\x02de\x00\x00"
	source := strings.NewReader(wire)
	stream := bufio.NewReaderSize(iotest.OneByteReader(source), 16)
	body := newH3RequestBodyReader(stream, nil, h3RequestBodyLimits{contentLength: 5})
	if n, err := body.Read(nil); n != 0 || err != nil || source.Len() != len(wire) {
		t.Fatalf("zero-length Read = (%d, %v); remaining wire bytes = %d, want %d", n, err, source.Len(), len(wire))
	}
	var p [2]byte
	for _, want := range []string{"a", "b", "c", "d", "e"} {
		n, err := body.Read(p[:])
		if string(p[:n]) != want || err != nil {
			t.Fatalf("Read = (%q, %v), want (%q, nil)", p[:n], err, want)
		}
	}
	if n, err := body.Read(p[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("final Read = (%d, %v), want (0, EOF)", n, err)
	}
}

func TestH3RequestBodyContentLength(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wire    string
		length  int64
		want    string
		wantErr error
	}{
		{"exact", "\x00\x02ab\x00\x02cd", 4, "abcd", nil},
		{"zero", "\x00\x00", 0, "", nil},
		{"omitted", "\x00\x02ab\x00\x03cde", -1, "abcde", nil},
		{"short", "\x00\x02ab", 3, "ab", io.ErrUnexpectedEOF},
		{"missing", "", 1, "", io.ErrUnexpectedEOF},
		{"excess within frame", "\x00\x02ab\x00\x03cde", 4, "abcd", errH3MalformedMessage},
		{"excess in next frame", "\x00\x02ab\x00\x02cd", 2, "ab", errH3MalformedMessage},
		{"excess over zero", "\x00\x01a", 0, "", errH3MalformedMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := bufio.NewReader(strings.NewReader(tc.wire))
			body := newH3RequestBodyReader(stream, nil, h3RequestBodyLimits{contentLength: tc.length})
			got, err := io.ReadAll(body)
			if string(got) != tc.want || !errors.Is(err, tc.wantErr) {
				t.Fatalf("ReadAll = (%q, %v), want (%q, %v)", got, err, tc.want, tc.wantErr)
			}
			if errors.Is(tc.wantErr, io.ErrUnexpectedEOF) && !errors.Is(err, errH3MalformedMessage) {
				t.Fatalf("length error = %v, want malformed message", err)
			}
		})
	}
}

func TestH3RequestBodyFrameEOF(t *testing.T) {
	for _, tc := range []struct {
		name    string
		wire    string
		want    string
		wantErr error
	}{
		{"empty", "", "", nil},
		{"skippable frame", "\x21\x01x", "", nil},
		{"partial kind", "\x40", "", errH3FrameTruncated},
		{"missing length", "\x00", "", errH3FrameTruncated},
		{"partial length", "\x00\x40", "", errH3FrameTruncated},
		{"missing data payload", "\x00\x01", "", errH3FrameTruncated},
		{"partial data payload", "\x00\x02a", "a", errH3FrameTruncated},
		{"partial extension payload", "\x21\x02x", "", errH3FrameTruncated},
		{"partial trailer payload", "\x01\x02\x00", "", errH3FrameTruncated},
		{"large extension length", "\x21\xff\xff\xff\xff\xff\xff\xff\xff", "", errH3FrameTruncated},
		{"large data length", "\x00\xff\xff\xff\xff\xff\xff\xff\xffa", "a", errH3FrameTruncated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := bufio.NewReader(strings.NewReader(tc.wire))
			limits := h3RequestBodyLimits{contentLength: -1, maxTrailerBytes: 64}
			body := newH3RequestBodyReader(stream, nil, limits)
			got, err := io.ReadAll(body)
			if string(got) != tc.want || !errors.Is(err, tc.wantErr) {
				t.Fatalf("ReadAll = (%q, %v), want (%q, %v)", got, err, tc.want, tc.wantErr)
			}
			if tc.wantErr != nil && (!errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)) {
				t.Fatalf("error = %v, want a truncated frame, not clean EOF", err)
			}
			terminalErr := tc.wantErr
			if terminalErr == nil {
				terminalErr = io.EOF
			}
			var p [1]byte
			if n, err := body.Read(p[:]); n != 0 || !errors.Is(err, terminalErr) {
				t.Fatalf("terminal Read = (%d, %v), want (0, %v)", n, err, terminalErr)
			}
		})
	}
}

func TestH3RequestBodyPreservesErrorWithData(t *testing.T) {
	payload := strings.Repeat("a", 32)
	reset := errors.New("request stream reset")
	for _, tc := range []struct {
		name      string
		header    string
		length    int64
		sourceErr error
		wantErr   error
	}{
		{"finished frame", "\x00\x20", 32, io.EOF, io.EOF},
		{"truncated frame", "\x00\x21", -1, io.EOF, errH3FrameTruncated},
		{"short content length", "\x00\x20", 33, io.EOF, errH3MalformedMessage},
		{"reset with finished frame", "\x00\x20", 32, reset, reset},
		{"reset with partial frame", "\x00\x21", -1, reset, reset},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := &h3BodyTestTerminalReader{data: tc.header + payload, err: tc.sourceErr}
			stream := bufio.NewReaderSize(source, 16)
			body := newH3RequestBodyReader(stream, nil, h3RequestBodyLimits{contentLength: tc.length})
			var p [32]byte
			n, err := body.Read(p[:])
			if n != 14 || string(p[:n]) != payload[:14] || err != nil {
				t.Fatalf("buffered Read = (%q, %v), want (%q, nil)", p[:n], err, payload[:14])
			}
			n, err = body.Read(p[:])
			if string(p[:n]) != payload[14:] || !errors.Is(err, tc.wantErr) {
				t.Fatalf("direct Read = (%q, %v), want (%q, %v)", p[:n], err, payload[14:], tc.wantErr)
			}
			if errors.Is(tc.sourceErr, io.EOF) && !errors.Is(tc.wantErr, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("direct Read error = %v, want unexpected EOF", err)
			}
			if n, err := body.Read(p[:]); n != 0 || !errors.Is(err, tc.wantErr) {
				t.Fatalf("terminal Read = (%d, %v), want (0, %v)", n, err, tc.wantErr)
			}
		})
	}
}

func TestH3RequestBodyPreservesReadError(t *testing.T) {
	cause := errors.New("request stream reset")
	for _, tc := range []struct {
		name string
		wire string
		want string
	}{
		{"frame boundary", "", ""},
		{"partial frame header", "\x00\x40", ""},
		{"partial data payload", "\x00\x02a", "a"},
		{"partial extension payload", "\x21\x02x", ""},
		{"partial trailer payload", "\x01\x02\x00", ""},
		{"after data", "\x00\x01a", "a"},
		{"after valid trailers", h3BodyTestTrailerFrame(h3BodyTestChecksumBlock), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := io.MultiReader(strings.NewReader(tc.wire), iotest.ErrReader(cause))
			limits := h3RequestBodyLimits{contentLength: -1, maxTrailerBytes: 64, maxFieldSectionSize: 256}
			trailers := make(stdhttp.Header)
			body := newH3RequestBodyReader(bufio.NewReader(source), trailers, limits)
			got, err := io.ReadAll(body)
			if string(got) != tc.want || !errors.Is(err, cause) {
				t.Fatalf("ReadAll = (%q, %v), want (%q, %v)", got, err, tc.want, cause)
			}
			if len(trailers) != 0 {
				t.Fatalf("published trailers after read error: %v", trailers)
			}
		})
	}
}

func TestH3RequestBodyRejectsForbiddenFrames(t *testing.T) {
	for _, tc := range []struct {
		name string
		wire string
	}{
		{"CANCEL_PUSH", "\x03\x00"},
		{"SETTINGS", "\x04\x02"},
		{"PUSH_PROMISE", "\x05\x00"},
		{"GOAWAY", "\x07\x00"},
		{"MAX_PUSH_ID", "\x0d\x00"},
		{"PRIORITY_UPDATE_REQUEST", "\x80\x0f\x07\x00\x00"},
		{"PRIORITY_UPDATE_PUSH", "\x80\x0f\x07\x01\x00"},
		{"reserved 0x02", "\x02\x00"},
		{"reserved 0x06", "\x06\x00"},
		{"reserved 0x08", "\x08\x00"},
		{"reserved 0x09", "\x09\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stream := bufio.NewReader(strings.NewReader(tc.wire))
			body := newH3RequestBodyReader(stream, nil, h3RequestBodyLimits{contentLength: -1})
			if _, err := io.ReadAll(body); !errors.Is(err, errH3FrameUnexpected) {
				t.Fatalf("ReadAll error = %v, want unexpected frame", err)
			}
		})
	}
}

func TestH3RequestBodyPublishesTrailers(t *testing.T) {
	const block = "\x00\x00" + "\x27\x03x-checksum\x05first" + "\x27\x03x-checksum\x06second" + "\x27\x00x-extra\x07present"
	wire := "\x00\x03hey" + h3BodyTestTrailerFrame(block) + "\x21\x20" + strings.Repeat("x", 32)
	trailers := stdhttp.Header{"X-Checksum": nil, "X-Absent": nil}
	stream := bufio.NewReaderSize(strings.NewReader(wire), 16)
	limits := h3RequestBodyLimits{contentLength: 3, maxTrailerBytes: 64, maxFieldSectionSize: 256}
	body := newH3RequestBodyReader(stream, trailers, limits)
	var p [3]byte
	if n, err := io.ReadFull(body, p[:]); n != len(p) || string(p[:]) != "hey" || err != nil {
		t.Fatalf("DATA = (%q, %v), want (hey, nil)", p[:n], err)
	}
	if trailers.Get("X-Checksum") != "" || len(trailers) != 2 {
		t.Fatalf("trailers before EOF = %v, want only empty announced keys", trailers)
	}
	if n, err := body.Read(p[:]); n != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("final Read = (%d, %v), want (0, EOF)", n, err)
	}
	if got := trailers.Values("X-Checksum"); !slices.Equal(got, []string{"first", "second"}) {
		t.Fatalf("X-Checksum = %v, want [first second]", got)
	}
	if got := trailers.Get("X-Extra"); got != "present" {
		t.Fatalf("unannounced X-Extra = %q, want present", got)
	}
	if absent, ok := trailers["X-Absent"]; !ok || absent != nil {
		t.Fatalf("absent announced trailer = (%v, %t), want (nil, true)", absent, ok)
	}
}

func TestH3RequestBodyRejectsBadTrailers(t *testing.T) {
	frame := h3BodyTestTrailerFrame(h3BodyTestChecksumBlock)
	empty := h3BodyTestTrailerFrame("\x00\x00")
	prohibited := h3BodyTestTrailerFrame(h3BodyTestChecksumBlock + "\x27\x07content-length\x010")
	for _, tc := range []struct {
		name    string
		wire    string
		length  int64
		wantErr error
	}{
		{"invalid QPACK", "\x01\x01\x00", 0, errH3QPACK},
		{"prohibited field after valid field", prohibited, 0, errH3MalformedMessage},
		{"DATA after trailers", frame + "\x00\x01a", 0, errH3FrameUnexpected},
		{"empty DATA after trailers", frame + "\x00\x00", 0, errH3FrameUnexpected},
		{"DATA after empty trailers", empty + "\x00\x00", 0, errH3FrameUnexpected},
		{"second trailer section", frame + frame, 0, errH3FrameUnexpected},
		{"second trailer after empty section", empty + frame, 0, errH3FrameUnexpected},
		{"truncated extension after trailers", frame + "\x21\x02x", 0, errH3FrameTruncated},
		{"short content length with trailers", frame, 1, errH3MalformedMessage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trailers := stdhttp.Header{"X-Checksum": nil}
			stream := bufio.NewReader(strings.NewReader(tc.wire))
			limits := h3RequestBodyLimits{contentLength: tc.length, maxTrailerBytes: 64, maxFieldSectionSize: 256}
			body := newH3RequestBodyReader(stream, trailers, limits)
			if _, err := io.ReadAll(body); !errors.Is(err, tc.wantErr) {
				t.Fatalf("ReadAll error = %v, want %v", err, tc.wantErr)
			}
			if value, ok := trailers["X-Checksum"]; len(trailers) != 1 || !ok || value != nil {
				t.Fatalf("published trailers after invalid stream: %v", trailers)
			}
		})
	}
}

func TestH3RequestBodyTrailerLimits(t *testing.T) {
	const block = "\x00\x00\x26x-note\x02ok"
	const maxLengthHeader = "\x01\xff\xff\xff\xff\xff\xff\xff\xff"
	frame := h3BodyTestTrailerFrame(block)
	compressed := uint64(len(block))
	maxLengthErr := errH3FrameTruncated
	if uint64(math.MaxInt) < 1<<62-1 {
		maxLengthErr = errH3FieldSectionTooLarge
	}
	for _, tc := range []struct {
		name       string
		wire       string
		compressed uint64
		decoded    uint64
		wantErr    error
	}{
		{"compressed below", frame, compressed - 1, 40, errH3FieldSectionTooLarge},
		{"decoded below", frame, compressed, 39, errH3FieldSectionTooLarge},
		{"both exact", frame, compressed, 40, nil},
		{"both above", frame, compressed + 1, 41, nil},
		{"oversized without payload", maxLengthHeader, compressed, 40, errH3FieldSectionTooLarge},
		{"maximum possible length without payload", maxLengthHeader, ^uint64(0), 40, maxLengthErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			trailers := make(stdhttp.Header)
			stream := bufio.NewReader(strings.NewReader(tc.wire))
			limits := h3RequestBodyLimits{contentLength: -1, maxTrailerBytes: tc.compressed, maxFieldSectionSize: tc.decoded}
			body := newH3RequestBodyReader(stream, trailers, limits)
			if _, err := io.ReadAll(body); !errors.Is(err, tc.wantErr) {
				t.Fatalf("ReadAll error = %v, want %v", err, tc.wantErr)
			}
			if tc.wantErr == nil && trailers.Get("X-Note") != "ok" {
				t.Fatalf("trailers = %v, want X-Note: ok", trailers)
			}
			if tc.wantErr != nil && len(trailers) != 0 {
				t.Fatalf("published oversized trailers: %v", trailers)
			}
		})
	}
}

func h3BodyTestTrailerFrame(block string) string {
	return string(append(appendH3FrameHeader(nil, 0x1, uint64(len(block))), block...))
}

type h3BodyTestTerminalReader struct {
	data string
	err  error
}

func (r *h3BodyTestTerminalReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, r.err
	}
	return n, nil
}

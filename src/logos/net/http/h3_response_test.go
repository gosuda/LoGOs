// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"bytes"
	"errors"
	"io"
	"net"
	stdhttp "net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

type h3ResponseTestStream struct {
	bytes.Buffer
	published     int
	writeLimit    int
	writeError    error
	flushError    error
	readDeadline  time.Time
	writeDeadline time.Time
	deadlineError error
}

func (s *h3ResponseTestStream) Write(p []byte) (int, error) {
	if s.writeError != nil {
		return 0, s.writeError
	}
	if s.writeLimit > 0 && len(p) > s.writeLimit {
		p = p[:s.writeLimit]
	}
	return s.Buffer.Write(p)
}

func (s *h3ResponseTestStream) Flush() error {
	if s.flushError != nil {
		return s.flushError
	}
	s.published = s.Len()
	return nil
}

func (s *h3ResponseTestStream) SetReadDeadline(deadline time.Time) error {
	s.readDeadline = deadline
	return s.deadlineError
}

func (s *h3ResponseTestStream) SetWriteDeadline(deadline time.Time) error {
	s.writeDeadline = deadline
	return s.deadlineError
}

func h3ResponseFixture(method string, limit uint64) (*h3ResponseWriter, *h3ResponseTestStream) {
	stream := new(h3ResponseTestStream)
	w := newH3ResponseWriter(stream, method, limit)
	w.Header()["Date"] = nil
	return w, stream
}

func h3ReadResponseTestVarint(t *testing.T, wire *bytes.Reader) uint64 {
	t.Helper()
	first, err := wire.ReadByte()
	if err != nil {
		t.Fatalf("read frame varint: %v", err)
	}
	width := 1 << (first >> 6)
	value := uint64(first & 63)
	for range width - 1 {
		b, err := wire.ReadByte()
		if err != nil {
			t.Fatalf("read frame varint continuation: %v", err)
		}
		value = value<<8 | uint64(b)
	}
	return value
}

func h3ReadResponseTestFrame(t *testing.T, wire *bytes.Reader, wantKind uint64) []byte {
	t.Helper()
	if kind := h3ReadResponseTestVarint(t, wire); kind != wantKind {
		t.Fatalf("frame kind = %#x, want %#x", kind, wantKind)
	}
	length := h3ReadResponseTestVarint(t, wire)
	if length > uint64(wire.Len()) {
		t.Fatalf("frame declares %d bytes, only %d left", length, wire.Len())
	}
	payload := make([]byte, int(length))
	if _, err := io.ReadFull(wire, payload); err != nil {
		t.Fatalf("read frame payload: %v", err)
	}
	return payload
}

func h3ExpectResponseFields(t *testing.T, wire *bytes.Reader, want map[string][]string) {
	t.Helper()
	payload := h3ReadResponseTestFrame(t, wire, 0x1)
	fields, err := decodeH3Fields(nil, payload, h3UnlimitedFieldSectionSize)
	if err != nil {
		t.Fatalf("decode HEADERS payload: %v", err)
	}
	if _, hasStatus := want[":status"]; hasStatus && (len(fields) == 0 || fields[0].name != ":status") {
		t.Fatalf("first HEADERS field is not :status: %v", fields)
	}
	got := make(map[string][]string)
	for _, field := range fields {
		got[field.name] = append(got[field.name], field.value)
	}
	if len(got) != len(want) {
		t.Fatalf("HEADERS fields = %v, want %v", got, want)
	}
	for name, values := range want {
		if !slices.Equal(got[name], values) {
			t.Fatalf("HEADERS fields = %v, want %v", got, want)
		}
	}
}

func h3ExpectResponseData(t *testing.T, wire *bytes.Reader, want string) {
	t.Helper()
	if got := string(h3ReadResponseTestFrame(t, wire, 0x0)); got != want {
		t.Fatalf("DATA payload = %q, want %q", got, want)
	}
}

func h3ExpectNoResponseFrames(t *testing.T, wire *bytes.Reader) {
	t.Helper()
	if wire.Len() != 0 {
		t.Fatalf("%d unexpected wire bytes", wire.Len())
	}
}

func h3WriteResponseTestBody(t *testing.T, w stdhttp.ResponseWriter, body string) {
	t.Helper()
	n, err := w.Write([]byte(body))
	if err != nil || n != len(body) {
		t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(body))
	}
}

func h3FinishResponseTest(t *testing.T, w *h3ResponseWriter) {
	t.Helper()
	if err := w.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
}

func TestH3ResponseWriterStreamsFinalAfterInformational(t *testing.T) {
	w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
	h := w.Header()
	h.Set("Link", "</style.css>; rel=preload")
	h.Set("Content-Length", "0")
	w.WriteHeader(stdhttp.StatusEarlyHints)
	wire := bytes.NewReader(stream.Bytes()[:stream.published])
	h3ExpectResponseFields(t, wire, map[string][]string{":status": {"103"}, "link": {"</style.css>; rel=preload"}})
	h3ExpectNoResponseFrames(t, wire)
	before := stream.published
	h.Del("Link")
	h.Del("Content-Length")
	w.WriteHeader(stdhttp.StatusProcessing)
	wire = bytes.NewReader(stream.Bytes()[before:stream.published])
	h3ExpectResponseFields(t, wire, map[string][]string{":status": {"102"}})
	h3ExpectNoResponseFrames(t, wire)
	before = stream.Len()

	h.Set("X-Frozen", "before")
	w.WriteHeader(stdhttp.StatusCreated)
	h.Set("X-Frozen", "after")
	w.WriteHeader(stdhttp.StatusInternalServerError)
	if stream.Len() != before {
		t.Fatal("final response headers emitted before Write or Flush")
	}
	if err := stdhttp.NewResponseController(w).Flush(); err != nil {
		t.Fatal(err)
	}
	wire = bytes.NewReader(stream.Bytes()[before:stream.published])
	h3ExpectResponseFields(t, wire, map[string][]string{":status": {"201"}, "x-frozen": {"before"}})
	h3ExpectNoResponseFrames(t, wire)
	before = stream.published

	large := strings.Repeat("a", 80)
	h3WriteResponseTestBody(t, w, large)
	var flusher stdhttp.Flusher = w
	flusher.Flush()
	wire = bytes.NewReader(stream.Bytes()[before:stream.published])
	h3ExpectResponseData(t, wire, large)
	h3ExpectNoResponseFrames(t, wire)
	before = stream.published
	h3WriteResponseTestBody(t, w, "next")
	h3FinishResponseTest(t, w)
	h3FinishResponseTest(t, w)
	wire = bytes.NewReader(stream.Bytes()[before:])
	h3ExpectResponseData(t, wire, "next")
	h3ExpectNoResponseFrames(t, wire)
	if n, err := w.Write([]byte("closed")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Write after finish = (%d, %v), want (0, io.ErrClosedPipe)", n, err)
	}
}

func TestH3ResponseWriterContentType(t *testing.T) {
	for _, tc := range []struct {
		name, encoding, body string
		fields               map[string][]string
	}{
		{"sniff", "", "<html>hello", map[string][]string{":status": {"200"}, "content-type": {"text/html; charset=utf-8"}}},
		{"encoded", "gzip", "opaque", map[string][]string{":status": {"200"}, "content-encoding": {"gzip"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
			if tc.encoding != "" {
				w.Header().Set("Content-Encoding", tc.encoding)
			}
			h3WriteResponseTestBody(t, w, tc.body)
			h3FinishResponseTest(t, w)
			wire := bytes.NewReader(stream.Bytes())
			h3ExpectResponseFields(t, wire, tc.fields)
			h3ExpectResponseData(t, wire, tc.body)
			h3ExpectNoResponseFrames(t, wire)
		})
	}
}

func TestH3ResponseWriterEmptyWriteFreezesHeaderWithoutData(t *testing.T) {
	w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
	w.Header().Set("X-Early", "kept")
	if n, err := w.Write(nil); n != 0 || err != nil || stream.Len() != 0 {
		t.Fatalf("empty Write = (%d, %v), wrote %d wire bytes", n, err, stream.Len())
	}
	w.Header().Set("X-Early", "late")
	h3FinishResponseTest(t, w)
	wire := bytes.NewReader(stream.Bytes())
	h3ExpectResponseFields(t, wire, map[string][]string{":status": {"200"}, "x-early": {"kept"}})
	h3ExpectNoResponseFrames(t, wire)
}

func TestH3ResponseWriterBodyRules(t *testing.T) {
	for _, tc := range []struct {
		method string
		status int
		length string
		fields map[string][]string
	}{
		{stdhttp.MethodHead, 200, "20", map[string][]string{":status": {"200"}, "content-length": {"20"}}},
		{stdhttp.MethodGet, 204, "20", map[string][]string{":status": {"204"}}},
		{stdhttp.MethodGet, 205, "0", map[string][]string{":status": {"205"}, "content-length": {"0"}}},
		{stdhttp.MethodGet, 304, "20", map[string][]string{":status": {"304"}, "content-length": {"20"}}},
	} {
		t.Run(tc.method+"/"+strconv.Itoa(tc.status), func(t *testing.T) {
			w, stream := h3ResponseFixture(tc.method, h3UnlimitedFieldSectionSize)
			w.Header().Set("Content-Length", tc.length)
			w.Header()["Content-Type"] = nil
			w.Header().Set("Trailer", "X-Final")
			w.WriteHeader(tc.status)
			w.Header().Set("X-Final", "omitted trailer")
			if tc.method == stdhttp.MethodHead {
				h3WriteResponseTestBody(t, w, "omitted body")
			} else if n, err := w.Write([]byte("omitted body")); n != 0 || !errors.Is(err, stdhttp.ErrBodyNotAllowed) {
				t.Fatalf("body-forbidden Write = (%d, %v)", n, err)
			}
			h3FinishResponseTest(t, w)
			wire := bytes.NewReader(stream.Bytes())
			h3ExpectResponseFields(t, wire, tc.fields)
			h3ExpectNoResponseFrames(t, wire)
		})
	}
}

func TestH3ResponseWriterConnectWritesTunnelData(t *testing.T) {
	w, stream := h3ResponseFixture(stdhttp.MethodConnect, h3UnlimitedFieldSectionSize)
	w.Header().Set("Content-Length", "0")
	w.Header().Set("Trailer", "X-Final")
	w.WriteHeader(stdhttp.StatusOK)
	controller := stdhttp.NewResponseController(w)
	if err := controller.EnableFullDuplex(); err != nil {
		t.Fatal(err)
	}
	if err := controller.Flush(); err != nil {
		t.Fatal(err)
	}
	h3WriteResponseTestBody(t, w, "tunnel bytes")
	w.Header().Set(stdhttp.TrailerPrefix+"X-Final", "omitted")
	h3FinishResponseTest(t, w)
	wire := bytes.NewReader(stream.Bytes())
	h3ExpectResponseFields(t, wire, map[string][]string{":status": {"200"}})
	h3ExpectResponseData(t, wire, "tunnel bytes")
	h3ExpectNoResponseFrames(t, wire)
}

func TestH3ResponseWriterStatus(t *testing.T) {
	for _, status := range []int{600, 999} {
		w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
		w.WriteHeader(status)
		w.WriteHeader(stdhttp.StatusOK)
		h3FinishResponseTest(t, w)
		wire := bytes.NewReader(stream.Bytes())
		h3ExpectResponseFields(t, wire, map[string][]string{":status": {strconv.Itoa(status)}})
		h3ExpectNoResponseFrames(t, wire)
	}
	for _, status := range []int{99, 101, 1000} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
			defer func() {
				if recover() == nil || stream.Len() != 0 {
					t.Errorf("invalid status %d did not panic before writing", status)
				}
			}()
			w.WriteHeader(status)
		})
	}
}

func TestH3ResponseWriterContentLength(t *testing.T) {
	t.Run("bounds", func(t *testing.T) {
		w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
		w.Header().Set("Content-Length", "3, "+strings.Repeat("0", 64)+"3")
		w.Header()["Content-Type"] = nil
		if n, err := w.Write([]byte("long")); n != 0 || !errors.Is(err, stdhttp.ErrContentLength) || stream.Len() != 0 {
			t.Fatalf("over-limit Write = (%d, %v), wrote %d wire bytes", n, err, stream.Len())
		}
		h3WriteResponseTestBody(t, w, "abc")
		h3FinishResponseTest(t, w)
		wire := bytes.NewReader(stream.Bytes())
		h3ExpectResponseFields(t, wire, map[string][]string{":status": {"200"}, "content-length": {"3"}})
		h3ExpectResponseData(t, wire, "abc")
		h3ExpectNoResponseFrames(t, wire)
	})
	t.Run("explicit-zero", func(t *testing.T) {
		w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
		w.Header().Set("Content-Length", "0")
		if n, err := w.Write([]byte("x")); n != 0 || !errors.Is(err, stdhttp.ErrContentLength) {
			t.Fatalf("Write with zero Content-Length = (%d, %v)", n, err)
		}
		h3FinishResponseTest(t, w)
		wire := bytes.NewReader(stream.Bytes())
		h3ExpectResponseFields(t, wire, map[string][]string{":status": {"200"}, "content-length": {"0"}})
		h3ExpectNoResponseFrames(t, wire)
	})
	t.Run("mismatch-after-data", func(t *testing.T) {
		w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
		w.Header().Set("Content-Length", "3")
		w.Header()["Content-Type"] = nil
		h3WriteResponseTestBody(t, w, "ab")
		for range 2 {
			if err := w.finish(); !errors.Is(err, stdhttp.ErrContentLength) {
				t.Fatalf("finish with too little data: %v", err)
			}
		}
		wire := bytes.NewReader(stream.Bytes())
		h3ExpectResponseFields(t, wire, map[string][]string{":status": {"200"}, "content-length": {"3"}})
		h3ExpectResponseData(t, wire, "ab")
		h3ExpectNoResponseFrames(t, wire)
	})
	t.Run("mismatch-before-data", func(t *testing.T) {
		w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
		w.Header().Set("Content-Length", "3")
		if err := w.finish(); !errors.Is(err, stdhttp.ErrContentLength) || stream.Len() != 0 {
			t.Fatalf("finish with a missing body = %v, wrote %d bytes", err, stream.Len())
		}
	})
	t.Run("invalid", func(t *testing.T) {
		for _, tc := range []struct {
			status int
			length string
		}{
			{200, "-1"}, {200, "-0"}, {200, "+1"}, {200, "1, 2"},
			{200, "9223372036854775808"}, {200, strings.Repeat("1", 64)}, {205, "1"},
		} {
			w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
			w.Header().Set("Content-Length", tc.length)
			w.WriteHeader(tc.status)
			if err := w.FlushError(); !errors.Is(err, stdhttp.ErrContentLength) || stream.Len() != 0 {
				t.Errorf("status %d, Content-Length %q: Flush = %v, wrote %d bytes", tc.status, tc.length, err, stream.Len())
			}
		}
	})
}

func TestH3ResponseWriterTrailers(t *testing.T) {
	w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
	h := w.Header()
	h.Set("Trailer", "X-Final, X-Trace")
	h.Set("X-Final", "initial placeholder")
	h.Set("Content-Type", "text/plain")
	h3WriteResponseTestBody(t, w, "body")
	h.Set("Trailer", "X-Undeclared")
	h.Set("X-Undeclared", "omitted")
	h.Set("X-Final", "done")
	h.Add("X-Trace", "one")
	h.Add("X-Trace", "two")
	h.Set(stdhttp.TrailerPrefix+"X-Late", "late")
	h3FinishResponseTest(t, w)
	h3FinishResponseTest(t, w)
	wire := bytes.NewReader(stream.Bytes())
	h3ExpectResponseFields(t, wire, map[string][]string{":status": {"200"}, "trailer": {"X-Final, X-Trace"}, "content-type": {"text/plain"}})
	h3ExpectResponseData(t, wire, "body")
	h3ExpectResponseFields(t, wire, map[string][]string{"x-final": {"done"}, "x-trace": {"one", "two"}, "x-late": {"late"}})
	h3ExpectNoResponseFrames(t, wire)
}

func TestH3ResponseWriterWriteFailures(t *testing.T) {
	t.Run("short-data", func(t *testing.T) {
		w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
		if err := w.FlushError(); err != nil {
			t.Fatal(err)
		}
		before := stream.Len()
		stream.writeLimit = 2
		if n, err := w.Write([]byte("abcde")); n != 2 || !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("short DATA Write = (%d, %v)", n, err)
		}
		if err := w.FlushError(); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("FlushError did not retain short write: %v", err)
		}
		if err := w.finish(); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("finish did not retain short write: %v", err)
		}
		if got := stream.Bytes()[before:]; !bytes.Equal(got, []byte{0x0, 5, 'a', 'b'}) {
			t.Fatalf("partial DATA wire = %x", got)
		}
	})
	t.Run("short-headers", func(t *testing.T) {
		w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
		stream.writeLimit = 1
		var flusher stdhttp.Flusher = w
		flusher.Flush()
		if err := w.finish(); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("finish after short HEADERS = %v", err)
		}
		if !bytes.Equal(stream.Bytes(), []byte{0x1}) || stream.published != 0 {
			t.Fatalf("partial HEADERS wire = %x, published %d bytes", stream.Bytes(), stream.published)
		}
	})
	t.Run("informational-write", func(t *testing.T) {
		w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
		stream.writeError = io.ErrClosedPipe
		w.WriteHeader(stdhttp.StatusEarlyHints)
		if n, err := w.Write([]byte("body")); n != 0 || !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("Write did not retain informational error: (%d, %v)", n, err)
		}
		if err := w.finish(); !errors.Is(err, io.ErrClosedPipe) || stream.Len() != 0 {
			t.Fatalf("finish after informational error = %v, wrote %d bytes", err, stream.Len())
		}
	})
}

func TestH3ResponseWriterStreamFlushErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status string
	}{
		{"final", "200"}, {"informational", "103"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
			stream.flushError = net.ErrClosed
			if tc.status == "103" {
				w.WriteHeader(stdhttp.StatusEarlyHints)
			}
			controller := stdhttp.NewResponseController(w)
			if err := controller.Flush(); !errors.Is(err, net.ErrClosed) || stream.published != 0 {
				t.Fatalf("Flush = %v, published %d bytes", err, stream.published)
			}
			stream.flushError = nil
			if n, err := w.Write([]byte("body")); n != 0 || !errors.Is(err, net.ErrClosed) {
				t.Fatalf("Write did not retain stream flush error: (%d, %v)", n, err)
			}
			if err := w.finish(); !errors.Is(err, net.ErrClosed) {
				t.Fatalf("finish did not retain stream flush error: %v", err)
			}
			wire := bytes.NewReader(stream.Bytes())
			h3ExpectResponseFields(t, wire, map[string][]string{":status": {tc.status}})
			h3ExpectNoResponseFrames(t, wire)
		})
	}
}

func TestH3ResponseWriterDeadlineForwarding(t *testing.T) {
	w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
	controller := stdhttp.NewResponseController(w)
	read := time.Unix(100, 0)
	if err := controller.SetReadDeadline(read); err != nil || !stream.readDeadline.Equal(read) {
		t.Fatalf("read deadline = (%v, %v), want (%v, nil)", stream.readDeadline, err, read)
	}
	write := time.Unix(200, 0)
	stream.deadlineError = net.ErrClosed
	if err := controller.SetWriteDeadline(write); !errors.Is(err, net.ErrClosed) || !stream.writeDeadline.Equal(write) {
		t.Fatalf("write deadline = (%v, %v), want (%v, net.ErrClosed)", stream.writeDeadline, err, write)
	}
	stream.deadlineError = nil
	if err := controller.SetWriteDeadline(time.Time{}); err != nil || !stream.writeDeadline.IsZero() {
		t.Fatalf("clearing write deadline = (%v, %v)", stream.writeDeadline, err)
	}
}

func TestH3ResponseWriterFieldLimits(t *testing.T) {
	t.Run("final-section", func(t *testing.T) {
		const exact = 42 + 2*(1+1+32)
		for _, limit := range []uint64{0, exact - 1, exact, h3UnlimitedFieldSectionSize} {
			w, stream := h3ResponseFixture(stdhttp.MethodGet, limit)
			w.Header().Add("X", "a")
			w.Header().Add("X", "b")
			err := w.finish()
			if limit < exact {
				if !errors.Is(err, errH3ResponseFieldSectionLimit) || stream.Len() != 0 {
					t.Errorf("limit %d: finish = %v, wrote %d bytes", limit, err, stream.Len())
				}
				continue
			}
			if err != nil {
				t.Fatalf("limit %d: %v", limit, err)
			}
			wire := bytes.NewReader(stream.Bytes())
			h3ExpectResponseFields(t, wire, map[string][]string{":status": {"200"}, "x": {"a", "b"}})
			h3ExpectNoResponseFrames(t, wire)
		}
	})
	t.Run("sniffed-field", func(t *testing.T) {
		w, stream := h3ResponseFixture(stdhttp.MethodGet, 42)
		if _, err := w.Write([]byte("x")); !errors.Is(err, errH3ResponseFieldSectionLimit) || stream.Len() != 0 {
			t.Fatalf("sniff over limit: Write = %v, wrote %d bytes", err, stream.Len())
		}
	})
	t.Run("trailer-section", func(t *testing.T) {
		w, stream := h3ResponseFixture(stdhttp.MethodGet, 86)
		w.Header().Set("Trailer", "X-End")
		if err := w.FlushError(); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("X-End", strings.Repeat("x", 50))
		if err := w.finish(); !errors.Is(err, errH3ResponseFieldSectionLimit) {
			t.Fatalf("trailer over limit: finish = %v", err)
		}
		wire := bytes.NewReader(stream.Bytes())
		h3ExpectResponseFields(t, wire, map[string][]string{":status": {"200"}, "trailer": {"X-End"}})
		h3ExpectNoResponseFrames(t, wire)
	})
}

func TestH3ResponseWriterValidatesAndFiltersFields(t *testing.T) {
	t.Run("connection-fields", func(t *testing.T) {
		w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
		h := w.Header()
		h["cOnNeCtIoN"] = []string{"X-Private, Keep-Alive"}
		h.Set("X-Private", "private")
		h.Set("Keep-Alive", "timeout=5")
		h.Set("Proxy-Connection", "close")
		h.Set("TE", "trailers")
		h.Set("Transfer-Encoding", "chunked")
		h.Set("Upgrade", "websocket")
		h.Set("X-Public", "\t keep \t")
		h3FinishResponseTest(t, w)
		wire := bytes.NewReader(stream.Bytes())
		h3ExpectResponseFields(t, wire, map[string][]string{":status": {"200"}, "x-public": {"keep"}})
		h3ExpectNoResponseFrames(t, wire)
	})
	t.Run("bad-initial-fields", func(t *testing.T) {
		for _, tc := range []struct{ key, value, problem string }{
			{"X-Bad Key", "value", "invalid response field name"},
			{":status", "201", "invalid response field name"},
			{"X-Bad", "value\r\nnot-a-field", "invalid response field value"},
			{"Connection", "not a token", "invalid response field list"},
			{"Trailer", "Content-Length", "invalid response trailer name"},
		} {
			w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
			w.Header()[tc.key] = []string{tc.value}
			if err := w.finish(); err == nil || !strings.Contains(err.Error(), tc.problem) || stream.Len() != 0 {
				t.Errorf("invalid %q: finish = %v, wrote %d bytes", tc.key, err, stream.Len())
			}
		}
	})
	t.Run("bad-trailer", func(t *testing.T) {
		for _, tc := range []struct{ name, value, problem string }{
			{"X-End", "bad\x00byte", "invalid response field value"},
			{stdhttp.TrailerPrefix + "X-Private", "value", "invalid response trailer name"},
			{stdhttp.TrailerPrefix + "X-\u212aey", "value", "invalid response trailer name"},
		} {
			w, stream := h3ResponseFixture(stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
			w.Header().Set("Connection", "X-Private")
			w.Header().Set("Trailer", "X-End")
			if err := w.FlushError(); err != nil {
				t.Fatal(err)
			}
			w.Header().Del("Connection")
			w.Header().Set(tc.name, tc.value)
			if err := w.finish(); err == nil || !strings.Contains(err.Error(), tc.problem) {
				t.Fatalf("invalid trailer %q: finish = %v", tc.name, err)
			}
			wire := bytes.NewReader(stream.Bytes())
			h3ExpectResponseFields(t, wire, map[string][]string{":status": {"200"}, "trailer": {"X-End"}})
			h3ExpectNoResponseFrames(t, wire)
		}
	})
}

func TestH3ResponseWriterGeneratesDate(t *testing.T) {
	stream := new(h3ResponseTestStream)
	w := newH3ResponseWriter(stream, stdhttp.MethodGet, h3UnlimitedFieldSectionSize)
	h3FinishResponseTest(t, w)
	wire := bytes.NewReader(stream.Bytes())
	fields, err := decodeH3Fields(nil, h3ReadResponseTestFrame(t, wire, 0x1), h3UnlimitedFieldSectionSize)
	if err != nil {
		t.Fatal(err)
	}
	if len(fields) != 2 || fields[0].name != ":status" || fields[0].value != "200" || fields[1].name != "date" {
		t.Fatalf("HEADERS fields = %v, want 200 and Date", fields)
	}
	if _, err := time.Parse(stdhttp.TimeFormat, fields[1].value); err != nil {
		t.Fatalf("parse Date: %v", err)
	}
	h3ExpectNoResponseFrames(t, wire)
}

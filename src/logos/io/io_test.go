// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Behavior gate for logos/io: take, fixed, and limit vectors carried
// from lib/std/Io/Reader.zig:1472, lib/std/Io/Writer.zig:2281, and
// lib/std/Io/Reader/Limited.zig at revision c7826aa7a0, plus a
// zero-allocation gate on the read path and a fuzz target over
// untrusted input with the standard library as oracle.
package io_test

import (
	"bytes"
	"errors"
	"fmt"
	stdio "io"
	"testing"

	logosio "logos/io"
)

type chunkSource struct {
	data  []byte
	chunk int
}

func (c *chunkSource) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(c.data) == 0 {
		return 0, stdio.EOF
	}
	n := c.chunk
	if n > len(c.data) {
		n = len(c.data)
	}
	if n > len(p) {
		n = len(p)
	}
	copy(p, c.data[:n])
	c.data = c.data[n:]
	return n, nil
}

type repeatSource struct {
	chunk []byte
}

func (c *repeatSource) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return copy(p, c.chunk), nil
}

func runTakeStep(t *testing.T, r logosio.Reader, n int, want string, fail bool) {
	t.Helper()
	got, err := logosio.Take(r, n)
	if fail {
		if !errors.Is(err, logosio.ErrEndOfStream) {
			t.Fatalf("Take(%d) = (%q, %v), want end of stream", n, got, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("Take(%d) = %v, want nil", n, err)
	}
	if string(got) != want {
		t.Fatalf("Take(%d) = %q, want %q", n, got, want)
	}
}

// Carries Reader.zig:1472 take. Planted fault: Discard advances pos
// by n+1, and the second take reads "c" or fails.
func TestTakeFixed(t *testing.T) {
	rows := []struct {
		name  string
		input string
		ns    []int
		want  []string
		fail  bool
	}{
		{"zig take", "abc", []int{2, 1}, []string{"ab", "c"}, false},
		{"take all", "foobar", []int{6}, []string{"foobar"}, false},
		{"take zero then one", "abc", []int{0, 1}, []string{"", "a"}, false},
		{"take zero of empty", "", []int{0}, []string{""}, false},
		{"take past end", "ab", []int{3}, []string{""}, true},
		{"take past end after take", "abc", []int{2, 2}, []string{"ab", ""}, true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			r := logosio.NewFixedReader([]byte(row.input))
			for i, n := range row.ns {
				runTakeStep(t, r, n, row.want[i], row.fail && i == len(row.ns)-1)
			}
		})
	}
}

func takeAll(t *testing.T, r logosio.Reader, ns []int) []byte {
	t.Helper()
	var got []byte
	for _, n := range ns {
		p, err := logosio.Take(r, n)
		if err != nil {
			t.Fatalf("Take(%d) = %v, want nil", n, err)
		}
		got = append(got, p...)
	}
	return got
}

// Carries the same take vectors over refilling reads. Short source
// reads stay legal, as in Zig. Planted fault: Fill appends at buf[0:]
// instead of buf[end:], and multi-fill takes corrupt.
func TestTakeStreamChunked(t *testing.T) {
	const input = "abcdefghijklmnopqrstuvwxyz"
	for _, chunk := range []int{1, 2, 5, 9} {
		t.Run(fmt.Sprintf("chunk %d", chunk), func(t *testing.T) {
			src := &chunkSource{data: []byte(input), chunk: chunk}
			r := logosio.NewReader(src, make([]byte, 16))
			if got := takeAll(t, r, []int{3, 7, 16}); string(got) != input {
				t.Fatalf("takes = %q, want %q", got, input)
			}
			if err := logosio.DiscardAll(r, 1); !errors.Is(err, logosio.ErrEndOfStream) {
				t.Fatalf("DiscardAll past end = %v, want end of stream", err)
			}
		})
	}
}

// A take larger than the window is a contract error: Zig peek and
// take assert capacity of at least n, and Fill reports it here as
// ErrBufferFull. Planted fault: Take loops on Fill forever instead
// of surfacing the error.
func TestTakeOversizedPinsBufferFull(t *testing.T) {
	src := &chunkSource{data: []byte("0123456789abcdef"), chunk: 16}
	r := logosio.NewReader(src, make([]byte, 8))
	if _, err := logosio.Take(r, 9); !errors.Is(err, logosio.ErrBufferFull) {
		t.Fatalf("Take(9) over 8-byte window = %v, want buffer full", err)
	}
}

// Fill compacts consumed bytes before reporting full: after Discard
// frees the head, the next Fill must succeed. Planted fault: Fill
// checks full before compacting, and this Fill reports buffer full.
func TestFillReusesDiscardedSpace(t *testing.T) {
	src := &chunkSource{data: []byte("0123456789abcdef"), chunk: 8}
	r := logosio.NewReader(src, make([]byte, 8))
	if _, err := logosio.Take(r, 5); err != nil {
		t.Fatalf("Take(5) = %v, want nil", err)
	}
	if err := r.Fill(); err != nil {
		t.Fatalf("Fill after Discard(5) = %v, want nil", err)
	}
	if r.Buffered() != 8 {
		t.Fatalf("Buffered = %d, want 8", r.Buffered())
	}
}

func runWriteChunks(t *testing.T, w *logosio.FixedWriter, chunks []string, want string, fail bool) {
	t.Helper()
	for i, c := range chunks {
		err := logosio.WriteAll(w, []byte(c))
		if fail && i == len(chunks)-1 {
			if !errors.Is(err, logosio.ErrWriteFailed) {
				t.Fatalf("WriteAll overflow = %v, want fixed-buffer error", err)
			}
			if string(w.Buffered()) != want {
				t.Fatalf("Buffered = %q, want %q", w.Buffered(), want)
			}
			return
		}
		if err != nil {
			t.Fatalf("WriteAll(%q) = %v, want nil", c, err)
		}
	}
	if fail {
		t.Fatalf("WriteAll overflow = nil, want fixed-buffer error")
	}
	if string(w.Buffered()) != want {
		t.Fatalf("Buffered = %q, want %q", w.Buffered(), want)
	}
}

// Carries Writer.zig:2281 fixed. Planted fault: Commit advances end
// by n+1, and Buffered holds "HelloWorld!" with a hole.
func TestFixedWriter(t *testing.T) {
	rows := []struct {
		name   string
		size   int
		chunks []string
		want   string
		fail   bool
	}{
		{"zig fixed", 255, []string{"Hello", "World", "!"}, "HelloWorld!", false},
		{"exact fit", 5, []string{"Hello"}, "Hello", false},
		{"overflow", 5, []string{"Hello", "World"}, "Hello", true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			runWriteChunks(t, logosio.NewFixedWriter(make([]byte, row.size)), row.chunks, row.want, row.fail)
		})
	}
}

// Pins the Reserve and Commit pairing directly. Planted fault:
// Reserve returns the whole tail past n, and the second Commit
// overruns the first reservation.
func TestFixedWriterReserveCommit(t *testing.T) {
	w := logosio.NewFixedWriter(make([]byte, 10))
	q := w.Reserve(5)
	if len(q) != 5 {
		t.Fatalf("Reserve(5) len = %d, want 5", len(q))
	}
	copy(q, "Hello")
	w.Commit(5)
	q = w.Reserve(5)
	copy(q, "World")
	w.Commit(5)
	if string(w.Buffered()) != "HelloWorld" {
		t.Fatalf("Buffered = %q, want %q", w.Buffered(), "HelloWorld")
	}
	if len(w.Reserve(1)) != 0 {
		t.Fatalf("Reserve past full buffer is visible, want empty")
	}
}

// Carries Io.zig:840 subtract. Planted fault: the over-limit check
// uses >=, and an exact consume falsely reports exhaustion.
func TestLimitSubtract(t *testing.T) {
	rows := []struct {
		name string
		l    logosio.Limit
		n    int64
		want logosio.Limit
		ok   bool
	}{
		{"unlimited stays", logosio.Unlimited, 5, logosio.Unlimited, true},
		{"unlimited zero", logosio.Unlimited, 0, logosio.Unlimited, true},
		{"exact", logosio.Limited(4), 4, logosio.Limited(0), true},
		{"over", logosio.Limited(4), 5, logosio.Limited(4), false},
		{"zero takes zero", logosio.Limited(0), 0, logosio.Limited(0), true},
		{"zero takes one", logosio.Limited(0), 1, logosio.Limited(0), false},
		{"partial", logosio.Limited(10), 3, logosio.Limited(7), true},
		{"clamped constructor", logosio.Limited(-3), 0, logosio.Limited(0), true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got, ok := row.l.Subtract(row.n)
			if ok != row.ok || got != row.want {
				t.Fatalf("Subtract(%d) = (%d, %v), want (%d, %v)", row.n, got, ok, row.want, row.ok)
			}
		})
	}
}

type limOp struct {
	take bool
	n    int
	want string
	fail bool
}

func runLimOp(t *testing.T, r *logosio.LimitedReader, op limOp) {
	t.Helper()
	if op.take {
		got, err := logosio.Take(r, op.n)
		if op.fail {
			if !errors.Is(err, logosio.ErrEndOfStream) {
				t.Fatalf("Take(%d) = (%q, %v), want end of stream", op.n, got, err)
			}
			return
		}
		if err != nil {
			t.Fatalf("Take(%d) = %v, want nil", op.n, err)
		}
		if string(got) != op.want {
			t.Fatalf("Take(%d) = %q, want %q", op.n, got, op.want)
		}
		return
	}
	err := logosio.DiscardAll(r, op.n)
	if op.fail {
		if !errors.Is(err, logosio.ErrEndOfStream) {
			t.Fatalf("DiscardAll(%d) = %v, want end of stream", op.n, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("DiscardAll(%d) = %v, want nil", op.n, err)
	}
}

// Carries Limited.zig stream, read, and discard limit cases. Planted
// fault: Peek ignores the remaining cap, and capped takes over-read.
func TestLimitedReader(t *testing.T) {
	rows := []struct {
		name  string
		input string
		limit logosio.Limit
		ops   []limOp
	}{
		{"read hit exactly", "i'm dying", logosio.Limited(4),
			[]limOp{{true, 2, "i'", false}, {true, 2, "m ", false}, {true, 2, "", true}}},
		{"read hit after partial", "i'm dying", logosio.Limited(5),
			[]limOp{{true, 2, "i'", false}, {true, 2, "m ", false}, {true, 2, "", true}}},
		{"discard hit exactly", "i'm dying", logosio.Limited(4),
			[]limOp{{false, 2, "", false}, {false, 2, "", false}, {false, 2, "", true}}},
		{"discard hit after partial", "i'm dying", logosio.Limited(5),
			[]limOp{{false, 2, "", false}, {false, 2, "", false}, {false, 2, "", true}}},
		{"under limit", "i'm dying", logosio.Limited(9),
			[]limOp{{true, 2, "i'", false}, {false, 2, "", false}, {true, 4, "dyin", false}}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			r := logosio.NewLimited(logosio.NewFixedReader([]byte(row.input)), row.limit)
			for _, op := range row.ops {
				runLimOp(t, r, op)
			}
		})
	}
}

// Pins the countdown behind the cap. Planted fault: Discard skips
// the countdown, and Remaining stays at 4 after two takes.
func TestLimitedRemaining(t *testing.T) {
	r := logosio.NewLimited(logosio.NewFixedReader([]byte("abcdef")), logosio.Limited(4))
	if got := r.Remaining(); got != logosio.Limited(4) {
		t.Fatalf("Remaining = %d, want 4", got)
	}
	if _, err := logosio.Take(r, 1); err != nil {
		t.Fatalf("Take = %v, want nil", err)
	}
	r.Discard(1)
	if got := r.Remaining(); got != logosio.Limited(2) {
		t.Fatalf("Remaining = %d, want 2", got)
	}
}

type streamRow struct {
	name     string
	input    string
	srcLimit logosio.Limit
	opLimit  logosio.Limit
	dstSize  int
	want     string
	total    int64
	fail     bool
}

func runStreamCase(t *testing.T, row streamRow) {
	t.Helper()
	var r logosio.Reader = logosio.NewFixedReader([]byte(row.input))
	if row.srcLimit != logosio.Unlimited {
		r = logosio.NewLimited(r, row.srcLimit)
	}
	w := logosio.NewFixedWriter(make([]byte, row.dstSize))
	total, err := logosio.Stream(r, w, row.opLimit)
	if row.fail {
		if !errors.Is(err, logosio.ErrWriteFailed) {
			t.Fatalf("Stream = (%d, %v), want fixed-buffer error", total, err)
		}
	} else if err != nil {
		t.Fatalf("Stream = %v, want nil", err)
	}
	if total != row.total {
		t.Fatalf("Stream total = %d, want %d", total, row.total)
	}
	if string(w.Buffered()) != row.want {
		t.Fatalf("Stream out = %q, want %q", w.Buffered(), row.want)
	}
}

// Carries the Limited.zig:36 bounded stream case. Planted fault:
// Stream never spends its limit, and the op-limit row copies all
// ten bytes instead of four.
func TestStream(t *testing.T) {
	rows := []streamRow{
		{"unlimited", "foobar", logosio.Unlimited, logosio.Unlimited, 16, "foobar", 6, false},
		{"zig limited stream", "test bytes", logosio.Limited(4), logosio.Limited(7), 16, "test", 4, false},
		{"op limit binds", "test bytes", logosio.Unlimited, logosio.Limited(4), 16, "test", 4, false},
		{"zero limit", "foobar", logosio.Unlimited, logosio.Limited(0), 16, "", 0, false},
		{"empty", "", logosio.Unlimited, logosio.Unlimited, 16, "", 0, false},
		{"dst overflow", "foobar", logosio.Unlimited, logosio.Unlimited, 3, "foo", 3, true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			runStreamCase(t, row)
		})
	}
}

func runDiscardCase(t *testing.T, input string, n int, wantBuffered int, fail bool) {
	t.Helper()
	r := logosio.NewFixedReader([]byte(input))
	err := logosio.DiscardAll(r, n)
	if fail {
		if !errors.Is(err, logosio.ErrEndOfStream) {
			t.Fatalf("DiscardAll(%d) = %v, want end of stream", n, err)
		}
		return
	}
	if err != nil {
		t.Fatalf("DiscardAll(%d) = %v, want nil", n, err)
	}
	if r.Buffered() != wantBuffered {
		t.Fatalf("Buffered = %d, want %d", r.Buffered(), wantBuffered)
	}
}

// Pins multi-fill discard. Planted fault: DiscardAll returns after
// the first Fill without rechecking, and the stream row leaves nine
// bytes behind.
func TestDiscardAll(t *testing.T) {
	rows := []struct {
		name         string
		input        string
		n            int
		wantBuffered int
		fail         bool
	}{
		{"exact", "foobar", 6, 0, false},
		{"zero", "abc", 0, 3, false},
		{"past end", "ab", 3, 0, true},
		{"empty past", "", 1, 0, true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			runDiscardCase(t, row.input, row.n, row.wantBuffered, row.fail)
		})
	}
	t.Run("stream multi-fill", func(t *testing.T) {
		src := &chunkSource{data: []byte("0123456789"), chunk: 1}
		r := logosio.NewReader(src, make([]byte, 4))
		if err := logosio.DiscardAll(r, 10); err != nil {
			t.Fatalf("DiscardAll(10) = %v, want nil", err)
		}
		if err := logosio.DiscardAll(r, 1); !errors.Is(err, logosio.ErrEndOfStream) {
			t.Fatalf("DiscardAll past end = %v, want end of stream", err)
		}
	})
}

// The allocation gate: repeated framing ops allocate nothing.
// Planted fault: Take copies into a fresh slice, and this gate
// counts one allocation per call.
func TestFramingAllocs(t *testing.T) {
	r := logosio.NewReader(&repeatSource{chunk: []byte("0123456789abcdef")}, make([]byte, 1024))
	if a := testing.AllocsPerRun(200, func() {
		p := r.Peek(64)
		r.Discard(len(p))
		if err := r.Fill(); err != nil {
			t.Fatalf("Fill = %v, want nil", err)
		}
	}); a != 0 {
		t.Fatalf("framing ops = %v allocs, want 0", a)
	}
	r2 := logosio.NewReader(&repeatSource{chunk: []byte("0123456789abcdef")}, make([]byte, 1024))
	if a := testing.AllocsPerRun(200, func() {
		p, err := logosio.Take(r2, 32)
		if err != nil {
			t.Fatalf("Take = %v, want nil", err)
		}
		if len(p) != 32 {
			t.Fatalf("Take = %d bytes, want 32", len(p))
		}
	}); a != 0 {
		t.Fatalf("Take = %v allocs, want 0", a)
	}
}

// The reader input is untrusted bytes, so the framing loop gets a
// fuzz target with the standard library as oracle. Planted fault:
// Fill compacts from buf[0:] instead of buf[seek:], and any corpus
// larger than the window mismatches.
func FuzzFramingOracle(f *testing.F) {
	f.Add([]byte("hello world, hello world"))
	f.Add([]byte(""))
	f.Add([]byte("a"))
	f.Add([]byte("0123456789abcdef0123456789abcdef"))
	f.Fuzz(func(t *testing.T, data []byte) {
		src := &chunkSource{data: data, chunk: 1 + len(data)%7}
		r := logosio.NewReader(src, make([]byte, 16))
		var got []byte
		for {
			p, err := logosio.Take(r, 5)
			if err != nil {
				if !errors.Is(err, logosio.ErrEndOfStream) {
					t.Fatalf("Take = %v, want end of stream", err)
				}
				break
			}
			got = append(got, p...)
		}
		w := logosio.NewFixedWriter(make([]byte, len(data)))
		total, err := logosio.Stream(r, w, logosio.Unlimited)
		if err != nil {
			t.Fatalf("Stream = %v, want nil", err)
		}
		if total != int64(len(w.Buffered())) {
			t.Fatalf("Stream total = %d, Buffered len = %d", total, len(w.Buffered()))
		}
		full := append(got, w.Buffered()...)
		if !bytes.Equal(full, data) {
			t.Fatalf("round trip = %q, want %q", full, data)
		}
	})
}

// A source error that arrives with data must surface instead of a clean
// end of stream. Planted fault: Fill drops an error returned alongside
// bytes, and Stream reports a truncated copy as successful.
type errDataSource struct {
	data  []byte
	err   error
	calls int
}

func (s *errDataSource) Read(p []byte) (int, error) {
	s.calls++
	if len(s.data) == 0 {
		return 0, stdio.EOF
	}
	n := copy(p, s.data)
	s.data = s.data[n:]
	if len(s.data) == 0 {
		return n, s.err
	}
	return n, nil
}

func TestStreamSurfacesErrorWithData(t *testing.T) {
	src := &errDataSource{data: []byte("abc"), err: stdio.ErrUnexpectedEOF}
	r := logosio.NewReader(src, make([]byte, 16))
	w := logosio.NewFixedWriter(make([]byte, 16))
	total, err := logosio.Stream(r, w, logosio.Unlimited)
	if total != 3 {
		t.Fatalf("Stream total = %d, want 3", total)
	}
	if !errors.Is(err, stdio.ErrUnexpectedEOF) {
		t.Fatalf("Stream err = %v, want unexpected EOF", err)
	}
	if string(w.Buffered()) != "abc" {
		t.Fatalf("Stream out = %q, want %q", w.Buffered(), "abc")
	}
}

// A Take past a capped stream's buffered end must fail instead of
// reading past the limit. Planted fault: Fill keeps reading after the
// remainder is buffered, waiting on bytes beyond the limit.
type fillCounter struct {
	logosio.Reader
	fills int
}

func (r *fillCounter) Fill() error {
	r.fills++
	return r.Reader.Fill()
}

func TestTakePastLimitEndsWithoutFilling(t *testing.T) {
	src := &fillCounter{Reader: logosio.NewFixedReader([]byte("a"))}
	inner := logosio.NewLimited(src, logosio.Limited(1))
	if _, err := logosio.Take(inner, 2); !errors.Is(err, logosio.ErrEndOfStream) {
		t.Fatalf("Take past limit = %v, want end of stream", err)
	}
	if src.fills != 0 {
		t.Fatalf("underlying fills = %d, want 0", src.fills)
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"testing/synctest"
	"time"
)

type h3DeadlineTestResult struct {
	n   int
	err error
}

type h3DeadlineTestCall struct {
	ctx   context.Context
	reply chan h3DeadlineTestResult
}

type h3DeadlineTestStream struct {
	guard    context.Context
	readCtx  context.Context
	writeCtx context.Context
	reads    chan h3DeadlineTestCall
	writes   chan h3DeadlineTestCall
	flushes  chan h3DeadlineTestCall
}

func newH3DeadlineTestStream(t *testing.T) *h3DeadlineTestStream {
	guard, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	return &h3DeadlineTestStream{
		guard:    guard,
		readCtx:  context.Background(),
		writeCtx: context.Background(),
		reads:    make(chan h3DeadlineTestCall, 1),
		writes:   make(chan h3DeadlineTestCall, 1),
		flushes:  make(chan h3DeadlineTestCall, 1),
	}
}

func (s *h3DeadlineTestStream) SetReadContext(ctx context.Context)  { s.readCtx = ctx }
func (s *h3DeadlineTestStream) SetWriteContext(ctx context.Context) { s.writeCtx = ctx }
func (s *h3DeadlineTestStream) Read([]byte) (int, error) {
	return s.call(&s.readCtx, s.reads)
}
func (s *h3DeadlineTestStream) Write([]byte) (int, error) {
	return s.call(&s.writeCtx, s.writes)
}
func (s *h3DeadlineTestStream) Flush() error {
	_, err := s.call(&s.writeCtx, s.flushes)
	return err
}

func (s *h3DeadlineTestStream) call(ctx *context.Context, calls chan<- h3DeadlineTestCall) (int, error) {
	call := h3DeadlineTestCall{ctx: *ctx, reply: make(chan h3DeadlineTestResult, 1)}
	select {
	case calls <- call:
	case <-s.guard.Done():
		return 0, s.guard.Err()
	}
	select {
	case r := <-call.reply:
		if *ctx != call.ctx {
			return r.n, errors.New("stream context changed during I/O")
		}
		return r.n, r.err
	case <-s.guard.Done():
		return 0, s.guard.Err()
	}
}

func h3DeadlineTestStartIO(op func() (int, error)) <-chan h3DeadlineTestResult {
	result := make(chan h3DeadlineTestResult, 1)
	go func() {
		n, err := op()
		result <- h3DeadlineTestResult{n: n, err: err}
	}()
	return result
}

func h3DeadlineTestAwait[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	guard := time.NewTimer(10 * time.Second)
	defer guard.Stop()
	select {
	case value := <-ch:
		return value
	case <-guard.C:
		t.Fatal("timed out waiting for stream I/O event")
		var zero T
		return zero
	}
}

func TestH3DeadlineStreamReadUpdates(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		raw := newH3DeadlineTestStream(t)
		stream := newH3DeadlineStream(t.Context(), raw)
		setReadDeadline := func(deadline time.Time) {
			t.Helper()
			if err := stream.SetReadDeadline(deadline); err != nil {
				t.Fatal(err)
			}
		}
		read := func() (int, error) { return stream.Read(make([]byte, 1)) }
		result := h3DeadlineTestStartIO(read)
		call := h3DeadlineTestAwait(t, raw.reads)

		first := time.Now().Add(time.Second)
		later := first.Add(time.Second)
		setReadDeadline(first)
		setReadDeadline(later)
		h3DeadlineTestAwait(t, time.After(time.Until(first)+time.Nanosecond))
		if err := call.ctx.Err(); err != nil {
			t.Fatalf("extending deadline canceled active read: %v", err)
		}
		setReadDeadline(time.Time{})
		h3DeadlineTestAwait(t, time.After(time.Until(later)+time.Nanosecond))
		if err := call.ctx.Err(); err != nil {
			t.Fatalf("clearing deadline canceled active read: %v", err)
		}

		setReadDeadline(time.Now().Add(time.Minute))
		setReadDeadline(time.Now().Add(time.Second))
		h3DeadlineTestAwait(t, call.ctx.Done())
		setReadDeadline(time.Now().Add(time.Minute))
		call.reply <- h3DeadlineTestResult{err: call.ctx.Err()}
		if r := h3DeadlineTestAwait(t, result); r.n != 0 || !errors.Is(r.err, os.ErrDeadlineExceeded) {
			t.Fatalf("expired read = (%d, %v), want (0, deadline exceeded)", r.n, r.err)
		}

		result = h3DeadlineTestStartIO(read)
		call = h3DeadlineTestAwait(t, raw.reads)
		call.reply <- h3DeadlineTestResult{err: io.EOF}
		if r := h3DeadlineTestAwait(t, result); r.n != 0 || !errors.Is(r.err, io.EOF) {
			t.Fatalf("read after refresh = (%d, %v), want (0, EOF)", r.n, r.err)
		}

		setReadDeadline(time.Now().Add(-time.Second))
		if r := h3DeadlineTestAwait(t, h3DeadlineTestStartIO(read)); r.n != 0 || !errors.Is(r.err, os.ErrDeadlineExceeded) {
			t.Fatalf("read with past deadline = (%d, %v), want (0, deadline exceeded)", r.n, r.err)
		}
	})
}

func TestH3DeadlineStreamDuplexCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, cancel := context.WithCancel(t.Context())
		defer cancel()
		raw := newH3DeadlineTestStream(t)
		stream := newH3DeadlineStream(parent, raw)
		read := h3DeadlineTestStartIO(func() (int, error) { return stream.Read(make([]byte, 1)) })
		readCall := h3DeadlineTestAwait(t, raw.reads)
		write := h3DeadlineTestStartIO(func() (int, error) { return stream.Write([]byte("hello")) })
		writeCall := h3DeadlineTestAwait(t, raw.writes)
		if err := stream.SetWriteDeadline(time.Now().Add(-time.Second)); err != nil {
			t.Fatal(err)
		}
		h3DeadlineTestAwait(t, writeCall.ctx.Done())
		writeCall.reply <- h3DeadlineTestResult{n: 2, err: writeCall.ctx.Err()}
		if r := h3DeadlineTestAwait(t, write); r.n != 2 || !errors.Is(r.err, os.ErrDeadlineExceeded) {
			t.Fatalf("partial write = (%d, %v), want (2, deadline exceeded)", r.n, r.err)
		}
		if err := readCall.ctx.Err(); err != nil {
			t.Fatalf("write deadline canceled read: %v", err)
		}

		flush := func() (int, error) { return 0, stream.Flush() }
		if r := h3DeadlineTestAwait(t, h3DeadlineTestStartIO(flush)); !errors.Is(r.err, os.ErrDeadlineExceeded) {
			t.Fatalf("flush with past deadline = %v, want deadline exceeded", r.err)
		}
		if err := stream.SetWriteDeadline(time.Time{}); err != nil {
			t.Fatal(err)
		}
		flushed := h3DeadlineTestStartIO(flush)
		flushCall := h3DeadlineTestAwait(t, raw.flushes)
		cancel()
		h3DeadlineTestAwait(t, readCall.ctx.Done())
		h3DeadlineTestAwait(t, flushCall.ctx.Done())
		readCall.reply <- h3DeadlineTestResult{err: readCall.ctx.Err()}
		if r := h3DeadlineTestAwait(t, read); !errors.Is(r.err, context.Canceled) || errors.Is(r.err, os.ErrDeadlineExceeded) {
			t.Fatalf("read on parent cancellation = %v, want context cancellation", r.err)
		}
		transportErr := errors.New("stream reset by peer")
		flushCall.reply <- h3DeadlineTestResult{err: transportErr}
		if r := h3DeadlineTestAwait(t, flushed); !errors.Is(r.err, transportErr) {
			t.Fatalf("flush on parent cancellation = %v, want transport error", r.err)
		}
	})
}

type h3ImmediateDeadlineTestStream struct {
	readCtx  context.Context
	writeCtx context.Context
}

func (s *h3ImmediateDeadlineTestStream) SetReadContext(ctx context.Context)  { s.readCtx = ctx }
func (s *h3ImmediateDeadlineTestStream) SetWriteContext(ctx context.Context) { s.writeCtx = ctx }

func (s *h3ImmediateDeadlineTestStream) Read(p []byte) (int, error) {
	clear(p)
	return len(p), s.readCtx.Err()
}

func (s *h3ImmediateDeadlineTestStream) Write(p []byte) (int, error) {
	return len(p), s.writeCtx.Err()
}

func (s *h3ImmediateDeadlineTestStream) Flush() error {
	return s.writeCtx.Err()
}

func TestH3DeadlineStreamSteadyStateAllocations(t *testing.T) {
	parent, cancel := context.WithCancel(t.Context())
	defer cancel()
	stream := newH3DeadlineStream(parent, new(h3ImmediateDeadlineTestStream))
	var buf [1]byte
	exchange := func() {
		if n, err := stream.Read(buf[:]); n != len(buf) || err != nil {
			t.Fatalf("Read = (%d, %v), want (%d, nil)", n, err, len(buf))
		}
		if n, err := stream.Write(buf[:]); n != len(buf) || err != nil {
			t.Fatalf("Write = (%d, %v), want (%d, nil)", n, err, len(buf))
		}
		if err := stream.Flush(); err != nil {
			t.Fatal(err)
		}
	}

	if allocs := testing.AllocsPerRun(100, exchange); allocs != 0 {
		t.Fatalf("allocations per exchange without deadlines = %g, want 0", allocs)
	}
	deadline := time.Now().Add(time.Hour)
	if err := stream.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := stream.SetWriteDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if allocs := testing.AllocsPerRun(100, exchange); allocs != 0 {
		t.Fatalf("allocations per exchange with deadlines = %g, want 0", allocs)
	}
}

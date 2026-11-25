// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"context"
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

type h3ContextStream interface {
	io.Reader
	io.Writer
	Flush() error
	SetReadContext(context.Context)
	SetWriteContext(context.Context)
}

type h3DeadlineStream struct {
	parent context.Context
	stream h3ContextStream
	read   h3StreamDeadline
	write  h3StreamDeadline
}

// The stream owner cancels parent after finishing the response.
func newH3DeadlineStream(parent context.Context, stream h3ContextStream) *h3DeadlineStream {
	return &h3DeadlineStream{parent: parent, stream: stream}
}

func (s *h3DeadlineStream) Read(p []byte) (int, error) {
	return s.read.run(s.parent, s.stream.SetReadContext, func() (int, error) {
		return s.stream.Read(p)
	})
}

func (s *h3DeadlineStream) Write(p []byte) (int, error) {
	return s.write.run(s.parent, s.stream.SetWriteContext, func() (int, error) {
		return s.stream.Write(p)
	})
}

func (s *h3DeadlineStream) Flush() error {
	_, err := s.write.run(s.parent, s.stream.SetWriteContext, func() (int, error) {
		return 0, s.stream.Flush()
	})
	return err
}

func (s *h3DeadlineStream) SetReadDeadline(deadline time.Time) error {
	s.read.set(deadline)
	return nil
}

func (s *h3DeadlineStream) SetWriteDeadline(deadline time.Time) error {
	s.write.set(deadline)
	return nil
}

// Each direction serializes stream I/O and its context setter with ioMu.
// Deadline updates only take mu, so they can cancel a blocked operation.
type h3StreamDeadline struct {
	ioMu sync.Mutex
	mu   sync.Mutex

	deadline time.Time
	active   bool
	ctx      context.Context
	cancel   context.CancelCauseFunc
	timer    *time.Timer
}

func (d *h3StreamDeadline) run(parent context.Context, setContext func(context.Context), operation func() (int, error)) (int, error) {
	d.ioMu.Lock()
	defer d.ioMu.Unlock()

	ctx, err := d.begin(parent)
	if err != nil {
		return 0, err
	}
	defer d.end()
	if err := ctx.Err(); err != nil {
		return 0, h3StreamIOError(ctx, err)
	}
	setContext(ctx)
	defer setContext(parent)

	n, err := operation()
	return n, h3StreamIOError(ctx, err)
}

func (d *h3StreamDeadline) begin(parent context.Context) (context.Context, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := parent.Err(); err != nil {
		return nil, h3StreamIOError(parent, err)
	}
	if !d.deadline.IsZero() && time.Until(d.deadline) <= 0 {
		return nil, os.ErrDeadlineExceeded
	}
	if d.ctx == nil || d.ctx.Err() != nil {
		d.ctx, d.cancel = context.WithCancelCause(parent)
	}
	d.active = true
	d.armTimer()
	return d.ctx, nil
}

func (d *h3StreamDeadline) end() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.stopTimer()
	d.active = false
}

func (d *h3StreamDeadline) set(deadline time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	// An update after expiry applies to the next operation, even if the old
	// timer callback has not run yet.
	if d.active && !d.deadline.IsZero() && !now.Before(d.deadline) {
		d.cancel(os.ErrDeadlineExceeded)
	}
	if !deadline.IsZero() {
		deadline = now.Add(deadline.Sub(now))
	}
	d.deadline = deadline
	if d.active {
		d.armTimer()
	}
}

func (d *h3StreamDeadline) armTimer() {
	if d.deadline.IsZero() || d.ctx.Err() != nil {
		d.stopTimer()
		return
	}
	remaining := time.Until(d.deadline)
	if remaining <= 0 {
		d.stopTimer()
		d.cancel(os.ErrDeadlineExceeded)
		return
	}
	if d.timer != nil {
		d.timer.Reset(remaining)
		return
	}
	d.timer = time.AfterFunc(remaining, func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.active && !d.deadline.IsZero() && time.Until(d.deadline) <= 0 {
			d.cancel(os.ErrDeadlineExceeded)
		}
	})
}

func (d *h3StreamDeadline) stopTimer() {
	if d.timer != nil {
		d.timer.Stop()
	}
}

func h3StreamIOError(ctx context.Context, err error) error {
	if err == nil || errors.Is(err, os.ErrDeadlineExceeded) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		(errors.Is(err, context.Canceled) && errors.Is(context.Cause(ctx), os.ErrDeadlineExceeded)) {
		return errors.Join(os.ErrDeadlineExceeded, err)
	}
	return err
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Translated from lib/std/Io/Writer.zig at revision c7826aa7a0 (Zig, MIT).

package io

import (
	"errors"
	"fmt"
	stdio "io"
)

// Writer is a visible-buffer sink. Reserve views the free window tail,
// Commit publishes written bytes, and Flush drains to the sink.
// Reserve returns at most n bytes; a short Reserve means the caller
// flushes and reserves again. Commit panics past the window.
type Writer interface {
	Reserve(n int) []byte
	Commit(n int)
	Flush() error
}

// FixedWriter writes into b and then fails. The zero value takes
// nothing. Buffered reports what landed.
type FixedWriter struct {
	buf []byte
	end int
}

// NewFixedWriter returns a Writer over b.
func NewFixedWriter(b []byte) *FixedWriter {
	return &FixedWriter{buf: b}
}

// Reserve returns up to n free tail bytes.
func (w *FixedWriter) Reserve(n int) []byte {
	if n < 0 {
		panic("io: Reserve negative n")
	}
	avail := len(w.buf) - w.end
	if n > avail {
		n = avail
	}
	return w.buf[w.end : w.end+n]
}

// Commit publishes n reserved bytes, panicking past the window.
func (w *FixedWriter) Commit(n int) {
	if n < 0 || w.end+n > len(w.buf) {
		panic("io: Commit beyond reserved window")
	}
	w.end += n
}

// Flush is a no-op: fixed bytes are already visible.
func (w *FixedWriter) Flush() error {
	return nil
}

// Buffered returns the written prefix of the fixed buffer.
func (w *FixedWriter) Buffered() []byte {
	return w.buf[:w.end]
}

// StreamWriter drains buf to dst on Flush. buf is caller-owned and
// must hold the largest single write. The zero value is unusable;
// build with NewWriter.
type StreamWriter struct {
	dst stdio.Writer
	buf []byte
	end int
}

// NewWriter returns a Writer that flushes buf to dst. It panics on a
// nil dst or an empty buf.
func NewWriter(dst stdio.Writer, buf []byte) *StreamWriter {
	if dst == nil {
		panic("io: NewWriter nil destination")
	}
	if len(buf) == 0 {
		panic("io: NewWriter empty buffer")
	}
	return &StreamWriter{dst: dst, buf: buf}
}

// Reserve returns up to n free tail bytes, empty when full.
func (w *StreamWriter) Reserve(n int) []byte {
	if n < 0 {
		panic("io: Reserve negative n")
	}
	avail := len(w.buf) - w.end
	if n > avail {
		n = avail
	}
	return w.buf[w.end : w.end+n]
}

// Commit publishes n reserved bytes, panicking past the window.
func (w *StreamWriter) Commit(n int) {
	if n < 0 || w.end+n > len(w.buf) {
		panic("io: Commit beyond reserved window")
	}
	w.end += n
}

// Flush writes every pending byte to dst.
func (w *StreamWriter) Flush() error {
	for w.end > 0 {
		n, err := w.dst.Write(w.buf[:w.end])
		if n > 0 {
			copy(w.buf, w.buf[n:w.end])
			w.end -= n
		}
		if err != nil {
			return fmt.Errorf("io: flush: %w", err)
		}
		if n == 0 {
			return ErrWriteFailed
		}
	}
	return nil
}

// WriteAll writes all of p, flushing as needed. A fixed Writer that
// cannot hold the rest reports ErrWriteFailed.
func WriteAll(w Writer, p []byte) error {
	for len(p) > 0 {
		q := w.Reserve(len(p))
		if len(q) == 0 {
			if err := w.Flush(); err != nil {
				return err
			}
			q = w.Reserve(len(p))
			if len(q) == 0 {
				return ErrWriteFailed
			}
		}
		c := copy(q, p)
		w.Commit(c)
		p = p[c:]
	}
	return nil
}

const streamChunk = 8192

// Stream moves bytes from r to w until r ends or l runs out, returning
// the moved count. End of stream and a spent limit both return a nil
// error. A short destination window is flushed, never grown.
func Stream(r Reader, w Writer, l Limit) (int64, error) {
	var total int64
	rem := l
	for rem == Unlimited || rem > 0 {
		p := r.Peek(rem.Clamp(streamChunk))
		if len(p) > 0 {
			if err := WriteAll(w, p); err != nil {
				return total, err
			}
			r.Discard(len(p))
			total += int64(len(p))
			if rem != Unlimited {
				rem -= Limit(len(p))
			}
			continue
		}
		err := r.Fill()
		if err == nil {
			continue
		}
		if errors.Is(err, ErrEndOfStream) {
			return total, nil
		}
		return total, err
	}
	return total, nil
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Translated from lib/std/Io/Reader.zig and lib/std/Io/Reader/Limited.zig
// at revision c7826aa7a0 (Zig, MIT).

package io

import (
	"fmt"
	stdio "io"
)

// Reader is a visible-buffer source. Peek views the buffered window,
// Discard consumes from it, and Fill moves more bytes into it. Peek
// returns at most n bytes and never fails; a short Peek means the
// caller fills again or takes what is there. Discard panics past the
// buffered window. Slices from Peek alias the window and expire on
// the next Fill, Discard, or Peek.
type Reader interface {
	Peek(n int) []byte
	Discard(n int)
	Fill() error
}

// FixedReader reads the bytes b and then ends. The zero value reads
// nothing. b stays owned by the caller and is never written.
type FixedReader struct {
	buf []byte
	pos int
}

// NewFixedReader returns a Reader over b.
func NewFixedReader(b []byte) *FixedReader {
	return &FixedReader{buf: b}
}

// Peek returns up to n unread bytes.
func (r *FixedReader) Peek(n int) []byte {
	if n < 0 {
		panic("io: Peek negative n")
	}
	avail := len(r.buf) - r.pos
	if n > avail {
		n = avail
	}
	return r.buf[r.pos : r.pos+n]
}

// Discard consumes n buffered bytes, panicking past the window.
func (r *FixedReader) Discard(n int) {
	if n < 0 || n > len(r.buf)-r.pos {
		panic("io: Discard beyond buffered window")
	}
	r.pos += n
}

// Fill always reports end of stream: fixed bytes are all visible.
func (r *FixedReader) Fill() error {
	return ErrEndOfStream
}

// Buffered returns the unread count.
func (r *FixedReader) Buffered() int {
	return len(r.buf) - r.pos
}

// StreamReader reads from src into buf, a caller-owned window that
// must hold the largest frame the caller takes at once. Short source
// reads are legal. A source error that arrives with data is held as
// pending and surfaces on the next Fill, so a truncated final read
// is never reported as a clean end of stream. The zero value is
// unusable; build with NewReader.
type StreamReader struct {
	src     stdio.Reader
	buf     []byte
	seek    int
	end     int
	pending error
}

// NewReader returns a Reader that fills buf from src. It panics on a
// nil src or an empty buf.
func NewReader(src stdio.Reader, buf []byte) *StreamReader {
	if src == nil {
		panic("io: NewReader nil source")
	}
	if len(buf) == 0 {
		panic("io: NewReader empty buffer")
	}
	return &StreamReader{src: src, buf: buf}
}

// Peek returns up to n buffered bytes without consuming them.
func (r *StreamReader) Peek(n int) []byte {
	if n < 0 {
		panic("io: Peek negative n")
	}
	avail := r.end - r.seek
	if n > avail {
		n = avail
	}
	return r.buf[r.seek : r.seek+n]
}

// Discard consumes n buffered bytes, panicking past the window.
func (r *StreamReader) Discard(n int) {
	if n < 0 || n > r.end-r.seek {
		panic("io: Discard beyond buffered window")
	}
	r.seek += n
}

// Fill moves one source read into the window, compacting first when
// the tail is full. It returns nil after adding bytes, ErrEndOfStream
// when src ends, and ErrBufferFull when the window stays full. An
// error that arrived with data on an earlier Fill is reported before
// the source is read again.
func (r *StreamReader) Fill() error {
	if r.pending != nil {
		err := r.pending
		r.pending = nil
		return err
	}
	if r.end == len(r.buf) {
		if r.seek == 0 {
			return ErrBufferFull
		}
		r.end = copy(r.buf, r.buf[r.seek:r.end])
		r.seek = 0
	}
	n, err := r.src.Read(r.buf[r.end:])
	if n > 0 {
		r.end += n
		if err != nil {
			r.pending = fillError(err)
		}
		return nil
	}
	if err == nil {
		return nil
	}
	return fillError(err)
}

// fillError translates a source error at a window boundary: EOF means
// the stream ended, anything else keeps its cause.
func fillError(err error) error {
	if err == stdio.EOF {
		return ErrEndOfStream
	}
	return fmt.Errorf("io: fill: %w", err)
}

// Buffered returns the buffered count.
func (r *StreamReader) Buffered() int {
	return r.end - r.seek
}

// LimitedReader caps what the caller sees at a Limit, counting every
// Discard down. Fill past the cap reports end of stream. It owns no
// buffer; the wrapped Reader keeps its window. Build with NewLimited.
type LimitedReader struct {
	r         Reader
	remaining Limit
}

// NewLimited returns a Reader capped at l. A negative finite l reads
// nothing. It panics on a nil Reader.
func NewLimited(r Reader, l Limit) *LimitedReader {
	if r == nil {
		panic("io: NewLimited nil reader")
	}
	if l != Unlimited && l < 0 {
		l = Limit(0)
	}
	return &LimitedReader{r: r, remaining: l}
}

// Peek returns up to n bytes within the remaining cap.
func (l *LimitedReader) Peek(n int) []byte {
	if n < 0 {
		panic("io: Peek negative n")
	}
	return l.r.Peek(l.remaining.Clamp(n))
}

// Discard consumes n capped bytes, panicking past the cap.
func (l *LimitedReader) Discard(n int) {
	if l.remaining != Unlimited {
		if n < 0 || int64(n) > int64(l.remaining) {
			panic("io: Discard beyond limit")
		}
		l.remaining -= Limit(n)
	}
	l.r.Discard(n)
}

// Fill reports end of stream past the cap, else fills below. Once the
// finite remainder is fully buffered, it reports end of stream without
// reading past the limit: a Take for more than the cap can never
// succeed, so filling further would only wait on bytes the caller may
// never spend.
func (l *LimitedReader) Fill() error {
	if l.remaining != Unlimited && l.remaining <= 0 {
		return ErrEndOfStream
	}
	if l.remaining != Unlimited && int64(len(l.r.Peek(l.remaining.Clamp(1<<30)))) >= int64(l.remaining) {
		return ErrEndOfStream
	}
	return l.r.Fill()
}

// Remaining returns the uncapped remainder, or Unlimited.
func (l *LimitedReader) Remaining() Limit {
	return l.remaining
}

// Take returns the next n bytes, filling as needed. The slice aliases
// the window and expires on the next window call.
func Take(r Reader, n int) ([]byte, error) {
	if n < 0 {
		panic("io: Take negative n")
	}
	for len(r.Peek(n)) < n {
		if err := r.Fill(); err != nil {
			return nil, err
		}
	}
	p := r.Peek(n)
	r.Discard(n)
	return p, nil
}

// DiscardAll skips n bytes, filling as needed.
func DiscardAll(r Reader, n int) error {
	if n < 0 {
		panic("io: DiscardAll negative n")
	}
	for n > 0 {
		q := r.Peek(n)
		if len(q) > 0 {
			r.Discard(len(q))
			n -= len(q)
			continue
		}
		if err := r.Fill(); err != nil {
			return err
		}
	}
	return nil
}

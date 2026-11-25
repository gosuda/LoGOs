// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Translated from lib/std/Io.zig (Limit) at revision c7826aa7a0 (Zig, MIT).

// Package io is the loGo/s visible-buffer input/output package: framing
// works the caller-visible window, Fill and Flush move bytes, and Limit
// bounds every bounded copy. .lgo code imports it as logos/io; eject
// leaves it a plain Go package.
package io

import "errors"

// Unlimited is the Limit for no bound.
const Unlimited Limit = -1

// ErrEndOfStream reports that no more bytes will arrive.
var ErrEndOfStream = errors.New("io: end of stream")

// ErrWriteFailed reports that a fixed buffer cannot take more bytes.
var ErrWriteFailed = errors.New("io: fixed buffer full")

// ErrBufferFull reports that Fill found a full window with nothing discarded.
var ErrBufferFull = errors.New("io: buffer full")

// Limit bounds how many bytes a bounded operation may move. Unlimited
// means no bound; every other negative value behaves as exhausted.
type Limit int64

// Limited returns n as a Limit, clamping negatives to exhausted.
func Limited(n int64) Limit {
	if n < 0 {
		return Limit(0)
	}
	return Limit(n)
}

// Subtract returns l reduced by n, reporting whether n fit in l.
// Unlimited stays Unlimited. A negative n panics.
func (l Limit) Subtract(n int64) (Limit, bool) {
	if l == Unlimited {
		return Unlimited, true
	}
	if n < 0 {
		panic("io: Subtract negative amount")
	}
	if n > int64(l) {
		return l, false
	}
	return l - Limit(n), true
}

// Clamp returns n bounded by l. Unlimited returns n unchanged.
func (l Limit) Clamp(n int) int {
	if l == Unlimited {
		return n
	}
	if l <= 0 {
		return 0
	}
	if int64(n) > int64(l) {
		return int(l)
	}
	return n
}

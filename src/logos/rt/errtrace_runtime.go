// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !logosejected

package rt

import (
	"strconv"
	"unsafe"
)

//go:linkname logosTraceRecord runtime.logosTraceRecord
func logosTraceRecord(site unsafe.Pointer, errTyp unsafe.Pointer, errData uintptr)

//go:linkname logosTraceRead runtime.logosTraceRead
//go:noescape
func logosTraceRead(buf []unsafe.Pointer) int

type errorWords struct {
	typ  unsafe.Pointer
	data uintptr
}

// TraceErr records s in the calling goroutine's error trace for err. A
// different error value resets the trace; the trace keeps the last 8
// sites in propagation order. It allocates nothing.
func TraceErr(err error, s *Site) {
	w := (*errorWords)(unsafe.Pointer(&err))
	logosTraceRecord(unsafe.Pointer(s), w.typ, w.data)
}

// ErrorTrace returns the calling goroutine's recorded sites in
// propagation order, at most the last 8.
func ErrorTrace() []Site {
	var buf [8]unsafe.Pointer
	n := logosTraceRead(buf[:])
	out := make([]Site, n)
	for i := range out {
		out[i] = *(*Site)(buf[i])
	}
	return out
}

// FormatTrace renders trace as one File:Line per site in propagation
// order, joined by newline. An empty trace renders as empty string.
func FormatTrace(trace []Site) string {
	if len(trace) == 0 {
		return ""
	}
	buf := make([]byte, 0, len(trace)*32)
	for i, s := range trace {
		if i > 0 {
			buf = append(buf, '\n')
		}
		buf = append(buf, s.File...)
		buf = append(buf, ':')
		buf = strconv.AppendInt(buf, int64(s.Line), 10)
	}
	return string(buf)
}

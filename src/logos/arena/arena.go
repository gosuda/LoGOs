// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package arena provides lifetime-scoped and caller-backed allocation regions.
//
// Open returns a Region, and New, Make, and Append allocate into it. The
// compiler releases a region's memory at the end of the block that owns its
// Open call. Without GOEXPERIMENT=logosarena, Open uses heap storage.
// OpenFixed uses the caller's buffer in either build mode.
package arena

import "unsafe"

// Region is a handle to an allocation region.
//
// The zero Region is the heap: every function treats a zero handle as an
// ordinary heap allocation, so a Region that outlives its owner block keeps
// heap semantics.
// The fixed handle is nil in heap mode and set by OpenFixed.
type Region struct {
	h     unsafe.Pointer
	gen   uintptr
	fixed fixedRef
}

// Open opens a new region and returns its handle. Close happens at the end
// of the block that owns the Open call; the program never calls Close.
func Open() Region { return Region{} }

// New allocates a zeroed T and returns its address.
func New[T any](r Region) *T {
	if r.fixed != nil {
		return fixedNew[T](r)
	}
	return new(T)
}

// Make allocates a slice of n zeroed T elements. It panics like make for a
// negative n.
func Make[T any](r Region, n int) []T {
	if r.fixed != nil {
		return fixedMake[T](r, n)
	}
	return make([]T, n)
}

// Append appends v to s. For a fixed region it must not grow past the buffer.
func Append[T any](r Region, s []T, v ...T) []T {
	if r.fixed != nil {
		return fixedAppend(r, s, v)
	}
	return append(s, v...)
}

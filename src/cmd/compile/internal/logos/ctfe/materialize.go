// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ctfe

func (v Value) IsNilValue() bool { return v.isNil }

func (v Value) SliceHeader() (backing []Value, lo, length, cap_ int, ok bool) {
	if v.slice == nil || v.slice.backing == nil {
		return nil, 0, 0, 0, false
	}
	return *v.slice.backing, v.slice.lo, v.slice.length, v.slice.cap_, true
}

func (v Value) SliceNeedsBacking() bool {
	if v.slice == nil {
		return false
	}
	return v.slice.lo != 0 || v.slice.cap_ != v.slice.length
}

func (v Value) sliceAccessible() []Value {
	if v.slice == nil || v.slice.backing == nil {
		return nil
	}
	b := *v.slice.backing
	lo, hi := v.slice.lo, v.slice.cap_
	if lo < 0 || lo > hi || hi > len(b) {
		return nil
	}
	return b[lo:hi]
}

func (v Value) sliceVisible() []Value {
	if v.slice == nil || v.slice.backing == nil {
		return nil
	}
	b := *v.slice.backing
	lo, hi := v.slice.lo, v.slice.lo+v.slice.length
	if lo < 0 || hi > len(b) || lo > hi {
		return nil
	}
	return b[lo:hi]
}

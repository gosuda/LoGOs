// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package strconv prints float64 values in their shortest round-trip form
// into a caller buffer. It follows the Zig fmt/float size contract idea:
// the caller pre-sizes once and each print stays allocation free. Output
// bytes are identical to strconv AppendFloat with precision -1 and 64 bits.
package strconv

import stdstrconv "strconv"

// MaxFloat64Len is the largest number of bytes AppendFloat writes for a
// finite float64 with verb 'e', 'E', 'g' or 'G'. Verb 'f' has no fixed
// bound and may write more. The bound is tight: -MaxFloat64 with 'e'
// writes exactly MaxFloat64Len bytes.
const MaxFloat64Len = 24

// AppendFloat appends the shortest string that round-trips v and returns
// the longer buffer. The format follows strconv AppendFloat with the given
// verb, precision -1 and 64 bits. Non-finite values print as NaN, +Inf
// or -Inf. A buffer with MaxFloat64Len spare capacity serves any finite
// 'e', 'E', 'g' or 'G' print without allocation.
func AppendFloat(dst []byte, v float64, fmt byte) []byte {
	return stdstrconv.AppendFloat(dst, v, fmt, -1, 64)
}

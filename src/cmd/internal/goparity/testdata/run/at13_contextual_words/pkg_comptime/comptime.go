// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package comptime

func comptime() int {
	return 46
}

func Call() int {
	return comptime()
}

func Who() string {
	return "comptime"
}

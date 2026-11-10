// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package errdefer

func errdefer() int {
	return 44
}

func Call() int {
	return errdefer()
}

func Who() string {
	return "errdefer"
}

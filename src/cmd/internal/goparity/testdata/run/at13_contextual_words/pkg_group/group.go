// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package group

func group() int {
	return 42
}

func Call() int {
	return group()
}

func Who() string {
	return "group"
}

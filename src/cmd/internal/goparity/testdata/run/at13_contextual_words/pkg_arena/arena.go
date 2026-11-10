// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package arena

func arena() int {
	return 47
}

func Call() int {
	return arena()
}

func Who() string {
	return "arena"
}

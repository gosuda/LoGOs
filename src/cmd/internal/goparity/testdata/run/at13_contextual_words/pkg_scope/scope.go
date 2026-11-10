// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package scope

func scope() int {
	return 43
}

func Call() int {
	return scope()
}

func Who() string {
	return "scope"
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package dep

func FirstNil(m map[string]int) int {
	if m == nil {
		return -1
	}
	return m["k"]
}

func DerefNil(p *int) int {
	if p == nil {
		return -1
	}
	return *p
}

func LenNil(s []int) int {
	if s == nil {
		return -1
	}
	return len(s)
}

type Box struct {
	V int
}

func (b *Box) Get() int {
	if b == nil {
		return -1
	}
	return b.V
}

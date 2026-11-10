// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package region

func region() int {
	return 48
}

func Call() int {
	return region()
}

func Who() string {
	return "region"
}

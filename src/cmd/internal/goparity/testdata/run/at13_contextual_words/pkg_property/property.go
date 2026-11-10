// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package property

func property() int {
	return 45
}

func Call() int {
	return property()
}

func Who() string {
	return "property"
}

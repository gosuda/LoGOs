// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rt

import "unsafe"

func StringBytes(s string) []byte {
	return unsafe.Slice(unsafe.StringData(s), len(s))
}

func BytesString(b []byte) string {
	return unsafe.String(unsafe.SliceData(b), len(b))
}

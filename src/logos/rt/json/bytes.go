// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package json

import "reflect"

// ByteSliceOf returns a view of v when v holds a slice with uint8 elements.
// The slice and its element type may be named. The result shares the backing
// array of the original slice. For other values, it returns nil, false.
func ByteSliceOf(v any) ([]byte, bool) {
	if b, ok := v.([]byte); ok {
		return b, true
	}
	t := reflect.TypeOf(v)
	if t != nil && t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.Uint8 {
		return reflect.ValueOf(v).Bytes(), true
	}
	return nil, false
}

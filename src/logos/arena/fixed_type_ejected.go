// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build logosejected

package arena

import "reflect"

func fixedElementHasPointers[T any]() bool {
	return fixedTypeHasPointers(reflect.TypeFor[T]())
}

func fixedTypeHasPointers(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.Array:
		return t.Len() != 0 && fixedTypeHasPointers(t.Elem())
	case reflect.Struct:
		for i := range t.NumField() {
			if fixedTypeHasPointers(t.Field(i).Type) {
				return true
			}
		}
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer,
		reflect.Slice, reflect.String, reflect.UnsafePointer:
		return true
	}
	return false
}

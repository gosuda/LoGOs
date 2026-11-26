// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

/*
#include <stdlib.h>
*/
import "C"
import (
	"fmt"
	"reflect"
	"unsafe"
)

// padded has addressable padding between its compared fields. IsZero
// reaches the generated equality through typ.Equal without passing
// walkCompare; the in-payload checks must report the freed fields
// without touching the padding.
type padded struct {
	Tag uint8
	_   [7]byte
	ID  uint64
}

func main() {
	p := C.calloc(1, C.size_t(unsafe.Sizeof(padded{})))
	freed := (*padded)(p)
	C.free(p)
	fmt.Println(isZero(freed))
}

//go:noinline
func isZero(q *padded) bool {
	return reflect.NewAt(reflect.TypeOf(padded{}), unsafe.Pointer(q)).Elem().IsZero() // BOOM
}

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

type quad [4]string

func main() {
	p := C.calloc(1, C.size_t(unsafe.Sizeof(quad{})))
	freed := (*quad)(p)
	C.free(p)
	fmt.Println(isZero(freed))
}

//go:noinline
func isZero(q *quad) bool {
	// IsZero reaches the generated equality for quad through
	// typ.Equal without passing walkCompare, so the call-site
	// operand checks do not apply. The equality itself must
	// report the freed string headers. // BOOM
	return reflect.NewAt(reflect.TypeOf(quad{}), unsafe.Pointer(q)).Elem().IsZero()
}

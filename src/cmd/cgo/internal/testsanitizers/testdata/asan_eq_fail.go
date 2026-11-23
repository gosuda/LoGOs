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
	"unsafe"
)

type quad [4]string

func main() {
	p := C.calloc(1, C.size_t(unsafe.Sizeof(quad{})))
	freed := (*quad)(p)
	C.free(p)
	var zero quad
	fmt.Println(equal(freed, &zero))
}

//go:noinline
func equal(a, b *quad) bool {
	return *a == *b // BOOM
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

/*
#include <stdint.h>
#include <stdlib.h>

struct rec { int8_t a; int64_t b; int8_t c; int64_t d; int8_t e; int64_t f; };

// The field f stays uninitialized.
struct rec *newRec(void) {
	struct rec *r = malloc(sizeof *r);
	r->a = 1;
	r->b = 2;
	r->c = 3;
	r->d = 4;
	r->e = 5;
	return r;
}
*/
import "C"
import (
	"fmt"
	"unsafe"
)

type rec struct {
	a int8
	b int64
	c int8
	d int64
	e int8
	f int64
}

func main() {
	got := (*rec)(unsafe.Pointer(C.newRec()))
	fmt.Println(equal(got, &rec{1, 2, 3, 4, 5, 6}))
}

//go:noinline
func equal(a, b *rec) bool {
	return *a == *b
}

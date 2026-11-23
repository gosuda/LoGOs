// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	uint8_t tag;
	uint64_t id;
} msan_eq_fail_rec;

typedef struct {
	msan_eq_fail_rec r[2];
} msan_eq_fail_pair;

msan_eq_fail_pair *msanEqFailAlloc(void) {
	return (msan_eq_fail_pair *)malloc(sizeof(msan_eq_fail_pair));
}

// Writes only the tags, leaving both ids uninitialized. Comparing two
// pairs initialized this way must report an uninitialized read exactly
// because the equality instrumentation checks the compared fields: with
// no checks emitted the comparison would silently report equal.
void msanEqFailInitTags(msan_eq_fail_pair *p, uint8_t t0, uint8_t t1) {
	p->r[0].tag = t0;
	p->r[1].tag = t1;
}

void msanEqFailFree(msan_eq_fail_pair *p) {
	free(p);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// msanEqFailRecord matches msan_eq_fail_rec above: one tag byte, seven
// padding bytes, then the id.
type msanEqFailRecord struct {
	Tag uint8
	ID  uint64
}

func main() {
	pa := C.msanEqFailAlloc()
	pb := C.msanEqFailAlloc()
	C.msanEqFailInitTags(pa, 1, 2)
	C.msanEqFailInitTags(pb, 1, 2)
	a := (*[2]msanEqFailRecord)(unsafe.Pointer(pa))
	b := (*[2]msanEqFailRecord)(unsafe.Pointer(pb))
	if *a != *b {
		panic("pairs differ")
	}
	fmt.Println("equal")
	C.msanEqFailFree(pa)
	C.msanEqFailFree(pb)
}

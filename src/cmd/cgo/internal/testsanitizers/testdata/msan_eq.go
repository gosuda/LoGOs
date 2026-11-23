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
} msan_eq_rec;

typedef struct {
	msan_eq_rec r[2];
} msan_eq_pair;

msan_eq_pair *msanEqAlloc(void) {
	return (msan_eq_pair *)malloc(sizeof(msan_eq_pair));
}

// Writes only the fields, leaving the seven padding bytes between
// tag and id uninitialized, the way a C caller that never touches
// padding does.
void msanEqInit(msan_eq_pair *p, uint8_t t0, uint64_t i0, uint8_t t1, uint64_t i1) {
	p->r[0].tag = t0;
	p->r[0].id = i0;
	p->r[1].tag = t1;
	p->r[1].id = i1;
}

void msanEqFree(msan_eq_pair *p) {
	free(p);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// msanEqRecord matches msan_eq_rec above: one tag byte, seven padding
// bytes, then the id.
type msanEqRecord struct {
	Tag uint8
	ID  uint64
}

func main() {
	pa := C.msanEqAlloc()
	pb := C.msanEqAlloc()
	C.msanEqInit(pa, 1, 100, 2, 200)
	C.msanEqInit(pb, 1, 100, 2, 200)
	a := (*[2]msanEqRecord)(unsafe.Pointer(pa))
	b := (*[2]msanEqRecord)(unsafe.Pointer(pb))
	if *a != *b {
		panic("pairs differ")
	}
	fmt.Println("equal")
	C.msanEqFree(pa)
	C.msanEqFree(pb)
}

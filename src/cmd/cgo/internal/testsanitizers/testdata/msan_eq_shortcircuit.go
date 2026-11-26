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
} msan_eq_sc_rec;

typedef struct {
	msan_eq_sc_rec r[2];
} msan_eq_sc_pair;

msan_eq_sc_pair *msanEqScAlloc(void) {
	return (msan_eq_sc_pair *)malloc(sizeof(msan_eq_sc_pair));
}

// Writes both tags and only the first id, leaving the second id
// uninitialized. The pairs below differ in the first tag, so the
// equality helper returns before reading the second elements; the
// instrumentation must not read them either.
void msanEqScInit(msan_eq_sc_pair *p, uint8_t t0, uint64_t i0, uint8_t t1) {
	p->r[0].tag = t0;
	p->r[0].id = i0;
	p->r[1].tag = t1;
}

void msanEqScFree(msan_eq_sc_pair *p) {
	free(p);
}
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// msanEqScRecord matches msan_eq_sc_rec above: one tag byte, seven
// padding bytes, then the id.
type msanEqScRecord struct {
	Tag uint8
	ID  uint64
}

func main() {
	pa := C.msanEqScAlloc()
	pb := C.msanEqScAlloc()
	C.msanEqScInit(pa, 1, 100, 9)
	C.msanEqScInit(pb, 2, 100, 9)
	a := (*[2]msanEqScRecord)(unsafe.Pointer(pa))
	b := (*[2]msanEqScRecord)(unsafe.Pointer(pb))
	if *a == *b {
		panic("pairs compare equal")
	}
	fmt.Println("mismatch")
	C.msanEqScFree(pa)
	C.msanEqScFree(pb)
}

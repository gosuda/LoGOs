// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	uint8_t tag;
	uint8_t pad[7];
	uint64_t id;
} asan_eq_pad_rec;

asan_eq_pad_rec *asanEqPadAlloc(void) {
	return (asan_eq_pad_rec *)malloc(sizeof(asan_eq_pad_rec));
}

// Writes only the compared fields, leaving the padding uninitialized.
// The equality reads exactly the fields, so no report is expected.
void asanEqPadInit(asan_eq_pad_rec *p, uint8_t tag, uint64_t id) {
	p->tag = tag;
	p->id = id;
}

// Writes only the padding bytes, leaving tag and id alone. Two records
// with equal fields and different padding distinguish a field-wise
// comparison, which reports equal, from a whole-record read, which sees
// the padding differ.
void asanEqPadFill(asan_eq_pad_rec *p, uint8_t byte) {
	for (int i = 0; i < 7; i++) {
		p->pad[i] = byte;
	}
}

void asanEqPadFree(asan_eq_pad_rec *p) {
	free(p);
}
*/
import "C"

import (
	"fmt"
	"reflect"
	"unsafe"
)

// padded matches asan_eq_pad_rec above. Comparing a live value whose
// padding was never written must stay silent: the checks cover the
// fields, never the padding. The zero tag below forces the comparison
// past the tag and into the id; with a nonzero tag the comparison would
// exit before reaching the id.
type padded struct {
	Tag uint8
	_   [7]byte
	ID  uint64
}

func main() {
	p := C.asanEqPadAlloc()
	C.asanEqPadInit(p, 5, 7)
	v := (*padded)(unsafe.Pointer(p))
	fmt.Println(isZero(v))
	C.asanEqPadFree(p)

	q1 := C.asanEqPadAlloc()
	q2 := C.asanEqPadAlloc()
	C.asanEqPadInit(q1, 0, 7)
	C.asanEqPadInit(q2, 0, 7)
	C.asanEqPadFill(q1, 0xAA)
	C.asanEqPadFill(q2, 0xBB)
	w1 := (*padded)(unsafe.Pointer(q1))
	w2 := (*padded)(unsafe.Pointer(q2))
	if *w1 != *w2 {
		C.asanEqPadFree(q1)
		C.asanEqPadFree(q2)
		panic("padded values with equal fields compare unequal: the comparison read padding")
	}
	fmt.Println("equal-fields ok")
	C.asanEqPadFree(q1)
	C.asanEqPadFree(q2)
}

//go:noinline
func isZero(q *padded) bool {
	return reflect.NewAt(reflect.TypeOf(padded{}), unsafe.Pointer(q)).Elem().IsZero()
}

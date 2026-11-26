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
// fields, never the padding.
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
}

//go:noinline
func isZero(q *padded) bool {
	return reflect.NewAt(reflect.TypeOf(padded{}), unsafe.Pointer(q)).Elem().IsZero()
}

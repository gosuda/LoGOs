// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package testlogos

/*
#include <stdint.h>
#include <time.h>

// The file carries //export, so the preamble must hold declarations
// only (cmd/cgo/doc.go): the bodies live in fastcall_c.c.

int fast_add(int a, int b);
int slow_noescape(int a, int b);
int slow_nocallback(int a, int b);
int block_sleep(int micros);
int plain_sleep(int micros);
int read_int(int *p);
int sum_bytes(unsigned char *p, int n);
unsigned long long busy_spin(unsigned long long iters);
unsigned long long plain_spin(unsigned long long iters);
int watch_spin(unsigned char *p, unsigned char want, int micros);
extern int GoCallback(int v);
int cb_fire(void);
int plain_cb_fire(void);

#cgo noescape fast_add
#cgo nocallback fast_add

#cgo noescape slow_noescape

#cgo nocallback slow_nocallback

#cgo noescape block_sleep
#cgo nocallback block_sleep

#cgo noescape read_int
#cgo nocallback read_int

#cgo noescape sum_bytes
#cgo nocallback sum_bytes

#cgo noescape busy_spin
#cgo nocallback busy_spin

#cgo noescape watch_spin
#cgo nocallback watch_spin

#cgo noescape cb_fire
#cgo nocallback cb_fire

#cgo nocallback plain_cb_fire
*/
import "C"

import "unsafe"

//export GoCallback
func GoCallback(v C.int) C.int { return v + 1 }

func callFastAdd(a, b int32) int32 { return int32(C.fast_add(C.int(a), C.int(b))) }

func callSlowNoescape(a, b int32) int32 { return int32(C.slow_noescape(C.int(a), C.int(b))) }

func callSlowNocallback(a, b int32) int32 {
	return int32(C.slow_nocallback(C.int(a), C.int(b)))
}

func callFastSleep(micros int32) int32 { return int32(C.block_sleep(C.int(micros))) }

func callPlainSleep(micros int32) int32 { return int32(C.plain_sleep(C.int(micros))) }

func callFastRead(p *int32) int32 { return int32(C.read_int((*C.int)(unsafe.Pointer(p)))) }

func callFastSlice(b []byte) int32 {
	if len(b) == 0 {
		return 0
	}
	return int32(C.sum_bytes((*C.uchar)(unsafe.Pointer(&b[0])), C.int(len(b))))
}

func callFastBusy(iters uint64) uint64 { return uint64(C.busy_spin(C.ulonglong(iters))) }

func callFastWatch(p *byte, want byte, micros int32) int32 {
	return int32(C.watch_spin((*C.uchar)(unsafe.Pointer(p)), C.uchar(want), C.int(micros)))
}

func callPlainBusy(iters uint64) uint64 { return uint64(C.plain_spin(C.ulonglong(iters))) }

func callFastCallback() int32 { return int32(C.cb_fire()) }

func callPlainCallback() int32 { return int32(C.plain_cb_fire()) }

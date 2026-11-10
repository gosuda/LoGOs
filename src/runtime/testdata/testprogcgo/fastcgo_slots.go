// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build cgo

package main

/*
#cgo noescape slots_spin
#cgo nocallback slots_spin

int slots_spin(unsigned char *p, unsigned char want, unsigned long long iters) {
	volatile unsigned char *vp = (volatile unsigned char *)p;
	for (unsigned long long i = 0; i < iters; i++) {
		if (*vp != want) {
			return 1;
		}
	}
	return 0;
}
*/
import "C"

import (
	"fmt"
	"runtime"
	"sync"
	"time"
	"unsafe"
)

func init() {
	register("LogosFastCgoSlotsNotRoots", LogosFastCgoSlotsNotRoots)
}

func slotsGCs() uint32 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.NumGC
}

func LogosFastCgoSlotsNotRoots() {
	done := make(chan struct{}, 1)
	res := make(chan int32, 1)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			runtime.GC()
		}
	})
	before := slotsGCs()
	go func() {
		b := new([256 << 10]byte)
		b[0] = 42
		runtime.AddCleanup(b, func(ch chan struct{}) { ch <- struct{}{} }, done)
		res <- int32(C.slots_spin((*C.uchar)(unsafe.Pointer(&b[0])), C.uchar(42), C.ulonglong(200000000)))
	}()
	var code int32
	select {
	case code = <-res:
	case <-time.After(60 * time.Second):
		fmt.Println("FAIL: fast call did not return within 60s")
		close(stop)
		wg.Wait()
		return
	}
	during := slotsGCs() - before
	close(stop)
	wg.Wait()
	if code != 0 {
		fmt.Println("FAIL: fast-call argument changed during collection")
		return
	}
	if during < 2 {
		fmt.Println("FAIL: no collection ran during the fast call")
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case <-done:
			fmt.Println("OK")
			return
		default:
		}
		if time.Now().After(deadline) {
			fmt.Println("FAIL: object referenced only by fast-call state survived collection")
			return
		}
		runtime.GC()
		time.Sleep(10 * time.Millisecond)
	}
}

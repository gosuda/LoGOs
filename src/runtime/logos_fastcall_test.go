// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime_test

import (
	"runtime"
	"testing"
	"unsafe"
)

func TestCgoFastcallPinSlots(t *testing.T) {
	var probe int
	arg := unsafe.Pointer(&probe)
	slots, stamp, flag := runtime.LogosEnterFastCallForTest(arg, 12345)
	if !flag {
		t.Fatal("inFastCall not set by enter")
	}
	if want := [4]uintptr{uintptr(arg), 0, 0, 0}; slots != want {
		t.Fatalf("slots = %v, want %v", slots, want)
	}
	if stamp != 12345 {
		t.Fatalf("fastCallEnterNano = %d, want 12345", stamp)
	}

	slots, stamp, flag = runtime.LogosExitFastCallForTest()
	if flag {
		t.Fatal("inFastCall still set after exit")
	}
	if slots != [4]uintptr{} {
		t.Fatalf("slots = %v after exit, want zeros", slots)
	}
	if stamp != 0 {
		t.Fatalf("fastCallEnterNano = %d after exit, want 0", stamp)
	}
}
func TestLogosFastCallRecordZeroesIdleSlots(t *testing.T) {
	runtime.LogosDirtyFastCallSlotsForTest([4]uintptr{11, 22, 33, 44}, 77, false)
	var probe int
	arg := unsafe.Pointer(&probe)
	slots, _, _ := runtime.LogosEnterFastCallForTest(arg, 0)
	defer runtime.LogosExitFastCallForTest()
	if want := [4]uintptr{uintptr(arg), 0, 0, 0}; slots != want {
		t.Fatalf("slots = %v, want %v", slots, want)
	}
}

func TestLogosFastCallClearsAllSlots(t *testing.T) {
	runtime.LogosDirtyFastCallSlotsForTest([4]uintptr{11, 22, 33, 44}, 77, true)
	slots, stamp, flag := runtime.LogosExitFastCallForTest()
	if flag {
		t.Fatal("inFastCall still set after exit")
	}
	if slots != [4]uintptr{} {
		t.Fatalf("slots = %v after exit, want zeros", slots)
	}
	if stamp != 0 {
		t.Fatalf("fastCallEnterNano = %d after exit, want 0", stamp)
	}
}

func TestLogosFastCallPreservesCallerArgs(t *testing.T) {
	v := 12345
	arg := unsafe.Pointer(&v)
	runtime.LogosEnterFastCallForTest(arg, 0)
	slots, _, _ := runtime.LogosFastCallStateForTest()
	if slots[0] != uintptr(arg) {
		runtime.LogosExitFastCallForTest()
		t.Fatalf("slot0 = %x, want %x", slots[0], uintptr(arg))
	}
	if v != 12345 {
		runtime.LogosExitFastCallForTest()
		t.Fatalf("caller value = %d, want 12345", v)
	}
	slots, stamp, flag := runtime.LogosExitFastCallForTest()
	if flag || stamp != 0 || slots != [4]uintptr{} {
		t.Fatalf("after exit = %v %d %v, want zeros", slots, stamp, flag)
	}
	if v != 12345 {
		t.Fatalf("caller value after exit = %d, want 12345", v)
	}
}

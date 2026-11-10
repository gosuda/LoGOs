// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime

import (
	"testing"
	"unsafe"
)

func TestLogosFastCallStaysOnOriginalM(t *testing.T) {
	mp := getg().m
	savedSlots := mp.fastCgoFrame
	savedFlag := mp.inFastCall
	defer func() {
		mp.fastCgoFrame = savedSlots
		mp.inFastCall = savedFlag
	}()
	var v int
	arg := unsafe.Pointer(&v)
	logosRecordFastCall(mp, arg)
	if !mp.inFastCall {
		t.Fatal("record did not set flag on calling m")
	}
	if mp.fastCgoFrame[0] != uintptr(arg) {
		t.Fatal("record did not store arg on calling m")
	}
	logosClearFastCall(mp)
	if mp.inFastCall || mp.fastCgoFrame != [4]uintptr{} {
		t.Fatal("clear did not zero the calling m")
	}
}

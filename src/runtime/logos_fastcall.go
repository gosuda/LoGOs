// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime

import (
	"internal/goexperiment"
	"internal/runtime/sys"
	"unsafe"
)

//go:linkname logosCgocallFast
//go:nosplit
func logosCgocallFast(fn, arg unsafe.Pointer) int32 {
	if !goexperiment.LogosFastCgo {
		return cgocall(fn, arg)
	}
	if !iscgo && GOOS != "solaris" && GOOS != "illumos" && GOOS != "windows" {
		throw("cgocall unavailable")
	}
	if fn == nil {
		throw("cgocall nil")
	}

	mp := getg().m
	if raceenabled {
		racereleasemerge(unsafe.Pointer(&racecgosync))
	}

	mp.ncgocall++
	if mp.cgoCallers != nil {
		mp.cgoCallers[0] = 0
	}
	logosRecordFastCall(mp, arg, 0)

	fp := getcallerfp()
	reentersyscall(sys.GetCallerPC(), sys.GetCallerSP(), fp)

	osPreemptExtEnter(mp)
	mp.incgo = true
	mp.ncgo++

	errno := asmcgocall(fn, arg)

	mp.incgo = false
	mp.ncgo--
	logosClearFastCall(mp)

	osPreemptExtExit(mp)

	winsyscall := mp.winsyscall

	exitsyscall()

	getg().m.winsyscall = winsyscall

	if raceenabled {
		raceacquire(unsafe.Pointer(&racecgosync))
	}

	if sys.DITSupported {
		ditEnabled := sys.DITEnabled()
		gp := getg()
		if !gp.ditWanted && ditEnabled {
			sys.DisableDIT()
		} else if gp.ditWanted && !ditEnabled {
			sys.EnableDIT()
		}
	}

	KeepAlive(fn)
	KeepAlive(arg)
	KeepAlive(mp)

	return errno
}

//go:nosplit
func logosRecordFastCall(mp *m, arg unsafe.Pointer, now int64) {
	mp.fastCgoFrame[0] = uintptr(arg)
	mp.fastCallEnterNano = now
	mp.inFastCall = true
}

//go:nosplit
func logosClearFastCall(mp *m) {
	mp.inFastCall = false
	mp.fastCallEnterNano = 0
	mp.fastCgoFrame[0] = 0
}

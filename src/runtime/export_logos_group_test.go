// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix || (js && wasm) || wasip1 || windows

package runtime

import "internal/runtime/atomic"

func LogosSlotsAcquire(n int) []uint32 {
	slots := make([]uint32, n)
	for i := range slots {
		slots[i] = logosGroupAcquire()
	}
	return slots
}

func LogosSlotsRelease(slots []uint32) {
	for _, slot := range slots {
		logosGroupRelease(slot)
	}
}

func LogosStaleReleaseKeepsCancel() bool {
	pd := pollcache.alloc()
	defer pollcache.free(pd)
	slot := logosGroupAcquire()
	defer logosGroupRelease(slot)
	gp := getg()
	gp.logosGroup = slot
	registered := logosWaitRegister(pd, 'r')
	gp.logosGroup = 0
	if !registered {
		return false
	}
	logosPollPurge(pd)
	atomic.Or32(&pd.logosCancel, logosCancelRead)
	logosWaitRelease(pd, 'r')
	kept := logosCancelled(pd, 'r')
	logosPollPurge(pd)
	return kept
}

func LogosPurgeClearsBit() bool {
	pd := pollcache.alloc()
	defer pollcache.free(pd)
	atomic.Or32(&pd.logosCancel, logosCancelRead|logosCancelWrite)
	logosPollPurge(pd)
	return atomic.Load(&pd.logosCancel) == 0
}

func LogosRegistryIdle() bool {
	return LogosWaiterCount() == 0
}

func LogosWaiterCount() int {
	return int(atomic.Load(&logosWaiterCount))
}

func LogosCancelFiltersByGroup() (otherBit, ownBit bool) {
	pd := pollcache.alloc()
	defer pollcache.free(pd)
	own, other := logosGroupAcquire(), logosGroupAcquire()
	defer logosGroupRelease(own)
	defer logosGroupRelease(other)
	gp := getg()
	gp.logosGroup = own
	logosWaitRegister(pd, 'r')
	gp.logosGroup = 0
	logosCancelGroup(other)
	otherBit = logosCancelled(pd, 'r')
	logosCancelGroup(own)
	ownBit = logosCancelled(pd, 'r')
	logosPollPurge(pd)
	return otherBit, ownBit
}

func LogosReusedSlotStartsClean() bool {
	pd := pollcache.alloc()
	defer pollcache.free(pd)
	slot := logosGroupAcquire()
	logosCancelGroup(slot)
	logosGroupRelease(slot)
	retaken := logosGroupAcquire()
	defer logosGroupRelease(retaken)
	if retaken != slot {
		return false
	}
	gp := getg()
	gp.logosGroup = retaken
	registered := logosWaitRegister(pd, 'r')
	gp.logosGroup = 0
	if !registered {
		return false
	}
	armed := logosCancelled(pd, 'r')
	logosWaitRelease(pd, 'r')
	logosPollPurge(pd)
	return !armed
}

func LogosReusedSlotCancel() (live, reused uint32, liveCancelled bool) {
	pd := pollcache.alloc()
	defer pollcache.free(pd)
	live, other := logosGroupAcquire(), logosGroupAcquire()
	gp := getg()
	gp.logosGroup = live
	logosWaitRegister(pd, 'r')
	gp.logosGroup = 0
	logosGroupRelease(other)
	reused = logosGroupAcquire()
	logosCancelGroup(reused)
	liveCancelled = logosCancelled(pd, 'r')
	logosPollPurge(pd)
	logosGroupRelease(reused)
	logosGroupRelease(live)
	return live, reused, liveCancelled
}

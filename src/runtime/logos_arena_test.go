// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build goexperiment.logosarena

package runtime_test

import (
	"fmt"
	"internal/goexperiment"
	"math"
	"math/bits"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"
	"weak"
)

const arenaScanUnit = runtime.LogosArenaUnit

var arenaSink any

func arenaStride(size uintptr) uintptr {
	return (size+arenaScanUnit-1)&^(arenaScanUnit-1) + runtime.LogosArenaScanHeaderBytes
}

func inFreshGoroutine(t *testing.T, fn func()) {
	t.Helper()
	goroutines := runtime.NumGoroutine()
	done := make(chan any)
	go func() {
		defer func() { done <- recover() }()
		fn()
	}()
	r := <-done
	waitGoroutines(t, goroutines)
	if r != nil {
		t.Fatalf("panic in goroutine: %v", r)
	}
}

func recoverMsg(fn func()) (msg string, panicked bool) {
	defer func() {
		if r := recover(); r != nil {
			msg, panicked = fmt.Sprint(r), true
		}
	}()
	fn()
	return
}

func waitLeased(t *testing.T, want int) int {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, leased, _ := runtime.LogosArenaCounts()
		if leased == want || time.Now().After(deadline) {
			return leased
		}
		time.Sleep(time.Millisecond)
	}
}

func waitGoroutines(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for runtime.NumGoroutine() > want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > want {
		t.Fatalf("goroutines = %d, want %d", got, want)
	}
}

func fillBytes(p unsafe.Pointer, size uintptr, v byte) {
	b := unsafe.Slice((*byte)(p), size)
	for i := range b {
		b[i] = v
	}
}

func checkBytes(p unsafe.Pointer, size uintptr, v byte) bool {
	for _, x := range unsafe.Slice((*byte)(p), size) {
		if x != v {
			return false
		}
	}
	return true
}

func fillWords(p unsafe.Pointer, size uintptr, v *int) {
	words := unsafe.Slice((**int)(p), size/unsafe.Sizeof(v))
	for i := range words {
		words[i] = v
	}
}

func TestLogosArenaParkReturnsChunk(t *testing.T) {
	const (
		waves   = 25
		perWave = 4000
		all     = waves * perWave
	)
	goroutines := runtime.NumGoroutine()
	totalBase, leasedBase, _ := runtime.LogosArenaCounts()
	retries := 0
	for w := 0; w < waves; {
		release := make(chan struct{})
		var started sync.WaitGroup
		started.Add(perWave)
		for range perWave {
			go func() {
				m := runtime.LogosArenaSave()
				runtime.LogosArenaAlloc(byte(0), 64)
				runtime.LogosArenaAllocScan((*int)(nil), 48)
				runtime.LogosArenaRestore(m)
				started.Done()
				<-release
			}()
		}
		started.Wait()
		if leased := waitLeased(t, leasedBase); leased != leasedBase {
			close(release)
			waitGoroutines(t, goroutines)
			if leased > leasedBase || retries >= 3 {
				t.Fatalf("wave %d: leased chunks = %d with %d goroutines parked, want baseline %d", w, leased, perWave, leasedBase)
			}
			retries++
			t.Logf("wave %d: leased baseline %d vanished, resampling", w, leasedBase)
			_, leasedBase, _ = runtime.LogosArenaCounts()
			continue
		}
		close(release)
		waitGoroutines(t, goroutines)
		w++
	}
	total, _, _ := runtime.LogosArenaCounts()
	if grown, limit := total-totalBase, 4*runtime.GOMAXPROCS(0)+16; grown > limit {
		t.Errorf("%d chunks created for %d goroutines, want at most %d: parked goroutines do not recycle chunks", grown, all, limit)
	}
}

func TestLogosArenaParkKeepsLiveChunk(t *testing.T) {
	goroutines := runtime.NumGoroutine()
	_, leasedBase, _ := runtime.LogosArenaCounts()
	parked := make(chan struct{})
	wake := make(chan struct{})
	wake2 := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		m := runtime.LogosArenaSave()
		runtime.LogosArenaAllocScan((*int)(nil), 32)
		parked <- struct{}{}
		<-wake
		runtime.LogosArenaRestore(m)
		<-wake2
	}()
	<-parked
	deadline := time.Now().Add(50 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, leased, _ := runtime.LogosArenaCounts(); leased != leasedBase+1 {
			t.Fatalf("leased = %d while a goroutine is parked with live arena memory, want %d", leased, leasedBase+1)
		}
		time.Sleep(time.Millisecond)
	}
	wake <- struct{}{}
	if leased := waitLeased(t, leasedBase); leased != leasedBase {
		t.Errorf("leased = %d after the goroutine parked with empty regions, want %d", leased, leasedBase)
	}
	wake2 <- struct{}{}
	<-done
	waitGoroutines(t, goroutines)
}

func TestLogosArenaMarkSurvivesPark(t *testing.T) {
	goroutines := runtime.NumGoroutine()
	_, leasedBase, _ := runtime.LogosArenaCounts()
	park := make(chan struct{})
	wake := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		m0 := runtime.LogosArenaSave()
		runtime.LogosArenaAlloc(byte(0), 16)
		runtime.LogosArenaRestore(m0)
		if st := runtime.LogosArenaStat(); st.Chunks != 1 || st.NoscanUsed != 0 {
			t.Errorf("after restore: %+v, want one empty chunk", st)
		}
		m := runtime.LogosArenaSave()
		park <- struct{}{}
		<-wake
		if st := runtime.LogosArenaStat(); st.Chunks != 0 {
			t.Errorf("chunks = %d after parking with empty regions, want 0", st.Chunks)
		}
		runtime.LogosArenaAlloc(byte(0), 16)
		runtime.LogosArenaRestore(m)
		if st := runtime.LogosArenaStat(); st.Chunks != 1 || st.NoscanUsed != 0 {
			t.Errorf("after restoring a mark from before the park: %+v, want one empty chunk", st)
		}
	}()
	<-park
	if leased := waitLeased(t, leasedBase); leased != leasedBase {
		t.Errorf("leased = %d with the goroutine parked at an empty mark, want %d", leased, leasedBase)
	}
	wake <- struct{}{}
	<-done
	waitGoroutines(t, goroutines)
}

const arenaParkBig = runtime.LogosArenaCapacity - runtime.LogosArenaUnit

func arenaEmptyMarks(addr uintptr) (noscan, scan uintptr) {
	return addr + runtime.LogosArenaChunkBytes - runtime.LogosArenaCapacity,
		addr + runtime.LogosArenaChunkBytes
}

func checkWords(p unsafe.Pointer, size uintptr, v *int) bool {
	words := unsafe.Slice((**int)(p), size/unsafe.Sizeof(v))
	for _, w := range words {
		if w != v {
			return false
		}
	}
	return true
}

func arenaIndexes(addrs, built []uintptr) []int {
	idx := make([]int, len(addrs))
	for i, a := range addrs {
		idx[i] = -1
		for j, b := range built {
			if a == b {
				idx[i] = j
			}
		}
	}
	return idx
}

func sameAddrs(a, b []uintptr) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func arenaShapeOK(chain []runtime.LogosArenaChunk, addrs []uintptr, live []bool) bool {
	if len(chain) != len(addrs) {
		return false
	}
	for k, c := range chain {
		j := len(addrs) - 1 - k
		if c.Addr != addrs[j] || (c.NoscanUsed+c.ScanUsed > 0) != live[j] {
			return false
		}
	}
	return true
}

const arenaParkMax = 4

type arenaParkBuilt struct {
	goid  uint64
	n     int
	addrs [arenaParkMax]uintptr
	objs  [arenaParkMax]unsafe.Pointer
}

func arenaParkCase(t *testing.T, live []bool, empty func(addr uintptr) bool) {
	t.Helper()
	if len(live) == 0 || len(live) > arenaParkMax {
		t.Fatalf("arenaParkCase: %d chunks, want 1 to %d", len(live), arenaParkMax)
	}
	_, leasedBase, _ := runtime.LogosArenaCounts()
	x := new(int)
	built := make(chan arenaParkBuilt)
	wake := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		m := runtime.LogosArenaSave()
		b := arenaParkBuilt{goid: runtime.LogosArenaGoid(), n: len(live)}
		for i := range live {
			var p unsafe.Pointer
			if i%2 == 0 {
				p = runtime.LogosArenaAlloc(byte(0), arenaParkBig)
				fillBytes(p, arenaParkBig, byte(0x40+i))
			} else {
				p = runtime.LogosArenaAllocScan((*int)(nil), arenaParkBig)
				fillWords(p, arenaParkBig, x)
			}
			head, _, _ := runtime.LogosArenaMarkSave()
			b.addrs[i] = head
			b.objs[i] = p
		}
		var buf [arenaParkMax]runtime.LogosArenaChunk
		wantLive := make([]bool, len(live))
		for i := range wantLive {
			wantLive[i] = true
		}
		n, _ := runtime.LogosArenaChainOf(b.goid, buf[:])
		if n != len(live) || !arenaShapeOK(buf[:n], b.addrs[:b.n], wantLive) {
			t.Errorf("built chain has %d chunks, want %d with live %v", n, len(live), live)
		}
		for i, keep := range live {
			if !keep {
				b.objs[i] = nil
				if !empty(b.addrs[i]) {
					t.Errorf("chunk %d: emptying it in place failed", i)
				}
			}
		}
		built <- b
		<-wake
		runtime.LogosArenaRestore(m)
	}()

	b := <-built
	addrs := b.addrs[:b.n]
	var want []uintptr
	for j := len(live) - 1; j >= 0; j-- {
		if live[j] {
			want = append(want, addrs[j])
		}
	}
	var buf [arenaParkMax]runtime.LogosArenaChunk
	deadline := time.Now().Add(20 * time.Second)
	n, waiting := runtime.LogosArenaChainOf(b.goid, buf[:])
	for !waiting {
		if time.Now().After(deadline) {
			close(wake)
			<-done
			t.Fatalf("goroutine %d did not park", b.goid)
		}
		runtime.Gosched()
		n, waiting = runtime.LogosArenaChainOf(b.goid, buf[:])
	}
	if n > len(buf) {
		close(wake)
		<-done
		t.Fatalf("goroutine %d holds %d chunks, more than the %d the test can record", b.goid, n, len(buf))
	}
	var got []uintptr
	for _, c := range buf[:n] {
		got = append(got, c.Addr)
	}
	if !sameAddrs(got, want) {
		t.Errorf("after parking, the goroutine holds chunks %v, want %v (build order, oldest first): every empty chunk must return to the P cache", arenaIndexes(got, addrs), arenaIndexes(want, addrs))
	}
	total, leased, pooled := runtime.LogosArenaCounts()
	if leased != leasedBase+len(want) {
		t.Errorf("leased = %d after the goroutine parked, want %d", leased, leasedBase+len(want))
	}
	if total != leased+pooled {
		t.Errorf("total = %d chunks, want leased %d + pooled %d", total, leased, pooled)
	}
	for j, keep := range live {
		if !keep {
			continue
		}
		if j%2 == 0 && !checkBytes(b.objs[j], arenaParkBig, byte(0x40+j)) {
			t.Errorf("chunk %d: noscan object lost its contents across the park", j)
		}
		if j%2 == 1 && !checkWords(b.objs[j], arenaParkBig, x) {
			t.Errorf("chunk %d: scan object lost its contents across the park", j)
		}
	}
	close(wake)
	<-done
	arenaWaitLeased(t, leasedBase)
}

func arenaWaitLeased(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		_, leased, _ := runtime.LogosArenaCounts()
		if leased == want || time.Now().After(deadline) {
			if leased != want {
				t.Errorf("leased = %d after cleanup, want %d", leased, want)
			}
			return
		}
		runtime.Gosched()
	}
}

func TestLogosArenaParkReleasesEmptyHeadOverLiveTail(t *testing.T) {
	arenaParkCase(t, []bool{true, false}, func(addr uintptr) bool {
		noscan, scan := arenaEmptyMarks(addr)
		runtime.LogosArenaMarkRestore(addr, noscan, scan)
		return true
	})
}

func TestLogosArenaParkReleasesEmptyChunksBehindLiveHead(t *testing.T) {
	for _, tc := range []struct {
		name string
		live []bool
	}{
		{"all live", []bool{true, true}},
		{"one empty", []bool{false}},
		{"all empty", []bool{false, false}},
		{"live head over empty tail", []bool{false, true}},
		{"empty head over live tail", []bool{true, false}},
		{"empty middle", []bool{true, false, true}},
		{"alternating", []bool{false, true, false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			arenaParkCase(t, tc.live, runtime.LogosArenaEmptyChunk)
		})
	}
}

func TestLogosArenaExitReturnsChunk(t *testing.T) {
	_, leasedBase, _ := runtime.LogosArenaCounts()
	const size = 4096
	var obj unsafe.Pointer
	filled := make(chan struct{})
	x := new(int)
	defer debug.SetGCPercent(debug.SetGCPercent(-1))
	runtime.GC()
	go func() {
		obj = runtime.LogosArenaAllocScan((*int)(nil), size)
		fillWords(obj, size, x)
		runtime.LogosArenaAlloc(byte(0), 64)
		close(filled)
	}()
	<-filled
	if leased := waitLeased(t, leasedBase); leased != leasedBase {
		t.Fatalf("leased = %d after the goroutine exited, want %d", leased, leasedBase)
	}
	if !checkBytes(obj, size, 0) {
		t.Error("scan memory of an exited goroutine is not zero")
	}
}

func TestLogosArenaChunksAreManualSpans(t *testing.T) {
	inFreshGoroutine(t, func() {
		m := runtime.LogosArenaSave()
		defer runtime.LogosArenaRestore(m)
		noscan := runtime.LogosArenaAlloc(byte(0), 8)
		scan := runtime.LogosArenaAllocScan((*int)(nil), 8)
		for name, p := range map[string]unsafe.Pointer{"noscan": noscan, "scan": scan} {
			manual, flagged := runtime.LogosArenaSpanKind(p)
			if !manual || !flagged {
				t.Errorf("%s object span: manual=%v flagged=%v, want both true", name, manual, flagged)
			}
			if !runtime.LogosArenaInChunk(p) {
				t.Errorf("%s object is not recognized as an arena address", name)
			}
		}
		heap := new(int)
		arenaSink = heap
		if manual, flagged := runtime.LogosArenaSpanKind(unsafe.Pointer(heap)); manual || flagged {
			t.Errorf("heap object span: manual=%v flagged=%v, want both false", manual, flagged)
		}
		if runtime.LogosArenaInChunk(unsafe.Pointer(heap)) {
			t.Error("heap object is recognized as an arena address")
		}
	})
}

func TestLogosArenaSetFinalizerPanics(t *testing.T) {
	type T struct{ a, b uintptr }
	type S struct {
		p *int
		n uintptr
	}
	inFreshGoroutine(t, func() {
		m := runtime.LogosArenaSave()
		defer runtime.LogosArenaRestore(m)
		noscan := (*T)(runtime.LogosArenaAlloc(uintptr(0), unsafe.Sizeof(T{})))
		scan := (*S)(runtime.LogosArenaAllocScan((*int)(nil), unsafe.Sizeof(S{})))
		const want = "runtime: SetFinalizer on loGo/s arena object"
		for name, obj := range map[string]any{"noscan": noscan, "scan": scan} {
			msg, panicked := recoverMsg(func() { runtime.SetFinalizer(obj, nil) })
			if !panicked || msg != want {
				t.Errorf("SetFinalizer on %s arena object: panicked=%v msg=%q, want %q", name, panicked, msg, want)
			}
		}
		msg, panicked := recoverMsg(func() { runtime.SetFinalizer(noscan, func(*T) {}) })
		if !panicked || msg != want {
			t.Errorf("SetFinalizer with a finalizer: panicked=%v msg=%q, want %q", panicked, msg, want)
		}
		wantCleanup := "runtime: AddCleanup on loGo/s arena object"
		msg, panicked = recoverMsg(func() { runtime.AddCleanup(noscan, func(int) {}, 0) })
		if !panicked || msg != wantCleanup {
			t.Errorf("AddCleanup on arena object: panicked=%v msg=%q, want %q", panicked, msg, wantCleanup)
		}
		heap := new(T)
		if msg, panicked := recoverMsg(func() { runtime.SetFinalizer(heap, func(*T) {}) }); panicked {
			t.Errorf("SetFinalizer on a heap object panicked: %s", msg)
		}
		big := runtime.LogosArenaAlloc(byte(0), 1<<20)
		if runtime.LogosArenaInChunk(big) {
			t.Fatal("oversize request was served from a chunk")
		}
		if msg, panicked := recoverMsg(func() { runtime.SetFinalizer((*byte)(big), func(*byte) {}) }); panicked {
			t.Errorf("SetFinalizer on a mallocgc fallback object panicked: %s", msg)
		}
	})
}

func TestLogosArenaCgoChecksAcceptArenaAddresses(t *testing.T) {
	type holder struct{ p *int }
	inFreshGoroutine(t, func() {
		m := runtime.LogosArenaSave()
		defer runtime.LogosArenaRestore(m)
		leaf := (*int)(runtime.LogosArenaAlloc(0, 8))
		h := (*holder)(runtime.LogosArenaAllocScan((*int)(nil), unsafe.Sizeof(holder{})))
		h.p = leaf
		bytes := unsafe.Slice((*byte)(runtime.LogosArenaAlloc(byte(0), 32)), 32)
		heapLeaf := new(int)
		if !runtime.LogosArenaIsGoPointer(unsafe.Pointer(leaf)) || !runtime.LogosArenaIsGoPointer(unsafe.Pointer(h)) {
			t.Error("an arena address is not a Go pointer for the cgo checks")
		}
		cases := []struct {
			name string
			fn   func()
		}{
			{"pointer to noscan object", func() { runtime.LogosArenaCgoCheckPointer(leaf, true) }},
			{"pointer to scan object", func() { runtime.LogosArenaCgoCheckPointer(h, true) }},
			{"unsafe.Pointer to arena", func() { runtime.LogosArenaCgoCheckPointer(unsafe.Pointer(h), true) }},
			{"slice over arena bytes", func() { runtime.LogosArenaCgoCheckPointer(&bytes[0], bytes) }},
			{"arena struct holding an arena pointer", func() { runtime.LogosArenaCgoCheckPointer(*h, nil) }},
			{"result holding an arena pointer", func() { runtime.LogosArenaCgoCheckResult(h) }},
			{"heap pointer stored into arena memory", func() {
				runtime.LogosArenaCgoCheckPtrWrite((*unsafe.Pointer)(unsafe.Pointer(&h.p)), unsafe.Pointer(heapLeaf))
			}},
			{"arena pointer stored into heap memory", func() {
				var slot *int
				runtime.LogosArenaCgoCheckPtrWrite((*unsafe.Pointer)(unsafe.Pointer(&slot)), unsafe.Pointer(leaf))
			}},
		}
		for _, c := range cases {
			if msg, panicked := recoverMsg(c.fn); panicked {
				t.Errorf("%s: cgo check panicked: %s", c.name, msg)
			}
		}
	})
}

func TestLogosArenaExactFit(t *testing.T) {
	inFreshGoroutine(t, func() {
		m := runtime.LogosArenaSave()
		defer runtime.LogosArenaRestore(m)
		const capacity = runtime.LogosArenaCapacity
		a := runtime.LogosArenaAlloc(byte(0), capacity-8)
		b := runtime.LogosArenaAlloc(byte(0), 8)
		st := runtime.LogosArenaStat()
		if st.Chunks != 1 || st.Free != 0 || st.NoscanUsed != capacity {
			t.Errorf("after an exact fill: %+v, want one chunk, no free space, %d bytes used", st, capacity)
		}
		fillBytes(a, capacity-8, 0x11)
		fillBytes(b, 8, 0x22)
		if !checkBytes(a, capacity-8, 0x11) || !checkBytes(b, 8, 0x22) {
			t.Error("objects of an exact fill overlap")
		}
	})
}

func TestLogosArenaExactFitBothRegions(t *testing.T) {
	inFreshGoroutine(t, func() {
		m := runtime.LogosArenaSave()
		defer runtime.LogosArenaRestore(m)
		const noscan = 4096
		runtime.LogosArenaAlloc(byte(0), noscan)
		scanSize := uintptr(runtime.LogosArenaCapacity-noscan) - runtime.LogosArenaScanHeaderBytes
		p := runtime.LogosArenaAllocScan((*int)(nil), scanSize)
		st := runtime.LogosArenaStat()
		if st.Chunks != 1 || st.Free != 0 {
			t.Errorf("after an exact dual-region fill: %+v, want one chunk, no free space", st)
		}
		if !runtime.LogosArenaInChunk(p) {
			t.Error("the exactly fitting scan object is not in the chunk")
		}
	})
}

func TestLogosArenaOneByteOverflowLinksChunk(t *testing.T) {
	inFreshGoroutine(t, func() {
		m := runtime.LogosArenaSave()
		const capacity = runtime.LogosArenaCapacity
		a := runtime.LogosArenaAlloc(byte(0), capacity-8)
		fillBytes(a, capacity-8, 0x11)
		b := runtime.LogosArenaAlloc(byte(0), 9)
		fillBytes(b, 9, 0x22)
		st := runtime.LogosArenaStat()
		if st.Chunks != 2 || st.NoscanUsed != 9 {
			t.Errorf("after the overflow: %+v, want two chunks and 9 bytes in the newest", st)
		}
		if !runtime.LogosArenaInChunk(b) {
			t.Error("the overflowing object was not served from a chunk")
		}
		if !checkBytes(a, capacity-8, 0x11) || !checkBytes(b, 9, 0x22) {
			t.Error("an object was damaged by the chunk link")
		}
		runtime.LogosArenaRestore(m)
		if st := runtime.LogosArenaStat(); st.Chunks != 1 || st.NoscanUsed != 0 || st.ScanUsed != 0 {
			t.Errorf("after restore: %+v, want the first chunk empty and the overflow chunk returned", st)
		}
	})
}

func TestLogosArenaScanOverflowLinksChunk(t *testing.T) {
	inFreshGoroutine(t, func() {
		m := runtime.LogosArenaSave()
		defer runtime.LogosArenaRestore(m)
		x := new(int)
		first := uintptr(runtime.LogosArenaCapacity) - runtime.LogosArenaScanHeaderBytes
		a := runtime.LogosArenaAllocScan((*int)(nil), first)
		fillWords(a, first, x)
		b := runtime.LogosArenaAllocScan((*int)(nil), 8)
		fillWords(b, 8, x)
		if st := runtime.LogosArenaStat(); st.Chunks != 2 {
			t.Errorf("chunks = %d after a scan overflow, want 2", st.Chunks)
		}
		words := unsafe.Slice((**int)(a), first/8)
		for i, w := range words {
			if w != x {
				t.Fatalf("word %d of the first scan object was damaged", i)
			}
		}
		if *(**int)(b) != x {
			t.Error("the second scan object was damaged")
		}
	})
}

func TestLogosArenaEmptyPoolFallsBackToMallocgc(t *testing.T) {
	total, _, pooled := runtime.LogosArenaCounts()
	old := runtime.LogosArenaSetLimit(int64(total) + 1)
	defer runtime.LogosArenaSetLimit(old)
	inFreshGoroutine(t, func() {
		m := runtime.LogosArenaSave()
		defer runtime.LogosArenaRestore(m)
		const size = 60000
		var arena []unsafe.Pointer
		var heap unsafe.Pointer
		for i := 0; i <= pooled+2; i++ {
			p := runtime.LogosArenaAlloc(byte(0), size)
			if !runtime.LogosArenaInChunk(p) {
				heap = p
				break
			}
			fillBytes(p, size, byte(i+1))
			arena = append(arena, p)
		}
		if len(arena) == 0 {
			t.Fatal("the first allocation fell back: no arena object exists to check against the fallback")
		}
		if heap == nil {
			t.Fatalf("no fallback after %d chunk-sized allocations with the limit one above the current chunk count", len(arena))
		}
		if !checkBytes(heap, size, 0) {
			t.Error("fallback memory is not zero")
		}
		fillBytes(heap, size, 0xEE)
		for i, p := range arena {
			if !checkBytes(p, size, byte(i+1)) {
				t.Errorf("arena object %d was damaged by the fallback", i)
			}
		}
	})
}

func TestLogosArenaOversizeAndZeroSize(t *testing.T) {
	inFreshGoroutine(t, func() {
		const over = runtime.LogosArenaCapacity + 1
		p := runtime.LogosArenaAlloc(byte(0), over)
		if runtime.LogosArenaInChunk(p) || !checkBytes(p, over, 0) {
			t.Error("oversize noscan request: want zeroed heap memory")
		}
		scanOver := uintptr(runtime.LogosArenaChunkBytes)
		q := runtime.LogosArenaAllocScan((*int)(nil), scanOver)
		if runtime.LogosArenaInChunk(q) || !checkBytes(q, scanOver, 0) {
			t.Error("oversize scan request: want zeroed heap memory")
		}
		if st := runtime.LogosArenaStat(); st.Chunks != 0 {
			t.Errorf("chunks = %d after oversize requests, want none leased", st.Chunks)
		}
		if runtime.LogosArenaAlloc(byte(0), 0) == nil || runtime.LogosArenaAllocScan((*int)(nil), 0) == nil {
			t.Error("zero-size request returned nil")
		}
	})
}

func TestLogosArenaNoscanReuseIsZeroed(t *testing.T) {
	inFreshGoroutine(t, func() {
		m := runtime.LogosArenaSave()
		p := runtime.LogosArenaAlloc(byte(0), 256)
		fillBytes(p, 256, 0xFF)
		runtime.LogosArenaRestore(m)
		m = runtime.LogosArenaSave()
		defer runtime.LogosArenaRestore(m)
		q := runtime.LogosArenaAlloc(byte(0), 256)
		if q != p {
			t.Errorf("restored watermark gave %p, want the freed address %p", q, p)
		}
		if !checkBytes(q, 256, 0) {
			t.Error("reused noscan memory is not zero")
		}
	})
}

func TestLogosArenaScanLayoutAndZeroing(t *testing.T) {
	inFreshGoroutine(t, func() {
		m := runtime.LogosArenaSave()
		x := new(int)
		sizes := []uintptr{8, 24, 40, 128}
		objs := make([]unsafe.Pointer, len(sizes))
		for i, size := range sizes {
			objs[i] = runtime.LogosArenaAllocScan((*int)(nil), size)
			fillWords(objs[i], size, x)
		}
		typ := runtime.LogosArenaType((*int)(nil))
		var strides uintptr
		for i, p := range objs {
			gotTyp, gotSize := runtime.LogosArenaScanHeader(p)
			if gotTyp != typ || gotSize != sizes[i] {
				t.Errorf("object %d header = (%p, %d), want (%p, %d)", i, gotTyp, gotSize, typ, sizes[i])
			}
			strides += arenaStride(sizes[i])
			if i > 0 {
				if gap := uintptr(objs[i-1]) - uintptr(p); gap != arenaStride(sizes[i]) {
					t.Errorf("objects %d and %d are %d bytes apart, want %d", i-1, i, gap, arenaStride(sizes[i]))
				}
			}
		}
		if st := runtime.LogosArenaStat(); st.ScanUsed != strides {
			t.Errorf("ScanUsed = %d, want %d", st.ScanUsed, strides)
		}
		lo := uintptr(objs[len(objs)-1]) - runtime.LogosArenaScanHeaderBytes
		hi := uintptr(objs[0]) + (sizes[0]+arenaScanUnit-1)&^(arenaScanUnit-1)
		runtime.LogosArenaRestore(m)
		if st := runtime.LogosArenaStat(); st.ScanUsed != 0 {
			t.Errorf("ScanUsed = %d after restore, want 0", st.ScanUsed)
		}
		if !checkBytes(unsafe.Pointer(lo), hi-lo, 0) {
			t.Error("popped scan memory is not zero after restore")
		}
	})
}

func TestLogosArenaRestorePopsLinkedChunks(t *testing.T) {
	inFreshGoroutine(t, func() {
		m := runtime.LogosArenaSave()
		x := new(int)
		var objs []unsafe.Pointer
		const size = 30000
		for range 4 {
			p := runtime.LogosArenaAllocScan((*int)(nil), size)
			fillWords(p, size, x)
			objs = append(objs, p)
		}
		if st := runtime.LogosArenaStat(); st.Chunks < 2 {
			t.Fatalf("chunks = %d, want the scan objects to span several chunks", st.Chunks)
		}
		runtime.LogosArenaRestore(m)
		if st := runtime.LogosArenaStat(); st.Chunks != 1 || st.ScanUsed != 0 || st.NoscanUsed != 0 {
			t.Errorf("after restore: %+v, want one empty chunk", st)
		}
		var qs []unsafe.Pointer
		for range 4 {
			q := runtime.LogosArenaAllocScan((*int)(nil), size)
			qs = append(qs, q)
		}
		if qs[0] != objs[0] {
			t.Errorf("reused scan address = %p, want first object %p", qs[0], objs[0])
		}
		for i, q := range qs {
			if !checkBytes(q, size, 0) {
				t.Errorf("reused scan object %d memory is not zero after restore", i)
			}
		}
	})
}

func TestLogosArenaGCFreesCachedChunks(t *testing.T) {
	const n = 512
	goroutines := runtime.NumGoroutine()
	runtime.GC()
	_, leasedBase, _ := runtime.LogosArenaCounts()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	hold := make(chan struct{})
	var held, finished sync.WaitGroup
	held.Add(n)
	finished.Add(n)
	for range n {
		go func() {
			defer finished.Done()
			m := runtime.LogosArenaSave()
			runtime.LogosArenaAlloc(byte(0), 100)
			held.Done()
			<-hold
			runtime.LogosArenaRestore(m)
		}()
	}
	held.Wait()
	var during runtime.MemStats
	runtime.ReadMemStats(&during)
	if got, want := during.StackInuse-before.StackInuse, uint64(n)*runtime.LogosArenaChunkBytes; got < want {
		t.Errorf("StackInuse grew by %d with %d chunks leased, want at least %d", got, n, want)
	}
	if _, leased, _ := runtime.LogosArenaCounts(); leased < leasedBase+n {
		t.Errorf("leased = %d with %d goroutines holding arena memory, want at least %d", leased, n, leasedBase+n)
	}
	close(hold)
	finished.Wait()
	waitGoroutines(t, goroutines)
	waitLeased(t, leasedBase)
	runtime.GC()
	total, leased, pooled := runtime.LogosArenaCounts()
	if pooled != 0 || total != leased {
		t.Errorf("after GC: total=%d leased=%d pooled=%d, want every cached chunk freed", total, leased, pooled)
	}
}

func TestLogosArenaProcResizeFreesCachedChunks(t *testing.T) {
	procs := runtime.GOMAXPROCS(0)
	if procs < 2 {
		t.Skip("needs at least two Ps")
	}
	goroutines := runtime.NumGoroutine()
	const n = 256
	hold := make(chan struct{})
	var held, finished sync.WaitGroup
	held.Add(n)
	finished.Add(n)
	for range n {
		go func() {
			defer finished.Done()
			m := runtime.LogosArenaSave()
			runtime.LogosArenaAlloc(byte(0), 100)
			held.Done()
			<-hold
			runtime.LogosArenaRestore(m)
		}()
	}
	held.Wait()
	close(hold)
	finished.Wait()
	waitGoroutines(t, goroutines)
	before := runtime.LogosArenaPoolCounts()
	old := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(old)
	total, leased, pooled := runtime.LogosArenaCounts()
	if total != leased+pooled {
		t.Errorf("after shrinking to one P: total=%d leased=%d pooled=%d, want total = leased + pooled", total, leased, pooled)
	}
	if pooled > before[0] {
		t.Errorf("pooled = %d after shrinking to one P, which held %d: a chunk outlived its destroyed P", pooled, before[0])
	}
}

func TestLogosArenaScanOverNoscanDirt(t *testing.T) {
	inFreshGoroutine(t, func() {
		m := runtime.LogosArenaSave()
		defer runtime.LogosArenaRestore(m)
		const dirty = 60000
		p := runtime.LogosArenaAlloc(byte(0), dirty)
		fillBytes(p, dirty, 0xFF)
		runtime.LogosArenaRestore(m)
		const size = 30000
		q := runtime.LogosArenaAllocScan((*int)(nil), size)
		if uintptr(q)+size <= uintptr(p) || uintptr(q) >= uintptr(p)+dirty {
			t.Fatalf("scan object %p does not overlay the dirtied bytes at %p: the test no longer probes the overlay", q, p)
		}
		if !checkBytes(q, size, 0) {
			t.Error("scan object over dirtied noscan bytes is not zero")
		}
	})
}

func TestLogosArenaRetTokenIsOneShot(t *testing.T) {
	inFreshGoroutine(t, func() {
		if runtime.LogosArenaRetTake() {
			t.Error("fresh goroutine holds a consent token")
		}
		runtime.LogosArenaRetSet(true)
		if !runtime.LogosArenaRetTake() {
			t.Error("take after set(true) = false")
		}
		if runtime.LogosArenaRetTake() {
			t.Error("second take after one set = true: the token was not cleared")
		}
		runtime.LogosArenaRetSet(true)
		runtime.LogosArenaRetSet(false)
		if runtime.LogosArenaRetTake() {
			t.Error("take after set(true) then set(false) = true")
		}
	})
}

func TestLogosArenaMarkWordPairRoundTrip(t *testing.T) {
	inFreshGoroutine(t, func() {
		chunk, noscan, scan := runtime.LogosArenaMarkSave()
		if chunk != 0 || noscan != 0 || scan != 0 {
			t.Errorf("empty arena MarkSave = (%d, %d, %d), want zero triple", chunk, noscan, scan)
		}
		if p := runtime.LogosArenaAlloc(byte(0), 64); p == nil {
			t.Fatal("LogosArenaAlloc returned nil")
		}
		if st := runtime.LogosArenaStat(); st.NoscanUsed == 0 {
			t.Error("NoscanUsed = 0 after allocation, want > 0")
		}
		runtime.LogosArenaMarkRestore(chunk, noscan, scan)
		if st := runtime.LogosArenaStat(); st.NoscanUsed != 0 || st.ScanUsed != 0 {
			t.Errorf("after restoring the zero triple: NoscanUsed=%d ScanUsed=%d, want 0, 0", st.NoscanUsed, st.ScanUsed)
		}
		if p := runtime.LogosArenaAlloc(byte(0), 32); p == nil {
			t.Fatal("LogosArenaAlloc returned nil")
		}
		base := runtime.LogosArenaStat()
		chunk, noscan, scan = runtime.LogosArenaMarkSave()
		if chunk == 0 {
			t.Error("MarkSave after allocation returned a zero chunk word")
		}
		if p := runtime.LogosArenaAlloc(byte(0), 64); p == nil {
			t.Fatal("LogosArenaAlloc returned nil")
		}
		if p := runtime.LogosArenaAllocScan((*int)(nil), 64); p == nil {
			t.Fatal("LogosArenaAllocScan returned nil")
		}
		runtime.LogosArenaMarkRestore(chunk, noscan, scan)
		if st := runtime.LogosArenaStat(); st != base {
			t.Errorf("after word-pair restore: stat = %+v, want %+v", st, base)
		}
	})
}

func TestLogosArenaExitClearsToken(t *testing.T) {
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() {
			runtime.LogosArenaRetSet(true)
			runtime.Goexit()
		})
	}
	wg.Wait()
	var stale atomic.Int32
	for range 64 {
		wg.Go(func() {
			if runtime.LogosArenaRetTake() {
				stale.Add(1)
			}
		})
	}
	wg.Wait()
	if n := stale.Load(); n != 0 {
		t.Errorf("%d goroutines started with a stale consent token", n)
	}
}

func TestLogosArenaFieldOffset(t *testing.T) {
	if want := 4 * unsafe.Sizeof(uintptr(0)); runtime.LogosArenaFieldOffset != want {
		t.Errorf("unsafe.Offsetof(g{}.logosArena) = %d, want %d (4*PtrSize)", runtime.LogosArenaFieldOffset, want)
	}
}

//go:noinline
func arenaEligibleMake(n int) uintptr {
	b := make([]byte, n)
	if len(b) > 0 {
		b[0] = 42
	}
	during := runtime.LogosArenaStat().NoscanUsed
	if len(b) > 0 && b[0] != 42 {
		panic("memory corrupted")
	}
	return during
}

func TestLogosArenaDynamicMakeWatermark(t *testing.T) {
	if !goexperiment.LogosArena {
		t.Skip("skipping test: GOEXPERIMENT=logosarena not enabled")
	}
	inFreshGoroutine(t, func() {
		before := runtime.LogosArenaStat().NoscanUsed

		during1 := arenaEligibleMake(1024)
		after1 := runtime.LogosArenaStat().NoscanUsed

		if during1 <= before {
			t.Errorf("call 1: watermark during make = %d, want > %d (watermark did not advance)", during1, before)
		}
		if after1 != before {
			t.Errorf("call 1: watermark after frame exit = %d, want %d (watermark did not fall back)", after1, before)
		}

		during2 := arenaEligibleMake(2048)
		after2 := runtime.LogosArenaStat().NoscanUsed

		if during2 <= before {
			t.Errorf("call 2: watermark during make = %d, want > %d (watermark did not advance)", during2, before)
		}
		if after2 != before {
			t.Errorf("call 2: watermark after frame exit = %d, want %d (watermark did not fall back)", after2, before)
		}
	})
}

func TestLogosArenaGCScansScanRegion(t *testing.T) {
	if !goexperiment.LogosArena {
		t.Skip("skipping test: GOEXPERIMENT=logosarena not enabled")
	}

	type ptrHolder struct {
		p *[64]byte
	}

	inFreshGoroutine(t, func() {
		m := runtime.LogosArenaSave()

		heapObj := new([64]byte)
		heapObj[0] = 42
		w := weak.Make(heapObj)

		holder := (*ptrHolder)(runtime.LogosArenaAllocScan(ptrHolder{}, unsafe.Sizeof(ptrHolder{})))
		holder.p = heapObj
		heapObj = nil

		runtime.GC()
		runtime.GC()

		if w.Value() == nil {
			t.Fatal("heap referent collected while referenced by arena scan region (AR3 violation)")
		}
		if holder.p[0] != 42 {
			t.Fatalf("heap referent corrupted: got %d, want 42", holder.p[0])
		}

		runtime.LogosArenaRestore(m)

		for range 5 {
			runtime.GC()
			if w.Value() == nil {
				break
			}
		}
		if w.Value() != nil {
			t.Errorf("heap referent not reclaimed after arena restore (popped scan memory not cleared)")
		}
	})
}

func TestLogosArenaPointerSliceScanRetention(t *testing.T) {
	if !goexperiment.LogosArena {
		t.Skip("skipping test: GOEXPERIMENT=logosarena not enabled")
	}
	inFreshGoroutine(t, func() {
		m := runtime.LogosArenaSave()
		defer runtime.LogosArenaRestore(m)

		obj := new([64]byte)
		obj[0] = 77
		w := weak.Make(obj)

		sliceData := (*[16]*[64]byte)(runtime.LogosArenaAllocScan((*[64]byte)(nil), 16*unsafe.Sizeof((*[64]byte)(nil))))
		sliceData[0] = obj
		obj = nil

		runtime.GC()
		runtime.GC()

		if w.Value() == nil {
			t.Fatal("heap referent collected while held in arena scan pointer slice")
		}
		if sliceData[0][0] != 77 {
			t.Fatalf("slice data corrupted: got %d, want 77", sliceData[0][0])
		}

		runtime.LogosArenaRestore(m)
		for range 5 {
			runtime.GC()
			if w.Value() == nil {
				break
			}
		}
		if w.Value() != nil {
			t.Errorf("heap referent not reclaimed after arena restore")
		}
	})
}

//go:noinline
func arenaMakeAndPanic(n int) {
	b := make([]byte, n)
	if len(b) > 0 {
		b[0] = 7
	}
	panic("arena panic unwind test")
}

func TestLogosArenaPanicUnwindRecovery(t *testing.T) {
	if !goexperiment.LogosArena {
		t.Skip("skipping test: GOEXPERIMENT=logosarena not enabled")
	}
	inFreshGoroutine(t, func() {
		before := runtime.LogosArenaStat().NoscanUsed

		func() {
			defer func() {
				_ = recover()
			}()
			arenaMakeAndPanic(1024)
		}()

		after := runtime.LogosArenaStat().NoscanUsed
		if after != before {
			t.Errorf("watermark leaked across recovered panic: before=%d, after=%d (G3 panic-unwind restore missing)", before, after)
		}
	})
}

//go:noinline
func arenaMakeLoop(iters, allocSize int) uintptr {
	var lastWatermark uintptr
	for i := range iters {
		b := make([]byte, allocSize)
		if len(b) > 0 {
			b[0] = byte(i)
		}
		lastWatermark = runtime.LogosArenaStat().NoscanUsed
	}
	return lastWatermark
}

func TestLogosArenaLoopWatermarkBounded(t *testing.T) {
	if !goexperiment.LogosArena {
		t.Skip("skipping test: GOEXPERIMENT=logosarena not enabled")
	}
	inFreshGoroutine(t, func() {
		before := runtime.LogosArenaStat().NoscanUsed

		wm1 := arenaMakeLoop(1, 1024)
		used1 := wm1 - before

		wm10 := arenaMakeLoop(10, 1024)
		used10 := wm10 - before

		if used10 > used1*2 {
			t.Errorf("loop arena watermark grew unboundedly: 1-iter used %d B, 10-iter used %d B (G4 loop back-edge restore missing)", used1, used10)
		}
	})
}

//go:noinline
func arenaMakeSwitchBreakLoop(outerIters, allocSize int) uintptr {
	var lastWatermark uintptr
	for o := range outerIters {
	SwitchOuter:
		switch o >= 0 {
		case true:
			for i := range 5 {
				b := make([]byte, allocSize)
				if len(b) > 0 {
					b[0] = byte(i + o)
				}
				lastWatermark = runtime.LogosArenaStat().NoscanUsed
				if i == 2 {
					break SwitchOuter
				}
			}
		case false:
		}
	}
	return lastWatermark
}

func TestLogosArenaSwitchBreakWatermarkBounded(t *testing.T) {
	if !goexperiment.LogosArena {
		t.Skip("skipping test: GOEXPERIMENT=logosarena not enabled")
	}
	if runtime.GOARCH != "amd64" {
		t.Skip("skipping test: arena make lowering is amd64-only")
	}
	inFreshGoroutine(t, func() {
		before := runtime.LogosArenaStat().NoscanUsed

		wm1 := arenaMakeSwitchBreakLoop(1, 256)
		used1 := wm1 - before
		if used1 == 0 {
			t.Fatalf("switch-break probe allocated nothing in the arena: 1-iter used 0 B, the attack is not live")
		}

		wm10 := arenaMakeSwitchBreakLoop(10, 256)
		used10 := wm10 - before

		if used10 > used1*2 {
			t.Errorf("switch-break arena watermark grew unboundedly: 1-iter used %d B, 10-iter used %d B (enclosing-switch exit restore missing)", used1, used10)
		}
	})
}

//go:noinline
func arenaMakeSelectBreakLoop(outerIters, allocSize int, ch chan int) uintptr {
	var lastWatermark uintptr
	for o := range outerIters {
	SelectOuter:
		select {
		case <-ch:
			for i := range 5 {
				b := make([]byte, allocSize)
				if len(b) > 0 {
					b[0] = byte(i + o)
				}
				lastWatermark = runtime.LogosArenaStat().NoscanUsed
				if i == 2 {
					break SelectOuter
				}
			}
		default:
		}
	}
	return lastWatermark
}

func TestLogosArenaSelectBreakWatermarkBounded(t *testing.T) {
	if !goexperiment.LogosArena {
		t.Skip("skipping test: GOEXPERIMENT=logosarena not enabled")
	}
	if runtime.GOARCH != "amd64" {
		t.Skip("skipping test: arena make lowering is amd64-only")
	}
	inFreshGoroutine(t, func() {
		ch := make(chan int)
		close(ch)
		before := runtime.LogosArenaStat().NoscanUsed

		wm1 := arenaMakeSelectBreakLoop(1, 256, ch)
		used1 := wm1 - before
		if used1 == 0 {
			t.Fatalf("select-break probe allocated nothing in the arena: 1-iter used 0 B, the attack is not live")
		}

		wm10 := arenaMakeSelectBreakLoop(10, 256, ch)
		used10 := wm10 - before

		if used10 > used1*2 {
			t.Errorf("select-break arena watermark grew unboundedly: 1-iter used %d B, 10-iter used %d B (enclosing-select exit restore missing)", used1, used10)
		}
	})
}

const (
	arenaMakeLenPanic = "runtime error: makeslice: len out of range"
	arenaMakeCapPanic = "runtime error: makeslice: cap out of range"

	arenaMakeAllocSlack = 1 << 20
)

type arenaMakeCase struct {
	len, cap int64
	want     string
}

type arenaElem24 [3]int64

//go:noinline
func arenaSeamInt64(n, c int) (length, capacity int, used uintptr) {
	b := make([]int64, n, c)
	return len(b), cap(b), runtime.LogosArenaStat().NoscanUsed
}

//go:noinline
func arenaSeamElem24(n, c int) (int, int) {
	b := make([]arenaElem24, n, c)
	return len(b), cap(b)
}

//go:noinline
func arenaSeamByte(n, c int) (int, int) {
	b := make([]byte, n, c)
	return len(b), cap(b)
}

//go:noinline
func arenaSeamUint32Wide(n, c uint64) (int, int) {
	b := make([]uint32, n, c)
	return len(b), cap(b)
}

//go:noinline
func arenaSeamFill(n, c int) bool {
	b := make([]int64, n, c)
	if len(b) != n || cap(b) != c {
		return false
	}
	b = b[:cap(b)]
	for i := range b {
		if b[i] != 0 {
			return false
		}
		b[i] = int64(i) + 1
	}
	for i := range b {
		if b[i] != int64(i)+1 {
			return false
		}
	}
	return true
}

//go:noinline
func heapSeamInt64(n, c int) { arenaSink = make([]int64, n, c) }

//go:noinline
func heapSeamElem24(n, c int) { arenaSink = make([]arenaElem24, n, c) }

//go:noinline
func heapSeamByte(n, c int) { arenaSink = make([]byte, n, c) }

//go:noinline
func heapSeamUint32Wide(n, c uint64) { arenaSink = make([]uint32, n, c) }

func arenaMakeMustPanic(t *testing.T, what, want string, fn func()) {
	t.Helper()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	msg, panicked := recoverMsg(fn)
	runtime.ReadMemStats(&after)
	if !panicked {
		t.Fatalf("%s: make returned without panic, want %q", what, want)
	}
	if msg != want {
		t.Errorf("%s: panic %q, want %q", what, msg, want)
	}
	if grew := after.TotalAlloc - before.TotalAlloc; grew > arenaMakeAllocSlack {
		t.Errorf("%s: panicking make allocated %d heap bytes, want at most %d", what, grew, arenaMakeAllocSlack)
	}
}

func arenaMakeOverflowCases(size uintptr) []arenaMakeCase {
	wrap := int64(^uintptr(0)/size + 1)
	minInt := int64(math.MinInt)
	return []arenaMakeCase{
		{0, wrap, arenaMakeCapPanic},
		{0, wrap + 1, arenaMakeCapPanic},
		{1, wrap, arenaMakeCapPanic},
		{1, wrap + 1, arenaMakeCapPanic},
		{wrap, wrap, arenaMakeLenPanic},
		{wrap + 1, wrap + 1, arenaMakeLenPanic},
		{wrap + 1, wrap + 2, arenaMakeLenPanic},
		{wrap, 1, arenaMakeLenPanic},
		{0, minInt, arenaMakeCapPanic},
		{minInt, 0, arenaMakeLenPanic},
		{minInt, minInt, arenaMakeLenPanic},
		{-1, 1, arenaMakeLenPanic},
		{-1, -1, arenaMakeLenPanic},
		{1, -1, arenaMakeCapPanic},
		{2, 1, arenaMakeCapPanic},
	}
}

func arenaMakeRun(t *testing.T, size uintptr, arena, heap func(n, c int)) {
	for _, tc := range arenaMakeOverflowCases(size) {
		t.Run(fmt.Sprintf("len=%d,cap=%d", tc.len, tc.cap), func(t *testing.T) {
			n, c := int(tc.len), int(tc.cap)
			arenaMakeMustPanic(t, "heap control", tc.want, func() { heap(n, c) })
			arenaMakeMustPanic(t, "arena make", tc.want, func() { arena(n, c) })
		})
	}
}

func TestLogosArenaMakeOverflow(t *testing.T) {
	if !goexperiment.LogosArena {
		t.Skip("skipping test: GOEXPERIMENT=logosarena not enabled")
	}
	t.Run("int64", func(t *testing.T) {
		arenaMakeRun(t, 8, func(n, c int) { arenaSeamInt64(n, c) }, heapSeamInt64)
	})
	t.Run("elem24", func(t *testing.T) {
		arenaMakeRun(t, 24, func(n, c int) { arenaSeamElem24(n, c) }, heapSeamElem24)
	})
}

func TestLogosArenaMakeOverflowWide(t *testing.T) {
	if !goexperiment.LogosArena {
		t.Skip("skipping test: GOEXPERIMENT=logosarena not enabled")
	}
	top := uint64(1) << 63
	wrap := uint64(1) << 62
	for _, tc := range [][2]uint64{
		{0, wrap}, {wrap, wrap}, {0, top}, {top, top}, {top, 0}, {1, top + 1}, {wrap + 1, wrap + 1},
	} {
		t.Run(fmt.Sprintf("len=%d,cap=%d", tc[0], tc[1]), func(t *testing.T) {
			n, c := tc[0], tc[1]
			msg, panicked := recoverMsg(func() { heapSeamUint32Wide(n, c) })
			if !panicked {
				t.Fatal("heap control: make returned without panic")
			}
			arenaMakeMustPanic(t, "arena make", msg, func() { arenaSeamUint32Wide(n, c) })
		})
	}
}

func TestLogosArenaMakeOverflowMaxAlloc(t *testing.T) {
	if !goexperiment.LogosArena {
		t.Skip("skipping test: GOEXPERIMENT=logosarena not enabled")
	}
	wrapsToZero := int(^uintptr(0)/8 + 1)
	if _, panicked := recoverMsg(func() { arenaSeamInt64(0, wrapsToZero) }); !panicked {
		t.Fatal("make([]int64, 0, cap) with a wrapping byte size returned without panic; refusing to attempt sizes past maxAlloc")
	}
	const pastInt64 = int(runtime.LogosArenaMaxAlloc/8 + 1)
	for _, tc := range []struct {
		name string
		n, c int
		want string
	}{
		{"cap", 0, pastInt64, arenaMakeCapPanic},
		{"len", pastInt64, pastInt64, arenaMakeLenPanic},
		{"maxint-cap", 0, math.MaxInt, arenaMakeCapPanic},
		{"maxint-len", math.MaxInt, math.MaxInt, arenaMakeLenPanic},
	} {
		t.Run("int64/"+tc.name, func(t *testing.T) {
			arenaMakeMustPanic(t, "heap control", tc.want, func() { heapSeamInt64(tc.n, tc.c) })
			arenaMakeMustPanic(t, "arena make", tc.want, func() { arenaSeamInt64(tc.n, tc.c) })
		})
	}
	if bits.UintSize == 64 {
		maxAlloc := runtime.LogosArenaMaxAlloc
		pastByte := int(maxAlloc) + 1
		t.Run("byte/cap", func(t *testing.T) {
			arenaMakeMustPanic(t, "heap control", arenaMakeCapPanic, func() { heapSeamByte(0, pastByte) })
			arenaMakeMustPanic(t, "arena make", arenaMakeCapPanic, func() { arenaSeamByte(0, pastByte) })
		})
		t.Run("byte/len", func(t *testing.T) {
			arenaMakeMustPanic(t, "heap control", arenaMakeLenPanic, func() { heapSeamByte(pastByte, pastByte) })
			arenaMakeMustPanic(t, "arena make", arenaMakeLenPanic, func() { arenaSeamByte(pastByte, pastByte) })
		})
	}
}

func TestLogosArenaMakeSeamPlacement(t *testing.T) {
	if !goexperiment.LogosArena {
		t.Skip("skipping test: GOEXPERIMENT=logosarena not enabled")
	}
	if runtime.GOARCH != "amd64" {
		t.Skip("skipping test: arena make lowering is amd64-only")
	}
	inFreshGoroutine(t, func() {
		before := runtime.LogosArenaStat().NoscanUsed
		n, c, during := arenaSeamInt64(64, 96)
		after := runtime.LogosArenaStat().NoscanUsed
		if n != 64 || c != 96 {
			t.Errorf("make([]int64, 64, 96) has len %d cap %d", n, c)
		}
		if want := before + 96*8; during < want {
			t.Errorf("watermark during make = %d, want at least %d (probe site is not arena-placed)", during, want)
		}
		if after != before {
			t.Errorf("watermark after frame exit = %d, want %d", after, before)
		}
		for _, tc := range [][2]int{{0, 0}, {1, 1}, {3, 5}, {64, 64}, {513, 1024}, {int(runtime.LogosArenaChunkBytes/8) + 1, int(runtime.LogosArenaChunkBytes/8) + 1}} {
			if !arenaSeamFill(tc[0], tc[1]) {
				t.Errorf("make([]int64, %d, %d) is not zeroed, writable, and cap-sized", tc[0], tc[1])
			}
		}
	})
}

func TestLogosArenaMakeSliceEntry(t *testing.T) {
	if !goexperiment.LogosArena {
		t.Skip("skipping test: GOEXPERIMENT=logosarena not enabled")
	}
	for _, size := range []uintptr{8, 24} {
		elem := any(int64(0))
		if size == 24 {
			elem = arenaElem24{}
		}
		for _, tc := range arenaMakeOverflowCases(size) {
			t.Run(fmt.Sprintf("size=%d/len=%d,cap=%d", size, tc.len, tc.cap), func(t *testing.T) {
				arenaMakeMustPanic(t, "entry", tc.want, func() { runtime.LogosArenaMakeSlice(elem, tc.len, tc.cap) })
			})
		}
	}
	if bits.UintSize == 32 {
		wide := int64(1) << 32
		t.Run("truncated-len", func(t *testing.T) {
			arenaMakeMustPanic(t, "entry", arenaMakeLenPanic, func() { runtime.LogosArenaMakeSlice(int64(0), wide, wide) })
		})
		t.Run("truncated-cap", func(t *testing.T) {
			arenaMakeMustPanic(t, "entry", arenaMakeCapPanic, func() { runtime.LogosArenaMakeSlice(int64(0), 0, wide) })
		})
	}
}

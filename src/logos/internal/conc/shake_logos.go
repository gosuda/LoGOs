// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conc

import (
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
)

const ShakeEnv = "LOGOSCONC_SHAKE"

const maxShakeYields = 4

var shakeSeed = sync.OnceValue(func() uint64 {
	v, err := strconv.ParseUint(os.Getenv(ShakeEnv), 10, 64)
	if err != nil {
		return 0
	}
	return v
})

func ShakeSeed() uint64 {
	return shakeSeed()
}

func ShakeYields(seed, goid, n uint64) int {
	if seed == 0 {
		return 0
	}
	mix := splitmix64(seed + goid*0x9E3779B97F4A7C15 + n*0xBF58476D1CE4E5B9)
	return 1 + int(mix%uint64(maxShakeYields))
}

func splitmix64(x uint64) uint64 {
	x += 0x9E3779B97F4A7C15
	z := x
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}

var shakeCounts [256]atomic.Uint64

func Shake() {
	seed := shakeSeed()
	if seed == 0 {
		return
	}
	g := goroutineID()
	n := shakeCounts[g&255].Add(1)
	for i := ShakeYields(seed, g, n); i > 0; i-- {
		runtime.Gosched()
	}
}

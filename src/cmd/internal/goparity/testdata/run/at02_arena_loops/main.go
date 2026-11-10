// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
)

var loopGlobal []byte

const mkSeed = 42

func fillB(b []byte, seed uint64) {
	for i := range b {
		b[i] = byte(seed*31+uint64(i))%251 + 1
	}
}

func verifyB(b []byte, seed uint64) bool {
	for i := range b {
		if b[i] != byte(seed*31+uint64(i))%251+1 {
			return false
		}
	}
	return true
}

//go:noinline
func mkB(n int) []byte {
	s := make([]byte, n)
	fillB(s, mkSeed)
	return s
}

func arenaTouch(n int) bool {
	c := make([]byte, n)
	fillB(c, 0x55)
	return verifyB(c, 0x55)
}

func iter4(yield func(int) bool) {
	for i := range 4 {
		if !yield(i) {
			return
		}
	}
}

func for3(n int) bool {
	var all [4][]byte
	for i := 0; i < 4; i++ {
		s := make([]byte, n)
		fillB(s, uint64(i))
		all[i] = s
	}
	arenaTouch(n)
	for k := range all {
		if !verifyB(all[k], uint64(k)) {
			return false
		}
	}
	return true
}

func for3Continue(n int) bool {
	var all [4][]byte
	for i := range 4 {
		s := make([]byte, n)
		fillB(s, uint64(i))
		all[i] = s
		if i == 1 {
			continue
		}
		_ = s[0]
	}
	arenaTouch(n)
	for k := range all {
		if !verifyB(all[k], uint64(k)) {
			return false
		}
	}
	return true
}

func rangeInt(n int) bool {
	var all [4][]byte
	for i := range 4 {
		s := make([]byte, n)
		fillB(s, uint64(i))
		all[i] = s
	}
	arenaTouch(n)
	for k := range all {
		if !verifyB(all[k], uint64(k)) {
			return false
		}
	}
	return true
}

func rangeFunc(n int) bool {
	var all [3][]byte
	iter4(func(i int) bool {
		s := make([]byte, n)
		fillB(s, uint64(i))
		all[i] = s
		return i != 2
	})
	arenaTouch(n)
	for k := range all {
		if !verifyB(all[k], uint64(k)) {
			return false
		}
	}
	return true
}

func gotoOut(n int) bool {
	var all [3][]byte
	for i := range 4 {
		s := make([]byte, n)
		fillB(s, uint64(i))
		all[i] = s
		if i == 2 {
			goto out
		}
	}
out:
	arenaTouch(n)
	for k := range all {
		if !verifyB(all[k], uint64(k)) {
			return false
		}
	}
	return true
}

func panicRecover(n int) (ok bool) {
	var all [3][]byte
	defer func() {
		if r := recover(); r != nil {
			_ = r
		}
		ok = true
		for k := range all {
			if !verifyB(all[k], uint64(k)) {
				ok = false
			}
		}
	}()
	for i := range 4 {
		s := make([]byte, n)
		fillB(s, uint64(i))
		all[i] = s
		if i == 2 {
			panic("boom")
		}
	}
	return true
}

func const4K(n int) bool {
	var all [4][]byte
	for i := range 4 {
		s := make([]byte, 4096)
		fillB(s, uint64(i))
		all[i] = s
	}
	arenaTouch(4096)
	for k := range all {
		if !verifyB(all[k], uint64(k)) {
			return false
		}
	}
	return true
}

func chainFor3(n int) []byte {
	for range 4 {
		loopGlobal = mkB(n)
	}
	if !arenaTouch(n) || !verifyB(loopGlobal, mkSeed) {
		return nil
	}
	return mkB(n)
}

func chainRange(n int) []byte {
	for i := range 4 {
		loopGlobal = mkB(n)
		if i == 1 {
			continue
		}
		_ = loopGlobal[0]
	}
	if !arenaTouch(n) || !verifyB(loopGlobal, mkSeed) {
		return nil
	}
	return mkB(n)
}

func chainLabeled(n int) []byte {
outer:
	for i := 0; i < 4; i++ {
		loopGlobal = mkB(n)
		for j := range 2 {
			if j == 1 {
				continue outer
			}
		}
	}
	if !arenaTouch(n) || !verifyB(loopGlobal, mkSeed) {
		return nil
	}
	return mkB(n)
}

func chainBreak(n int) []byte {
outer:
	for i := 0; i < 4; i++ {
		loopGlobal = mkB(n)
		for j := range 3 {
			loopGlobal = mkB(n)
			if j == 2 {
				break outer
			}
		}
	}
	if !arenaTouch(n) || !verifyB(loopGlobal, mkSeed) {
		return nil
	}
	return mkB(n)
}

func chainGoto(n int) []byte {
	for i := 0; i < 4; i++ {
		loopGlobal = mkB(n)
		if i == 2 {
			goto out
		}
	}
out:
	if !arenaTouch(n) || !verifyB(loopGlobal, mkSeed) {
		return nil
	}
	return mkB(n)
}

type bcase struct {
	name string
	fn   func(int) bool
}

type ccase struct {
	name string
	fn   func(int) []byte
}

var bcases = []bcase{
	{"for3", for3},
	{"for3c", for3Continue},
	{"rangeint", rangeInt},
	{"rangefunc", rangeFunc},
	{"goto", gotoOut},
	{"panic", panicRecover},
	{"const4k", const4K},
}

var ccases = []ccase{
	{"chainFor3", chainFor3},
	{"chainRange", chainRange},
	{"chainLabeled", chainLabeled},
	{"chainBreak", chainBreak},
	{"chainGoto", chainGoto},
}

func main() {
	if len(mkB(8)) != 8 {
		fmt.Println("LAW arena_loops producer broken")
		os.Exit(1)
	}
	caseNo := 0
	bad := 0
	for _, n := range []int{33, 257} {
		for _, c := range bcases {
			caseNo++
			if c.fn(n) {
				fmt.Printf("case %03d n%d %s ok\n", caseNo, n, c.name)
			} else {
				bad++
				fmt.Printf("case %03d n%d %s LAW clobbered\n", caseNo, n, c.name)
			}
		}
		for _, c := range ccases {
			caseNo++
			s := c.fn(n)
			if s != nil && verifyB(s, mkSeed) {
				fmt.Printf("case %03d n%d %s ok\n", caseNo, n, c.name)
			} else {
				bad++
				fmt.Printf("case %03d n%d %s LAW clobbered\n", caseNo, n, c.name)
			}
		}
	}
	if bad > 0 {
		fmt.Printf("LAW arena_loops %d of %d cases clobbered\n", bad, caseNo)
		os.Exit(1)
	}
	fmt.Println("arena_loops ok")
}

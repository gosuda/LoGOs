// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
)

var sinkHold []byte
var globalA []byte

type prod struct{}

func (prod) mk(n int) []byte {
	s := make([]byte, n)
	fillB(s)
	return s
}

func fillB(b []byte) {
	for i := range b {
		b[i] = byte(i*17^0x5A)%251 + 1
	}
}

func verifyB(b []byte, n int) bool {
	if len(b) != n {
		return false
	}
	for i := range b {
		if b[i] != byte(i*17^0x5A)%251+1 {
			return false
		}
	}
	return true
}

func clobberB(n int) {
	b := make([]byte, n)
	for i := range b {
		b[i] = 0x55
	}
	sinkHold = b
}

func mkB(n int) []byte {
	s := make([]byte, n)
	fillB(s)
	return s
}

func mid(n int) []byte {
	return mkB(n)
}

func mid2(n int) []byte {
	return mid(n)
}

func recMk(n, d int) []byte {
	if d == 0 {
		return mkB(n)
	}
	return recMk(n, d-1)
}

func recMid(n, d int) []byte {
	if d == 0 {
		return mid(n)
	}
	return recMid(n, d-1)
}

func cDirect(n int) []byte {
	return mkB(n)
}

func cGlobal(n int) []byte {
	globalA = mkB(n)
	return globalA
}

func cChain(n int) []byte {
	return mid(n)
}

func cChain2(n int) []byte {
	return mid2(n)
}

func cFuncVal(n int) []byte {
	f := mkB
	return f(n)
}

func cMethodExpr(n int) []byte {
	return prod.mk(prod{}, n)
}

func cMethodVal(n int) []byte {
	p := prod{}
	mv := p.mk
	return mv(n)
}

func cClosure(n int) []byte {
	f := func() []byte { return mkB(n) }
	return f()
}

func cClosureChain(n int) []byte {
	f := func() []byte { return mid(n) }
	return f()
}

func cGoChan(n int) []byte {
	ch := make(chan []byte, 1)
	go func() { ch <- mkB(n) }()
	return <-ch
}

func cGoChain(n int) []byte {
	ch := make(chan []byte, 1)
	go func() { ch <- mid(n) }()
	return <-ch
}

func cDefer(n int) []byte {
	return func() (r []byte) {
		defer func() { r = mkB(n) }()
		return nil
	}()
}

func cRec(n int) []byte {
	return recMk(n, 3)
}

func cRecChain(n int) []byte {
	return recMid(n, 3)
}

func cGlobalChain(n int) []byte {
	globalA = mid(n)
	return globalA
}

func cFuncValChain(n int) []byte {
	f := mid
	return f(n)
}

type ccase struct {
	name string
	fn   func(int) []byte
}

var ccases = []ccase{
	{"direct", cDirect},
	{"global", cGlobal},
	{"chain", cChain},
	{"chain2", cChain2},
	{"funcval", cFuncVal},
	{"methodexpr", cMethodExpr},
	{"methodval", cMethodVal},
	{"closure", cClosure},
	{"closurechain", cClosureChain},
	{"gochan", cGoChan},
	{"gochain", cGoChain},
	{"defer", cDefer},
	{"rec", cRec},
	{"recchain", cRecChain},
	{"globalchain", cGlobalChain},
	{"funcvalchain", cFuncValChain},
}

func arenaTouch(n int) bool {
	c := make([]byte, n)
	fillB(c)
	return verifyB(c, n)
}

func main() {
	if len(mkB(8)) != 8 {
		fmt.Println("LAW arena_consent producer broken")
		os.Exit(1)
	}
	caseNo := 0
	bad := 0
	for _, n := range []int{33, 257, 4097} {
		for _, c := range ccases {
			caseNo++
			s := c.fn(n)
			clobberB(n)
			arenaTouch(n)
			if verifyB(s, n) {
				fmt.Printf("case %03d n%d %s ok\n", caseNo, n, c.name)
			} else {
				bad++
				fmt.Printf("case %03d n%d %s LAW clobbered\n", caseNo, n, c.name)
			}
		}
	}
	if bad > 0 {
		fmt.Printf("LAW arena_consent %d of %d cases clobbered\n", bad, caseNo)
		os.Exit(1)
	}
	fmt.Println("arena_consent ok")
}

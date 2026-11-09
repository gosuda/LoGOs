// errorcheck -0 -m -goexperiment logosarena,logosescape

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package p

import "logos/arena"

type node struct {
	next *node
	v    int
}

var global *node

var sinkRegion arena.Region

var sinkFn func()

//go:noinline
func keepHandle(h arena.Region) { // ERROR "leaking param: h"
	sinkRegion = h
}

//go:noinline
func storeClo(f func()) { // ERROR "leaking param: f"
	sinkFn = f
}

//go:noinline
func link(o, p *node) { // ERROR "o does not escape" "p does not escape"
	o.next = p
}

func buildList() {
	r := arena.Open()
	var head *node
	for i := range 10 {
		n := arena.New[node](r) // ERROR "placed in region"
		n.v = i
		n.next = head
		head = n
	}
	_ = head
}

func storeGlobal() {
	r := arena.Open()
	p := arena.New[node](r) // ERROR "region fallback: .* global"
	p.v = 1
	global = p
	_ = p
}

func storeOutside() {
	var q *node
	{
		r := arena.Open()
		p := arena.New[node](r) // ERROR "region fallback: .* q"
		p.v = 2
		q = p
		_ = p
	}
	_ = q
}

func nest193() {
	{
		rOut := arena.Open()
		po := arena.New[node](rOut) // ERROR "placed in region"
		{
			rIn := arena.Open()
			pi := arena.New[node](rIn) // ERROR "region fallback"
			po.next = pi
			_, _ = po, pi
		}
		_ = po
	}
}

func nest193Rev() {
	{
		rOut := arena.Open()
		po := arena.New[node](rOut) // ERROR "placed in region"
		{
			rIn := arena.Open()
			pi := arena.New[node](rIn) // ERROR "placed in region"
			pi.next = po
			_, _ = pi, po
		}
		_ = po
	}
}

func link193() {
	{
		rOut := arena.Open()
		po := arena.New[node](rOut) // ERROR "placed in region"
		{
			rIn := arena.Open()
			pi := arena.New[node](rIn) // ERROR "region fallback: .* link"
			link(po, pi)
			_, _ = po, pi
		}
		_ = po
	}
}

func link193Rev() {
	{
		rOut := arena.Open()
		po := arena.New[node](rOut) // ERROR "placed in region"
		{
			rIn := arena.Open()
			pi := arena.New[node](rIn) // ERROR "placed in region"
			link(pi, po)
			_, _ = pi, po
		}
		_ = po
	}
}

func loadSink193() {
	{
		rOut := arena.Open()
		po := arena.New[node](rOut) // ERROR "placed in region"
		{
			rIn := arena.Open()
			slot := arena.New[*node](rIn) // ERROR "placed in region"
			*slot = po
			q := *slot
			pi := arena.New[node](rIn) // ERROR "region fallback"
			q.next = pi
			_, _ = po, pi
		}
		_ = po
	}
}

func appendUnknown() {
	r := arena.Open()
	heapSlice := make([]int, 0, 8) // ERROR "does not escape"
	x := 1
	arena.Append[int](r, heapSlice, x) // ERROR "region fallback: region unknown" "argument does not escape"
	_ = x
	_ = heapSlice
}

func withGoto() {
	r := arena.Open()
	p := arena.New[node](r) // ERROR "region fallback: function contains goto"
	i := 0
loop:
	i++
	if i < 3 {
		goto loop
	}
	_ = p
}

func killedRecord() {
	r := arena.Open()       // ERROR "region fallback"
	p := arena.New[node](r) // ERROR "region fallback"
	p.v = 3
	keepHandle(r)
	_ = p
}

func closure191() {
	r := arena.Open()       // ERROR "region fallback"
	p := arena.New[node](r) // ERROR "region fallback: region unknown"
	p.v = 4
	f := func() { _ = r } // ERROR "can inline closure191.func1" "func literal escapes to heap"
	storeClo(f)
	_ = p
}

func storeLocalAddr() {
	r := arena.Open()
	pi := arena.New[*int](r) // ERROR "placed in region"
	lv := 5                  // ERROR "moved to heap: lv"
	*pi = &lv
	_ = *pi
}

// errorcheck -0 -m -goexperiment logosescape

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package p

type node struct{ next *node }

//go:noinline
func link(o, p *node) { // ERROR "o does not escape" "p does not escape"
	o.next = p
}

func linkCaller() { // ERROR "can inline linkCaller"
	var a, b node
	link(&a, &b)
	_, _ = a, b
}

var global *node

//go:noinline
func storeGlobal(o, p *node) { // ERROR "o does not escape" "leaking param: p"
	global = p
	o.next = p
}

func storeGlobalCaller() { // ERROR "can inline storeGlobalCaller"
	var a, b node // ERROR "moved to heap: b"
	storeGlobal(&a, &b)
	_, _ = a, b
}

//go:noinline
func ping(o, p *node) { // ERROR "o does not escape" "p does not escape"
	pong(p, o)
}

//go:noinline
func pong(p, o *node) { // ERROR "p does not escape" "o does not escape"
	ping(o, p)
}

func pingCaller() { // ERROR "can inline pingCaller"
	var a, b node
	ping(&a, &b)
	_, _ = a, b
}

type writer interface{ set(*node) }

type box struct{ at *node }

//go:noinline
func (b box) set(n *node) { b.at.next = n } // ERROR "b does not escape" "leaking param: n"

//go:noinline
func storeInterface(w writer, o, p *node) { // ERROR "leaking param: w" "o does not escape" "leaking param: p"
	w.set(p)
	o.next = p
}

func storeInterfaceCaller() {
	var a, b node // ERROR "moved to heap: a" "moved to heap: b"
	storeInterface(box{&a}, &a, &b)
	_, _ = a, b
}

//go:noinline
func callFn(f func()) { f() } // ERROR "f does not escape"

//go:noinline
func storeClosure(o, p *node) { // ERROR "o does not escape" "leaking param: p"
	f := func() { o.next = p } // ERROR "func literal does not escape" "can inline storeClosure.func1"
	callFn(f)
}

func storeClosureCaller() { // ERROR "can inline storeClosureCaller"
	var a, b node // ERROR "moved to heap: b"
	storeClosure(&a, &b)
	_, _ = a, b
}

//go:noinline
func storeSlice(o, p *node) { // ERROR "o does not escape" "leaking param: p"
	s := []*node{p} // ERROR "\[\]\*node{...} does not escape"
	o.next = s[0]
}

func storeSliceCaller() { // ERROR "can inline storeSliceCaller"
	var a, b node // ERROR "moved to heap: b"
	storeSlice(&a, &b)
	_, _ = a, b
}

//go:noinline
func storeTwoLevels(o, mid, p *node) { // ERROR "o does not escape" "leaking param: mid" "leaking param: p"
	mid.next = p
	o.next = mid
}

func storeTwoLevelsCaller() {
	var a, mid, b node // ERROR "moved to heap: mid" "moved to heap: b"
	storeTwoLevels(&a, &mid, &b)
	_, _, _ = a, mid, b
}

//go:noinline
func storeReassignedTarget(o, p *node) { // ERROR "o does not escape" "leaking param: p"
	g := o
	g.next = p
	_ = g
}

func storeReassignedTargetCaller() { // ERROR "can inline storeReassignedTargetCaller"
	var a, b node // ERROR "moved to heap: b"
	storeReassignedTarget(&a, &b)
	_, _ = a, b
}

//go:noinline
func storeMixedTargets(o, q, p *node) { // ERROR "o does not escape" "q does not escape" "leaking param: p"
	o.next = p
	q.next = p
}

func storeMixedTargetsCaller() {
	var a, q, b node // ERROR "moved to heap: b"
	storeMixedTargets(&a, &q, &b)
	_, _, _ = a, q, b
}

var msink *node

//go:noinline
func mesc(x *node) { msink = x } // ERROR "leaking param: x"

//go:noinline
func mwrap(depth int) {
	var a, b node // ERROR "moved to heap: a" "moved to heap: b"
	mesc(&a)
	mlink(&a, &b, depth)
	b.next = &node{} // ERROR "&node{} escapes to heap"
	if depth > 0 {
		mwrap(depth - 1)
	}
	_, _ = a, b
}

//go:noinline
func mlink(o, p *node, d int) { // ERROR "o does not escape" "p does not escape"
	o.next = p
	if d > 0 {
		mwrap(d - 1)
	}
}

func mutualCaller() { // ERROR "can inline mutualCaller"
	mwrap(2)
}

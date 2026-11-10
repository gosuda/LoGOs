// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import "context"

type TChain struct {
	V int
}

func (t TChain) B() TChain {
	return TChain{V: t.V*2 + 1}
}

func (t TChain) C() int {
	return t.V + 1
}

func Idioms() int {
	total := 0
	done := make(chan int, 1)
	group := func(ctx context.Context) {
		select {
		case <-ctx.Done():
			done <- -1
		default:
			done <- 7
		}
	}
	go group(context.Background())
	var order []int
	scope := func(x int) {
		order = append(order, x)
	}
	scope(1)
	func() {
		defer scope(2)
		scope(3)
	}()
	seq := func(yield func(int) bool) {
		for i := 0; i < 5; i++ {
			if !yield(i) {
				return
			}
		}
	}
	sum := 0
	for v := range seq {
		sum += v
		if sum >= 3 {
			break
		}
	}
	total += sum
	flag := 0
	errdefer := func() {
		flag = 11
	}
	errdefer()
	total += flag
	yield := 1
	yield++
	total += yield
	x := 1 +
		.5
	if x == 1.5 {
		total += 5
	}
	t := TChain{V: 3}.
		B()
	total += t.C()
	for _, v := range order {
		total += v
	}
	total += <-done
	return total
}

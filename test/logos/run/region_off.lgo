// run

// U0 region_off contract: with GOEXPERIMENT unset, every logos/arena call
// below prints the same values as the plain heap equivalent printed next to it.

package main

import (
	"fmt"
	"logos/arena"
)

func main() {
	r := arena.Open()

	p := arena.New[int](r)
	*p = 42
	hp := new(int)
	*hp = 42
	fmt.Println(*p, *hp)

	s := arena.Make[int](r, 3)
	s[0], s[1], s[2] = 1, 2, 3
	s = arena.Append(r, s, 4)
	s = arena.Append(r, s, 5, 6)
	hs := make([]int, 3)
	hs[0], hs[1], hs[2] = 1, 2, 3
	hs = append(hs, 4)
	hs = append(hs, 5, 6)
	fmt.Println(s, hs, len(s), cap(s), len(hs), cap(hs))

	m := arena.Make[int](r, 5)
	hm := make([]int, 5)
	fmt.Println(len(m), cap(m), len(hm), cap(hm), m, hm)

	g := arena.Append(r, []int(nil), 9)
	hg := append([]int(nil), 9)
	fmt.Println(g, hg, len(g), cap(g), len(hg), cap(hg))

	keep := arena.Append(r, s)
	fmt.Println(len(keep), cap(keep), &keep[0] == &s[0])
}

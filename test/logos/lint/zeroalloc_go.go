// errorcheck

package p

type point struct{ x, y int }

//logos:zeroalloc
func total(xs []int) int {
	n := 0
	for _, x := range xs {
		n += x
	}
	return n
}

//logos:zeroalloc
func pack(v int) []int {
	return []int{v} // ERROR "\[perf::zeroalloc_violation\] pack allocates: \[\]int\{\.\.\.\} escapes to heap: flows to result ~r0 \(return\)"
}

//logos:zeroalloc
func (p *point) clone() *point {
	return &point{p.x, p.y} // ERROR "\[perf::zeroalloc_violation\] \(\*point\)\.clone allocates: &point\{\.\.\.\} escapes to heap: flows to result ~r0 \(return\)"
}

//logos:zeroalloc
func constantPanic(i int, xs []int) int {
	if i < 0 {
		panic("negative index")
	}
	return xs[i]
}

func unmarked(v int) []int {
	return []int{v}
}

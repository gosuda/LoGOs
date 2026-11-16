// run -goexperiment simd

package main

//go:noinline
func startAdd(dst, src []uint32, start, end int) int {
	if start < 0 || end > len(dst) || end > len(src) {
		return start
	}
	i := start
	for ; i < end; i++ {
		dst[i] += src[i]
	}
	return i
}

//go:noinline
func startScalarAdd(a, b uint32) uint32 { return a + b }

func startReference(dst, src []uint32, start, end int) int {
	if start < 0 || end > len(dst) || end > len(src) {
		return start
	}
	i := start
	for ; i < end; i++ {
		dst[i] = startScalarAdd(dst[i], src[i])
	}
	return i
}

func main() {
	const length = 48
	for _, start := range []int{-1, 0, 1, 2, 3, 4, 5, 8, 13, 31, 47, 48, 49} {
		for _, end := range []int{-1, 0, 1, 3, 4, 5, 8, 9, 12, 13, 16, 17, 31, 47, 48, 49} {
			for _, distance := range []int{0, 1, 2, 3, 4, 5, 7, 8, 12, 16, length} {
				for _, reverse := range []bool{false, true} {
					dstOffset, srcOffset := 1, 1+distance
					if reverse {
						dstOffset, srcOffset = srcOffset, dstOffset
					}
					backing := make([]uint32, length+distance+2)
					for i := range backing {
						backing[i] = uint32(i+1)*0x9e3779b9 + 0x80000001
					}
					want := append([]uint32(nil), backing...)
					dst := backing[dstOffset : dstOffset+length]
					src := backing[srcOffset : srcOffset+length]
					wantDst := want[dstOffset : dstOffset+length]
					wantSrc := want[srcOffset : srcOffset+length]
					wantIndex := startReference(wantDst, wantSrc, start, end)
					if got := startAdd(dst, src, start, end); got != wantIndex {
						panic("nonzero-start final index changed")
					}
					for i, got := range backing {
						if got != want[i] {
							panic("nonzero-start memory effects changed")
						}
					}
				}
			}
		}
	}
}

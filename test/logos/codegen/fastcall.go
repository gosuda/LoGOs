// build

package main

import "unsafe"

//go:linkname fastcall runtime.logosCgocallFast
func fastcall(fn, arg unsafe.Pointer) int32

var sink int32

func main() {
	sink = fastcall(nil, nil)
}

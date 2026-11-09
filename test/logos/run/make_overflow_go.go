// run

package main

import "fmt"

func main() {
	w, h := 1<<32, 1<<32
	buf := make([]byte, w*h)
	fmt.Printf("len=%d\n", len(buf))
}

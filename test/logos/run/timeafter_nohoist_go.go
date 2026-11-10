// run

package main

import (
	"fmt"
	"time"
)

func main() {
	var chs []<-chan time.Time
	for range 3 {
		chs = append(chs, time.After(time.Hour))
	}
	if chs[0] == chs[1] || chs[1] == chs[2] {
		fmt.Println("shared")
		return
	}
	fmt.Println("distinct")
}

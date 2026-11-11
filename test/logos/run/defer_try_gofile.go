// run

package main

import (
	"fmt"

	rt "logos/rt"
)

var order []string

func f() error {
	order = append(order, "f")
	return nil
}

func guarded() (err error) {
	defer rt.DeferTry(&err, f())
	order = append(order, "after-defer-statement")
	return nil
}

func main() {
	if err := guarded(); err != nil {
		fmt.Println("FAIL unexpected error:", err)
		return
	}
	for _, s := range order {
		fmt.Println(s)
	}
}

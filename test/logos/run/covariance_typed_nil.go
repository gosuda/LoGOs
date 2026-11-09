// run

package main

import (
	"fmt"

	"logos/rt"
)

type Creature interface {
	pointerName() string
}

type Monster struct{ name string }

func (m *Monster) pointerName() string {
	if m == nil {
		return "<nil>"
	}
	return m.name
}

func main() {
	typed := rt.CovSlice[Creature]([]*Monster{nil, {name: "imp"}})
	fmt.Println(typed[0] == nil, typed[0].pointerName(), typed[1].pointerName())
	fmt.Println(len(typed))

	var none []*Monster
	fmt.Println(rt.CovSlice[Creature](none) == nil)
	empty := rt.CovSlice[Creature]([]*Monster{})
	fmt.Println(empty == nil, len(empty))

	ifaces := rt.CovSlice[Creature]([]Creature{nil, &Monster{name: "orc"}})
	fmt.Println(ifaces[0] == nil, ifaces[1].pointerName())
}

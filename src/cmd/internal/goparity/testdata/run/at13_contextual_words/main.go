// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"

	"example.com/parity/at13_contextual_words/pkg_arena"
	"example.com/parity/at13_contextual_words/pkg_comptime"
	"example.com/parity/at13_contextual_words/pkg_errdefer"
	"example.com/parity/at13_contextual_words/pkg_group"
	"example.com/parity/at13_contextual_words/pkg_property"
	"example.com/parity/at13_contextual_words/pkg_region"
	"example.com/parity/at13_contextual_words/pkg_scope"
	"example.com/parity/at13_contextual_words/pkg_yield"
)

func main() {
	bad := false
	emit := func(name string, got string, want string) {
		fmt.Printf("%s %s\n", name, got)
		if got != want {
			fmt.Printf("LAW %s got %s want %s\n", name, got, want)
			bad = true
		}
	}
	emit("yield", fmt.Sprint(yield.Call())+yield.Who()+AliasYield()+fmt.Sprint(DYield()), "41yieldgroup167")
	emit("group", fmt.Sprint(group.Call())+group.Who()+AliasGroup()+fmt.Sprint(DGroup()), "42groupscope169")
	emit("scope", fmt.Sprint(scope.Call())+scope.Who()+AliasScope()+fmt.Sprint(DScope()), "43scopeerrdefer171")
	emit("errdefer", fmt.Sprint(errdefer.Call())+errdefer.Who()+AliasErrdefer()+fmt.Sprint(DErrdefer()), "44errdeferproperty173")
	emit("property", fmt.Sprint(property.Call())+property.Who()+AliasProperty()+fmt.Sprint(DProperty()), "45propertycomptime175")
	emit("comptime", fmt.Sprint(comptime.Call())+comptime.Who()+AliasComptime()+fmt.Sprint(DComptime()), "46comptimearena177")
	emit("arena", fmt.Sprint(arena.Call())+arena.Who()+AliasArena()+fmt.Sprint(DArena()), "47arenaregion179")
	emit("region", fmt.Sprint(region.Call())+region.Who()+AliasRegion()+fmt.Sprint(DRegion()), "48regionyield181")
	emit("idioms", fmt.Sprint(Idioms()), "42")
	if bad {
		os.Exit(1)
	}
}

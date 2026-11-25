// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import "cmd/compile/internal/syntax"

type LogosPlatformRet struct {
	Value, Slot Type
}

type resultSlot struct {
	stmt  *syntax.ReturnStmt
	index int
}

func (check *Checker) logosPlatformReturn(x *operand, T Type, context string, result resultSlot) {
	if x == nil || !check.isLogosFile(x) || context != "return statement" || check.Info == nil {
		return
	}
	if T == nil || !isNonTypeParamInterface(T) || !x.isValid() || x.expr == nil || x.isNil() {
		return
	}
	if !check.logosIsPlatform(x) {
		return
	}
	if check.Info.LogosPlatformRets == nil {
		check.Info.LogosPlatformRets = make(map[syntax.Pos][]LogosPlatformRet)
	}
	pos := syntax.StartPos(x.expr)
	fact := LogosPlatformRet{Value: x.typ(), Slot: T}
	check.Info.LogosPlatformRets[pos] = append(check.Info.LogosPlatformRets[pos], fact)
	if result.stmt == nil {
		return
	}
	if check.Info.LogosPlatformRetSlots == nil {
		check.Info.LogosPlatformRetSlots = make(map[*syntax.ReturnStmt]map[int]LogosPlatformRet)
	}
	slots := check.Info.LogosPlatformRetSlots[result.stmt]
	if slots == nil {
		slots = make(map[int]LogosPlatformRet)
		check.Info.LogosPlatformRetSlots[result.stmt] = slots
	}
	slots[result.index] = fact
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package noder

import (
	"internal/types/errors"
	"slices"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// logosCtInterfaceCycles reports the anonymous interface cycles (#56103) of
// the checked comptime instances. The package check leaves the body of a
// comptime generic to each instance, so the file walk in checkFiles sees
// only untyped syntax there.
func logosCtInterfaceCycles(m posMap, info *types2.Info) {
	insts := make([]*types2.LogosCtInst, 0, len(info.LogosCtPrepared))
	for _, inst := range info.LogosCtPrepared {
		insts = append(insts, inst)
	}
	slices.SortFunc(insts, func(a, b *types2.LogosCtInst) int { return a.Decl.Pos().Cmp(b.Decl.Pos()) })
	var f cycleFinder
	visit := func(n syntax.Node) bool {
		it, ok := n.(*syntax.InterfaceType)
		if !ok {
			return true
		}
		typ, _ := types2.Unalias(it.GetTypeInfo().Type).(*types2.Interface)
		if typ != nil && f.hasCycle(typ) {
			base.ErrorfAt(m.makeXPos(it.Pos()), errors.InvalidTypeCycle, "invalid recursive type: anonymous interface refers to itself (see https://go.dev/issue/56103)")
			for cyclic := range f.cyclic {
				f.cyclic[cyclic] = false
			}
		}
		return false
	}
	for _, inst := range insts {
		syntax.Inspect(inst.Decl, visit)
		for _, helper := range inst.Helpers {
			syntax.Inspect(helper, visit)
		}
	}
	base.ExitIfErrors()
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import "cmd/compile/internal/syntax"

func (check *Checker) logosGoInterfaceOptionMatch(f, m *Func, pos syntax.Pos) bool {
	if check == nil || !check.conf.Logos || (pos.FileBase() != nil && logosGoModePos(pos)) {
		return false
	}

	provider, _ := f.typ.(*Signature)
	requirement, _ := m.typ.(*Signature)
	if provider == nil || requirement == nil || provider.variadic != requirement.variadic ||
		provider.params.Len() != requirement.params.Len() ||
		provider.TypeParams().Len() != 0 || requirement.TypeParams().Len() != 0 {
		return false
	}

	widened := false
	for i := range provider.params.Len() {
		have, want := provider.params.At(i).typ, requirement.params.At(i).typ
		if Identical(have, want) {
			continue
		}
		option, ok := Unalias(have).(*Option)
		if !ok || !Identical(option.Elem, want) {
			return false
		}
		if _, ok := want.Underlying().(*Pointer); !ok {
			return false
		}
		widened = true
	}
	if !widened || !Identical(provider.results, requirement.results) {
		return false
	}

	origin := m.Origin()
	return origin.pkg != nil && origin.pos.FileBase() != nil &&
		check.logosFuncInLgo(f) && !check.logosFuncInLgo(origin)
}

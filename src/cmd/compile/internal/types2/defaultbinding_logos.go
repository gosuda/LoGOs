// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

// LogosDefaultInstance substitutes a checked declaration's type arguments in
// its default's type or declaration RHS. An uninstantiated declaration keeps
// its own parameters. The input types are not modified.
func LogosDefaultInstance(t Type, params *TypeParamList, args *TypeList) Type {
	if args.Len() == 0 {
		return t
	}
	return (*Checker)(nil).subst(nopos, t, makeSubstMap(params.list(), args.list()), nil, NewContext())
}

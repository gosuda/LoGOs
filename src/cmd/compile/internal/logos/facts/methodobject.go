// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package facts

import "cmd/compile/internal/types2"

// MethodObject spells a method record object: T.M or (*T).M.
func MethodObject(typeName string, recv *types2.Var, method string) string {
	if recv == nil {
		return typeName + "." + method
	}
	if _, ok := types2.Unalias(recv.Type()).(*types2.Pointer); ok {
		return "(*" + typeName + ")." + method
	}
	return typeName + "." + method
}

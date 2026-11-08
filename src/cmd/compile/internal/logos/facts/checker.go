// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package facts

import "cmd/compile/internal/types2"

func init() {
	types2.LogosImportedConstFuncSource = constFuncSource
}

func constFuncSource(path, object string) []byte {
	f, ok := Lookup(path)
	if !ok || f == nil {
		return nil
	}
	for _, c := range f.ConstFuncs {
		if c.Object == object {
			return c.Source
		}
	}
	return nil
}

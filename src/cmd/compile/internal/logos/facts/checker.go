// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package facts

import "cmd/compile/internal/types2"

func init() {
	types2.LogosImportedConstFuncSource = constFuncSource
	types2.LogosImportedParamProvenZero = paramProvenZero
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

func paramProvenZero(path, object string, param int) bool {
	f, ok := Lookup(path)
	if !ok || f == nil || !f.GoPackage {
		return false
	}
	for i := range f.Fns {
		if f.Fns[i].Object != object {
			continue
		}
		mask := f.Fns[i].ParamMutated
		if mask == "" {
			return false
		}
		bits, ok := parseHexMask(mask)
		if !ok {
			return false
		}
		return !maskBit(bits, param)
	}
	return false
}

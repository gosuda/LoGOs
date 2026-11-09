// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package facts

import "cmd/compile/internal/types2"

// M6-S3 struct field default facts (SPEC T-104.5, T-Sec5): one default
// record per defaulted field of each exported struct type, carrying the
// default expression source text for .lgo importers. The writer calls it
// from Write beside writeOpts.

// writeDefaults adds the struct field defaults of pkg to f. Only
// exported non-alias named types are recorded: importers cannot name
// anything else. Same-package lowering reads check info directly; facts
// serve other packages.
func writeDefaults(pkg *types2.Package, info *types2.Info, f *Facts) {
	if pkg == nil || pkg.Scope() == nil || info == nil || len(info.LogosFieldDefaults) == 0 {
		return
	}
	add := func(d DefaultFact) {
		f.Defaults = appendUnique(f.Defaults, d)
	}
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types2.TypeName)
		if !ok || obj == nil || !obj.Exported() || obj.IsAlias() {
			continue
		}
		named, ok := obj.Type().(*types2.Named)
		if !ok {
			continue
		}
		st, ok := named.Underlying().(*types2.Struct)
		if !ok {
			continue
		}
		for _, d := range info.LogosFieldDefaults[st] {
			add(DefaultFact{Type: name, Field: d.Field, Text: renderExpr(d.Default)})
		}
	}
}

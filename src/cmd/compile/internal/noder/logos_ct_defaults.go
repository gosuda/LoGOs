// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package noder

import (
	"internal/pkgbits"

	"cmd/compile/internal/base"
	"cmd/compile/internal/logos/facts"
	"cmd/compile/internal/types2"
)

// Capture only declaration-owned defaults. The same struct can be reached
// through a different named type, whose ObjType carries its own typed RHS.
func logosCtCaptureNamedDefaults(pkg *types2.Package, info *types2.Info) types2.LogosCtNamedDefaults {
	if len(info.LogosFieldDefaults) == 0 {
		return nil
	}
	var plan map[string]string
	scope := pkg.Scope()
	var table types2.LogosCtNamedDefaults
	for st, defs := range info.LogosFieldDefaults {
		if len(defs) == 0 || defs[0].Owner == "" {
			continue
		}
		name := defs[0].Owner
		obj, ok := scope.Lookup(name).(*types2.TypeName)
		base.Assertf(ok && !obj.IsAlias(), "missing comptime default type %s.%s", pkg.Path(), name)
		named := obj.Type().(*types2.Named)
		underlying, isStruct := named.Underlying().(*types2.Struct)
		if !isStruct {
			// An option can pass its declaration owner to a nested struct.
			continue
		}
		base.Assertf(underlying == st, "comptime default struct does not match its declaration %v", obj)
		if table == nil {
			plan = facts.FactoryNamePlan(info, pkg)
			table = make(types2.LogosCtNamedDefaults)
		}
		fields := make([]types2.LogosCtNamedDefault, 0, len(defs))
		for _, d := range defs {
			base.Assertf(d.Owner == name, "multiple origins for defaults of %v", obj)
			symbol := plan[name+"."+d.Field]
			base.Assertf(symbol != "", "missing factory plan for %v.%s", obj, d.Field)
			fields = append(fields, types2.LogosCtNamedDefault{Field: d.Field, FactoryName: symbol})
		}
		table[name] = types2.LogosCtNamedTypeDefaults{Fields: fields}
	}
	return table
}

func (w *writer) logosCtWriteNamedDefaults(fields []types2.LogosCtNamedDefault) {
	w.Len(len(fields))
	scope := w.p.curpkg.Scope()
	for _, d := range fields {
		factory, ok := scope.Lookup(d.FactoryName).(*types2.Func)
		base.Assertf(ok && factory.Pkg() == w.p.curpkg, "missing lowered comptime default factory %s.%s", w.p.curpkg.Path(), d.FactoryName)
		w.String(d.Field)
		w.Reloc(pkgbits.SectionObj, w.p.objIdx(factory))
	}
}

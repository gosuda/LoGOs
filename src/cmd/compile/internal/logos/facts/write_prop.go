// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package facts

import "cmd/compile/internal/types2"

// This file carries the M4-S2 property facts (SPEC Sec 2.3, T-203.2,
// Sec 5, T-Sec5). For each exported property interface it emits one
// prop record naming the interface and the accessor prefix; the property
// set itself travels in the lowered Logos_GetF/Logos_SetF methods of the
// export data, which .lgo importers read back through the same accessor
// shapes the checker uses. For each exported struct with a `: I`
// declaration it emits one conforms record per asserted interface, named
// as the interface's package path and type name, so an importer in
// another package can match its own spelling of the same interface and
// type-check the conformance without seeing the declaration.

// propAccessorPrefix is the accessor prefix every generated method
// carries (types2 logosGetterName/logosSetterName).
const propAccessorPrefix = "Logos_"

// writeProps adds the prop and conforms records of pkg to f.
// Only exported names are recorded: importers cannot name anything else.
func writeProps(pkg *types2.Package, info *types2.Info, f *Facts) {
	if pkg == nil || pkg.Scope() == nil || info == nil {
		return
	}
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types2.TypeName)
		if !ok || obj == nil || !obj.Exported() || obj.IsAlias() {
			continue
		}
		if _, ok := types2.LogosPropsOf(obj.Type()); ok {
			f.Props = append(f.Props, PropFact{Interface: name, Prefix: propAccessorPrefix})
		}
	}
	for _, decl := range info.LogosPropDecls {
		if decl == nil || decl.Def == nil || !decl.Def.Exported() {
			continue
		}
		for _, iface := range decl.Ifaces {
			path, iname, ok := propIfaceKey(iface)
			if !ok {
				continue
			}
			ifaceObj := ifaceScopeLookup(pkg, path, iname)
			if ifaceObj == nil || !ifaceObj.Exported() {
				continue
			}
			f.Conforms = append(f.Conforms, ConformsFact{
				Type:      decl.Def.Name(),
				IfacePath: path,
				Iface:     iname,
			})
		}
	}
}

// propIfaceKey names iface as its origin package path and type name,
// following aliases to the named interface type. It reports false for
// types with no such name.
func propIfaceKey(iface types2.Type) (string, string, bool) {
	named, ok := types2.Unalias(iface).(*types2.Named)
	if !ok {
		return "", "", false
	}
	obj := named.Origin().Obj()
	if obj == nil || obj.Pkg() == nil || obj.Name() == "" {
		return "", "", false
	}
	return obj.Pkg().Path(), obj.Name(), true
}

// ifaceScopeLookup reports the type name iname of the package at path as
// seen from pkg: the package's own scope for a self reference, or the
// scope of the directly imported package otherwise. It returns nil when
// the name does not resolve, in which case the conformance is unusable
// to importers and the caller skips it.
func ifaceScopeLookup(pkg *types2.Package, path, iname string) *types2.TypeName {
	var scope *types2.Scope
	if path == pkg.Path() {
		scope = pkg.Scope()
	} else {
		for _, imp := range pkg.Imports() {
			if imp.Path() == path {
				scope = imp.Scope()
				break
			}
		}
	}
	if scope == nil {
		return nil
	}
	obj, ok := scope.Lookup(iname).(*types2.TypeName)
	if !ok {
		return nil
	}
	return obj
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package facts

import (
	"bytes"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// DefaultBinding describes one producer-owned helper. The expressions keep
// their source bindings; TypeArgs substitutes the original struct's type
// parameters when an alias or a defined type specializes that struct.
type DefaultBinding struct {
	Type     *types2.TypeName
	Fields   []types2.LogosFieldDefault
	TypeArgs map[*types2.TypeParam]types2.Type
}

func DefaultHelperName(name string) string { return "Logos_Default_" + name }

// DefaultBindings is shared by the facts writer and the helper generator so
// every advertised helper has exactly the same checked declaration origin.
func DefaultBindings(pkg *types2.Package, info *types2.Info, files []*syntax.File) []DefaultBinding {
	if pkg == nil || info == nil || len(info.LogosFieldDefaults) == 0 {
		return nil
	}
	decls := make(map[*types2.TypeName]syntax.Expr)
	for _, file := range files {
		for _, decl := range file.DeclList {
			if td, ok := decl.(*syntax.TypeDecl); ok {
				if obj, ok := info.Defs[td.Name].(*types2.TypeName); ok {
					decls[obj] = td.Type
				}
			}
		}
	}
	var out []DefaultBinding
	for _, name := range pkg.Scope().Names() {
		obj, ok := pkg.Scope().Lookup(name).(*types2.TypeName)
		if !ok {
			continue
		}
		fields, args := defaultOrigin(obj.Type(), info, decls, make(map[*types2.TypeName]bool))
		if len(fields) > 0 {
			out = append(out, DefaultBinding{Type: obj, Fields: fields, TypeArgs: args})
		}
	}
	return out
}

func defaultOrigin(t types2.Type, info *types2.Info, decls map[*types2.TypeName]syntax.Expr, seen map[*types2.TypeName]bool) ([]types2.LogosFieldDefault, map[*types2.TypeParam]types2.Type) {
	switch t := t.(type) {
	case *types2.Alias:
		return defaultOrigin(t.Rhs(), info, decls, seen)
	case *types2.Named:
		st, _ := t.Origin().Underlying().(*types2.Struct)
		if defs := info.LogosFieldDefaults[st]; len(defs) > 0 {
			args := make(map[*types2.TypeParam]types2.Type)
			for i := range t.TypeArgs().Len() {
				args[t.TypeParams().At(i)] = t.TypeArgs().At(i)
			}
			return defs, args
		}
		obj := t.Obj()
		if !seen[obj] {
			seen[obj] = true
			if rhs := decls[obj]; rhs != nil {
				instance := types2.LogosDefaultInstance(info.TypeOf(rhs), t.TypeParams(), t.TypeArgs())
				return defaultOrigin(instance, info, decls, seen)
			}
		}
	case *types2.Struct:
		return info.LogosFieldDefaults[t], nil
	}
	return nil, nil
}

func writeDefaults(pkg *types2.Package, info *types2.Info, files []*syntax.File, f *Facts) {
	for _, binding := range DefaultBindings(pkg, info, files) {
		name := binding.Type.Name()
		for _, d := range binding.Fields {
			if d.Field == "_" {
				continue
			}
			var buf bytes.Buffer
			_, _ = syntax.Fprint(&buf, d.Default, syntax.LineForm)
			f.Defaults = appendUnique(f.Defaults, DefaultFact{Type: name, Field: d.Field, Text: buf.String()})
		}
		f.DefaultHelpers = appendUnique(f.DefaultHelpers, DefaultHelperFact{Type: name, Helper: DefaultHelperName(name)})
	}
}

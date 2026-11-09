// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package facts

import (
	"fmt"
	"slices"

	"cmd/compile/internal/types2"
)

// writeOpts records one opt record per option position in the exported
// declarations of pkg into f. Objects are func names, type names, and
// T.M or (*T).M methods; paths are R<i>, P<i>, F<name>, E, K, D, M<name>
// steps. A nested option extends its parent with E, so (T?)? yields R0
// and R0.E.
func writeOpts(pkg *types2.Package, f *Facts) {
	if pkg == nil || pkg.Scope() == nil {
		return
	}
	emit := func(object, path string) {
		f.Opts = append(f.Opts, PathFact{Object: object, Path: path})
	}
	scope := pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if obj == nil || !obj.Exported() {
			continue
		}
		switch obj := obj.(type) {
		case *types2.Func:
			sig, ok := obj.Type().(*types2.Signature)
			if !ok || sig.Recv() != nil {
				continue
			}
			if tp := sig.TypeParams(); tp != nil && tp.Len() > 0 {
				continue
			}
			emitFuncOpts(name, sig, emit)
		case *types2.TypeName:
			if !obj.IsAlias() {
				emitTypeOpts(name, obj.Type(), emit)
			}
		}
	}
	for _, name := range scope.Names() {
		obj, ok := scope.Lookup(name).(*types2.TypeName)
		if !ok || obj == nil || !obj.Exported() || obj.IsAlias() {
			continue
		}
		named, ok := obj.Type().(*types2.Named)
		if !ok {
			continue
		}
		for i := range named.NumMethods() {
			m := named.Method(i)
			if m == nil || !m.Exported() {
				continue
			}
			sig, ok := m.Type().(*types2.Signature)
			if !ok {
				continue
			}
			if tp := sig.TypeParams(); tp != nil && tp.Len() > 0 {
				continue
			}
			emitFuncOpts(MethodObject(name, sig.Recv(), m.Name()), sig, emit)
		}
	}
}

func emitFuncOpts(object string, sig *types2.Signature, emit func(string, string)) {
	if params := sig.Params(); params != nil {
		for i := range params.Len() {
			v := params.At(i)
			if v == nil {
				continue
			}
			walkOptType(v.Type(), []string{fmt.Sprintf("P%d", i)}, object, emit, make(map[types2.Type]bool))
		}
	}
	if results := sig.Results(); results != nil {
		for i := range results.Len() {
			v := results.At(i)
			if v == nil {
				continue
			}
			walkOptType(v.Type(), []string{fmt.Sprintf("R%d", i)}, object, emit, make(map[types2.Type]bool))
		}
	}
}

func emitTypeOpts(name string, t types2.Type, emit func(string, string)) {
	u, ok := types2.Unalias(t).(*types2.Named)
	if !ok {
		return
	}
	if tp := u.TypeParams(); tp != nil && tp.Len() > 0 {
		return
	}
	switch u := u.Underlying().(type) {
	case *types2.Struct:
		for i := range u.NumFields() {
			f := u.Field(i)
			if f == nil || !f.Exported() {
				continue
			}
			walkOptType(f.Type(), []string{"F" + f.Name()}, name, emit, make(map[types2.Type]bool))
		}
	case *types2.Interface:
		for i := range u.NumExplicitMethods() {
			m := u.ExplicitMethod(i)
			if m == nil || !m.Exported() {
				continue
			}
			sig, ok := m.Type().(*types2.Signature)
			if !ok {
				continue
			}
			base := []string{"M" + m.Name()}
			if params := sig.Params(); params != nil {
				for j := range params.Len() {
					v := params.At(j)
					if v == nil {
						continue
					}
					walkOptType(v.Type(), append(slices.Clone(base), fmt.Sprintf("P%d", j)), name, emit, make(map[types2.Type]bool))
				}
			}
			if results := sig.Results(); results != nil {
				for j := range results.Len() {
					v := results.At(j)
					if v == nil {
						continue
					}
					walkOptType(v.Type(), append(slices.Clone(base), fmt.Sprintf("R%d", j)), name, emit, make(map[types2.Type]bool))
				}
			}
		}
	}
}

func walkOptType(t types2.Type, path []string, object string, emit func(string, string), seen map[types2.Type]bool) {
	walkQualType(t, path, object, emit, nil, seen)
}

// walkQualType is the one traversal owner for qualifier facts: it visits
// every qualifier position once, invoking emitOpt at option positions
// and emitConst at const-view positions. A nil emitter still descends
// through that qualifier, so an option nested in a const view (and the
// reverse) records its own fact instead of vanishing at the foreign
// layer. Cycle protection stays on named types only, with path-scoped
// release so diamonds revisit.
func walkQualType(t types2.Type, path []string, object string, emitOpt, emitConst func(string, string), seen map[types2.Type]bool) {
	if t == nil {
		return
	}
	if opt := optionOf(t); opt != nil {
		if emitOpt != nil {
			emitOpt(object, joinPath(path))
		}
		walkQualType(opt.Elem, append(slices.Clone(path), "E"), object, emitOpt, emitConst, seen)
		return
	}
	if c := constView(t); c != nil {
		if emitConst != nil {
			emitConst(object, joinPath(path))
		}
		walkQualType(c.Elem, append(slices.Clone(path), "E"), object, emitOpt, emitConst, seen)
		return
	}
	u := types2.Unalias(t)
	switch u := u.(type) {
	case *types2.Slice:
		walkQualType(u.Elem(), append(slices.Clone(path), "E"), object, emitOpt, emitConst, seen)
	case *types2.Array:
		walkQualType(u.Elem(), append(slices.Clone(path), "E"), object, emitOpt, emitConst, seen)
	case *types2.Map:
		walkQualType(u.Key(), append(slices.Clone(path), "K"), object, emitOpt, emitConst, seen)
		walkQualType(u.Elem(), append(slices.Clone(path), "E"), object, emitOpt, emitConst, seen)
	case *types2.Pointer:
		walkQualType(u.Elem(), append(slices.Clone(path), "D"), object, emitOpt, emitConst, seen)
	case *types2.Chan:
		walkQualType(u.Elem(), append(slices.Clone(path), "D"), object, emitOpt, emitConst, seen)
	case *types2.Signature:
		if params := u.Params(); params != nil {
			for i := range params.Len() {
				v := params.At(i)
				if v == nil {
					continue
				}
				walkQualType(v.Type(), append(slices.Clone(path), fmt.Sprintf("P%d", i)), object, emitOpt, emitConst, seen)
			}
		}
		if results := u.Results(); results != nil {
			for i := range results.Len() {
				v := results.At(i)
				if v == nil {
					continue
				}
				walkQualType(v.Type(), append(slices.Clone(path), fmt.Sprintf("R%d", i)), object, emitOpt, emitConst, seen)
			}
		}
	case *types2.Struct:
		for i := range u.NumFields() {
			f := u.Field(i)
			if f == nil || !f.Exported() {
				continue
			}
			walkQualType(f.Type(), append(slices.Clone(path), "F"+f.Name()), object, emitOpt, emitConst, seen)
		}
	case *types2.Interface:
		for i := range u.NumExplicitMethods() {
			m := u.ExplicitMethod(i)
			if m == nil || !m.Exported() {
				continue
			}
			sig, ok := m.Type().(*types2.Signature)
			if !ok {
				continue
			}
			base := append(slices.Clone(path), "M"+m.Name())
			if params := sig.Params(); params != nil {
				for j := range params.Len() {
					v := params.At(j)
					if v == nil {
						continue
					}
					walkQualType(v.Type(), append(slices.Clone(base), fmt.Sprintf("P%d", j)), object, emitOpt, emitConst, seen)
				}
			}
			if results := sig.Results(); results != nil {
				for j := range results.Len() {
					v := results.At(j)
					if v == nil {
						continue
					}
					walkQualType(v.Type(), append(slices.Clone(base), fmt.Sprintf("R%d", j)), object, emitOpt, emitConst, seen)
				}
			}
		}
	case *types2.Named:
		if seen[u] {
			return
		}
		seen[u] = true
		walkQualType(u.Underlying(), path, object, emitOpt, emitConst, seen)
		delete(seen, u)
	}
}

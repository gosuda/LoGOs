// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"cmd/compile/internal/logos/facts"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// Cloning an imported package for the overlay must retarget every generic
// instance to the shells: after the fill, no shell underlying may keep an
// instance built on an original once that original has a shell. A shell
// filled before the shells it references used to keep the original
// instances, and the kept reference depended on map iteration order, so an
// imported closed union intermittently carried original terms and failed
// the member check with "[correctness::union_nonmember]
// exp.Left[string] is not a member of exp.Pair[string]".
//
// Left and Pair form a use cycle (Left holds a Pair[T]? field, Pair admits
// a Left[T] term), so whichever shell fills first deterministically keeps
// a stale instance on the unfixed code: exactly one of the two shells is
// stale in every fill order. The test asserts over all shells, so its
// verdict does not depend on the order.
func TestUnionOverlayFillRetargetsShells(t *testing.T) {
	const src = `package exp

type Left[T any] struct {
	V T
	P Pair[T]?
}

type Right[T any] struct{ V T }

func (l Left[T]) Name() string { return "left" }

func (r Right[T]) Name() string { return "right" }

type Pair[T any] interface {
	Left[T] | Right[T]
	Name() string
}

func PickPair[T any](i int, v T) Pair[T]? {
	switch i {
	case 0:
		return Pair[T](Left[T]{V: v})
	case 1:
		return Pair[T](Right[T]{V: v})
	}
	return nil
}

func DescribePair[T any](p Pair[T]?) string {
	if p != nil {
		return p.Name()
	}
	return "absent"
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "exp.lgo")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	file, err := syntax.ParseFile(path, func(err error) { t.Fatalf("parse: %v", err) }, nil, syntax.LogosMode)
	if err != nil {
		t.Fatal(err)
	}
	info := &types2.Info{
		Types:      make(map[syntax.Expr]types2.TypeAndValue),
		Defs:       make(map[*syntax.Name]types2.Object),
		Uses:       make(map[*syntax.Name]types2.Object),
		Selections: make(map[*syntax.SelectorExpr]*types2.Selection),
		Instances:  make(map[*syntax.Name]types2.Instance),
	}
	var checkErrs []error
	conf := types2.Config{
		Importer: newTestImporter(t),
		Error:    func(err error) { checkErrs = append(checkErrs, err) },
		Logos:    true,
	}
	pkg, _ := conf.Check("example.com/unionfill/exp", []*syntax.File{file}, info)
	if err := errors.Join(checkErrs...); err != nil {
		t.Fatalf("check: %v", err)
	}
	f := facts.Write(pkg, info, []*syntax.File{file}, nil)
	cloned, err := CloneWithOverlaysAndReadonly(pkg, f.Opts, f.Consts, f.Fns, f.Readonlys, f.ConstMaps)
	if err != nil {
		t.Fatal(err)
	}
	shells := make(map[string]*types2.Named)
	for _, name := range []string{"Left", "Right", "Pair"} {
		cobj := cloned.Scope().Lookup(name)
		oobj := pkg.Scope().Lookup(name)
		if cobj == nil || oobj == nil {
			t.Fatalf("lost declaration %q", name)
		}
		if cobj == oobj {
			continue
		}
		shell, ok := cobj.Type().(*types2.Named)
		if !ok {
			t.Fatalf("clone %q is %T, want *types2.Named", name, cobj.Type())
		}
		shells[name] = shell
	}
	// Left and Pair form the use cycle that forces the order-dependent
	// fallback on the unfixed code; both must be shelled for the test
	// to exercise it. Right may stay shared.
	for _, name := range []string{"Left", "Pair"} {
		if shells[name] == nil {
			t.Fatalf("fixture does not shell %q", name)
		}
	}
	var stale []string
	seen := make(map[types2.Type]bool)
	for name, shell := range shells {
		u := shell.Underlying()
		if u == nil {
			t.Fatalf("shell %q has no underlying", name)
		}
		collectInstances(name, u, seen, cloned, &stale)
		if tp := shell.TypeParams(); tp != nil {
			for i := range tp.Len() {
				collectInstances(name, tp.At(i).Constraint(), seen, cloned, &stale)
			}
		}
	}
	if len(stale) != 0 {
		sort.Strings(stale)
		t.Fatalf("shells keep pre-fill instances:\n%s", joinLines(stale))
	}
}

func collectInstances(shell string, typ types2.Type, seen map[types2.Type]bool, cloned *types2.Package, stale *[]string) {
	if typ == nil || seen[typ] {
		return
	}
	seen[typ] = true
	if named, ok := typ.(*types2.Named); ok {
		if args := named.TypeArgs(); args != nil && args.Len() != 0 {
			origin := named.Origin()
			if obj, ok := cloned.Scope().Lookup(origin.Obj().Name()).(*types2.TypeName); ok && obj != origin.Obj() {
				*stale = append(*stale, shell+" keeps "+origin.Obj().Name())
			}
			for i := range args.Len() {
				collectInstances(shell, args.At(i), seen, cloned, stale)
			}
			return
		}
	}
	switch typ := typ.(type) {
	case *types2.Pointer:
		collectInstances(shell, typ.Elem(), seen, cloned, stale)
	case *types2.Slice:
		collectInstances(shell, typ.Elem(), seen, cloned, stale)
	case *types2.Array:
		collectInstances(shell, typ.Elem(), seen, cloned, stale)
	case *types2.Map:
		collectInstances(shell, typ.Key(), seen, cloned, stale)
		collectInstances(shell, typ.Elem(), seen, cloned, stale)
	case *types2.Chan:
		collectInstances(shell, typ.Elem(), seen, cloned, stale)
	case *types2.Struct:
		for i := range typ.NumFields() {
			collectInstances(shell, typ.Field(i).Type(), seen, cloned, stale)
		}
	case *types2.Tuple:
		for i := range typ.Len() {
			collectInstances(shell, typ.At(i).Type(), seen, cloned, stale)
		}
	case *types2.Signature:
		if recv := typ.Recv(); recv != nil {
			collectInstances(shell, recv.Type(), seen, cloned, stale)
		}
		collectInstances(shell, typ.Params(), seen, cloned, stale)
		collectInstances(shell, typ.Results(), seen, cloned, stale)
	case *types2.Interface:
		for i := range typ.NumExplicitMethods() {
			collectInstances(shell, typ.ExplicitMethod(i).Type(), seen, cloned, stale)
		}
		for i := range typ.NumEmbeddeds() {
			collectInstances(shell, typ.EmbeddedType(i), seen, cloned, stale)
		}
	case *types2.Union:
		for i := range typ.Len() {
			collectInstances(shell, typ.Term(i).Type(), seen, cloned, stale)
		}
	case *types2.Option:
		collectInstances(shell, typ.Elem, seen, cloned, stale)
	case *types2.ConstView:
		collectInstances(shell, typ.Elem, seen, cloned, stale)
	case *types2.Alias:
		collectInstances(shell, typ.Rhs(), seen, cloned, stale)
		if args := typ.TypeArgs(); args.Len() != 0 {
			for i := range args.Len() {
				collectInstances(shell, args.At(i), seen, cloned, stale)
			}
		}
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, line := range lines {
		out += line + "\n"
	}
	return out
}

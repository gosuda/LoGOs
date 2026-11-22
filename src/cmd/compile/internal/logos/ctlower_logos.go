// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"maps"
	"slices"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

const ctPackageQualifier = "_logos_ct_package"

// ctLowerContext belongs to one detached comptime declaration. Only generated
// names use its bindings; the owner file and ordinary lexical lookup stay intact.
type ctLowerContext struct {
	owner       *types2.Package
	consumer    *types2.Package
	importer    types2.Importer
	context     *types2.Context
	localTypes  map[*types2.TypeName]*types2.TypeName
	sites       *ctTraceSites
	packages    map[string]*types2.Package
	abi         map[types2.Type]types2.Type
	packageUses map[string][]*syntax.Name
	types       map[syntax.Expr]types2.Type
	uses        map[*syntax.Name]types2.Object
	owned       map[syntax.Expr]types2.LogosOwnedType
	err         error

	instanceName    string
	constMaps       map[types2.Object]*constMapGen
	constMapUses    map[*syntax.Name]ctConstMapUse
	byteConversions map[*syntax.CallExpr]types2.BasicKind
	makeLenProducts map[*syntax.Operation]bool
}

func (ct *ctLowerContext) retain(root syntax.Node, helpers []syntax.Decl) {
	if len(ct.types)+len(ct.uses)+len(ct.packageUses)+len(ct.owned)+len(ct.constMapUses) == 0 {
		return
	}
	live := make(map[*syntax.Name]bool)
	var ownedLive map[syntax.Expr]bool
	visit := func(n syntax.Node) bool {
		if id, ok := n.(*syntax.Name); ok {
			_, mapUse := ct.constMapUses[id]
			if mapUse || ct.types[id] != nil || ct.uses[id] != nil || id.Value == ctPackageQualifier {
				live[id] = true
			}
		}
		if e, ok := n.(syntax.Expr); ok {
			if _, owned := ct.owned[e]; owned {
				if ownedLive == nil {
					ownedLive = make(map[syntax.Expr]bool)
				}
				ownedLive[e] = true
			}
		}
		return true
	}
	syntax.Inspect(root, visit)
	for _, decl := range helpers {
		syntax.Inspect(decl, visit)
	}
	maps.DeleteFunc(ct.types, func(n syntax.Expr, _ types2.Type) bool {
		id, ok := n.(*syntax.Name)
		return !ok || !live[id]
	})
	maps.DeleteFunc(ct.uses, func(n *syntax.Name, _ types2.Object) bool { return !live[n] })
	maps.DeleteFunc(ct.constMapUses, func(n *syntax.Name, _ ctConstMapUse) bool { return !live[n] })
	maps.DeleteFunc(ct.owned, func(n syntax.Expr, _ types2.LogosOwnedType) bool { return !ownedLive[n] })
	for path, ids := range ct.packageUses {
		ids = slices.DeleteFunc(ids, func(n *syntax.Name) bool { return !live[n] })
		if len(ids) == 0 {
			delete(ct.packageUses, path)
		} else {
			ct.packageUses[path] = ids
		}
	}
}

func (ct *ctLowerContext) configure(conf *types2.Config) error {
	if ct.err != nil {
		return ct.err
	}
	if ct.context == nil {
		ct.context = conf.Context
	}
	if ct.context == nil {
		ct.context = types2.NewContext()
	}
	for _, path := range slices.Sorted(maps.Keys(ct.packageUses)) {
		ids := ct.packageUses[path]
		pkg, err := ct.importPackage(path)
		if err != nil {
			return fmt.Errorf("%s: import %q for comptime lowering: %w", ids[0].Pos(), path, err)
		}
		pname := types2.NewPkgName(syntax.Pos{}, ct.owner, pkg.Name(), pkg)
		for _, id := range ids {
			id.Value = pname.Name()
			ct.bindUse(id, pname)
		}
	}
	conf.LogosCtTypes = nil
	if len(ct.types) != 0 {
		conf.LogosCtTypes = make(map[syntax.Expr]types2.Type, len(ct.types))
		for expr, semantic := range ct.types {
			typ, err := ct.abiType(semantic)
			if err != nil {
				return fmt.Errorf("%s: comptime instance ABI: %w", expr.Pos(), err)
			}
			conf.LogosCtTypes[expr] = typ
		}
	}
	if len(ct.constMapUses) != 0 {
		if err := ct.bindConstMapUses(conf); err != nil {
			return err
		}
	}
	conf.LogosCtUses = ct.uses
	if len(ct.owned) != 0 {
		if len(conf.LogosOwnedTypes) == 0 {
			conf.LogosOwnedTypes = ct.owned
		} else {
			conf.LogosOwnedTypes = maps.Clone(conf.LogosOwnedTypes)
			for e, owned := range ct.owned {
				if _, exists := conf.LogosOwnedTypes[e]; !exists {
					conf.LogosOwnedTypes[e] = owned
				}
			}
		}
	}
	return nil
}

func (ct *ctLowerContext) importPaths() []string {
	paths := make([]string, 0, len(ct.packages))
	for path := range ct.packages {
		if path != ct.owner.Path() {
			paths = append(paths, path)
		}
	}
	slices.Sort(paths)
	return paths
}

// ctTypeExpr records a semantic type by exact node. configure projects that
// binding to the Go ABI without changing the type or the instance key.
func (fl *fileLower) ctTypeExpr(pos syntax.Pos, typ types2.Type) syntax.Expr {
	ct := fl.ct
	if ct.types == nil {
		ct.types = make(map[syntax.Expr]types2.Type)
	}
	return types2.LogosCtTypeExpr(pos, typ, ct.types)
}

func (fl *fileLower) ctBuiltin(pos syntax.Pos, name string) *syntax.Name {
	id := nameOf(pos, name)
	ct := fl.ct
	obj := types2.Universe.Lookup(name)
	if obj != nil {
		ct.bindUse(id, obj)
	} else if ct.err == nil {
		ct.err = fmt.Errorf("%s: no predeclared name %q for comptime lowering", pos, name)
	}
	return id
}

func (fl *fileLower) ctPkgSelector(pos syntax.Pos, path, name string) *syntax.SelectorExpr {
	ct := fl.ct
	id := nameOf(pos, ctPackageQualifier)
	if ct.packageUses == nil {
		ct.packageUses = make(map[string][]*syntax.Name)
	}
	ct.packageUses[path] = append(ct.packageUses[path], id)
	return selOf(pos, id, name)
}

func (ct *ctLowerContext) bindUse(id *syntax.Name, obj types2.Object) {
	if ct.uses == nil {
		ct.uses = make(map[*syntax.Name]types2.Object)
	}
	ct.uses[id] = obj
}

func (ct *ctLowerContext) bindOwnedLiteral(lit *syntax.CompositeLit) {
	ct.bindOwnedAccess(lit)
}

func (ct *ctLowerContext) bindOwnedSelector(sel *syntax.SelectorExpr) {
	ct.bindOwnedAccess(sel)
}

func (ct *ctLowerContext) bindOwnedAccess(e syntax.Expr) {
	if ct.owned == nil {
		ct.owned = make(map[syntax.Expr]types2.LogosOwnedType)
	}
	ct.owned[e] = types2.LogosOwnedType{}
}

func (ct *ctLowerContext) importPackage(path string) (*types2.Package, error) {
	if path == ct.owner.Path() {
		return ct.owner, nil
	}
	if ct.consumer != nil && path == ct.consumer.Path() {
		return ct.consumer, nil
	}
	if pkg := ct.packages[path]; pkg != nil {
		return pkg, nil
	}
	if ct.importer == nil {
		return nil, fmt.Errorf("no importer from the final Go check")
	}
	pkg, err := ct.importer.Import(path)
	if err != nil {
		return nil, err
	}
	if pkg == nil || !pkg.Complete() {
		return nil, fmt.Errorf("importer returned no complete package")
	}
	if ct.packages == nil {
		ct.packages = make(map[string]*types2.Package)
	}
	ct.packages[path] = pkg
	return pkg, nil
}

func (ct *ctLowerContext) abiType(semantic types2.Type) (types2.Type, error) {
	if typ := ct.abi[semantic]; typ != nil {
		return typ, nil
	}
	typ, err := ct.projectABI(semantic)
	if err != nil {
		return nil, err
	}
	if ct.abi == nil {
		ct.abi = make(map[types2.Type]types2.Type)
	}
	ct.abi[semantic] = typ
	return typ, nil
}

func (ct *ctLowerContext) projectABI(typ types2.Type) (types2.Type, error) {
	switch t := typ.(type) {
	case *types2.Basic:
		return t, nil
	case *types2.ConstView:
		return ct.abiType(t.Elem)
	case *types2.ReadonlyDescendants:
		return ct.abiType(t.Elem)
	case *types2.Option:
		elem, err := ct.abiType(t.Elem)
		if err != nil || types2.LogosOptionIsBare(t.Elem) {
			return elem, err
		}
		pkg, err := ct.importPackage(rtImportPath)
		if err != nil {
			return nil, err
		}
		option, ok := pkg.Scope().Lookup("Option").(*types2.TypeName)
		if !ok {
			return nil, fmt.Errorf("%s does not declare Option", pkg.Path())
		}
		return types2.Instantiate(ct.context, option.Type(), []types2.Type{elem}, true)
	case *types2.LogosTuple:
		return ct.abiType(t.Underlying())
	case *types2.Named:
		return ct.abiOrigin(t.Obj(), t.TypeArgs())
	case *types2.Alias:
		return ct.abiOrigin(t.Obj(), t.TypeArgs())
	case *types2.Array:
		elem, err := ct.abiType(t.Elem())
		if err != nil || elem == t.Elem() {
			return t, err
		}
		return types2.NewArray(elem, t.Len()), nil
	case *types2.Slice:
		elem, err := ct.abiType(t.Elem())
		if err != nil || elem == t.Elem() {
			return t, err
		}
		return types2.NewSlice(elem), nil
	case *types2.Pointer:
		elem, err := ct.abiType(t.Elem())
		if err != nil || elem == t.Elem() {
			return t, err
		}
		return types2.NewPointer(elem), nil
	case *types2.Chan:
		elem, err := ct.abiType(t.Elem())
		if err != nil || elem == t.Elem() {
			return t, err
		}
		return types2.NewChan(t.Dir(), elem), nil
	case *types2.Map:
		key, err := ct.abiType(t.Key())
		if err != nil {
			return nil, err
		}
		elem, err := ct.abiType(t.Elem())
		if err != nil || (elem == t.Elem() && key == t.Key()) {
			return t, err
		}
		return types2.NewMap(key, elem), nil
	case *types2.Struct:
		return ct.abiStruct(t)
	case *types2.Interface:
		return ct.abiInterface(t)
	case *types2.Union:
		return ct.abiUnion(t)
	case *types2.Tuple:
		return ct.abiTuple(t)
	case *types2.Signature:
		return ct.abiSignature(t)
	default:
		return nil, fmt.Errorf("no Go representation for %T (%v)", typ, typ)
	}
}

func (ct *ctLowerContext) abiOrigin(old *types2.TypeName, args *types2.TypeList) (types2.Type, error) {
	origin := old.Type()
	if canonical := ct.localTypes[old]; canonical != nil {
		origin = canonical.Type()
	}
	if pkg := old.Pkg(); pkg != nil && old.Parent() == pkg.Scope() {
		canonical, err := ct.importPackage(pkg.Path())
		if err != nil {
			return nil, fmt.Errorf("resolve %s.%s: %w", pkg.Path(), old.Name(), err)
		}
		name, ok := canonical.Scope().Lookup(old.Name()).(*types2.TypeName)
		if !ok {
			return nil, fmt.Errorf("Go owner %q does not declare type %q", pkg.Path(), old.Name())
		}
		origin = name.Type()
	}
	if args.Len() == 0 {
		return origin, nil
	}
	targs := make([]types2.Type, args.Len())
	for i := range targs {
		arg, err := ct.abiType(args.At(i))
		if err != nil {
			return nil, err
		}
		targs[i] = arg
	}
	return types2.Instantiate(ct.context, origin, targs, true)
}

func ctConsumerLocalTypes(before, after *types2.Info) map[*types2.TypeName]*types2.TypeName {
	var out map[*types2.TypeName]*types2.TypeName
	for id, obj := range before.Defs {
		old, ok := obj.(*types2.TypeName)
		if !ok || old.Pkg() == nil || old.Parent() == old.Pkg().Scope() {
			continue
		}
		switch old.Type().(type) {
		case *types2.Named, *types2.Alias:
		default:
			continue
		}
		fresh, ok := after.Defs[id].(*types2.TypeName)
		if !ok {
			continue
		}
		if out == nil {
			out = make(map[*types2.TypeName]*types2.TypeName)
		}
		out[old] = fresh
	}
	return out
}

func (ct *ctLowerContext) abiStruct(t *types2.Struct) (types2.Type, error) {
	var fields []*types2.Var
	for i := 0; i < t.NumFields(); i++ {
		f := t.Field(i)
		typ, err := ct.abiType(f.Type())
		if err != nil {
			return nil, err
		}
		if typ == f.Type() && fields == nil {
			continue
		}
		if fields == nil {
			fields = make([]*types2.Var, t.NumFields())
			for j := range fields[:i] {
				fields[j] = t.Field(j)
			}
		}
		fields[i] = f
		if typ != f.Type() {
			fields[i] = types2.NewField(f.Pos(), f.Pkg(), f.Name(), typ, f.Embedded())
		}
	}
	if fields == nil {
		return t, nil
	}
	var tags []string
	for i := range fields {
		if tag := t.Tag(i); tag != "" {
			for len(tags) <= i {
				tags = append(tags, "")
			}
			tags[i] = tag
		}
	}
	return types2.NewStruct(fields, tags), nil
}

func (ct *ctLowerContext) abiTuple(t *types2.Tuple) (*types2.Tuple, error) {
	var vars []*types2.Var
	for i := 0; i < t.Len(); i++ {
		v := t.At(i)
		typ, err := ct.abiType(v.Type())
		if err != nil {
			return nil, err
		}
		if typ == v.Type() && vars == nil {
			continue
		}
		if vars == nil {
			vars = make([]*types2.Var, t.Len())
			for j := range vars[:i] {
				vars[j] = t.At(j)
			}
		}
		vars[i] = v
		if typ != v.Type() {
			vars[i] = types2.NewVar(v.Pos(), v.Pkg(), v.Name(), typ)
		}
	}
	if vars == nil {
		return t, nil
	}
	return types2.NewTuple(vars...), nil
}

func (ct *ctLowerContext) abiSignature(t *types2.Signature) (*types2.Signature, error) {
	params, err := ct.abiTuple(t.Params())
	if err != nil {
		return nil, err
	}
	results, err := ct.abiTuple(t.Results())
	if err != nil {
		return nil, err
	}
	if t.TypeParams().Len() != 0 || t.RecvTypeParams().Len() != 0 {
		return nil, fmt.Errorf("uninstantiated generic signature: %v", t)
	}
	if params == t.Params() && results == t.Results() {
		return t, nil
	}
	return types2.NewSignatureType(nil, nil, nil, params, results, t.Variadic()), nil
}

func (ct *ctLowerContext) abiInterface(t *types2.Interface) (types2.Type, error) {
	methodCount, embedCount := t.NumExplicitMethods(), t.NumEmbeddeds()
	changed := false
	for i := range methodCount {
		m := t.ExplicitMethod(i)
		sig, err := ct.abiType(m.Signature())
		if err != nil {
			return nil, err
		}
		changed = changed || sig != m.Signature() || m.LogosReadonly()
	}
	for i := range embedCount {
		old := t.EmbeddedType(i)
		typ, err := ct.abiType(old)
		if err != nil {
			return nil, err
		}
		changed = changed || typ != old
	}
	if !changed {
		return t, nil
	}
	methods := make([]*types2.Func, methodCount)
	for i := range methods {
		m := t.ExplicitMethod(i)
		sig := ct.abi[m.Signature()].(*types2.Signature)
		plain := types2.NewSignatureType(nil, nil, nil, sig.Params(), sig.Results(), sig.Variadic())
		methods[i] = types2.NewFunc(m.Pos(), m.Pkg(), m.Name(), plain)
	}
	embedded := make([]types2.Type, embedCount)
	for i := range embedded {
		embedded[i] = ct.abi[t.EmbeddedType(i)]
	}
	out := types2.NewInterfaceType(methods, embedded)
	if t.IsImplicit() {
		out.MarkImplicit()
	}
	out.NumMethods()
	return out, nil
}

func (ct *ctLowerContext) abiUnion(t *types2.Union) (types2.Type, error) {
	var terms []*types2.Term
	for i := range t.Len() {
		term := t.Term(i)
		typ, err := ct.abiType(term.Type())
		if err != nil {
			return nil, err
		}
		if typ == term.Type() && terms == nil {
			continue
		}
		if terms == nil {
			terms = make([]*types2.Term, t.Len())
			for j := range terms[:i] {
				terms[j] = t.Term(j)
			}
		}
		terms[i] = term
		if typ != term.Type() {
			terms[i] = types2.NewTerm(term.Tilde(), typ)
		}
	}
	if terms == nil {
		return t, nil
	}
	return types2.NewUnion(terms), nil
}

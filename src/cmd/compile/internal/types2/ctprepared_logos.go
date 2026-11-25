// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	"errors"
	. "internal/types/errors"
)

// LogosCtTypeExpr binds an opaque type name to the supplied type without
// reconstructing it. The caller passes an initialized bound map as
// Config.LogosCtTypes when checking the returned node. Cloning the node
// does not copy its binding.
func LogosCtTypeExpr(pos syntax.Pos, typ Type, bound map[syntax.Expr]Type) syntax.Expr {
	assert(typ != nil && isValid(typ) && isTyped(typ))
	name := syntax.NewName(pos, "_logos_ct_type")
	bound[name] = typ
	return name
}

func (check *Checker) logosSyntheticTypeOf(e syntax.Expr) (Type, bool) {
	if bound := check.conf.LogosCtTypes; bound != nil {
		if typ := bound[e]; typ != nil {
			if check.logosCtLocalDefs != nil {
				typ = check.logosCtRebindType(e.Pos(), typ)
			}
			return typ, true
		}
	}
	return check.logosOwnedTypeOf(e)
}

func (check *Checker) logosCtLookupScope(id *syntax.Name) (*Scope, Object) {
	if bound := check.conf.LogosCtUses; bound != nil {
		if obj := bound[id]; obj != nil {
			return nil, obj
		}
	}
	return check.lookupScope(id.Value)
}

func (check *Checker) logosPrepareLocalDefs() error {
	oldToClone := check.conf.LogosCtLocalTypes
	if len(oldToClone) == 0 {
		return nil
	}
	cloneToOld := make(map[*syntax.Name]*TypeName, len(oldToClone))
	for old, clone := range oldToClone {
		if old == nil || clone == nil || cloneToOld[clone] != nil {
			return errors.New("logos: invalid local type provenance in prepared comptime function")
		}
		cloneToOld[clone] = old
	}
	for clone := range cloneToOld {
		delete(check.Defs, clone)
	}
	check.logosCtLocalDefs = cloneToOld
	return nil
}

// LogosCheckCtPrepared checks a reduced, non-generic function and its detached
// package variable and function helpers as one Go unit in the defining package.
// Parent is the already-checked lexical scope. Config supplies the effective Go
// version and shared type context for the defining file.
// The caller owns the syntax, config bindings, and info. The checker records
// exact definition objects and helper InitOrder in info. Helpers are visible in
// a private overlay only; the returned function is not installed in any scope.
// InitOrder contains only initializers in this unit, not those of the owner.
// Synthetic PkgName bindings must belong to pkg and refer to the canonical
// imported package, just as bindings in the owner scope do.
// LogosCtLocalTypes links locally declared types to their cloned definitions
// so opaque uses resolve to the corresponding fresh types in this check.
func LogosCheckCtPrepared(conf *Config, pkg *Package, parent *Scope, decl *syntax.FuncDecl, helpers []syntax.Decl, info *Info) (_ *Func, err error) {
	if pkg == nil || parent == nil {
		return nil, errors.New("logos: prepared comptime function needs an owner package and scope")
	}
	if !logosPreparedFunc(decl) {
		return nil, errors.New("logos: expected a concrete prepared comptime function with a body")
	}

	var prepared Config
	if conf != nil {
		prepared = *conf
	}
	prepared.Logos = false
	prepared.IgnoreFuncBodies = false
	check := NewChecker(&prepared, pkg, info)
	if err := check.logosPrepareLocalDefs(); err != nil {
		return nil, err
	}
	if check.logosCtLocalDefs != nil && check.Defs == nil {
		check.Defs = make(map[*syntax.Name]Object)
		defer func() { check.Defs = nil }()
	}
	defer check.handleBailout(&err)
	check.version = asGoVersion(prepared.GoVersion)
	check.scope = &Scope{parent: parent, pos: decl.Pos(), end: syntax.EndPos(decl), comment: "comptime preparation"}
	for _, helper := range helpers {
		check.logosCollectPreparedHelper(helper)
	}
	helperObjs := check.objList
	obj := check.logosCollectPreparedFunc(decl, nil)
	if check.firstErr != nil {
		return nil, check.firstErr
	}
	check.objDecl(obj)
	if len(helperObjs) != 0 {
		// A lazy helper use can rebind a local type only after its declaration
		// has been checked in the primary body.
		check.processDelayed(0)
	}
	for _, helper := range helperObjs {
		check.objDecl(helper)
	}
	check.logosCopyFinish()
	if len(helpers) != 0 {
		check.initOrder()
	} else {
		check.InitOrder = check.InitOrder[:0]
	}
	if check.firstErr != nil {
		return nil, check.firstErr
	}
	check.Info.LogosCtRecordGlobalRefs(prepared.LogosCtUses)
	return obj, nil
}

// LogosCtRecordGlobalRefs marks unscoped package functions that a successful
// check resolved through exact generated-name bindings. These are references
// to owner definitions, not definitions to emit.
func (info *Info) LogosCtRecordGlobalRefs(uses map[*syntax.Name]Object) {
	for id, obj := range uses {
		fn, ok := obj.(*Func)
		if !ok || fn == nil || info.Uses[id] != fn || fn.Pkg() == nil || fn.Parent() != nil || fn.Signature().Recv() != nil {
			continue
		}
		if info.LogosCtGlobalRefs == nil {
			info.LogosCtGlobalRefs = make(map[Object]bool)
		}
		info.LogosCtGlobalRefs[fn] = true
	}
}

func logosPreparedFunc(decl *syntax.FuncDecl) bool {
	return decl != nil && decl.Name != nil && decl.Type != nil && decl.Body != nil && decl.Recv == nil && len(decl.TParamList) == 0
}

func (check *Checker) logosCollectPreparedHelper(decl syntax.Decl) {
	switch d := decl.(type) {
	case *syntax.FuncDecl:
		if !logosPreparedFunc(d) {
			check.error(d, BadDecl, "logos: expected a non-generic comptime helper function with a body")
			return
		}
		check.logosCollectPreparedFunc(d, check.scope)
	case *syntax.VarDecl:
		check.logosCollectPreparedVars(d)
	default:
		check.errorf(decl, BadDecl, "logos: unsupported prepared comptime helper declaration %T", decl)
	}
}

func (check *Checker) logosCollectPreparedFunc(decl *syntax.FuncDecl, scope *Scope) *Func {
	obj := NewFunc(decl.Name.Pos(), check.pkg, decl.Name.Value, nil)
	check.logosCollectPreparedObject(decl.Name, obj, &declInfo{fdecl: decl}, scope)
	return obj
}

func (check *Checker) logosCollectPreparedVars(decl *syntax.VarDecl) {
	values := syntax.UnpackListExpr(decl.Values)
	if decl.Type == nil || values != nil {
		check.arity(decl.Pos(), decl.NameList, values, false, false)
	}
	var shared *declInfo
	if len(values) == 1 && len(decl.NameList) > 1 {
		shared = &declInfo{lhs: make([]*Var, len(decl.NameList)), vtyp: decl.Type, init: values[0]}
	}
	for i, name := range decl.NameList {
		obj := newVar(PackageVar, name.Pos(), check.pkg, name.Value, nil)
		d := shared
		if d == nil {
			d = &declInfo{vtyp: decl.Type}
			if i < len(values) {
				d.init = values[i]
			}
		} else {
			d.lhs[i] = obj
		}
		check.logosCollectPreparedObject(name, obj, d, check.scope)
	}
}

func (check *Checker) logosCollectPreparedObject(name *syntax.Name, obj Object, decl *declInfo, scope *Scope) {
	if name.Value == "" || name.Value == "_" || name.Value == "init" || name.Value == "main" && check.pkg.Name() == "main" {
		check.errorf(name, BadDecl, "logos: invalid prepared comptime declaration name %s", name.Value)
		return
	}
	if conflict := check.lookup(name.Value); conflict != nil {
		e := check.newError(DuplicateDecl)
		e.addf(name, "logos: prepared comptime declaration %s conflicts with an existing name", name.Value)
		e.addAltDecl(conflict)
		e.report()
		return
	}
	if scope == nil {
		check.recordDef(name, obj)
	} else {
		check.declare(scope, name, obj, nopos)
	}
	if versions := check.FileVersions; versions != nil {
		versions[name.Pos().FileBase()] = check.conf.GoVersion
	}
	decl.file, decl.version = check.scope, check.version
	check.objMap[obj] = decl
	check.objList = append(check.objList, obj)
	obj.setOrder(uint32(len(check.objMap)))
}

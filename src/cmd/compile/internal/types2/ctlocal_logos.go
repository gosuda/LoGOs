// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"path/filepath"
	"slices"
	"sync"

	"cmd/compile/internal/syntax"
	. "internal/types/errors"
)

type logosCtLocalID struct {
	file, name, discriminator string
	line, col                 uint
	abiName                   string
}

type logosCtLocalType struct {
	origin   *Named
	id       logosCtLocalID
	abiName  string
	captures []Type
	explicit []Type
}

type logosCtLocalTypes struct {
	mu    sync.Mutex
	scope *Scope
	types map[logosCtLocalID][]*Named
}

func logosCtLocalIDAt(pos syntax.Pos, name, discriminator string) logosCtLocalID {
	id := logosCtLocalID{name: name, discriminator: discriminator, line: pos.Line(), col: pos.Col()}
	if base := pos.FileBase(); base != nil {
		id.file = filepath.Base(base.Filename())
	}
	return id
}

func (check *Checker) logosCtRecordLocal(n *Named, name *syntax.Name) {
	if previous := check.logosCtLocalDefs[name]; previous != nil {
		if old, ok := previous.Type().(*Named); ok && old.logosCtLocal != nil {
			local := *old.logosCtLocal
			local.origin = n
			n.logosCtLocal = &local
		}
		return
	}
	if !check.conf.Logos || n.obj.Parent() == nil || n.obj.Parent() == n.obj.Pkg().Scope() {
		return
	}
	var captures []Type
	for _, sig := range check.logosFuncSigs {
		for _, p := range sig.rparams.list() {
			captures = append(captures, p)
		}
		for _, p := range sig.tparams.list() {
			captures = append(captures, p)
		}
	}
	var discriminator logosCtTypeKey
	if len(check.logosCtSubst) != 0 {
		args := make([]Type, len(check.logosCtSubst))
		for param, arg := range check.logosCtSubst {
			args[param.Index()] = arg
		}
		discriminator.WriteByte('c')
		discriminator.list(args)
	}
	for field := check.logosFieldBind; field != nil; field = field.outer {
		discriminator.WriteByte('f')
		discriminator.number(int64(field.index))
	}
	n.logosCtLocal = &logosCtLocalType{
		origin:   n,
		id:       logosCtLocalIDAt(n.obj.pos, n.obj.name, discriminator.String()),
		captures: captures,
	}
}

// LogosCtLocalNamed returns the lexical declaration and the current values of its
// implicit and explicit binders. The returned slices must not be modified.
func LogosCtLocalNamed(t *Named) (origin *Named, captures, explicit []Type) {
	local := t.Origin().logosCtLocal
	if local == nil {
		return nil, nil, nil
	}
	explicit = local.explicit
	if args := t.TypeArgs(); args != nil {
		explicit = args.list()
	}
	return local.origin, local.captures, explicit
}

// LogosCtLocalABIName returns the exact object name encoded in the archive, or
// empty for a source declaration whose ABI name will be assigned by the writer.
func LogosCtLocalABIName(t *Named) string {
	if local := t.Origin().logosCtLocal; local != nil {
		return local.abiName
	}
	return ""
}

// LogosCtLocalDiscriminator distinguishes compiler-generated declarations
// which share a source position.
func LogosCtLocalDiscriminator(t *Named) string {
	if local := t.Origin().logosCtLocal; local != nil {
		return local.id.discriminator
	}
	return ""
}

// LogosCtNewLocal interns an archive's hidden semantic named type before its
// recursive type graph is decoded. The first reader completes the definition.
func LogosCtNewLocal(pos syntax.Pos, pkg *Package, name, abiName, discriminator string, captures, explicit []Type) (*Named, bool) {
	id := logosCtLocalIDAt(pos, name, discriminator)
	return logosCtNewLocal(pos, pkg, id, abiName, captures, explicit)
}

// LogosCtNewABILocal interns the lowered archive type independently of the
// original semantic type, whose underlying structure may differ.
func LogosCtNewABILocal(pos syntax.Pos, pkg *Package, name, abiName string, captures, explicit []Type) (*Named, bool) {
	id := logosCtLocalID{name: name, abiName: abiName}
	return logosCtNewLocal(pos, pkg, id, abiName, captures, explicit)
}

func logosCtNewLocal(pos syntax.Pos, pkg *Package, id logosCtLocalID, abiName string, captures, explicit []Type) (*Named, bool) {
	local := &logosCtLocalType{id: id, abiName: abiName, captures: captures, explicit: explicit}
	return pkg.logosCtLocals.intern(local, func() *Named {
		return NewNamed(NewTypeName(pos, pkg, id.name, nil), nil, nil)
	})
}

func (locals *logosCtLocalTypes) intern(local *logosCtLocalType, create func() *Named) (*Named, bool) {
	for start := 0; ; {
		locals.mu.Lock()
		types := locals.types[local.id]
		locals.mu.Unlock()
		for _, t := range types[start:] {
			prev := t.logosCtLocal
			if slices.EqualFunc(local.captures, prev.captures, Identical) && slices.EqualFunc(local.explicit, prev.explicit, Identical) {
				return t, false
			}
		}
		start = len(types)
		locals.mu.Lock()
		if len(locals.types[local.id]) != start {
			locals.mu.Unlock()
			continue
		}
		t := create()
		if local.origin == nil {
			local.origin = t
		}
		t.logosCtLocal = local
		if locals.scope == nil {
			locals.scope = &Scope{parent: t.obj.pkg.Scope(), comment: "hidden local type declarations"}
		}
		t.obj.setParent(locals.scope)
		if locals.types == nil {
			locals.types = make(map[logosCtLocalID][]*Named)
		}
		locals.types[local.id] = append(types, t)
		locals.mu.Unlock()
		return t, true
	}
}

// LogosCtSetLocal completes a hidden imported type without changing the
// declaration, explicit methods, or interface receiver types it references.
func LogosCtSetLocal(n *Named, underlying Type, methods []*Func) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.methods = methods
	n.fromRHS = logosCtLocalInterface(underlying, underlying, n)
	n.underlying = n.fromRHS
	n.allowNilRHS = false
	n.setState(lazyLoaded | unpacked | hasMethods | hasUnder)
}

func logosCtLocalInterface(underlying, recv Type, n *Named) Type {
	iface, ok := underlying.(*Interface)
	if !ok {
		return underlying
	}
	if methods, copied := replaceRecvType(iface.methods, recv, n); copied {
		out := NewInterfaceType(methods, iface.embeddeds)
		out.implicit = iface.implicit
		return out
	}
	return underlying
}

func (s *subster) logosCtLocalNamed(t *Named) *Named {
	local := t.Origin().logosCtLocal
	_, captures, explicit := LogosCtLocalNamed(t)
	newCaptures := substList(captures, s.typ)
	if newCaptures == nil && t.inst != nil {
		return nil
	}
	newExplicit := substList(explicit, s.typ)
	if newCaptures == nil && newExplicit == nil {
		return t
	}
	if newCaptures == nil {
		newCaptures = captures
	}
	if newExplicit == nil {
		newExplicit = explicit
	}
	copy := *local
	copy.captures, copy.explicit = newCaptures, newExplicit
	obj := local.origin.obj
	n, _ := obj.pkg.logosCtLocals.intern(&copy, func() *Named {
		// A recursive RHS must see this new identity in the package cache.
		// Its materialization runs only after the cache records the placeholder.
		subst := *s
		if subst.ctxt == nil {
			subst.ctxt = subst.expanding.inst.ctxt
		}
		subst.check, subst.expanding = nil, nil
		subst.localBody = true
		return NewTypeNameLazy(obj.pos, obj.pkg, obj.name, func(n *Named) ([]*TypeParam, Type, []*Func, []func()) {
			underlying := subst.typ(t.Underlying())
			underlying = logosCtLocalInterface(underlying, t, n)
			methods := make([]*Func, t.NumMethods())
			for i := range methods {
				method := t.Method(i)
				sig := *subst.typ(method.Signature()).(*Signature)
				recv := Type(n)
				if method.hasPtrRecv() {
					recv = NewPointer(n)
				}
				sig.recv = cloneVar(sig.recv, recv)
				methods[i] = cloneFunc(method, &sig)
			}
			return nil, underlying, methods, nil
		}).Type().(*Named)
	})
	return n
}

func (check *Checker) logosCtRebindType(pos syntax.Pos, typ Type) Type {
	s := subster{pos: pos, check: check, ctxt: check.context(), localRebind: check}
	return s.typ(typ)
}

func (s *subster) logosCtReboundLocal(obj *TypeName, args []Type) Type {
	check := s.localRebind
	name := check.conf.LogosCtLocalTypes[obj]
	if name == nil {
		return nil
	}
	fresh, _ := check.Defs[name].(*TypeName)
	if fresh == nil || fresh.Type() == nil {
		check.errorf(s.pos, InvalidPkgUse, "missing checked declaration for local type %s", obj.Name())
		return Typ[Invalid]
	}
	if len(args) == 0 {
		return fresh.Type()
	}
	if updated := substList(args, s.typ); updated != nil {
		args = updated
	}
	return check.instance(s.pos, fresh.Type().(genericType), args, nil, s.ctxt)
}

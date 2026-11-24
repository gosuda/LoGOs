// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"slices"

	"cmd/compile/internal/syntax"
)

func (check *Checker) logosCtKeepType(obj Object, original Type) {
	if check.logosCtAbstract != nil && original != nil && logosContainsTypeParam(original, nil) {
		check.logosCtAbstract[obj] = original
	}
}

func (check *Checker) logosCtKeepDeclaredType(obj Object, e syntax.Expr) {
	if check.logosCtAbstract != nil {
		check.logosCtKeepType(obj, check.logosCtOriginalType(e, 0))
	}
}

func (check *Checker) logosCtOriginalType(e syntax.Expr, result int) Type {
	switch n := syntax.Unparen(e).(type) {
	case *syntax.Name:
		return check.logosCtAbstract[check.Uses[n]]
	case *syntax.SelectorExpr:
		return check.logosCtOriginalSelection(n)
	case *syntax.IndexExpr:
		if orig := check.logosCtOriginalInstance(n, nil); orig != nil {
			return orig
		}
		return logosCtElement(check.logosCtOriginalType(n.X, 0))
	case *syntax.SliceExpr:
		t := check.logosCtOriginalType(n.X, 0)
		if elem := logosCtElement(t); elem != nil {
			return NewSlice(elem)
		}
	case *syntax.CallExpr:
		return check.logosCtOriginalResult(n, result)
	case *syntax.AssertExpr:
		return check.logosCtOriginalType(n.Type, 0)
	case *syntax.CompositeLit:
		return check.logosCtOriginalType(n.Type, 0)
	case *syntax.FuncLit:
		return check.logosCtOriginalSignature(n)
	case *syntax.FuncType:
		return check.logosCtOriginalSignature(n)
	case *syntax.Operation:
		return check.logosCtOriginalUnary(n)
	case *syntax.SliceType:
		if elem := check.logosCtOriginalType(n.Elem, 0); elem != nil {
			return NewSlice(elem)
		}
	case *syntax.ArrayType:
		if elem := check.logosCtOriginalType(n.Elem, 0); elem != nil {
			if a, ok := check.Info.TypeOf(n).(*Array); ok {
				return NewArray(elem, a.Len())
			}
		}
	case *syntax.MapType:
		return check.logosCtOriginalMap(n)
	case *syntax.ChanType:
		if elem := check.logosCtOriginalType(n.Elem, 0); elem != nil {
			if ch, ok := check.Info.TypeOf(n).(*Chan); ok {
				return NewChan(ch.Dir(), elem)
			}
		}
	case *syntax.FieldDefault:
		return check.logosCtOriginalType(n.Type, 0)
	}
	return nil
}

func logosCtElement(t Type) Type {
	if t == nil {
		return nil
	}
	t, _ = deref(t)
	switch t := t.Underlying().(type) {
	case *Array:
		return t.elem
	case *Slice:
		return t.elem
	case *Map:
		return t.elem
	case *Chan:
		return t.elem
	}
	return nil
}

func (check *Checker) logosCtOriginalMap(n *syntax.MapType) Type {
	key := check.logosCtOriginalType(n.Key, 0)
	elem := check.logosCtOriginalType(n.Value, 0)
	if key == nil && elem == nil {
		return nil
	}
	if key == nil {
		key = check.Info.TypeOf(n.Key)
	}
	if elem == nil {
		elem = check.Info.TypeOf(n.Value)
	}
	if key == nil || elem == nil {
		return nil
	}
	return NewMap(key, elem)
}

func (check *Checker) logosCtOriginalUnary(n *syntax.Operation) Type {
	if n.Y != nil {
		return nil
	}
	t := check.logosCtOriginalType(n.X, 0)
	if t == nil {
		return nil
	}
	switch n.Op {
	case syntax.And:
		return NewPointer(t)
	case syntax.Mul:
		if check.Types[n].IsType() {
			return NewPointer(t)
		}
		if base, ptr := deref(t); ptr {
			return base
		}
	case syntax.Recv:
		return logosCtElement(t)
	}
	return nil
}

func (check *Checker) logosCtOriginalSelection(n *syntax.SelectorExpr) Type {
	obj := check.Uses[n.Sel]
	if orig := check.logosCtAbstract[obj]; orig != nil {
		return orig
	}
	if v, ok := obj.(*Var); ok {
		if orig := check.logosCtAbstract[v.Origin()]; orig != nil {
			return orig
		}
	}
	recv := check.logosCtOriginalType(n.X, 0)
	if recv == nil {
		return nil
	}
	if sel := logosSelectProperty(check.pkg, recv, n.Sel.Value); sel.kind == logosSelectInterface || sel.kind == logosSelectPromoted {
		return sel.prop.typ
	}
	member, _, _ := lookupFieldOrMethodImpl(recv, true, check.pkg, n.Sel.Value, false)
	if member != nil {
		return member.Type()
	}
	return nil
}

func (check *Checker) logosCtOriginalSignature(e syntax.Expr) Type {
	sig, _ := check.Info.TypeOf(e).(*Signature)
	if sig == nil {
		return nil
	}
	copy := *sig
	copy.params = check.logosCtOriginalTuple(sig.params)
	copy.results = check.logosCtOriginalTuple(sig.results)
	if copy.params == sig.params && copy.results == sig.results {
		return nil
	}
	return &copy
}

func (check *Checker) logosCtOriginalTuple(t *Tuple) *Tuple {
	var vars []*Var
	for i := range t.Len() {
		v := t.At(i)
		if orig := check.logosCtAbstract[v]; orig != nil {
			if vars == nil {
				vars = slices.Clone(t.vars)
			}
			replaced := *v
			replaced.typ = orig
			vars[i] = &replaced
		}
	}
	if vars == nil {
		return t
	}
	return NewTuple(vars...)
}

func (check *Checker) logosCtOriginalResult(call *syntax.CallExpr, result int) Type {
	if tv := check.Types[call.Fun]; tv.IsType() {
		return check.logosCtOriginalType(call.Fun, 0)
	}
	if id, ok := syntax.Unparen(call.Fun).(*syntax.Name); ok && len(call.ArgList) != 0 {
		if obj, ok := check.Uses[id].(*Builtin); ok {
			switch obj.id {
			case _Append, _Make:
				return check.logosCtOriginalType(call.ArgList[0], 0)
			case _New:
				if elem := check.logosCtOriginalType(call.ArgList[0], 0); elem != nil {
					return NewPointer(elem)
				}
			}
		}
	}
	fun := check.logosCtOriginalInstance(call.Fun, call)
	if fun == nil {
		fun = check.logosCtOriginalType(call.Fun, 0)
	}
	if fun == nil {
		return nil
	}
	sig, _ := fun.Underlying().(*Signature)
	if sig == nil {
		return nil
	}
	if sig.results.Len() == 1 {
		result = 0
	}
	if result < sig.results.Len() {
		return sig.results.At(result).typ
	}
	return nil
}

func (check *Checker) logosCtOriginalInstance(e syntax.Expr, call *syntax.CallExpr) Type {
	e = syntax.Unparen(e)
	var indices []syntax.Expr
	if ix, ok := e.(*syntax.IndexExpr); ok {
		e = syntax.Unparen(ix.X)
		indices = syntax.UnpackListExpr(ix.Index)
	}
	var id *syntax.Name
	switch e := e.(type) {
	case *syntax.Name:
		id = e
	case *syntax.SelectorExpr:
		id = e.Sel
	default:
		return nil
	}
	inst, ok := check.Instances[id]
	if !ok || len(indices) > inst.TypeArgs.Len() {
		return nil
	}
	obj := check.Uses[id]
	if obj == nil {
		return nil
	}
	origin := obj.Type()
	var targs []Type
	for i, ix := range indices {
		if orig := check.logosCtOriginalType(ix, 0); orig != nil {
			if targs == nil {
				targs = slices.Clone(inst.TypeArgs.list())
			}
			targs[i] = orig
		}
	}
	if sig, ok := origin.(*Signature); ok && len(indices) < inst.TypeArgs.Len() {
		inferred := check.logosCtInferOriginalArgs(sig, call)
		for i := len(indices); i < len(inferred); i++ {
			if inferred[i] != nil {
				if targs == nil {
					targs = slices.Clone(inst.TypeArgs.list())
				}
				targs[i] = inferred[i]
			}
		}
	}
	if targs != nil {
		t, err := Instantiate(check.context(), origin, targs, false)
		if err == nil {
			return t
		}
	}
	return nil
}

func (check *Checker) logosCtInferOriginalArgs(sig *Signature, call *syntax.CallExpr) []Type {
	if call == nil || sig.TypeParams().Len() == 0 {
		return nil
	}
	n := len(call.ArgList)
	var tupleCall syntax.Expr
	if n == 1 {
		if tuple, ok := check.Info.TypeOf(call.ArgList[0]).(*Tuple); ok {
			n, tupleCall = tuple.Len(), call.ArgList[0]
		}
	}
	fixed := sig.params.Len()
	spread := sig.Variadic() && !call.HasDots
	if spread {
		fixed--
	}
	if n < fixed || !spread && n != fixed {
		return nil
	}
	var u *unifier
	for i := range n {
		var orig Type
		if tupleCall != nil {
			orig = check.logosCtOriginalType(tupleCall, i)
		} else {
			orig = check.logosCtOriginalType(call.ArgList[i], 0)
		}
		if orig != nil {
			if u == nil {
				u = newUnifier(check, sig.TypeParams().list(), nil, true)
			}
			formal := sig.params.At(min(i, fixed)).typ
			if spread && i >= fixed {
				formal = formal.(*Slice).elem
			}
			if !u.unify(formal, orig, assign) {
				return nil
			}
		}
	}
	if u == nil {
		return nil
	}
	return u.inferred(sig.TypeParams().list())
}

func (check *Checker) logosCtProperty(x *operand, e *syntax.SelectorExpr, name string) (logosSelection, bool) {
	if check.logosCtAbstract == nil {
		return logosSelection{}, false
	}
	original, _ := Unalias(check.logosCtOriginalType(e.X, 0)).(*TypeParam)
	if original == nil {
		return logosSelection{}, false
	}
	iface := logosPropertyInterface(original)
	if iface == nil {
		return logosSelection{}, false
	}
	prop, ok := logosFindProperty(iface, name)
	if !ok {
		return logosSelection{}, false
	}
	prop.typ = check.subst(e.Pos(), prop.typ, check.logosCtSubst, nil, check.context())
	getter := logosCtCheckedAccessor(x, iface, check.pkg, logosGetterName(name))
	var setter *Func
	if !prop.readOnly {
		setter = logosCtCheckedAccessor(x, iface, check.pkg, logosSetterName(name))
	}
	if check.LogosCtProperties == nil {
		check.LogosCtProperties = make(map[*syntax.SelectorExpr]LogosCtProperty)
	}
	check.LogosCtProperties[e] = LogosCtProperty{
		Prop:   LogosProp{Name: name, Typ: prop.typ, ReadOnly: prop.readOnly},
		Getter: getter,
		Setter: setter,
	}
	return logosSelection{kind: logosSelectInterface, prop: prop}, true
}

func logosCtCheckedAccessor(x *operand, iface *Interface, pkg *Package, name string) *Func {
	obj, _, _ := lookupFieldOrMethodImpl(x.typ(), x.mode() == variable, pkg, name, false)
	if method, ok := obj.(*Func); ok {
		return method
	}
	for _, method := range iface.typeSet().methods {
		if method.name == name {
			return method
		}
	}
	panic("property has no checked accessor")
}

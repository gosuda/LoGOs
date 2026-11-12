// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	. "internal/types/errors"
)

type LogosTuple struct {
	names []string
	types []Type
	under *Struct
	pkg   *Package
}

func tupleFieldName(name string, i int) string {
	if name == "" {
		return sprintf(nil, true, "T_%d", i)
	}
	return "T_" + name
}

func NewLogosTuple(pos syntax.Pos, pkg *Package, names []string, types []Type) *LogosTuple {
	t := &LogosTuple{names: names, types: types, pkg: pkg}
	fields := make([]*Var, len(types))
	for i := range types {
		name := ""
		if i < len(names) {
			name = names[i]
		}
		fields[i] = NewVar(pos, pkg, tupleFieldName(name, i), types[i])
	}
	t.under = NewStruct(fields, nil)
	return t
}

func (t *LogosTuple) Len() int { return len(t.types) }

func (t *LogosTuple) Elem(i int) Type { return t.types[i] }

func (t *LogosTuple) Name(i int) string {
	if i < len(t.names) {
		return t.names[i]
	}
	return ""
}

func (t *LogosTuple) Underlying() Type { return t.under }

func (t *LogosTuple) String() string { return TypeString(t, nil) }

func logosAsTuple(T Type) *LogosTuple {
	if t, ok := Unalias(T).(*LogosTuple); ok {
		return t
	}
	return nil
}

func tupleElemsOf(T Type) (types []Type, ok bool) {
	if t := logosAsTuple(T); t != nil {
		return t.types, true
	}
	st, ok := Unalias(T).(*Struct)
	if !ok || !logosTupleShaped(st) {
		return nil, false
	}
	types = make([]Type, st.NumFields())
	for i := range types {
		types[i] = st.Field(i).Type()
	}
	return types, true
}

func logosTupleShaped(st *Struct) bool {
	if st.NumFields() < 2 {
		return false
	}
	for i := range st.NumFields() {
		nm := st.Field(i).Name()
		if len(nm) < 3 || nm[:2] != "T_" {
			return false
		}
	}
	return true
}

func checkTupleStructIdentical(x *LogosTuple, y Type) bool {
	st, ok := Unalias(y).(*Struct)
	if !ok || st.NumFields() != x.Len() {
		return false
	}
	under := x.Underlying().(*Struct)
	for i := range x.Len() {
		f, g := under.Field(i), st.Field(i)
		if f.Name() != g.Name() || !Identical(f.Type(), g.Type()) {
			return false
		}
	}
	return true
}

func (check *Checker) logosTupleType(e *syntax.TupleType, def *TypeName) Type {
	if len(e.Fields) < 2 {
		check.error(e, InvalidSyntaxTree, "tuple type needs at least two element types")
		return Typ[Invalid]
	}
	names := make([]string, len(e.Fields))
	types := make([]Type, len(e.Fields))
	for i, f := range e.Fields {
		if f.Name != nil {
			names[i] = f.Name.Value
		}
		typ := check.declaredType(f.Type, def)
		if !isValid(typ) {
			return Typ[Invalid]
		}
		check.validVarType(f.Type, typ)
		types[i] = typ
	}
	return NewLogosTuple(e.Pos(), check.pkg, names, types)
}

func logosTupleFieldIndex(t *LogosTuple, sel string) int {
	for i := range t.Len() {
		if t.Name(i) == sel || tupleFieldName(t.Name(i), i) == sel {
			return i
		}
	}
	return -1
}

func (check *Checker) logosTupleSelect(x *operand, e *syntax.SelectorExpr) bool {
	if !check.conf.Logos {
		return false
	}
	t := logosAsTuple(x.typ())
	if t == nil {
		return false
	}
	if i := logosTupleFieldIndex(t, e.Sel.Value); i >= 0 {
		x.mode_ = value
		x.expr = e
		x.typ_ = t.Elem(i)
		check.record(x)
		return true
	}
	check.errorf(e.Sel, MissingFieldOrMethod, "undefined tuple field %s.%s", x.expr, e.Sel.Value)
	x.invalidate()
	return true
}

func (check *Checker) logosImportedTupleSelect(x *operand, e *syntax.SelectorExpr) bool {
	if !check.conf.Logos {
		return false
	}
	st, ok := Unalias(x.typ()).(*Struct)
	if !ok || !logosTupleShaped(st) {
		return false
	}
	want := "T_" + e.Sel.Value
	for i := range st.NumFields() {
		if st.Field(i).Name() == want {
			x.mode_ = value
			x.expr = e
			x.typ_ = st.Field(i).Type()
			check.record(x)
			return true
		}
	}
	return false
}

func (check *Checker) logosTupleLit(x *operand, e *syntax.TupleLit) exprKind {
	types := make([]Type, len(e.Elems))
	for i, elem := range e.Elems {
		var op operand
		if !check.logosLambdaDefer(&op, elem) {
			check.expr(nil, &op, elem)
		}
		if !op.isValid() {
			x.mode_ = invalid
			x.expr = e
			x.typ_ = Typ[Invalid]
			return expression
		}
		typ := op.typ()
		if isUntyped(typ) {
			if typ == Typ[UntypedNil] {
				types[i] = typ
				continue
			}
			typ = Default(typ)
		}
		types[i] = typ
	}
	x.mode_ = value
	x.expr = e
	x.typ_ = NewLogosTuple(e.Pos(), check.pkg, make([]string, len(types)), types)
	check.record(x)
	return expression
}

func (check *Checker) logosTupleAssign(x *operand, T Type, context string) bool {
	if !check.conf.Logos {
		return false
	}
	targets, tOK := tupleElemsOf(T)
	if !tOK {
		return false
	}
	sources, sOK := tupleElemsOf(x.typ())
	if !sOK {
		return false
	}
	if len(targets) != len(sources) {
		check.errorf(x, IncompatibleAssign, "cannot assign %s to %s in %s (arity mismatch)", x.typ(), T, context)
		x.invalidate()
		return true
	}
	if lit, ok := syntax.Unparen(x.expr).(*syntax.TupleLit); ok && len(lit.Elems) == len(targets) {
		for i, elem := range lit.Elems {
			var op operand
			if !check.logosLambdaDefer(&op, elem) {
				check.expr(nil, &op, elem)
			}
			check.assignment(&op, targets[i], context)
		}
		x.typ_ = T
		check.record(x)
		return true
	}
	for i := range targets {
		op := operand{mode_: value, expr: x.expr, typ_: sources[i]}
		check.assignment(&op, targets[i], context)
	}
	x.typ_ = T
	check.record(x)
	return true
}

func tupleElemOperand(rhs syntax.Expr, i int, typ Type) operand {
	if lit, ok := syntax.Unparen(rhs).(*syntax.TupleLit); ok && i < len(lit.Elems) {
		return operand{mode_: value, expr: lit.Elems[i], typ_: typ}
	}
	elem := &syntax.ParenExpr{X: rhs}
	elem.SetPos(rhs.Pos())
	return operand{mode_: value, expr: elem, typ_: typ}
}

func (check *Checker) tupleCallResults(e *syntax.CallExpr) ([]string, []Type, bool) {
	var fop operand
	check.expr(nil, &fop, e.Fun)
	sig, ok := fop.typ().(*Signature)
	if !ok || sig.results.Len() < 2 {
		return nil, nil, false
	}
	names := make([]string, sig.results.Len())
	types := make([]Type, sig.results.Len())
	for i := range names {
		names[i] = sig.results.vars[i].name
		types[i] = sig.results.vars[i].typ
	}
	return names, types, true
}

func (check *Checker) logosTuplePack(lhs []*Var, rhs []syntax.Expr, results []*operand, context string) bool {
	if !check.conf.Logos || len(lhs) != 1 || len(results) < 2 {
		return false
	}
	if _, ok := syntax.Unparen(rhs[0]).(*syntax.CallExpr); !ok {
		return false
	}
	v := lhs[0]
	if v.typ == nil {
		names, types, ok := check.tupleCallResults(syntax.Unparen(rhs[0]).(*syntax.CallExpr))
		if !ok || len(types) != len(results) {
			return false
		}
		v.typ = NewLogosTuple(rhs[0].Pos(), check.pkg, names, types)
	}
	targets, ok := tupleElemsOf(v.typ)
	if !ok || len(targets) != len(results) {
		return false
	}
	for i, r := range results {
		check.assignment(r, targets[i], context)
	}
	return true
}

func (check *Checker) logosTupleInitVars(lhs []*Var, orig_rhs []syntax.Expr, rhs []*operand, context string) bool {
	if !check.conf.Logos {
		return false
	}
	if len(lhs) == 1 && len(rhs) >= 2 {
		return check.logosTuplePack(lhs, orig_rhs, rhs, context)
	}
	if len(rhs) == 1 && len(lhs) >= 2 {
		elems, ok := tupleElemsOf(rhs[0].typ())
		if !ok || len(elems) != len(lhs) {
			return false
		}
		for i, v := range lhs {
			el := tupleElemOperand(orig_rhs[0], i, elems[i])
			check.initVar(v, &el, context)
		}
		return true
	}
	return false
}

func (check *Checker) logosTupleAssignStmt(lhs []syntax.Expr, orig_rhs []syntax.Expr, rhs []*operand, context string) bool {
	if !check.conf.Logos {
		return false
	}
	if len(lhs) == 1 && len(rhs) >= 2 {
		call, ok := syntax.Unparen(orig_rhs[0]).(*syntax.CallExpr)
		if !ok {
			return false
		}
		_, types, ok := check.tupleCallResults(call)
		if !ok || len(types) != len(rhs) {
			return false
		}
		target := check.tupleAssignTarget(lhs[0])
		if target == nil {
			return false
		}
		targets, ok := tupleElemsOf(target)
		if !ok || len(targets) != len(rhs) {
			return false
		}
		for i, r := range rhs {
			check.assignment(r, targets[i], context)
		}
		return true
	}
	if len(rhs) == 1 && len(lhs) >= 2 {
		elems, ok := tupleElemsOf(rhs[0].typ())
		if !ok || len(elems) != len(lhs) {
			return false
		}
		for i, l := range lhs {
			el := tupleElemOperand(orig_rhs[0], i, elems[i])
			check.assignVar(l, orig_rhs[0], &el, context)
		}
		return true
	}
	return false
}

func (check *Checker) tupleAssignTarget(lhs syntax.Expr) Type {
	ident, ok := syntax.Unparen(lhs).(*syntax.Name)
	if !ok || ident.Value == "_" {
		return nil
	}
	if v, ok := check.lookup(ident.Value).(*Var); ok {
		return v.typ
	}
	return nil
}

func (check *Checker) logosTupleNilCheck(lhs *Var, x *operand, context string) bool {
	if !check.conf.Logos || lhs.typ != nil || !x.isValid() {
		return false
	}
	elems, ok := tupleElemsOf(x.typ())
	if !ok {
		return false
	}
	for i := range elems {
		if elems[i] == Typ[UntypedNil] {
			check.errorf(x, UntypedNilUse, "use of untyped nil in tuple literal in %s", context)
			lhs.typ = Typ[Invalid]
			x.invalidate()
			return true
		}
	}
	return false
}

func (check *Checker) logosTupleVarSpec(obj *Var, init syntax.Expr) bool {
	if !check.conf.Logos {
		return false
	}
	call, ok := syntax.Unparen(init).(*syntax.CallExpr)
	if !ok {
		return false
	}
	if obj.typ != nil {
		if _, ok := tupleElemsOf(obj.typ); !ok {
			return false
		}
	}
	rhs, _ := check.multiExpr(init, false)
	if len(rhs) < 2 {
		return false
	}
	if obj.typ == nil {
		names, types, ok := check.tupleCallResults(call)
		if !ok || len(types) != len(rhs) {
			return false
		}
		obj.typ = NewLogosTuple(init.Pos(), check.pkg, names, types)
	}
	targets, ok := tupleElemsOf(obj.typ)
	if !ok || len(targets) != len(rhs) {
		return false
	}
	for i, r := range rhs {
		check.assignment(r, targets[i], "variable declaration")
	}
	return true
}

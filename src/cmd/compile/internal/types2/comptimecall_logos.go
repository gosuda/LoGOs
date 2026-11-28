// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	"go/constant"
	. "internal/types/errors"
)

// LoGo/s M7-S2/M7-S3 comptime call positions (SPEC Sec 2.5, T-205).
// callExpr calls in through one hook line. Values of the virtual
// package exist only at compile time: Fields serves const-for ranges
// (checked by the const-for checker, never here), type-kind predicates serve
// `const if` conditions (folded here), and every other call position
// reports [comptime::runtime_use], except a live comptime.Error, which
// fails compilation with its message.

// logosPredState is the `const if` condition context: depth counts the
// condition expressions being checked, and calls holds the predicate
// calls whose answer waits for an instantiation.
type logosPredState struct {
	depth int
	calls map[*syntax.CallExpr]bool
}

// logosComptimeCall vets one call for compile-time-only placement. It
// reports whether it answered the call itself: a type-kind predicate in
// a `const if` condition becomes a constant (or a deferred value inside
// a comptime generic). Every other call keeps the normal check, so the
// arguments and results are still typed; Error messages go out through a
// deferred check that reads the checked constant, keeping error matching
// per-line. ViewAs keeps the normal check under its own M7-S5
// validation, and unknown names keep the upstream undefined-selector
// diagnostic.
func (check *Checker) logosComptimeCall(x *operand, call *syntax.CallExpr) bool {
	sel, fname, ok := check.logosComptimeQualifier(call)
	if !ok {
		return false
	}
	if check.logosComptimeCallee == nil {
		check.logosComptimeCallee = make(map[*syntax.SelectorExpr]bool)
	}
	check.logosComptimeCallee[sel] = true
	check.recordUse(sel.Sel, check.logosComptimeObj(fname))
	if name, ok := sel.X.(*syntax.Name); ok {
		if _, obj := check.lookupScope(name.Value); obj != nil {
			check.recordUse(name, obj)
			if pn, ok := obj.(*PkgName); ok {
				check.usedPkgNames[pn] = true
			}
		}
	}
	switch {
	case (fname == "IsStruct" || fname == "IsInterface") && check.logosPred.depth > 0 && check.logosTypeKind(x, call, fname):
		return true
	case isLogosLayoutName(fname) && check.logosLayoutCall(x, call, fname):
		return true
	case fname == "TypeOf" && check.logosTypeOfCall(x, call):
		return true
	case fname == "Fields", fname == "IsStruct", fname == "IsInterface", fname == "TypeOf",
		fname == "SizeOf", fname == "AlignOf", fname == "HasPointers",
		fname == "HasUnexported":
		check.errorf(syntax.StartPos(call), InvalidPkgUse, "[comptime::runtime_use] comptime.%s is compile-time only", fname)
	// Invariant (deferred error): Error messages go out through a
	// deferred check reading the checked constant, keeping per-line matching.
	case fname == "Error":
		check.logosComptimeError(call)
	}
	return false
}

func (check *Checker) logosComptimeValueUse(e *syntax.SelectorExpr, obj Object) {
	fname, ok := logosComptimeFunc(obj)
	if !ok || fname == "ViewAs" || check.logosComptimeCallee[e] {
		return
	}
	check.errorf(syntax.StartPos(e), InvalidPkgUse, "[comptime::runtime_use] comptime.%s is compile-time only", fname)
}

// logosTypeKind answers IsStruct and IsInterface in a const-if condition.
// A concrete X gives an untyped boolean constant. A type parameter X,
// even under transparent const-view or read-only wrappers, and the field
// type of a const for over one, give a boolean value that is not a
// constant: the answer is per instantiation, so the condition is
// deferred to the copies. It reports false for any other call shape,
// which the normal check then rejects.
func (check *Checker) logosTypeKind(x *operand, call *syntax.CallExpr, name string) bool {
	idx, ok := call.Fun.(*syntax.IndexExpr)
	if !ok || len(call.ArgList) != 0 {
		return false
	}
	targs := syntax.UnpackListExpr(idx.Index)
	if len(targs) != 1 {
		return false
	}
	targ := check.varType(targs[0])
	// A read-only view names its element: `const T` depends on the
	// parameter T, so a predicate on it waits for instantiation like a
	// bare T does. Deciding on the wrapper would fold the constraint
	// interface for IsInterface, or miss the struct for IsStruct, before
	// any copy sees a concrete type.
	for {
		u := Unalias(targ)
		if cv, ok := u.(*ConstView); ok {
			targ = cv.Elem
			continue
		}
		if rd, ok := u.(*ReadonlyDescendants); ok {
			targ = rd.Elem
			continue
		}
		break
	}
	x.expr = call
	switch {
	case !isValid(targ):
		x.invalidate()
	case isTypeParam(logosTypeKindBase(targ)) || check.logosGenericFieldType(targs[0]):
		x.mode_ = value
		x.typ_ = Typ[Bool]
		if check.logosPred.calls == nil {
			check.logosPred.calls = make(map[*syntax.CallExpr]bool)
		}
		check.logosPred.calls[call] = true
	default:
		matches := false
		switch name {
		case "IsStruct":
			_, matches = targ.Underlying().(*Struct)
		case "IsInterface":
			matches = logosInterfaceType(targ)
		default:
			panic("unexpected comptime type-kind predicate")
		}
		x.mode_ = constant_
		x.typ_ = Typ[UntypedBool]
		x.val = constant.MakeBool(matches)
	}
	return true
}

// logosTypeKindBase strips the transparent wrappers a type-kind predicate
// sees through — const views, read-only descendants, and options — to the
// type that decides whether the verdict waits for an instantiation. A type
// parameter under those wrappers still defers, so the verdict follows the
// instantiated type argument instead of folding against its constraint.
func logosTypeKindBase(t Type) Type {
	for {
		switch u := Unalias(t).(type) {
		case *ConstView:
			t = u.Elem
		case *ReadonlyDescendants:
			t = u.Elem
		case *Option:
			t = u.Elem
		default:
			return t
		}
	}
}

// Read-only wrappers change access, not the interface value's dynamic type. An
// option the lowering keeps bare erases to its payload, so the predicate
// answers for the payload; a wrapped option is a struct, not an interface.
func logosInterfaceType(t Type) bool {
	for {
		switch u := Unalias(t).Underlying().(type) {
		case *ConstView:
			t = u.Elem
		case *ReadonlyDescendants:
			t = u.Elem
		case *Option:
			if !LogosOptionIsBare(u.Elem) {
				return false
			}
			t = u.Elem
		case *Interface:
			return true
		default:
			return false
		}
	}
}

func (check *Checker) logosTypeOfCall(x *operand, call *syntax.CallExpr) bool {
	if !check.logosComptimeInGeneric() && check.logosCtSubst == nil {
		return false
	}
	idx, ok := call.Fun.(*syntax.IndexExpr)
	if !ok || len(call.ArgList) != 0 {
		return false
	}
	targs := syntax.UnpackListExpr(idx.Index)
	if len(targs) != 1 {
		return false
	}
	targ := check.varType(targs[0])
	x.expr = call
	if !isValid(targ) {
		x.invalidate()
		return true
	}
	obj := check.logosComptimeObj("Type")
	tn, ok := obj.(*TypeName)
	if !ok || tn.Type() == nil {
		return false
	}
	x.mode_ = value
	x.typ_ = tn.Type()
	if isTypeParam(targ) {
		if check.logosTypeOfDeferred == nil {
			check.logosTypeOfDeferred = make(map[syntax.Expr]bool)
		}
		check.logosTypeOfDeferred[call.Fun] = true
	}
	if check.logosCtSubst != nil && !isTypeParam(targ) {
		if check.logosTypeOfFact == nil {
			check.logosTypeOfFact = make(map[syntax.Expr]Type)
		}
		check.logosTypeOfFact[call] = targ
	}
	return true
}

// logosPredOnly reports whether every non-constant leaf of the condition
// e is one of the deferred predicate calls. The connectives `!`, `&&`,
// `||`, `==`, `!=` and parentheses are looked through; a constant leaf
// is a typed constant recorded in Info.Types or an untyped constant
// still waiting in the untyped table. Short-circuit constants do not
// fold here: `true || p` stays deferred and the copies fold it per
// instantiation, so no constant branch is ever proven from a deferred
// predicate.
func (check *Checker) logosPredOnly(e syntax.Expr, calls map[*syntax.CallExpr]bool) bool {
	switch e := e.(type) {
	case *syntax.ParenExpr:
		return check.logosPredOnly(e.X, calls)
	case *syntax.Operation:
		switch e.Op {
		case syntax.Not, syntax.AndAnd, syntax.OrOr, syntax.Eql, syntax.Neq:
			return check.logosPredOnly(e.X, calls) && (e.Y == nil || check.logosPredOnly(e.Y, calls))
		}
	case *syntax.CallExpr:
		if calls[e] {
			return true
		}
	}
	return check.logosConstLeaf(e)
}

// logosChecked returns the type and constant value recorded for e: the
// Info.Types entry when that map exists, else the syntax-node store the
// compiler fills (Info.TypeOf reads the same two places). ok is false
// when nothing is recorded.
func (check *Checker) logosChecked(e syntax.Expr) (Type, constant.Value, bool) {
	if m := check.Info.Types; m != nil {
		tv, ok := m[e]
		return tv.Type, tv.Value, ok
	}
	if check.Info.StoreTypesInSyntax {
		if tv := e.GetTypeInfo(); tv.Type != nil {
			return tv.Type, tv.Value, true
		}
	}
	return nil, nil, false
}

// logosConstLeaf reports whether e was checked to a constant.
func (check *Checker) logosConstLeaf(e syntax.Expr) bool {
	if _, val, ok := check.logosChecked(e); ok && val != nil {
		return true
	}
	info, ok := check.untyped[e]
	return ok && info.val != nil
}

// logosGenericFieldType reports whether e is the f.Type of a const for
// field variable whose loop is not being checked per concrete field: its
// type is then only approximated, so a predicate on it cannot answer.
func (check *Checker) logosGenericFieldType(e syntax.Expr) bool {
	sel, ok := e.(*syntax.SelectorExpr)
	if !ok || sel.Sel.Value != "Type" {
		return false
	}
	name, ok := sel.X.(*syntax.Name)
	if !ok {
		return false
	}
	_, obj := check.lookupScope(name.Value)
	v, ok := obj.(*Var)
	if !ok || check.logosFieldVars[v] == nil {
		return false
	}
	return check.logosFieldBindingOf(v) == nil
}

// logosComptimeError defers a comptime.Error call: the message is read
// from the checked argument. A call inside a generic function is left
// for the per-instantiation expansion (M7-S3 lowering reports it); a
// call in a dead const-if branch (M7-S1) is silent; any other live call
// with a constant message fails with that message, and a
// non-constant message fails as not constant.
// logosComptimeInGeneric reports whether the call sits inside a generic
// function declaration: only declarations with a function type-parameter
// list get per-instantiation copies, so only those defer and the copies
// answer. A method of a generic type with no function type parameters
// is never copied, so a call there answers eagerly at the first check
// and the diagnostic cannot vanish. It reads the declaration stack, not
// a global, so lowered rechecks (fresh checkers sharing Info) decide
// from their own concrete copies.
func (check *Checker) logosComptimeInGeneric() bool {
	// Invariant (instance): a concrete instantiation analysis answers
	// comptime calls directly; its signature has no type parameters.
	if check.logosCtSubst != nil {
		return false
	}
	// Invariant (innermost decl): the current signature's own function
	// type parameters defer the call to per-instantiation copies.
	if check.sig != nil && check.sig.TypeParams().Len() > 0 {
		return true
	}
	for _, sig := range check.logosFuncSigs {
		// Invariant (enclosing decl): an outer generic function
		// declaration defers the call too; its copy answers per
		// instantiation.
		if sig != nil && sig.TypeParams().Len() > 0 {
			return true
		}
	}
	// Invariant (concrete): no enclosing function type parameters means
	// the first check answers directly.
	return false
}

func (check *Checker) logosComptimeError(call *syntax.CallExpr) {
	// Invariant (deferral): generic instantiations answer per copy.
	generic := check.logosComptimeInGeneric()
	// Invariant (dead silence): M7-S1 dead-branch membership is the
	// first-check Info proof, shared with lowered rechecks.
	dead := check.logosDead(check.logosCurStmt) || check.logosCtDeclCheck
	// Invariant (switch-tail silence): in an instance outside a const-for
	// iteration, an Error after a const switch whose selected case
	// terminates cannot be reached, so it stays silent while the tail
	// itself is still type-checked for other errors. The verdict is
	// recorded for lowering, which erases exactly the calls the
	// instance check silenced instead of redoing termination analysis.
	switchTail := check.logosErrorAfterTerminatingSwitch()
	var arg syntax.Expr
	if len(call.ArgList) == 1 {
		arg = call.ArgList[0]
	}
	if switchTail && arg != nil && !generic && !dead {
		if check.Info.LogosCtSilentError == nil {
			check.Info.LogosCtSilentError = make(map[*syntax.CallExpr]bool)
		}
		check.Info.LogosCtSilentError[call] = true
	}
	check.later(func() {
		// Invariant (no double report): malformed, generic-deferred,
		// or dead-branch calls stay silent here.
		if arg == nil || generic || dead {
			return
		}
		typ, val, ok := check.logosChecked(arg)
		// Invariant (typed-mirror): untyped or non-string arguments
		// keep the normal check's diagnostic, never a second one.
		if !ok || !isValid(typ) || !isString(typ) {
			return
		}
		// Invariant (constant obligation): a live non-constant message
		// fails as not constant without weakening the text. The tail
		// rule silences only the requested message, never this check.
		if val == nil || val.Kind() != constant.String {
			check.error(arg.Pos(), InvalidConstInit, "[comptime::error_not_constant] comptime.Error needs a constant message")
			return
		}
		// Invariant (switch-tail silence): a constant message after a
		// terminating const switch stays silent.
		if switchTail {
			return
		}
		// Invariant (live error): a live constant message fails with its text.
		// Inside an instance analysis the diagnostic points at the
		// instantiation site so errorcheck fixtures name the caller.
		at := arg.Pos()
		if check.logosCtSubst != nil && len(check.logosCtPos) > 0 {
			at = check.logosCtPos[len(check.logosCtPos)-1]
		}
		check.errorf(at, InvalidConstInit, "[comptime::error] %s", constant.StringVal(val))
	}).describef(call, "comptime.Error %s", call.Pos())
}

// logosErrorAfterTerminatingSwitch reports whether the Error call under
// check sits after a const switch whose selected case terminates, in an
// instance outside a const-for iteration. The switch and the Error share
// one statement list: a block or a switch, select, or const-switch clause
// body. The tail itself is still type-checked for other errors; only the
// Error is silent. Labels between the switch and the Error keep it live,
// since a jump can reach it.
func (check *Checker) logosErrorAfterTerminatingSwitch() bool {
	if check.logosCtSubst == nil || check.logosCtDeclCheck || check.logosFieldBind != nil {
		return false
	}
	fdecl := check.logosCtFuncDecl
	cur := check.logosCurStmt
	if fdecl == nil || cur == nil || fdecl.Body == nil {
		return false
	}
	var list []syntax.Stmt
	var found bool
	at := func(stmts []syntax.Stmt) {
		if found {
			return
		}
		for _, s := range stmts {
			if s == cur {
				list = stmts
				found = true
				return
			}
		}
	}
	syntax.Inspect(fdecl.Body, func(n syntax.Node) bool {
		if found {
			return false
		}
		switch t := n.(type) {
		case *syntax.BlockStmt:
			at(t.List)
		case *syntax.CaseClause:
			at(t.Body)
		case *syntax.CommClause:
			at(t.Body)
		case *syntax.ConstSwitchStmt:
			for _, c := range t.Cases {
				at(c.Body)
			}
		}
		return !found
	})
	if !found {
		return false
	}
	idx := -1
	for i, s := range list {
		if s == cur {
			idx = i
			break
		}
	}
	if idx <= 0 {
		return false
	}
	for i := idx - 1; i >= 0; i-- {
		if _, ok := list[i].(*syntax.LabeledStmt); ok {
			return false
		}
		if cs, ok := list[i].(*syntax.ConstSwitchStmt); ok {
			// An earlier terminating switch already keeps control from
			// reaching the tail, so a nearer switch that does not
			// terminate must not erase it. Keep scanning past it.
			if check.logosTerminatingConstSwitch(cs) {
				return true
			}
			continue
		}
		if check.isTerminating(list[i], "") {
			return false
		}
	}
	return false
}

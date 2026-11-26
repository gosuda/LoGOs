// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	. "internal/types/errors"
)

const (
	logosUnreachableCase = "[comptime::unreachable_case]"
	logosSwitchNotType   = "[comptime::switch_not_type]"
	logosCaseNotType     = "[comptime::case_not_type]"
	logosTildeNotBasic   = "[comptime::tilde_not_basic]"
)

type logosCaseKind int

const (
	logosCaseExact logosCaseKind = iota
	logosCaseTilde
	logosCaseIface
	logosCaseDefault
)

type logosCaseInfo struct {
	syn   *syntax.ConstSwitchCase
	elem  int // index into syn.Elems; -1 for the default arm
	epos  syntax.Pos
	typ   Type
	kind  logosCaseKind
	tilde bool
	tpar  *TypeParam
	pend  bool // case type is an unbound f.Type: unknown until instantiation
	vary  bool // case type is concrete in this check but varies across checks: an outer loop's bound f.Type, or a type parameter an instance substituted
}

type LogosConstCase struct {
	Live     bool
	Target   *TypeName
	Vars     map[*Var]Type
	Switched *TypeParam
}

type logosIfaceCase struct {
	recv  Type
	iface *Interface
	typ   Type
	syn   *syntax.ConstSwitchCase
	elem  int
}

// LogosConstCaseKey names one arm of a const switch: Elem indexes the
// case's Elems, or is -1 for the default arm.
type LogosConstCaseKey struct {
	Case *syntax.ConstSwitchCase
	Elem int
}

// LogosConstCaseRef names the arm an interface-case use was checked
// under: Elem indexes the case's Elems.
type LogosConstCaseRef struct {
	Case *syntax.ConstSwitchCase
	Elem int
}

func (check *Checker) logosConstSwitch(ctxt stmtContext, s *syntax.ConstSwitchStmt) {
	if check.logosCtDeclCheck {
		check.logosNoteDeadUses(s)
		return
	}
	tag := check.logosConstTag(s.Targ)
	if tag == nil {
		return
	}
	if check.Info.LogosConstSwitchScope == nil {
		check.Info.LogosConstSwitchScope = make(map[*syntax.ConstSwitchStmt]LogosConstSwitchCtx)
	}
	check.Info.LogosConstSwitchScope[s] = LogosConstSwitchCtx{Scope: check.scope, Sig: check.environment.sig, Tag: tag, Version: check.versions[s.Pos().Base()]}
	// A tag resolved through a const-for field binding is concrete for
	// this iteration only: cases that cannot match it are dead for the
	// iteration, like copy-evaluation cases, not authoring errors.
	fieldTag := check.logosFieldBind != nil && check.logosFieldBound[s.Targ]
	// A tag resolved through an unbound field type, or through a field
	// of an enclosing outer loop, is the comptime any or varies per
	// outer iteration: reachability defers to the instantiations that
	// bind it.
	owner := check.logosFieldTypeOwner[syntax.Unparen(s.Targ)]
	tagPend := check.logosFieldTypePend(owner)
	narrow := owner
	// An instance, copy or field iteration knows the tag: only the first
	// matching case is checked, and every other case is marked dead so
	instance := (check.logosCtSubst != nil || check.logosCopy || check.logosFieldBind != nil) && !isTypeParam(tag) && !tagPend
	bound := fieldTag || instance && check.logosCtSubst != nil
	// A declaration with a type-parameter tag dispatches at run time: every
	// live element is checked, each under its own binding.
	deadFlag := fieldTag || instance
	inner := ctxt &^ (fallthroughOk | finalSwitchCase | inTypeSwitch)
	var seen []logosCaseInfo
	for _, c := range s.Cases {
		if c.Default {
			check.logosConstDefault(inner, s, c, tag, seen, bound, deadFlag, fieldTag, tagPend, instance)
			seen = append(seen, logosCaseInfo{syn: c, elem: -1, epos: c.Case, kind: logosCaseDefault})
			continue
		}
		var infos []*logosCaseInfo
		for i := range c.Elems {
			infos = append(infos, check.logosConstCaseElem(c, i, tag))
		}
		live := check.logosLiveElems(c, infos, &seen, tag, bound, deadFlag, fieldTag, tagPend)
		if len(live) == 0 {
			// Every element is dead. A case whose every element is
			// covered by earlier cases is a shadowed case; elements
			// that are only dead for this copy or iteration stay
			// silent, like a dead single-type case.
			// A copy repeats a checked declaration and reports nothing.
			if !check.logosCopy && check.logosAllCovered(infos, seen, tag, bound, fieldTag, tagPend) {
				check.errorf(c.Elems[0].Type, InvalidSyntaxTree, "%s const switch case is shadowed by an earlier case", logosUnreachableCase)
			} else if instance {
				check.logosCtMarkDead(c.Body...)
				check.logosNoteDeadUses(c.Body...)
			}
			continue
		}
		for _, info := range live {
			check.logosConstCaseBody(inner, c, info, c.Body, tag, narrow)
		}
	}
}

// logosLiveElems answers which elements of a case are checked: an element
// with no type stays out, and so does one that an earlier element covers
// or that is dead for this copy or iteration. Live elements join seen in
// order, so later elements see earlier ones.
func (check *Checker) logosLiveElems(c *syntax.ConstSwitchCase, infos []*logosCaseInfo, seen *[]logosCaseInfo, tag Type, bound, deadFlag, fieldTag, tagPend bool) []*logosCaseInfo {
	var live []*logosCaseInfo
	for _, info := range infos {
		if info == nil {
			continue
		}
		if check.logosCopy || !check.logosCaseCovered(info, *seen, tag, bound, tagPend) {
			*seen = append(*seen, *info)
			if !check.logosDeadCopyCase(c, tag, info, deadFlag, (*seen)[:len(*seen)-1]) {
				live = append(live, info)
				continue
			}
			*seen = (*seen)[:len(*seen)-1]
		}
	}
	return live
}

// logosAllCovered answers whether every typed element of a case is covered
// by earlier cases. An element without a type cannot decide coverage, so a
// case holding one never reports a shadow.
func (check *Checker) logosAllCovered(infos []*logosCaseInfo, seen []logosCaseInfo, tag Type, bound, fieldTag, tagPend bool) bool {
	typed := false
	for _, info := range infos {
		if info == nil {
			return false
		}
		typed = true
		if !check.logosCaseCovered(info, seen, tag, bound, tagPend) {
			return false
		}
	}
	return typed
}

// logosConstDefault checks the default arm: it matches every tag, so it is
// covered exactly when an earlier default or an empty interface case
// covers it, and it is dead for a copy or iteration an earlier element
// already serves. The arm never rebinds: the tag keeps its identity and
// gains no method set.
func (check *Checker) logosConstDefault(ctxt stmtContext, s *syntax.ConstSwitchStmt, c *syntax.ConstSwitchCase, tag Type, seen []logosCaseInfo, bound, deadFlag, fieldTag, tagPend bool, instance bool) {
	info := &logosCaseInfo{syn: c, elem: -1, epos: c.Case, kind: logosCaseDefault}
	covered := check.logosCaseCovered(info, seen, tag, bound, tagPend)
	// A copy repeats a checked declaration and reports nothing.
	if !check.logosCopy && covered {
		check.errorf(c.Case, InvalidSyntaxTree, "%s const switch case is shadowed by an earlier case", logosUnreachableCase)
		return
	}
	if check.logosCopy || !covered {
		prior := append(seen, *info)
		if !check.logosDeadCopyCase(c, tag, info, deadFlag, prior[:len(prior)-1]) {
			inner := ctxt &^ (fallthroughOk | finalSwitchCase | inTypeSwitch)
			if inner&continueOk != 0 {
				inner |= logosConstSwitchContinue
			}
			inner &= ^(breakOk | continueOk)
			check.openScope(s, "const switch case")
			defer check.closeScope()
			check.stmtList(inner, c.Body)
			return
		}
	}
	if instance {
		check.logosCtMarkDead(c.Body...)
		check.logosNoteDeadUses(c.Body...)
	}
}

// logosDeadCopyCase answers whether an element is dead for the current
// copy or field iteration: when the tag's type is known, only the first
// matching element ever runs it.
func (check *Checker) logosDeadCopyCase(c *syntax.ConstSwitchCase, tag Type, info *logosCaseInfo, deadFlag bool, seen []logosCaseInfo) bool {
	if !check.logosCopy && !deadFlag {
		return false
	}
	if _, ok := tag.(*TypeParam); ok {
		return false
	}
	for i := range seen {
		// A pending case resolves per iteration; it neither kills a
		// later case nor is killed by an earlier one statically.
		if seen[i].pend {
			continue
		}
		if logosConstElemMatches(tag, seen[i].typ, seen[i].tilde, seen[i].kind == logosCaseDefault) {
			return true
		}
	}
	if info.pend {
		return false
	}
	return !logosConstElemMatches(tag, info.typ, info.tilde, info.kind == logosCaseDefault)
}

// logosFieldTypeSel admits e as a type when it is f.Type of a const-for
// field variable, the same admission type positions get.
func (check *Checker) logosFieldTypeSel(e syntax.Expr, x *operand) bool {
	sel, ok := syntax.Unparen(e).(*syntax.SelectorExpr)
	return ok && check.logosFieldTypeType(sel, x)
}

func (check *Checker) logosConstTag(e syntax.Expr) Type {
	var x operand
	if !check.logosFieldTypeSel(e, &x) {
		check.exprOrType(&x, e, false)
	}
	if !x.isValid() {
		return nil
	}
	if x.mode() != typexpr {
		check.errorf(e, InvalidSyntaxTree, "%s const switch needs a type, not a value", logosSwitchNotType)
		return nil
	}
	check.record(&x)
	return x.typ()
}

func (check *Checker) logosConstCaseElem(c *syntax.ConstSwitchCase, elem int, tag Type) *logosCaseInfo {
	e := &c.Elems[elem]
	var x operand
	if !check.logosFieldTypeSel(e.Type, &x) {
		check.exprOrType(&x, e.Type, true)
	}
	if !x.isValid() {
		return nil
	}
	if x.mode() != typexpr {
		check.errorf(e.Type, InvalidSyntaxTree, "%s const switch case needs a type, not a value", logosCaseNotType)
		return nil
	}
	check.record(&x)
	owner := check.logosFieldTypeOwner[syntax.Unparen(e.Type)]
	info := &logosCaseInfo{syn: c, elem: elem, epos: e.Type.Pos(), typ: x.typ(), tilde: e.Tilde, pend: check.logosFieldTypePend(owner), vary: check.logosFieldTypeVary(owner) || check.logosCtParamCase(e.Type)}
	if tpar, ok := tag.(*TypeParam); ok {
		info.tpar = tpar
	}
	info.kind = logosConstCaseKind(x.typ(), e.Tilde)
	if info.kind == logosCaseTilde {
		base, ok := underBasic(x.typ())
		if !ok {
			check.errorf(e.TildePos, InvalidSyntaxTree, "%s const switch tilde case needs a basic type, not %s", logosTildeNotBasic, x.typ())
			return nil
		}
		info.typ = base
	}
	return info
}

// logosCtParamCase answers whether case type e names a type parameter
// of the comptime instance being checked: the instance resolves it to
// the argument, while the declaration's case is the parameter.
func (check *Checker) logosCtParamCase(e syntax.Expr) bool {
	if check.logosCtSubst == nil {
		return false
	}
	found := false
	syntax.Inspect(e, func(n syntax.Node) bool {
		if name, ok := n.(*syntax.Name); ok && check.logosCtBinder(check.Uses[name]) {
			found = true
		}
		return !found
	})
	return found
}

// logosCtBinder answers whether obj is the name logosCtCheckBody binds
// to a type argument. The binder sits in the instance's parameter scope
// with no parent link, while every declared type name has one.
func (check *Checker) logosCtBinder(obj Object) bool {
	tn, ok := obj.(*TypeName)
	if !ok || tn.Parent() != nil {
		return false
	}
	for tpar, arg := range check.logosCtSubst {
		if tpar.Obj().Name() == tn.Name() && tn.Type() == arg {
			return true
		}
	}
	return false
}

func logosConstCaseKind(typ Type, tilde bool) logosCaseKind {
	switch {
	case tilde:
		return logosCaseTilde
	case isIfaceCase(typ):
		return logosCaseIface
	default:
		return logosCaseExact
	}
}

func LogosConstSelectedCase(info *Info, s *syntax.ConstSwitchStmt) (sel *syntax.ConstSwitchCase, static bool) {
	sel, _, static = LogosConstSelectedElem(info, s)
	return sel, static
}

// LogosConstSelectedElem answers which arm a const switch takes for a
// known tag: the first case in source order holding the tag, the first
// element of a type list that matches, or the default arm when nothing
// matches. elem indexes the case's Elems, or is -1 for the default arm
// and when nothing matches.
func LogosConstSelectedElem(info *Info, s *syntax.ConstSwitchStmt) (sel *syntax.ConstSwitchCase, elem int, static bool) {
	tag := logosConstTypeOf(info, s.Targ)
	if tag == nil {
		return nil, -1, false
	}
	if isTypeParam(tag) {
		return nil, -1, false
	}
	for _, c := range s.Cases {
		if c.Default {
			return c, -1, true
		}
		for i := range c.Elems {
			typ := logosConstTypeOf(info, c.Elems[i].Type)
			if typ == nil {
				return nil, -1, false
			}
			if logosConstElemMatches(tag, typ, c.Elems[i].Tilde, false) {
				return c, i, true
			}
		}
	}
	return nil, -1, true
}

func logosConstTypeOf(info *Info, e syntax.Expr) Type {
	if typ := info.TypeOf(e); typ != nil {
		return typ
	}
	if sel, ok := e.(*syntax.SelectorExpr); ok {
		if tn, ok := info.Uses[sel.Sel].(*TypeName); ok {
			return tn.Type()
		}
	}
	return nil
}

// logosConstElemMatches answers whether tag selects one const switch
// element the way a const switch does: a default element matches every
// tag, then exact, ~basic, or interface match.
func logosConstElemMatches(tag Type, typ Type, tilde bool, isDefault bool) bool {
	if isDefault {
		return true
	}
	switch logosConstCaseKind(typ, tilde) {
	case logosCaseTilde:
		ub, ok := Unalias(tag).Underlying().(*Basic)
		if !ok {
			return false
		}
		cb, ok := underBasic(typ)
		return ok && Identical(ub, cb)
	case logosCaseIface:
		iface, _ := Unalias(typ).Underlying().(*Interface)
		return iface != nil && Implements(tag, iface)
	default:
		return Identical(typ, tag)
	}
}

func isIfaceCase(t Type) bool {
	_, ok := Unalias(t).Underlying().(*Interface)
	return ok
}

func underBasic(t Type) (Type, bool) {
	if b, ok := Unalias(t).Underlying().(*Basic); ok {
		return b, true
	}
	return nil, false
}

func (check *Checker) logosCaseCovered(info *logosCaseInfo, seen []logosCaseInfo, tag Type, fieldTag, tagPend bool) bool {
	if info.pend || info.vary {
		// A pending or varying case resolves only per iteration or
		// instance; whether earlier cases cover it varies with them, so
		// it is never statically covered.
		return false
	}
	if !fieldTag && info.kind == logosCaseExact && !check.logosCaseSatisfiable(info.typ, tag, tagPend) {
		return true
	}
	for i := range seen {
		if check.logosCaseCoveredBy(&seen[i], info) {
			return true
		}
	}
	return false
}

func (check *Checker) logosCaseCoveredBy(prev, info *logosCaseInfo) bool {
	if prev.pend || prev.vary {
		// An unbound or outer-bound f.Type case, or a case that names a
		// substituted type parameter, varies per iteration or instance
		// and covers nothing statically.
		return false
	}
	// A default arm matches every tag, so it covers every later arm.
	if prev.kind == logosCaseDefault {
		return true
	}
	// Only an earlier default or an empty interface covers the default.
	if info.kind == logosCaseDefault {
		return prev.kind == logosCaseIface && prev.iface().Empty()
	}
	switch info.kind {
	case logosCaseExact:
		return check.logosCoversExact(prev, info.typ)
	case logosCaseTilde:
		return logosCoversTilde(prev, info.typ)
	}
	return prev.kind == logosCaseIface && logosMethodSetCovers(prev.iface(), info.iface())
}

func (check *Checker) logosCoversExact(prev *logosCaseInfo, typ Type) bool {
	switch prev.kind {
	case logosCaseExact:
		return Identical(prev.typ, typ)
	case logosCaseTilde:
		return logosUnderlyingIs(typ, prev.typ)
	}
	return check.implements(typ, prev.iface(), false, nil)
}

func logosCoversTilde(prev *logosCaseInfo, typ Type) bool {
	switch prev.kind {
	case logosCaseTilde:
		return Identical(prev.typ, typ)
	case logosCaseIface:
		return prev.iface().Empty()
	}
	return false
}

func (info *logosCaseInfo) iface() *Interface {
	iface, _ := Unalias(info.typ).Underlying().(*Interface)
	return iface
}

func (check *Checker) logosCaseSatisfiable(c, tag Type, tagPend bool) bool {
	if tpar, ok := tag.(*TypeParam); ok {
		return check.implements(c, tpar.iface(), false, nil)
	}
	// An unbound field type is the comptime any: every case stays
	// reachable until an iteration binds it to a field's type. A plain
	// any tag does not defer: cases that cannot match it are dead.
	if tagPend && Identical(tag, logosComptimeAny) {
		return true
	}
	return Identical(c, tag)
}

func logosUnderlyingIs(t, b Type) bool {
	ub, ok := Unalias(t).Underlying().(*Basic)
	return ok && Identical(ub, b)
}

func logosMethodSetCovers(super, sub *Interface) bool {
	if super == nil || sub == nil {
		return false
	}
	for i := range super.NumMethods() {
		m := super.Method(i)
		obj, _, _ := lookupFieldOrMethod(sub, false, nil, m.Name(), false)
		fn, ok := obj.(*Func)
		if !ok || !Identical(fn.Type(), m.Type()) {
			return false
		}
	}
	return true
}

func (check *Checker) logosConstCaseBody(ctxt stmtContext, c *syntax.ConstSwitchCase, info *logosCaseInfo, body []syntax.Stmt, tag Type, narrow *LogosConstFor) {
	// Liveness is distinct from narrowing: an interface case is live even
	// though it introduces no replacement type or variable binding.
	if check.Info.LogosConstCases == nil {
		check.Info.LogosConstCases = make(map[LogosConstCaseKey]LogosConstCase)
	}
	key := LogosConstCaseKey{Case: c, Elem: info.elem}
	check.Info.LogosConstCases[key] = LogosConstCase{Live: true}
	if ctxt&continueOk != 0 {
		ctxt |= logosConstSwitchContinue
	}
	ctxt &= ^(breakOk | continueOk)
	if narrow != nil && !info.pend && info.kind != logosCaseDefault {
		// Inside `case T:` over an unbound f.Type, the field projects
		// as T: the case runs only for fields of that type. A bound
		// field keeps its own type under ~T; an unbound one narrows to
		// a parameter constrained by ~T so conversions to T check while
		// assignments to T still fail until the field binds. The default
		// arm never narrows: the field keeps its own type.
		var ntyp Type
		switch {
		case info.kind != logosCaseTilde:
			ntyp = info.typ
		case Identical(tag, logosComptimeAny):
			tname := NewTypeName(info.epos, check.pkg, "T", nil)
			ntyp = NewTypeParam(tname, NewInterfaceType(nil, []Type{NewUnion([]*Term{NewTerm(true, info.typ)})}))
		}
		if ntyp != nil {
			old, had := check.logosFieldNarrow[narrow]
			if check.logosFieldNarrow == nil {
				check.logosFieldNarrow = make(map[*LogosConstFor]Type)
			}
			check.logosFieldNarrow[narrow] = ntyp
			defer func() {
				if had {
					check.logosFieldNarrow[narrow] = old
				} else {
					delete(check.logosFieldNarrow, narrow)
				}
			}()
		}
	}
	if info.kind == logosCaseIface {
		check.logosCSwitch = append(check.logosCSwitch, logosIfaceCase{recv: tag, iface: info.iface(), typ: info.typ, syn: c, elem: info.elem})
		defer func() { check.logosCSwitch = check.logosCSwitch[:len(check.logosCSwitch)-1] }()
	}
	check.openScope(c.Elems[info.elem].Type, "const switch case")
	defer check.closeScope()
	if tpar, ok := tag.(*TypeParam); ok && info.kind != logosCaseIface && info.kind != logosCaseDefault {
		check.logosShadowConstTarget(tpar, info)
	}
	check.stmtList(ctxt, body)
}

func (check *Checker) logosTerminatingConstSwitch(s *syntax.ConstSwitchStmt) bool {
	sel, static := LogosConstSelectedCase(check.Info, s)
	return static && sel != nil && check.isTerminatingList(sel.Body, "")
}

func (check *Checker) logosHasBreakConstSwitch(s *syntax.ConstSwitchStmt, label string, implicit bool) bool {
	if sel, static := LogosConstSelectedCase(check.Info, s); static {
		return sel != nil && check.hasBreakList(sel.Body, label, implicit)
	}
	for _, c := range s.Cases {
		if check.hasBreakList(c.Body, label, implicit) {
			return true
		}
	}
	return false
}

func (check *Checker) logosShadowConstTarget(tpar *TypeParam, info *logosCaseInfo) {
	pos := info.epos
	bind := info.typ
	var shadow *TypeName
	var switched *TypeParam
	if info.kind == logosCaseTilde {
		// Inside a ~B case the switched type keeps its identity — a nested
		// const switch still dispatches on the actual type argument — but
		// gains the constraint ~B, so a value of a numeric type can be
		// converted to it and variables naming it keep their spelling.
		shadow = NewTypeName(pos, check.pkg, tpar.Obj().Name(), nil)
		bind = NewTypeParam(shadow, NewInterfaceType(nil, []Type{NewUnion([]*Term{NewTerm(true, info.typ)})}))
		switched = tpar
	} else {
		shadow = NewTypeName(pos, check.pkg, tpar.Obj().Name(), bind)
	}
	check.scope.Insert(shadow)
	rec := LogosConstCase{Live: true, Target: shadow, Vars: make(map[*Var]Type), Switched: switched}
	seen := make(map[string]bool)
	for sc := check.scope.Parent(); sc != nil; sc = sc.Parent() {
		for _, objName := range sc.Names() {
			if seen[objName] {
				// A nearer scope already binds this name, so uses inside the
				// case resolve to that object, not to an outer variable.
				continue
			}
			seen[objName] = true
			v, ok := sc.Lookup(objName).(*Var)
			if !ok {
				continue
			}
			sub := check.subst(pos, v.Type(), makeSubstMap([]*TypeParam{tpar}, []Type{bind}), nil, check.context())
			if sub == v.Type() {
				continue
			}
			emit := sub
			if info.kind == logosCaseTilde {
				// The lowered code spells the rebound variable with the
				// basic case type (int, not the narrowed parameter), so the
				// rebinding puns the storage; the tilde match guarantees
				// identical layouts.
				emit = check.subst(pos, v.Type(), makeSubstMap([]*TypeParam{tpar}, []Type{info.typ}), nil, check.context())
			}
			shadowVar := NewVar(v.Pos(), v.Pkg(), v.Name(), sub)
			shadowVar.SetKind(v.Kind())
			if alt := check.scope.Insert(shadowVar); alt != nil {
				continue
			}
			check.usedVars[shadowVar] = true
			rec.Vars[shadowVar] = emit
		}
	}
	if check.Info.LogosConstCases == nil {
		check.Info.LogosConstCases = make(map[LogosConstCaseKey]LogosConstCase)
	}
	check.Info.LogosConstCases[LogosConstCaseKey{Case: info.syn, Elem: info.elem}] = rec
}

// logosConstIfaceAssign admits a value of the switched type where the active
// interface case's type is required: inside `case I:` a value of type T keeps
// its concrete layout and gains I's method set, so it can be stored or passed
// as I (SPEC Sec 2.5). The operand is recorded so the lowerer boxes it as
// rt.Box[I](x) at the use site; the type name T itself is not rebound.
func (check *Checker) logosConstIfaceAssign(x *operand, T Type) bool {
	if !check.conf.Logos || len(check.logosCSwitch) == 0 || x.expr == nil {
		return false
	}
	top := check.logosCSwitch[len(check.logosCSwitch)-1]
	if x.typ() != top.recv || !Identical(T, top.typ) {
		return false
	}
	if check.Info.LogosConstConv == nil {
		check.Info.LogosConstConv = make(map[syntax.Expr]LogosConstCaseRef)
	}
	check.Info.LogosConstConv[x.expr] = LogosConstCaseRef{Case: top.syn, Elem: top.elem}
	return true
}

func (check *Checker) logosConstIfaceSelect(x *operand, e *syntax.SelectorExpr, sel string) bool {
	if !check.conf.Logos || len(check.logosCSwitch) == 0 {
		return false
	}
	top := check.logosCSwitch[len(check.logosCSwitch)-1]
	if x.typ() != top.recv {
		return false
	}
	obj, _, _ := lookupFieldOrMethod(top.iface, false, check.pkg, sel, false)
	fn, ok := obj.(*Func)
	if !ok {
		return false
	}
	check.objDecl(fn)
	sig := *fn.Type().(*Signature)
	sig.recvold = sig.recv
	sig.recv = nil
	x.mode_ = value
	x.typ_ = &sig
	x.expr = e
	check.recordUse(e.Sel, fn)
	if check.Info.LogosConstSel == nil {
		check.Info.LogosConstSel = make(map[*syntax.SelectorExpr]LogosConstCaseRef)
	}
	check.Info.LogosConstSel[e] = LogosConstCaseRef{Case: top.syn, Elem: top.elem}
	return true
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"strings"

	"cmd/compile/internal/syntax"
	. "internal/types/errors"
)

const (
	logosOpenUnion   = "[correctness::open_union_value]"
	logosNonMember   = "[correctness::union_nonmember]"
	logosMemberLacks = "[correctness::union_member_method]"
	logosNonExhaust  = "[correctness::nonexhaustive_switch]"
	logosArmMismatch = "[correctness::switch_arm_mismatch]"
	logosNeedDefault = "[correctness::switch_expr_needs_default]"
	logosAssertFail  = "[correctness::assertion_failed]"
)

func logosIsClosed(tset *_TypeSet) bool {
	if !tset.hasTerms() {
		return false
	}
	for _, t := range tset.terms {
		if t.tilde {
			return false
		}
	}
	return true
}

func logosClosedUnion(t Type) *_TypeSet {
	if t == nil || isTypeParam(t) {
		return nil
	}
	iface, _ := t.Underlying().(*Interface)
	if iface == nil {
		return nil
	}
	tset := iface.typeSet()
	if !logosIsClosed(tset) {
		return nil
	}
	return tset
}

// logosClosedUnionView returns the closed-union term set of t, seeing
// through a const view (or readonly-descendants) qualifier to the
// interface it qualifies. A type switch on a qualified interface reads
// the same dynamic type as on the plain interface, so exhaustiveness
// and member checks use the same terms. Callers that convert or assign
// to a union keep logosClosedUnion: qualifying the target is a
// different obligation from switching on a qualified source.
func logosClosedUnionView(t Type) *_TypeSet {
	if t == nil {
		return nil
	}
	base, _ := logosViewBase(t)
	return logosClosedUnion(base)
}

// logosAssertSource returns the interface a type assertion enumerates
// methods over: a qualified source asserts through the interface it
// views, which carries the method set the view itself does not expose
// as an underlying interface. An unqualified source is unchanged.
func (check *Checker) logosAssertSource(V Type) Type {
	if check == nil || !check.conf.Logos {
		return V
	}
	if base, qualified := logosViewBase(V); qualified {
		return base
	}
	return V
}

// INVARIANT: a type switch is exhaustive over the values its operand can hold
// at that point. A nil case is unreachable when the operand is a local or
// parameter that narrowing has proven non-nil from an option whose absence
// is the interface nil value itself (a bare option): the guard excluded
// exactly the value a nil case would match, and no present-but-nil value can
// exist without a separate presence flag. Assignment ends narrowing,
// address-taken, closure-assigned, and goto-containing functions never
// narrow, so an active narrowing at the switch means no intervening store
// or jump could have restored nil.
func (check *Checker) logosSwitchNilExcused(sx *operand) bool {
	if sx == nil || !sx.isValid() {
		return false
	}
	id, ok := syntax.Unparen(sx.expr).(*syntax.Name)
	if !ok || id.Value == "_" {
		return false
	}
	obj := check.lookup(id.Value)
	v, ok := obj.(*Var)
	if !ok || v.pkg != check.pkg || check.logosNarrow[v] == 0 {
		return false
	}
	opt := logosAsOption(v.typ)
	if opt == nil {
		return false
	}
	return LogosOptionIsBare(opt.Elem)
}

func logosMemberOf(t Type, tset *_TypeSet) bool {
	for _, term := range tset.terms {
		if Identical(term.typ, t) {
			return true
		}
	}
	return false
}

func logosUnionAdmits(V Type, tset *_TypeSet) bool {
	if iface, ok := V.Underlying().(*Interface); ok {
		return iface.typeSet().subsetOf(tset)
	}
	return logosMemberOf(V, tset)
}

func (check *Checker) logosUnionsOK() bool {
	return check.conf.Logos || check.conf.LogosUnions
}

func (check *Checker) logosUnionMethods(ityp *Interface, iface *syntax.InterfaceType) {
	if !check.logosUnionsOK() || ityp.typeSet().NumMethods() == 0 {
		return
	}
	next := 0
	for _, f := range iface.MethodList {
		if f.Name != nil {
			continue
		}
		u, _ := ityp.embeddeds[next].(*Union)
		next++
		if u == nil {
			continue
		}
		_, tlist := flattenUnion(nil, f.Type)
		for i, t := range u.terms {
			if i < len(tlist) {
				check.logosTermMethods(ityp, t, tlist[i])
			}
		}
	}
}

func (check *Checker) logosTermMethods(ityp *Interface, t *Term, at syntax.Expr) {
	if t.tilde || !isValid(t.typ) {
		return
	}
	members := []Type{t.typ}
	if nested, _ := t.typ.Underlying().(*Interface); nested != nil {
		members = members[:0]
		for _, term := range nested.typeSet().terms {
			if !term.tilde {
				members = append(members, term.typ)
			}
		}
	}
	for _, m := range members {
		var cause string
		if !check.hasAllMethods(m, ityp, true, Identical, &cause) {
			check.errorf(at, InvalidUnion, "%s union member %s does not implement the methods of the union %s", logosMemberLacks, m, cause)
		}
	}
}

func (check *Checker) logosValueType(pos syntax.Pos, typ Type, tset *_TypeSet) bool {
	if !check.logosUnionsOK() {
		return false
	}
	switch {
	case logosIsClosed(tset):
		return true
	case tset.hasTerms() || tset.comparable:
		check.softErrorf(pos, MisplacedConstraintIface, "%s %s has ~ terms or an unbounded type set", logosOpenUnion, typ)
		return true
	}
	return false
}

func (check *Checker) logosConversionUnion(T Type) bool {
	return check.logosUnionsOK() && logosClosedUnion(T) != nil
}

func (check *Checker) logosUnionDefault(x *operand, u *Interface) (Type, bool) {
	if !check.logosUnionsOK() || !logosIsClosed(u.typeSet()) {
		return nil, false
	}
	if check.logosReadonlyBoundary(x, u, "union default") {
		return nil, false
	}
	d := Default(x.typ())
	if !check.implements(d, u, false, nil) {
		return nil, false
	}
	return d, true
}

func (check *Checker) logosUnionConversion(x *operand, T Type) bool {
	if !check.logosConversionUnion(T) {
		return false
	}
	if check.logosReadonlyBoundary(x, T, "union conversion") {
		return true
	}
	if ok, _ := x.assignableTo(check, T, nil); ok {
		return false
	}
	from := x.typ()
	if isUntyped(from) {
		from = Default(from)
	}
	check.errorf(x, InvalidConversion, "%s %s is not a member of %s", logosNonMember, from, T)
	x.invalidate()
	return true
}

func (check *Checker) logosNonMemberAssert(e syntax.Expr, x *operand, T Type) bool {
	if !check.conf.Logos {
		return false
	}
	if logosContainsReadonlyDescendants(x.typ()) {
		probe := *x
		if check.logosReadonlyBoundary(&probe, T, "type assertion") && !probe.isValid() {
			x.invalidate()
			return true
		}
	}
	tset := logosClosedUnionView(x.typ())
	if tset == nil || isTypeParam(T) || logosMemberOf(T, tset) {
		return false
	}
	if _, ok := e.(*syntax.AssertExpr); ok {
		return true
	}
	if IsInterface(T) {
		if iface, ok := T.Underlying().(*Interface); ok {
			if ttset := iface.typeSet(); ttset != nil && ttset.hasTerms() && !ttset.subsetOf(tset) {
				check.errorf(e, ImpossibleAssert, "%s %s is not a member of %s", logosNonMember, T, x.typ())
				return true
			}
		}
		return false
	}
	check.errorf(e, ImpossibleAssert, "%s %s is not a member of %s", logosNonMember, T, x.typ())
	return true
}
func (check *Checker) logosUnionAssign(x *operand, T Type, context string) bool {
	if !check.conf.Logos {
		return false
	}
	tset := logosClosedUnion(T)
	if tset == nil {
		return false
	}
	if check.logosReadonlyBoundary(x, T, context) {
		return true
	}
	from := x.typ()
	if from == nil {
		return false
	}
	if isUntyped(from) {
		from = Default(from)
	}
	if logosMemberOf(from, tset) {
		return false
	}
	check.errorf(x, IncompatibleAssign, "%s cannot use %s as %s value in %s: %s is not a member of %s", logosNonMember, x, T, context, from, T)
	x.invalidate()
	return true
}

func (check *Checker) logosUnionMemberOf(x *operand, elem Type, context string) bool {
	if !check.logosUnionsOK() {
		return false
	}
	tset := logosClosedUnion(elem)
	if tset == nil {
		// The import overlay does not restore union terms, so an imported
		// union has no type set here; its members are in the union facts.
		return check.logosForeignUnionMemberOf(x, elem, context)
	}
	if check.logosReadonlyBoundary(x, elem, context) {
		return true
	}
	from := x.typ()
	if from == nil {
		return false
	}
	if isUntyped(from) {
		from = Default(from)
	}
	return logosMemberOf(from, tset)
}

// logosForeignUnionMemberOf reports whether x's type is a recorded member
// of an imported union elem, whose terms were stripped at export.
func (check *Checker) logosForeignUnionMemberOf(x *operand, elem Type, context string) bool {
	if _, imported := logosForeignUnionName(elem); !imported {
		return false
	}
	if check.logosReadonlyBoundary(x, elem, context) {
		return true
	}
	from := x.typ()
	if from == nil {
		return false
	}
	if isUntyped(from) {
		from = Default(from)
	}
	members, ok := LogosUnionMembers(elem)
	if !ok {
		return false
	}
	text, hasText := logosMemberTypeText(from)
	for _, m := range members {
		if m.Type != nil {
			if Identical(from, m.Type) {
				return true
			}
			continue
		}
		if hasText && m.Text() == text {
			return true
		}
	}
	return false
}

// logosMemberTypeText spells a named type the way imported union member
// facts spell it: "pkgpath.Type", or "*pkgpath.Type" behind a pointer.
// Const and descendant-readonly views of a member are not that member:
// spelling them alike would let the union option hand the shared
// backing storage to writers.
func logosMemberTypeText(t Type) (string, bool) {
	u := Unalias(t)
	switch u.(type) {
	case *ConstView, *ReadonlyDescendants:
		return "", false
	}
	ptr := false
	if p, ok := u.(*Pointer); ok {
		ptr = true
		u = Unalias(p.Elem())
	}
	n, ok := u.(*Named)
	if !ok || n.Obj() == nil || n.Obj().Pkg() == nil {
		return "", false
	}
	text := n.Obj().Pkg().Path() + "." + n.Obj().Name()
	if ptr {
		return "*" + text, true
	}
	return text, true
}

func logosUnionShape(t Type) ([]Type, bool, bool) {
	if t == nil {
		return nil, false, false
	}
	iface, ok := Unalias(t).Underlying().(*Interface)
	if !ok {
		return nil, false, false
	}
	var out []Type
	for i := range iface.NumEmbeddeds() {
		u, ok := iface.EmbeddedType(i).(*Union)
		if !ok {
			continue
		}
		for j := range u.Len() {
			term := u.Term(j)
			if term.Tilde() {
				return nil, true, false
			}
			out = append(out, term.Type())
		}
	}
	if len(out) == 0 {
		return nil, false, false
	}
	return out, true, true
}

func LogosUnionTerms(t Type) ([]Type, bool) {
	direct, isUnion, closed := logosUnionShape(t)
	if !isUnion || !closed {
		return nil, false
	}
	var out []Type
	var expanding []Type
	var expand func(m Type) bool
	expand = func(m Type) bool {
		sub, isSub, closedSub := logosUnionShape(m)
		if !isSub {
			for _, e := range out {
				if Identical(e, m) {
					return true
				}
			}
			out = append(out, m)
			return true
		}
		if !closedSub {
			return false
		}
		for _, e := range expanding {
			if Identical(e, m) {
				return true
			}
		}
		expanding = append(expanding, m)
		for _, s := range sub {
			if !expand(s) {
				return false
			}
		}
		return true
	}
	for _, d := range direct {
		if !expand(d) {
			return nil, false
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	iface, _ := Unalias(t).Underlying().(*Interface)
	if iface != nil {
		var kept []Type
		for _, m := range out {
			if _, imported := logosForeignUnionName(m); imported || Implements(m, iface) {
				kept = append(kept, m)
			}
		}
		out = kept
	}
	return out, true
}

type LogosUnionMember struct {
	Type Type
	Pkg  string
	Name string
	Ptr  bool
}

func (m LogosUnionMember) Text() string {
	text := m.Pkg + "." + m.Name
	if m.Ptr {
		return "*" + text
	}
	return text
}

func LogosUnionMembers(t Type) ([]LogosUnionMember, bool) {
	t = Unalias(t)
	if cv, ok := t.(*ConstView); ok {
		t = Unalias(cv.Elem)
	}
	if rd := logosAsReadonlyDescendants(t); rd != nil {
		t = Unalias(rd.Elem)
	}
	var out []LogosUnionMember
	seen := make(map[string]bool)
	visited := make(map[string]bool)
	if terms, ok := LogosUnionTerms(t); ok {
		for _, term := range terms {
			if name, imported := logosForeignUnionName(term); imported {
				logosAppendImportedMembers(name, &out, seen, visited)
				continue
			}
			logosAppendTypeMember(term, &out, seen)
		}
	} else if name, imported := logosForeignUnionName(t); imported {
		logosAppendImportedMembers(name, &out, seen, visited)
	}
	return out, len(out) > 0
}

// logosOutermostTypeName returns the type name the holder spelled: the
// outermost alias when the unwrapped type names one, otherwise the
// resolved named type. Pointer, const-view and readonly-descendant
// qualifiers unwrap; aliases never resolve. Recording the alias keeps an
// exported spelling of an unexported implementation reachable: the trial
// allocates through the alias, so the export check must see the alias.
func logosOutermostTypeName(t Type) (*TypeName, bool) {
	for {
		switch u := t.(type) {
		case *Alias:
			if u.Obj() == nil {
				return nil, false
			}
			return u.Obj(), true
		case *Named:
			if u.Obj() == nil {
				return nil, false
			}
			return u.Obj(), true
		case *Pointer:
			t = u.Elem()
		case *ConstView:
			t = u.Elem
		default:
			if rd := logosAsReadonlyDescendants(t); rd != nil {
				t = rd.Elem
				continue
			}
			return nil, false
		}
	}
}
func logosAppendTypeMember(term Type, out *[]LogosUnionMember, seen map[string]bool) {
	key := TypeString(term, nil)
	if seen[key] {
		return
	}
	seen[key] = true
	m := LogosUnionMember{Type: term}
	elem := Unalias(term)
	if rd := logosAsReadonlyDescendants(elem); rd != nil {
		elem = Unalias(rd.Elem)
	}
	if p, ok := elem.(*Pointer); ok {
		m.Ptr = true
		elem = Unalias(p.Elem())
		if rd := logosAsReadonlyDescendants(elem); rd != nil {
			elem = Unalias(rd.Elem)
		}
	}
	if tn, ok := logosOutermostTypeName(term); ok && tn.Pkg() != nil {
		m.Pkg = tn.Pkg().Path()
		m.Name = tn.Name()
	} else if named, ok := elem.(*Named); ok && named.Obj() != nil && named.Obj().Pkg() != nil {
		m.Pkg = named.Obj().Pkg().Path()
		m.Name = named.Obj().Name()
	}
	*out = append(*out, m)
}

// logosAccessibleAlias reports the outermost alias in t's written form
// (via Rhs, never the expansion cache) whose name the holder package can
// spell: either declared in the holder's own package or exported. It lets
// a union member that wraps an unexported type in an accessible alias
// stay reachable. A nil type or a chain with no accessible spelling
// reports false.
func logosAccessibleAlias(t Type, pkg *Package) (*TypeName, bool) {
	for {
		a, ok := t.(*Alias)
		if !ok {
			return nil, false
		}
		if obj := a.Obj(); obj != nil && (obj.Pkg() == nil || obj.Pkg() == pkg || obj.Exported()) {
			return obj, true
		}
		rhs := a.Rhs()
		if rhs == nil {
			return nil, false
		}
		t = rhs
	}
}

// logosWrittenPointedElem returns the element of a pointer reached through
// aliases as written, without consulting the alias expansion cache: type
// checking may already have expanded an alias elsewhere, which would hide
// an accessible middle spelling. A nil right-hand side or a non-pointer
// shape reports false.
func logosWrittenPointedElem(t Type) (Type, bool) {
	for {
		if a, ok := t.(*Alias); ok {
			rhs := a.Rhs()
			if rhs == nil {
				return nil, false
			}
			t = rhs
			continue
		}
		if cv, ok := t.(*ConstView); ok {
			t = cv.Elem
			continue
		}
		if p, ok := t.(*Pointer); ok {
			return p.Elem(), true
		}
		return nil, false
	}
}

// logosNameAccessible reports whether the holder package can name obj: a
// nil holder or a nil package accepts, otherwise the name must be declared
// in the holder's own package or exported.
func logosNameAccessible(obj *TypeName, pkg *Package) bool {
	if obj == nil {
		return false
	}
	op := obj.Pkg()
	if op == nil || pkg == nil {
		return true
	}
	if op.Path() == pkg.Path() {
		return true
	}
	return obj.Exported()
}

// logosSpellableInPkg reports whether the holder package can spell t. An
// alias counts by its own accessible name, a defined type by its exported
// or same-package name, and both require every type argument to be
// spellable, mirroring spellableType in the decoder generator. Composite
// types are walked so an unexported name nested in an argument is still
// rejected. Anything else keeps the conservative answer true so only a
// clearly unspellable element is ever rejected.
func logosSpellableInPkg(t Type, pkg *Package) bool {
	seen := make(map[Type]bool)
	return logosSpellableWalk(t, pkg, seen)
}

func logosSpellableWalk(t Type, pkg *Package, seen map[Type]bool) bool {
	if t == nil || seen[t] {
		return true
	}
	seen[t] = true
	switch t := t.(type) {
	case *Basic, *TypeParam:
		return true
	case *Alias:
		if !logosNameAccessible(t.Obj(), pkg) {
			return false
		}
		if args := t.TypeArgs(); args != nil {
			for i := range args.Len() {
				if !logosSpellableWalk(args.At(i), pkg, seen) {
					return false
				}
			}
		}
		return true
	case *Named:
		if !logosNameAccessible(t.Obj(), pkg) {
			return false
		}
		if args := t.TypeArgs(); args != nil {
			for i := range args.Len() {
				if !logosSpellableWalk(args.At(i), pkg, seen) {
					return false
				}
			}
		}
		return true
	case *Pointer:
		return logosSpellableWalk(t.Elem(), pkg, seen)
	case *Slice:
		return logosSpellableWalk(t.Elem(), pkg, seen)
	case *Array:
		return logosSpellableWalk(t.Elem(), pkg, seen)
	case *Map:
		return logosSpellableWalk(t.Key(), pkg, seen) && logosSpellableWalk(t.Elem(), pkg, seen)
	case *Chan:
		return logosSpellableWalk(t.Elem(), pkg, seen)
	case *Signature:
		return logosSpellableTuple(t.Params(), pkg, seen) && logosSpellableTuple(t.Results(), pkg, seen)
	case *Tuple:
		return logosSpellableTuple(t, pkg, seen)
	case *Interface:
		for i := range t.NumMethods() {
			if !logosSpellableWalk(t.Method(i).Type(), pkg, seen) {
				return false
			}
		}
		for i := range t.NumEmbeddeds() {
			if !logosSpellableWalk(t.EmbeddedType(i), pkg, seen) {
				return false
			}
		}
		return true
	case *Struct:
		for i := range t.NumFields() {
			if !logosSpellableWalk(t.Field(i).Type(), pkg, seen) {
				return false
			}
		}
		return true
	default:
		return true
	}
}

func logosSpellableTuple(t *Tuple, pkg *Package, seen map[Type]bool) bool {
	if t == nil {
		return true
	}
	for i := range t.Len() {
		if !logosSpellableWalk(t.At(i).Type(), pkg, seen) {
			return false
		}
	}
	return true
}

// logosUnionPointerAllocSpellable reports whether the trial allocation for
// a pointer union member can be spelled by holder: the written pointed
// element when the holder can name it, else the unaliased element. It
// mirrors the trial's allocation choice in json_unmarshal, so validation
// and the generated decoder agree.
func logosUnionPointerAllocSpellable(term Type, pkg *Package) bool {
	elem, ok := logosWrittenPointedElem(term)
	if !ok {
		return true
	}
	if logosSpellableInPkg(elem, pkg) {
		return true
	}
	return logosSpellableInPkg(Unalias(elem), pkg)
}

func logosAppendImportedMembers(name logosForeignName, out *[]LogosUnionMember, seen, visited map[string]bool) {
	key := name.pkgPath + "." + name.typeName
	if visited[key] {
		return
	}
	visited[key] = true
	for _, member := range logosLookupImportedUnionMembers(name.pkgPath, name.typeName) {
		trimmed, ptr := strings.CutPrefix(member.Member, "*")
		pkgPath, typeName, ok := strings.CutLast(trimmed, ".")
		if !ok || pkgPath == "" || typeName == "" {
			continue
		}
		nested := logosForeignName{pkgPath: pkgPath, typeName: typeName}
		if !ptr && len(logosLookupImportedUnionMembers(pkgPath, typeName)) > 0 {
			logosAppendImportedMembers(nested, out, seen, visited)
			continue
		}
		if seen[member.Member] {
			continue
		}
		seen[member.Member] = true
		*out = append(*out, LogosUnionMember{Pkg: pkgPath, Name: typeName, Ptr: ptr})
	}
}

func (check *Checker) logosUnionLiteralMiss(x, y *operand) (lit *operand, union Type, from Type) {
	if !check.conf.Logos {
		return nil, nil, nil
	}
	for _, pair := range [2][2]*operand{{x, y}, {y, x}} {
		un, c := pair[0], pair[1]
		tset := logosClosedUnion(un.typ())
		if tset == nil || c.mode() != constant_ || !isUntyped(c.typ()) {
			continue
		}
		if d := Default(c.typ()); !logosMemberOf(d, tset) {
			return c, un.typ(), d
		}
	}
	return nil, nil, nil
}

func (check *Checker) logosUnionCompareMiss(x, y *operand, op syntax.Operator) bool {
	lit, union, from := check.logosUnionLiteralMiss(x, y)
	if lit == nil {
		return false
	}
	check.errorf(y, MismatchedTypes, "%s invalid operation: %s %s %s (%s is not a member of %s)", logosNonMember, x.expr, op, y.expr, from, union)
	x.invalidate()
	return true
}

func (check *Checker) logosUnionConstMiss(x, y *operand) bool {
	lit, union, from := check.logosUnionLiteralMiss(x, y)
	if lit == nil {
		return false
	}
	check.errorf(lit, InvalidUntypedConversion, "%s %s is not a member of %s", logosNonMember, from, union)
	return true
}

// logosIfaceWord reports whether a value of type t is stored as a fixed-size
// interface word under LoGOs value-union lowering (SPEC 1.2 lowers a
// value-used closed union to a methods-only interface). A field,
// element, or type argument of such a type does not nest the memory of
// the members its type set admits, so a union member may hold the union
// itself by value (say, a tree node) without creating an
// infinitely-sized type. Interface-typed subterms are left out of the
// memory nest; a type's own declaration still checks the type's
// internal (type-set) cycles. It applies only when value unions are
// enabled; a plain Go package keeps the upstream recursive-type rule
// exactly.
func (check *Checker) logosIfaceWord(t Type) bool {
	if !check.logosUnionsOK() {
		return false
	}
	base, _ := logosViewBase(t)
	return isNonTypeParamInterface(base)
}

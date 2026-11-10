// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	. "internal/types/errors"
	"slices"
	"strings"
	"sync"
)

const (
	logosPropUnsatisfied = "[correctness::property_unsatisfied]"
	logosPropRequires    = "[correctness::property_requires_conformance]"
	logosPropNeedsPtr    = "[correctness::property_needs_pointer]"
	logosPropConflict    = "[correctness::property_conflict]"
	logosPropAddress     = "[correctness::property_address]"
)

func logosGetterName(prop string) string { return "Logos_Get" + prop }

func logosSetterName(prop string) string { return "Logos_Set" + prop }

func logosIsAccessor(name string) bool {
	return strings.HasPrefix(name, "Logos_Get") || strings.HasPrefix(name, "Logos_Set")
}

func logosPropertyInterface(t Type) *Interface {
	if t == nil {
		return nil
	}
	iface, _ := t.Underlying().(*Interface)
	if iface == nil {
		return nil
	}
	for _, m := range iface.typeSet().methods {
		if strings.HasPrefix(m.name, "Logos_Get") {
			return iface
		}
	}
	return nil
}

type logosProperty struct {
	name     string
	typ      Type
	readOnly bool
}

func logosProperties(iface *Interface) []logosProperty {
	const get = "Logos_Get"
	var props []logosProperty
	for _, m := range iface.typeSet().methods {
		name, ok := strings.CutPrefix(m.name, get)
		if !ok {
			continue
		}
		sig, ok := m.typ.(*Signature)
		if !ok || sig.params.Len() != 0 || sig.results.Len() != 1 {
			continue
		}
		pr := logosProperty{name: name, typ: sig.results.At(0).typ, readOnly: true}
		for _, s := range iface.typeSet().methods {
			if s.name != logosSetterName(name) {
				continue
			}
			ssig, ok := s.typ.(*Signature)
			if !ok || ssig.params.Len() != 1 || ssig.results.Len() != 0 {
				continue
			}
			if Identical(ssig.params.At(0).typ, pr.typ) {
				pr.readOnly = false
			}
		}
		props = append(props, pr)
	}
	slices.SortFunc(props, func(a, b logosProperty) int { return strings.Compare(a.name, b.name) })
	return props
}

func logosFindProperty(iface *Interface, sel string) (logosProperty, bool) {
	for _, pr := range logosProperties(iface) {
		if pr.name == sel {
			return pr, true
		}
	}
	return logosProperty{}, false
}

type logosPropDecl struct {
	typ    Type
	getter bool
	setter bool
}

func (check *Checker) logosPropertyMethod(ityp *Interface, f *syntax.Field, pt *syntax.PropertyType, def *TypeName, seen map[string]*logosPropDecl) map[string]*logosPropDecl {
	name := f.Name.Value
	ptyp := check.typ(pt.Type)
	if !isValid(ptyp) {
		return seen
	}
	d := seen[name]
	if d != nil && !Identical(d.typ, ptyp) {
		check.errorf(f.Name, InvalidIfaceAssign, "%s property %s has conflicting types %s and %s", logosPropConflict, name, d.typ, ptyp)
		return seen
	}
	if seen == nil {
		seen = make(map[string]*logosPropDecl)
	}
	if d == nil {
		d = &logosPropDecl{typ: ptyp}
		seen[name] = d
	}
	var recvTyp Type = ityp
	if def != nil {
		if named := asNamed(def.typ); named != nil {
			recvTyp = named
		}
	}
	pos := f.Name.Pos()
	hasMethod := func(name string) bool {
		for _, m := range ityp.methods {
			if m.name == name {
				return true
			}
		}
		return false
	}
	if !d.getter && !hasMethod(logosGetterName(name)) {
		d.getter = true
		sig := &Signature{recv: newVar(RecvVar, pos, check.pkg, "", recvTyp)}
		sig.results = NewTuple(NewParam(pos, check.pkg, "", ptyp))
		ityp.methods = append(ityp.methods, NewFunc(pos, check.pkg, logosGetterName(name), sig))
	}
	if !pt.Const && !d.setter && !hasMethod(logosSetterName(name)) {
		d.setter = true
		sig := &Signature{recv: newVar(RecvVar, pos, check.pkg, "", recvTyp)}
		sig.params = NewTuple(NewParam(pos, check.pkg, "", ptyp))
		ityp.methods = append(ityp.methods, NewFunc(pos, check.pkg, logosSetterName(name), sig))
	}
	return seen
}

type LogosPropDecl struct {
	Def    *TypeName
	Ifaces []Type
}

func (check *Checker) logosRecordConforms(def *TypeName, asserts []Type) {
	named, _ := def.Type().(*Named)
	if named == nil {
		return
	}
	var props []Type
	for _, T := range asserts {
		if !isValid(T) || logosPropertyInterface(T) == nil {
			continue
		}
		if check.logosConforms == nil {
			check.logosConforms = make(map[*Named][]Type)
		}
		check.logosConforms[named] = append(check.logosConforms[named], T)
		props = append(props, T)
	}
	if len(props) > 0 {
		check.Info.LogosPropDecls = append(check.Info.LogosPropDecls, &LogosPropDecl{Def: def, Ifaces: props})
	}
}

func (check *Checker) logosDeclaredConforms(named *Named, T, iface Type) bool {
	match := func(R Type) bool {
		if Identical(R, T) {
			return true
		}
		Ri, ok := R.Underlying().(*Interface)
		Ii, _ := iface.Underlying().(*Interface)
		return ok && Ii != nil && Ri == Ii
	}
	for _, R := range check.logosConforms[named] {
		if match(R) {
			return true
		}
	}
	if origin := named.Origin(); origin != named {
		for _, R := range check.logosConforms[origin] {
			if match(R) {
				return true
			}
		}
	}
	return check.logosImportedConforms(named, T)
}

type LogosProp struct {
	Name     string
	Typ      Type
	ReadOnly bool
}

func LogosPropsOf(t Type) ([]LogosProp, bool) {
	iface := logosPropertyInterface(t)
	if iface == nil {
		return nil, false
	}
	inner := logosProperties(iface)
	out := make([]LogosProp, len(inner))
	for i, pr := range inner {
		out[i] = LogosProp{Name: pr.name, Typ: pr.typ, ReadOnly: pr.readOnly}
	}
	return out, true
}

type LogosConformsIface struct {
	PkgPath string
	Name    string
}

var logosImportedConforms = struct {
	mu sync.Mutex
	m  map[string]map[string][]LogosConformsIface
}{m: make(map[string]map[string][]LogosConformsIface)}

func LogosNoteImportedConforms(structPath, typeName string, ifaces []LogosConformsIface) {
	if structPath == "" || typeName == "" || len(ifaces) == 0 {
		return
	}
	logosImportedConforms.mu.Lock()
	defer logosImportedConforms.mu.Unlock()
	byName := logosImportedConforms.m[structPath]
	if byName == nil {
		byName = make(map[string][]LogosConformsIface)
		logosImportedConforms.m[structPath] = byName
	}
	byName[typeName] = append(byName[typeName], ifaces...)
}

func logosNamedKey(T Type) (string, string, bool) {
	named, ok := Unalias(T).(*Named)
	if !ok {
		return "", "", false
	}
	obj := named.Origin().Obj()
	if obj == nil || obj.Pkg() == nil || obj.Name() == "" {
		return "", "", false
	}
	return obj.Pkg().Path(), obj.Name(), true
}

func (check *Checker) logosImportedConforms(named *Named, T Type) bool {
	obj := named.Origin().Obj()
	if obj == nil || obj.Pkg() == nil {
		return false
	}
	tp, tn, ok := logosNamedKey(T)
	if !ok || tp == "" || tn == "" {
		return false
	}
	logosImportedConforms.mu.Lock()
	defer logosImportedConforms.mu.Unlock()
	for _, r := range logosImportedConforms.m[obj.Pkg().Path()][obj.Name()] {
		if r.PkgPath == tp && r.Name == tn {
			return true
		}
	}
	return false
}

type logosPropFailKind int

const (
	logosPropOK logosPropFailKind = iota
	logosPropNoGetter
	logosPropNoSetter
	logosPropAmbig
	logosPropOrdinary
	logosPropFailNeedsPtr
)

func (check *Checker) logosPropertySource(V Type, name string, want Type) (getter, setter, ambig bool) {
	obj, index, _ := lookupFieldOrMethodImpl(V, false, check.pkg, name, false)
	if obj == nil {
		if index != nil {
			return false, false, true
		}
	} else if fld, ok := obj.(*Var); ok {
		if Identical(fld.typ, want) {
			return true, true, false
		}
		return false, false, false
	} else if m, ok := obj.(*Func); ok {
		check.objDecl(m)
		if sig, ok := m.typ.(*Signature); ok && sig.params.Len() == 0 && sig.results.Len() == 1 && Identical(sig.results.At(0).typ, want) {
			getter = true
		}
	}
	sobj, sindex, _ := lookupFieldOrMethodImpl(V, false, check.pkg, "Set"+name, false)
	if sobj == nil {
		return getter, false, sindex != nil
	}
	if sm, ok := sobj.(*Func); ok {
		check.objDecl(sm)
		if sig, ok := sm.typ.(*Signature); ok && sig.params.Len() == 1 && sig.results.Len() == 0 && Identical(sig.params.At(0).typ, want) {
			return getter, true, false
		}
	}
	return getter, false, false
}

func (check *Checker) logosCheckPropertySources(V Type, iface *Interface, pos syntax.Pos) (logosPropFailKind, string) {
	for _, pr := range logosProperties(iface) {
		getter, setter, ambig := check.logosPropertySource(V, pr.name, pr.typ)
		switch {
		case ambig:
			return logosPropAmbig, pr.name
		case !getter:
			return logosPropNoGetter, pr.name
		case !pr.readOnly && !setter:
			return logosPropNoSetter, pr.name
		}
	}
	if name := check.logosMissingOrdinary(V, iface, pos); name != "" {
		return logosPropOrdinary, name
	}
	_, isPtr := deref(V)
	if !isPtr {
		for _, pr := range logosProperties(iface) {
			if !pr.readOnly {
				return logosPropFailNeedsPtr, pr.name
			}
		}
	}
	return logosPropOK, ""
}

func (check *Checker) logosMissingOrdinary(V Type, iface *Interface, pos syntax.Pos) string {
	for _, m := range iface.typeSet().methods {
		if logosIsAccessor(m.name) {
			continue
		}
		obj, _, _ := lookupFieldOrMethodImpl(V, false, m.pkg, m.name, false)
		f, _ := obj.(*Func)
		if obj == nil || f == nil {
			return m.name
		}
		check.objDecl(f)
		if f.nointerface || !Identical(f.typ, m.typ) {
			return m.name
		}
		if m.LogosReadonly() {
			if !check.logosConcreteReadonlySatisfiedAt(V, f, m, pos) {
				return m.name
			}
		}
	}
	return ""
}

func (check *Checker) logosCheckPropertyAssert(e *syntax.AssertedStruct, T Type, i int, merged map[string]logosMergedProp) bool {
	iface := logosPropertyInterface(T)
	if iface == nil {
		return false
	}
	errored := false
	for _, pr := range logosProperties(iface) {
		if prev, ok := merged[pr.name]; ok {
			if !Identical(prev.typ, pr.typ) {
				check.errorf(e.Asserts[i], InvalidIfaceAssign, "%s property %s is %s in %s and %s in %s", logosPropConflict, pr.name, prev.typ, prev.from, pr.typ, T)
				errored = true
				continue
			}
			if !pr.readOnly {
				prev.readOnly = false
				merged[pr.name] = prev
			}
			continue
		}
		merged[pr.name] = logosMergedProp{typ: pr.typ, readOnly: pr.readOnly, from: T, pos: e.Asserts[i]}
	}
	return errored
}

type logosMergedProp struct {
	typ      Type
	readOnly bool
	from     Type
	pos      syntax.Expr
}

func (check *Checker) logosCheckMergedProp(sname string, V Type, mp logosMergedProp) bool {
	getter, setter, ambig := check.logosPropertySource(V, sname, mp.typ)
	switch {
	case ambig:
		check.errorf(mp.pos, AmbiguousSelector, "ambiguous selector %s.%s", V, sname)
		return true
	case !getter:
		check.errorf(mp.pos, InvalidIfaceAssign, "%s %s declares %s but has no field or method for property %s", logosPropUnsatisfied, V, mp.from, sname)
		return true
	case !mp.readOnly && !setter:
		check.errorf(mp.pos, InvalidIfaceAssign, "%s %s declares %s but has no field or Set%s method for mutable property %s", logosPropUnsatisfied, V, mp.from, sname, sname)
		return true
	}
	return false
}

func (check *Checker) logosPropGate(Ti *Interface, Vu Type) bool {
	return check.logosPropGateFor(Ti, Vu, false)
}
func (check *Checker) logosPropGateConstraint(Ti *Interface, Vu Type) bool {
	return check.logosPropGateFor(Ti, Vu, true)
}
func (check *Checker) logosPropGateFor(Ti *Interface, Vu Type, constraint bool) bool {
	if check == nil || !check.conf.Logos {
		return false
	}
	if _, ok := Vu.(*Interface); ok {
		return false
	}
	if constraint && isTypeParam(Vu) {
		return false
	}
	return logosPropertyInterface(Ti) != nil
}

func (check *Checker) logosImplementsProperty(V, T Type, Ti *Interface, cause *string, pos syntax.Pos) bool {
	fail := func(format string, args ...any) bool {
		if cause != nil {
			*cause = check.sprintf(format, args...)
		}
		return false
	}
	base, _ := deref(V)
	named := asNamed(base)
	kind, name := check.logosCheckPropertySources(V, Ti, pos)
	declared := named != nil && check.logosDeclaredConforms(named, T, Ti)
	if named == nil {
		if _, ok := base.Underlying().(*Struct); !ok {
			return fail("%s does not implement %s (missing method %s)", V, Ti, name)
		}
	}
	switch {
	case declared && kind == logosPropOK:
		return true
	case kind == logosPropFailNeedsPtr:
		return fail("%s %s has mutable property %s; use *%s", logosPropNeedsPtr, V, name, base)
	case declared && kind == logosPropAmbig:
		return fail("ambiguous selector %s.%s", V, name)
	case declared && kind == logosPropOrdinary:
		return fail("%s %s declares %s but does not implement method %s", logosAssertFail, V, Ti, name)
	case declared && kind == logosPropNoSetter:
		return fail("%s %s declares %s but has no field or Set%s method for mutable property %s", logosPropUnsatisfied, V, Ti, name, name)
	case declared:
		return fail("%s %s declares %s but has no field or method for property %s", logosPropUnsatisfied, V, Ti, name)
	case kind == logosPropOK:
		sname := V.String()
		if named != nil {
			sname = named.Obj().Name()
		}
		return fail("%s %s has the fields of %s but does not declare it; write type %s struct : %s { ... }", logosPropRequires, V, Ti, sname, Ti)
	default:
		return fail("%s does not implement %s (missing method %s)", V, Ti, name)
	}
}

func (check *Checker) logosPropertySelector(x *operand, e *syntax.SelectorExpr, sel string) bool {
	if !check.conf.Logos || x.typ() == nil {
		return false
	}
	iface := logosPropertyInterface(x.typ())
	if iface == nil {
		return false
	}
	pr, ok := logosFindProperty(iface, sel)
	if !ok {
		return false
	}
	if pr.readOnly {
		x.mode_ = value
	} else {
		x.mode_ = variable
	}
	x.typ_ = pr.typ
	x.expr = e
	if check.logosPropSel == nil {
		check.logosPropSel = make(map[*syntax.SelectorExpr]bool)
	}
	check.logosPropSel[e] = true
	return true
}

func (check *Checker) logosPropertyAddress(x *operand, e *syntax.Operation) bool {
	if !check.conf.Logos || !x.isValid() {
		return false
	}
	sel, ok := syntax.Unparen(e.X).(*syntax.SelectorExpr)
	if !ok || !check.logosPropSel[sel] {
		return false
	}
	check.errorf(sel.Sel, UnaddressableOperand, "%s cannot take the address of property %s", logosPropAddress, sel.Sel.Value)
	x.invalidate()
	return true
}

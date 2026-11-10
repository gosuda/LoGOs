// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	. "internal/types/errors"
	"strconv"
	"strings"
	"unicode"
)

type LogosJSONField struct {
	Name      string
	Tagged    bool
	OmitEmpty bool
	OmitZero  bool
	StringOpt bool
}

type logosMethodShape int

const (
	logosShapeMarshal logosMethodShape = iota
	logosShapeUnmarshal
	logosShapeAppend
)

type logosStaticMethod struct {
	name    string
	pointer bool
	shape   logosMethodShape
}

var logosStaticMethods = []logosStaticMethod{
	{"MarshalJSON", false, logosShapeMarshal},
	{"UnmarshalJSON", true, logosShapeUnmarshal},
	{"MarshalBinary", false, logosShapeMarshal},
	{"UnmarshalBinary", true, logosShapeUnmarshal},
	{"AppendBinary", false, logosShapeAppend},
}

var logosBinaryMethods = []string{"MarshalBinary", "UnmarshalBinary", "AppendBinary"}

const logosNoUnionJSON = "[correctness::union_member_no_json]"

func LogosBinaryMethodNames() []string {
	return logosBinaryMethods
}

func logosTagKey(tag string) (name, rest string, ok bool) {
	tag = strings.TrimLeft(tag, " ")
	i := 0
	for i < len(tag) && tag[i] > ' ' && tag[i] != ':' && tag[i] != '"' && tag[i] != 0x7f {
		i++
	}
	if i == 0 || i+1 >= len(tag) || tag[i] != ':' || tag[i+1] != '"' {
		return "", "", false
	}
	return tag[:i], tag[i+1:], true
}

func logosTagQuoted(s string) (quoted, rest string, ok bool) {
	i := 1
	for i < len(s) && s[i] != '"' {
		if s[i] == '\\' {
			i++
		}
		i++
	}
	if i >= len(s) {
		return "", "", false
	}
	return s[:i+1], s[i+1:], true
}

func logosTagLookup(tag, key string) (string, bool) {
	for {
		name, rest, ok := logosTagKey(tag)
		if !ok {
			return "", false
		}
		quoted, rest, ok := logosTagQuoted(rest)
		if !ok {
			return "", false
		}
		if name == key {
			value, err := strconv.Unquote(quoted)
			return value, err == nil
		}
		tag = rest
	}
}

func logosValidTagName(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		switch {
		case strings.ContainsRune("!#$%&()*+-./:;<=>?@[]^_{|}~ ", c):
		case !unicode.IsLetter(c) && !unicode.IsDigit(c):
			return false
		}
	}
	return true
}

func logosTagOption(opts, name string) bool {
	for opts != "" {
		var cur string
		cur, opts, _ = strings.Cut(opts, ",")
		if cur == name {
			return true
		}
	}
	return false
}

func logosTagOptionsSupported(opts string) bool {
	seen := make(map[string]bool)
	for opts != "" {
		var cur string
		cur, opts, _ = strings.Cut(opts, ",")
		if (cur != "omitempty" && cur != "omitzero" && cur != "string") || seen[cur] {
			return false
		}
		seen[cur] = true
	}
	return true
}

func logosJSONTagSupported(st *Struct, i int) bool {
	tag, has := logosTagLookup(st.Tag(i), "json")
	if !has || tag == "-" {
		return true
	}
	if !st.Field(i).Exported() || strings.HasSuffix(tag, ",") {
		return false
	}
	name, opts, _ := strings.Cut(tag, ",")
	if name != "" && !logosValidTagName(name) {
		return false
	}
	return logosTagOptionsSupported(opts)
}

func LogosJSONFieldOf(st *Struct, i int) (LogosJSONField, bool) {
	fld := st.Field(i)
	if !fld.Exported() {
		return LogosJSONField{}, false
	}
	tag, _ := logosTagLookup(st.Tag(i), "json")
	if tag == "-" {
		return LogosJSONField{}, false
	}
	name, opts, _ := strings.Cut(tag, ",")
	if !logosValidTagName(name) {
		name = ""
	}
	out := LogosJSONField{
		Name:      name,
		Tagged:    name != "",
		OmitEmpty: logosTagOption(opts, "omitempty"),
		OmitZero:  logosTagOption(opts, "omitzero"),
		StringOpt: logosTagOption(opts, "string"),
	}
	if out.Name == "" {
		out.Name = fld.Name()
	}
	return out, true
}

func logosStaticUnwrap(t Type) Type {
	for {
		if o := logosAsOption(t); o != nil {
			t = o.Elem
			continue
		}
		if cv, ok := t.Underlying().(*ConstView); ok {
			t = cv.Elem
			continue
		}
		return t
	}
}

func logosStaticKindOK(t Type) bool {
	u := logosStaticUnwrap(t)
	switch iface := u.Underlying().(type) {
	case *Interface:
		return logosClosedUnion(u) != nil && !logosContainsLock(u)
	case *Map:
		b, ok := iface.Key().Underlying().(*Basic)
		return ok && b.Info()&IsString != 0
	}
	return true
}

func logosIsNonNilPointer(t Type) bool {
	if cv, ok := t.Underlying().(*ConstView); ok {
		t = cv.Elem
	}
	_, ok := Unalias(t).(*Pointer)
	return ok
}

func logosStaticTrigger(info *Info, st *Struct) bool {
	if info != nil && len(info.LogosFieldDefaults[st]) > 0 {
		return true
	}
	for i := range st.NumFields() {
		if _, ok := logosTagLookup(st.Tag(i), "json"); ok || logosIsNonNilPointer(st.Field(i).Type()) {
			return true
		}
	}
	return false
}

func logosFamilyNameClash(st *Struct, family string) bool {
	keys := make(map[string]bool)
	for i := range st.NumFields() {
		for _, m := range logosStaticMethods {
			if !strings.HasSuffix(m.name, family) {
				continue
			}
			if st.Field(i).Name() == m.name {
				return true
			}
		}
		f, ok := LogosJSONFieldOf(st, i)
		if !ok {
			continue
		}
		if keys[f.Name] {
			return true
		}
		keys[f.Name] = true
	}
	return false
}

func logosContainsLock(t Type) bool {
	return logosContainsLockSeen(t, make(map[Type]bool))
}

func logosContainsLockSeen(t Type, seen map[Type]bool) bool {
	if t == nil {
		return false
	}
	t = Unalias(t)
	if seen[t] {
		return false
	}
	seen[t] = true
	if o := logosAsOption(t); o != nil {
		return logosContainsLockSeen(o.Elem, seen)
	}
	if named, ok := t.(*Named); ok {
		if obj := named.Obj(); obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == "sync" {
			switch obj.Name() {
			case "Mutex", "RWMutex", "Cond", "WaitGroup", "Once", "Map", "Pool", "noCopy":
				return true
			}
		}
	}
	switch u := t.Underlying().(type) {
	case *ConstView:
		return logosContainsLockSeen(u.Elem, seen)
	case *Array:
		return logosContainsLockSeen(u.Elem(), seen)
	case *Struct:
		for i := range u.NumFields() {
			if logosContainsLockSeen(u.Field(i).Type(), seen) {
				return true
			}
		}
	case *Interface:
		if tset := logosClosedUnion(t); tset != nil {
			for _, term := range tset.terms {
				if logosContainsLockSeen(term.typ, seen) {
					return true
				}
			}
		}
	}
	return false
}

func LogosStaticStruct(info *Info, named *Named, rhs syntax.Expr) ([]*Var, bool) {
	if _, ok := syntax.Unparen(rhs).(*syntax.StructType); !ok {
		return nil, false
	}
	if named == nil || named.TypeParams().Len() != 0 || named.TypeArgs().Len() != 0 {
		return nil, false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil || obj.Parent() != obj.Pkg().Scope() {
		return nil, false
	}
	st, ok := named.Underlying().(*Struct)
	if !ok {
		return nil, false
	}
	if logosContainsLock(named) {
		return nil, false
	}
	fields := make([]*Var, st.NumFields())
	for i := range fields {
		fld := st.Field(i)
		if fld.Embedded() || !logosStaticKindOK(fld.Type()) || !logosJSONTagSupported(st, i) {
			return nil, false
		}
		fields[i] = fld
	}
	if logosFamilyNameClash(st, "JSON") || !logosStaticTrigger(info, st) {
		return nil, false
	}
	return fields, true
}

func LogosBinaryStruct(info *Info, named *Named, rhs syntax.Expr) ([]*Var, bool) {
	if _, ok := syntax.Unparen(rhs).(*syntax.StructType); !ok {
		return nil, false
	}
	if named == nil || named.TypeParams().Len() != 0 || named.TypeArgs().Len() != 0 {
		return nil, false
	}
	obj := named.Obj()
	if obj == nil || obj.Pkg() == nil || obj.Parent() != obj.Pkg().Scope() {
		return nil, false
	}
	st, ok := named.Underlying().(*Struct)
	if !ok {
		return nil, false
	}
	if logosContainsLock(named) {
		return nil, false
	}
	fields := make([]*Var, st.NumFields())
	for i := range fields {
		fld := st.Field(i)
		if fld.Embedded() || !logosStaticKindOK(fld.Type()) || !logosJSONTagSupported(st, i) {
			return nil, false
		}
		fields[i] = fld
	}
	if logosFamilyNameClash(st, "Binary") || !logosStaticTrigger(info, st) || !LogosBinaryTypeOK(named) {
		return nil, false
	}
	return fields, true
}

func LogosBinaryTypeOK(t Type) bool {
	return logosBinaryTypeOK(t, make(map[Type]bool))
}

func logosBinaryTypeOK(t Type, seen map[Type]bool) bool {
	t = Unalias(t)
	if seen[t] {
		return true
	}
	seen[t] = true
	if o := logosAsOption(t); o != nil {
		return logosBinaryTypeOK(o.Elem, seen)
	}
	switch u := t.Underlying().(type) {
	case *ConstView:
		return logosBinaryTypeOK(u.Elem, seen)
	case *Basic:
		return u.Info()&(IsBoolean|IsInteger|IsFloat|IsString) != 0
	case *Pointer:
		return logosBinaryTypeOK(u.Elem(), seen)
	case *Slice:
		return logosBinaryTypeOK(u.Elem(), seen)
	case *Array:
		return logosBinaryTypeOK(u.Elem(), seen)
	case *Map:
		key, ok := u.Key().Underlying().(*Basic)
		return ok && key.Info()&IsString != 0 && logosBinaryTypeOK(u.Elem(), seen)
	case *Struct:
		return logosBinaryFieldsOK(u, seen)
	}
	return false
}

func logosBinaryFieldsOK(st *Struct, seen map[Type]bool) bool {
	for i := range st.NumFields() {
		if _, ok := LogosJSONFieldOf(st, i); !ok {
			continue
		}
		if !logosBinaryTypeOK(st.Field(i).Type(), seen) {
			return false
		}
	}
	return true
}

func (check *Checker) logosSynthStaticMethods() {
	if !check.conf.Logos {
		return
	}
	for _, obj := range check.objList {
		tname, ok := obj.(*TypeName)
		if !ok || tname.typ == nil || check.objMap[tname].tdecl.Alias {
			continue
		}
		named, ok := tname.typ.(*Named)
		if !ok {
			continue
		}
		_, jsonOK := LogosStaticStruct(check.Info, named, check.objMap[tname].tdecl.Type)
		_, binaryOK := LogosBinaryStruct(check.Info, named, check.objMap[tname].tdecl.Type)
		if jsonOK {
			check.logosAddStaticFamilyMethods(named, "JSON")
		}
		if binaryOK {
			check.logosAddStaticFamilyMethods(named, "Binary")
		}
	}
	check.logosCheckUnionJSONMembers()
}

func (check *Checker) logosCheckUnionJSONMembers() {
	for _, obj := range check.objList {
		tname, ok := obj.(*TypeName)
		if !ok || tname.typ == nil || check.objMap[tname].tdecl.Alias {
			continue
		}
		named, ok := tname.typ.(*Named)
		if !ok {
			continue
		}
		fields, jsonOK := LogosStaticStruct(check.Info, named, check.objMap[tname].tdecl.Type)
		if !jsonOK {
			continue
		}
		for _, fld := range fields {
			check.logosCheckUnionFieldJSON(fld)
		}
	}
}

func (check *Checker) logosCheckUnionFieldJSON(fld *Var) {
	t := fld.Type()
	if cv, ok := Unalias(t).(*ConstView); ok {
		t = cv.Elem
	}
	tset := logosClosedUnion(t)
	if tset == nil || !logosIsClosed(tset) {
		return
	}
	for _, term := range tset.terms {
		if term.tilde || logosUnionMemberHasJSON(term.typ, check.pkg) {
			continue
		}
		check.errorf(fld, BadDecl, "%s union member %s has no UnmarshalJSON method", logosNoUnionJSON, term.typ)
	}
}

func logosUnionMemberHasJSON(m Type, pkg *Package) bool {
	u := Unalias(m)
	if cv, ok := u.(*ConstView); ok {
		u = Unalias(cv.Elem)
	}
	if ptr, ok := u.(*Pointer); ok {
		u = Unalias(ptr.Elem())
		if cv, ok := u.(*ConstView); ok {
			u = Unalias(cv.Elem)
		}
	}
	named, ok := u.(*Named)
	if !ok || named.Obj() == nil {
		return false
	}
	obj, _, _ := LookupFieldOrMethod(NewPointer(named), false, pkg, "UnmarshalJSON")
	return obj != nil
}

func (check *Checker) logosAddStaticFamilyMethods(named *Named, family string) {
	for _, m := range logosStaticMethods {
		if !strings.HasSuffix(m.name, family) {
			continue
		}
		if !logosHasMethod(named, m.name) {
			named.AddMethod(check.logosStaticFunc(named, m))
		}
	}
}

func logosHasMethod(named *Named, name string) bool {
	for i := range named.NumMethods() {
		if named.Method(i).Name() == name {
			return true
		}
	}
	return false
}

func (check *Checker) logosStaticFunc(named *Named, m logosStaticMethod) *Func {
	var recvType Type = named
	if m.pointer {
		recvType = NewPointer(named)
	}
	bytes := NewSlice(universeByte)
	result := func(t Type) *Var { return newVar(ResultVar, nopos, check.pkg, "", t) }
	var params, results []*Var
	switch m.shape {
	case logosShapeMarshal:
		results = []*Var{result(bytes), result(universeError)}
	case logosShapeUnmarshal:
		params = []*Var{newVar(ParamVar, nopos, check.pkg, "data", bytes)}
		results = []*Var{result(universeError)}
	case logosShapeAppend:
		params = []*Var{newVar(ParamVar, nopos, check.pkg, "dst", bytes)}
		results = []*Var{result(bytes), result(universeError)}
	}
	recv := newVar(RecvVar, nopos, check.pkg, "v", recvType)
	sig := NewSignatureType(recv, nil, nil, NewTuple(params...), NewTuple(results...), false)
	return NewFunc(nopos, check.pkg, m.name, sig)
}

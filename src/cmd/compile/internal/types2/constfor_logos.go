// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	"go/constant"
	. "internal/types/errors"
	"reflect"
	"slices"
)

type LogosConstFor struct {
	Target    Type
	Struct    *Struct
	FieldVar  *Var
	IndexVar  *Var
	Generic   bool
	GoVersion string
	// Term and Breaks record, per visible field in expansion order, the
	// terminating and break verdict of the body under that field's
	// predicate values, for the analysis in return.go. They stay empty
	// for a generic const for, whose expansion is not yet known.
	Term   []bool
	Breaks []LogosConstForBreaks
	// Iterations holds the reachable iterations, indexed by field
	// declaration index; nil for skipped fields.
	Iterations []*Info
}

type LogosCopyContext struct {
	Scope     *Scope
	Sig       *Signature
	Version   string
	Disqual   []map[string]bool
	Aliases   map[*Package]string
	MaxSteps  int
	NodeLimit int
	Importer  Importer
	Context   *Context
	Bind      LogosTypeBindings
}

func (ctx LogosCopyContext) logosCopyChecker(pkg *Package, info *Info) *Checker {
	imp := ctx.Importer
	if imp == nil && info != nil {
		imp = info.LogosImporter
	}
	ctxt := ctx.Context
	if ctxt == nil && info != nil {
		ctxt = info.LogosContext
	}
	if ctxt == nil {
		ctxt = NewContext()
	}
	conf := &Config{
		Logos:              true,
		GoVersion:          ctx.Version,
		Context:            ctxt,
		Importer:           imp,
		IgnoreBranchErrors: true,
		LogosCTFEMaxSteps:  ctx.MaxSteps,
		Error:              func(error) {},
	}
	conf.LogosComptimeNodeLimit = ctx.NodeLimit
	if conf.LogosCTFEMaxSteps == 0 && info != nil {
		conf.LogosCTFEMaxSteps = info.LogosCTFEMaxSteps
	}
	mc := NewChecker(conf, pkg, info)
	mc.version = asGoVersion(ctx.Version)
	mc.logosCopy = true
	mc.scope = &Scope{parent: ctx.Scope, comment: "logos copies"}
	for imported, alias := range ctx.Aliases {
		mc.scope.Insert(NewPkgName(nopos, pkg, alias, imported))
	}
	mc.logosBindCopyTypes(ctx.Bind)
	mc.sig = ctx.Sig
	mc.logosDisqual = slices.Clone(ctx.Disqual)
	mc.logosSeedConstFuncs(info)
	return mc
}

func (check *Checker) logosSeedConstFuncs(info *Info) {
	if info == nil || len(info.LogosConstFuncs) == 0 {
		return
	}
	bodies, consts := check.logosConstFuncBodies()
	for key, rec := range info.LogosConstFuncs {
		if rec.Decl == nil {
			continue
		}
		if _, dup := bodies[key]; !dup {
			bodies[key] = rec.Decl
		}
		if rec.Const {
			consts[rec.Decl] = true
		}
	}
}

func (check *Checker) logosCopyFinish() {
	check.processDelayed(0)
	check.cleanup()
	check.recordUntyped()
}

func LogosCheckCopy(pkg *Package, info *Info, ctx LogosCopyContext, blocks []*syntax.BlockStmt) {
	if ctx.Scope == nil || ctx.Sig == nil {
		return
	}
	mc := ctx.logosCopyChecker(pkg, info)
	for _, b := range blocks {
		mc.stmt(0, b)
	}
	mc.logosCopyFinish()
}

type LogosTypeBindings struct {
	Pkgs map[string]*Package
}

func (check *Checker) logosBindCopyTypes(bind LogosTypeBindings) {
	for name, p := range bind.Pkgs {
		check.scope.Insert(NewPkgName(nopos, check.pkg, name, p))
	}
}

type logosFieldBinding struct {
	info  *LogosConstFor
	index int
	field *Var
	outer *logosFieldBinding
	tag   string
}

var logosFieldValueSels = []string{"Name", "Index", "Tag", "Exported"}

func (check *Checker) logosConstForIndexValue(x *operand, obj Object) {
	if check.logosPred.depth > 0 {
		return
	}
	if n := len(check.logosLHS); n > 0 && check.logosLHS[n-1] == obj {
		return
	}
	for b := check.logosFieldBind; b != nil; b = b.outer {
		if b.info.IndexVar != nil && obj == b.info.IndexVar && x.isValid() {
			x.mode_ = constant_
			x.val = constant.MakeInt64(int64(b.index))
			return
		}
	}
}

func (check *Checker) logosConstFor(s *syntax.ConstForStmt, ctxt stmtContext) {
	info := &LogosConstFor{GoVersion: check.conf.GoVersion}
	if check.logosConstForDepth == 0 {
		check.logosConstForBudget = 0
	}
	check.logosConstForDepth++
	defer func() { check.logosConstForDepth-- }()
	if check.logosConstForRange(s, info) {
		check.logosConstForBody(s, info, ctxt)
	}
	if check.Info.LogosConstFor == nil {
		check.Info.LogosConstFor = make(map[syntax.Node]*LogosConstFor)
	}
	check.Info.LogosConstFor[s] = info
}

func (check *Checker) logosConstForRange(s *syntax.ConstForStmt, info *LogosConstFor) bool {
	bad := func(pos syntax.Pos, format string, args ...any) bool {
		check.errorf(pos, InvalidRangeExpr, "[comptime::const_for_fields] "+format, args...)
		return false
	}
	call, ok := s.Range.(*syntax.CallExpr)
	if !ok {
		return bad(s.Range.Pos(), "const for range must be comptime.Fields[T]()")
	}
	if len(call.ArgList) != 0 {
		return bad(s.Range.Pos(), "const for range must be comptime.Fields[T]()")
	}
	idx, ok := call.Fun.(*syntax.IndexExpr)
	if !ok {
		return bad(s.Range.Pos(), "const for range must be comptime.Fields[T]()")
	}
	targs := syntax.UnpackListExpr(idx.Index)
	if len(targs) != 1 {
		return bad(s.Range.Pos(), "const for range must be comptime.Fields[T]()")
	}
	targExpr := targs[0]
	sel, fname, ok := check.logosComptimeQualifier(call)
	if !ok || fname != "Fields" {
		return bad(s.Range.Pos(), "const for range must be comptime.Fields[T]()")
	}
	check.recordUse(sel.Sel, check.logosComptimeObj("Fields"))
	if name, ok := sel.X.(*syntax.Name); ok {
		if _, obj := check.lookupScope(name.Value); obj != nil {
			check.recordUse(name, obj)
			if pn, ok := obj.(*PkgName); ok {
				check.usedPkgNames[pn] = true
			}
		}
	}
	targ := check.varType(targExpr)
	if !isValid(targ) {
		return false
	}
	if tp, ok := targ.(*TypeParam); ok {
		_ = tp
		info.Target = targ
		info.Generic = true
		return true
	}
	st, ok := targ.Underlying().(*Struct)
	if !ok && check.logosDead(s) {
		return false
	}
	if !ok {
		return bad(targExpr.Pos(), "const for range must be a struct type, have %s", targ)
	}
	info.Target = targ
	info.Struct = st
	return true
}

func (check *Checker) logosComptimeObj(name string) Object {
	return logosComptimePackage().Scope().Lookup(name)
}

func (check *Checker) logosConstForBody(s *syntax.ConstForStmt, info *LogosConstFor, ctxt stmtContext) {
	check.openScope(s, "const for")
	defer check.closeScope()

	fieldTyp := logosComptimeAny
	if obj := check.logosComptimeObj("Field"); obj != nil {
		fieldTyp = obj.Type()
	}
	fv := NewVar(s.Field.Pos(), check.pkg, s.Field.Value, fieldTyp)
	check.declare(check.scope, s.Field, fv, s.Field.Pos())
	info.FieldVar = fv
	if check.logosFieldVars == nil {
		check.logosFieldVars = make(map[*Var]*LogosConstFor)
	}
	check.logosFieldVars[fv] = info
	if s.Index != nil && s.Index.Value != "_" {
		iv := NewVar(s.Index.Pos(), check.pkg, s.Index.Value, Typ[Int])
		check.declare(check.scope, s.Index, iv, s.Index.Pos())
		info.IndexVar = iv
		check.logosMarkIndexVar(iv)
	}

	inner := ctxt &^ (fallthroughOk | finalSwitchCase | inTypeSwitch)
	if info.Generic && check.logosCtDeclCheck {
		check.scope.logosUsageDeferred = true
		check.logosNoteDeadUses(s.Body)
		return
	}
	if info.Struct != nil {
		check.logosConstForConcrete(s, info, inner)
	} else if info.Generic {
		check.stmtList(inner, s.Body.List)
	}
	check.logosFieldDrain(s, info)
}

func (check *Checker) logosConstForConcrete(s *syntax.ConstForStmt, info *LogosConstFor, inner stmtContext) {
	if saved := check.conf.Error; saved != nil {
		seen := make(map[string]bool)
		check.conf.Error = func(err error) {
			if key := err.Error(); !seen[key] {
				seen[key] = true
				saved(err)
			}
		}
		defer func() { check.conf.Error = saved }()
	}
	outer := check.logosFieldBind
	defer func() { check.logosFieldBind = outer }()
	loopScope := check.scope
	checked := 0
	bodyNodes := logosCountBodyNodes(s.Body)
	info.Iterations = make([]*Info, info.Struct.NumFields())
	liveFields := len(info.Iterations)
	terminated := false
	for i := range info.Struct.NumFields() {
		if !check.logosConstForCharge(s, bodyNodes) {
			break
		}
		f := info.Struct.Field(i)
		if !f.Exported() && f.Pkg() != check.pkg {
			continue
		}
		checked++
		check.logosFieldBind = &logosFieldBinding{info: info, index: i, field: f, tag: info.Struct.Tag(i), outer: outer}
		savedInfo, savedUntyped := check.Info, check.untyped
		savedTypeOf, savedDeferred := check.logosTypeOfFact, check.logosTypeOfDeferred
		check.Info = logosIsolatedInfo(savedInfo)
		if check.logosDeclInfo == nil {
			check.logosDeclInfo = savedInfo
		}
		check.untyped, check.logosTypeOfFact, check.logosTypeOfDeferred = nil, nil, nil
		check.openScope(s.Body, "const for iteration")
		check.stmtList(inner, s.Body.List)
		check.closeScope()
		check.logosKeepOuterUses(savedInfo, loopScope)
		term := check.isTerminatingList(s.Body.List, "")
		info.Term = append(info.Term, term)
		info.Breaks = append(info.Breaks, check.logosLiveBreaks(s.Body))
		if !terminated {
			info.Iterations[i] = check.Info
			if term {
				terminated = true
				liveFields = i + 1
			}
		}
		check.recordUntyped()
		check.Info, check.untyped = savedInfo, savedUntyped
		check.logosTypeOfFact, check.logosTypeOfDeferred = savedTypeOf, savedDeferred
		if check.logosDeclInfo == savedInfo {
			check.logosDeclInfo = nil
		}
	}
	info.Iterations = info.Iterations[:liveFields]
	if checked == 0 {
		check.usedVars[info.FieldVar] = true
		if info.IndexVar != nil {
			check.usedVars[info.IndexVar] = true
		}
		if check.Info.LogosConstIfDead == nil {
			check.Info.LogosConstIfDead = make(map[syntax.Stmt]bool)
		}
		for _, st := range s.Body.List {
			markDeadStmts(check.Info.LogosConstIfDead, st)
		}
		check.logosNoteDeadUses(s.Body)
	}
}

func (check *Checker) logosKeepOuterUses(parent *Info, loop *Scope) {
	if parent.Uses == nil {
		return
	}
	for id, obj := range check.Info.Uses {
		if obj.Parent() != nil && !logosScopeWithin(obj.Parent(), loop) {
			parent.Uses[id] = obj
		}
	}
}

func logosScopeWithin(s, outer *Scope) bool {
	for ; s != nil; s = s.parent {
		if s == outer {
			return true
		}
	}
	return false
}

func (check *Checker) logosConstForCharge(s *syntax.ConstForStmt, n int) bool {
	if check.logosConstForBudget > 8*check.logosNodeLimit() {
		return false
	}
	check.logosConstForBudget += n
	if check.logosConstForBudget > 8*check.logosNodeLimit() {
		check.errorf(s.Pos(), InvalidRangeExpr, "[comptime::node_limit] const for has %d iterations above the selected limit %d", check.logosConstForBudget, check.logosNodeLimit())
		return false
	}
	return true
}

func (check *Checker) logosNodeLimit() int {
	if check.conf.LogosComptimeNodeLimit > 0 {
		return check.conf.LogosComptimeNodeLimit
	}
	return 4096
}

func logosCountBodyNodes(root syntax.Node) int {
	n := 0
	syntax.Inspect(root, func(x syntax.Node) bool {
		n++
		return true
	})
	if n < 1 {
		return 1
	}
	return n
}

func (check *Checker) logosFieldDrain(s *syntax.ConstForStmt, info *LogosConstFor) {
	exempt := make(map[*syntax.Name]bool)
	syntax.Inspect(s.Body, func(n syntax.Node) bool {
		as, ok := n.(*syntax.AssignStmt)
		if !ok || as.Op != 0 {
			return true
		}
		lhs := syntax.UnpackListExpr(as.Lhs)
		rhs := syntax.UnpackListExpr(as.Rhs)
		if len(lhs) != 1 || len(rhs) != 1 {
			return true
		}
		lname, ok := lhs[0].(*syntax.Name)
		if !ok || lname.Value != "_" {
			return true
		}
		if rhs, ok := rhs[0].(*syntax.Name); ok {
			exempt[rhs] = true
		}
		return true
	})
	for name, v := range check.logosFieldPend {
		if v != info.FieldVar || exempt[name] {
			continue
		}
		check.errorf(name.Pos(), InvalidPkgUse, "[comptime::runtime_use] field variable %s is compile-time only", name.Value)
		check.usedVars[v] = true
		delete(check.logosFieldPend, name)
	}
}

func (check *Checker) logosFieldVarNote(e *syntax.Name, obj Object) {
	v, ok := obj.(*Var)
	if !ok || check.logosFieldVars[v] == nil {
		return
	}
	if check.logosFieldPend == nil {
		check.logosFieldPend = make(map[*syntax.Name]*Var)
	}
	check.logosFieldPend[e] = v
}

func (check *Checker) logosFieldVarSelMark(x *operand, e *syntax.SelectorExpr) bool {
	name, ok := e.X.(*syntax.Name)
	if !ok {
		return false
	}
	_, obj := check.lookupScope(name.Value)
	v, ok := obj.(*Var)
	if !ok || check.logosFieldVars[v] == nil {
		return false
	}
	if b := check.logosFieldBindingOf(v); b != nil {
		var typ Type
		var val constant.Value
		switch e.Sel.Value {
		case "Name":
			typ, val = Typ[String], constant.MakeString(b.field.Name())
		case "Index":
			typ, val = Typ[Int], constant.MakeInt64(int64(b.index))
		case "Exported":
			typ, val = Typ[Bool], constant.MakeBool(b.field.Exported())
		case "Tag":
			typ, val = check.logosComptimeObj("StructTag").Type(), constant.MakeString(b.tag)
		}
		if val != nil {
			check.recordUse(name, obj)
			check.usedVars[v] = true
			delete(check.logosFieldPend, name)
			x.mode_, x.typ_, x.val, x.expr = constant_, typ, val, e
			return true
		}
	}
	for _, sel := range logosFieldValueSels {
		if e.Sel.Value == sel {
			delete(check.logosFieldPend, name)
			return false
		}
	}
	if e.Sel.Value != "Type" {
		delete(check.logosFieldPend, name)
	}
	return false
}

func (check *Checker) logosFieldTypeType(e *syntax.SelectorExpr, x *operand) bool {
	name, ok := e.X.(*syntax.Name)
	if !ok || e.Sel.Value != "Type" {
		return false
	}
	_, obj := check.lookupScope(name.Value)
	v, ok := obj.(*Var)
	if !ok || check.logosFieldVars[v] == nil {
		return false
	}
	delete(check.logosFieldPend, name)
	check.recordUse(name, obj)
	check.usedVars[v] = true
	x.expr = e
	owner := check.logosFieldVars[v]
	// The owning loop is recorded for bound and unbound f.Type alike so
	// a case or tag can tell an outer loop's field (still varying per
	// outer iteration) from the innermost loop's (concrete now).
	if check.logosFieldTypeOwner == nil {
		check.logosFieldTypeOwner = make(map[syntax.Expr]*LogosConstFor)
	}
	check.logosFieldTypeOwner[e] = owner
	if b := check.logosFieldBindingOf(v); b != nil {
		x.mode_ = typexpr
		x.typ_ = b.field.Type()
		if check.logosFieldBound == nil {
			check.logosFieldBound = make(map[syntax.Expr]bool)
		}
		check.logosFieldBound[e] = true
		return true
	}
	x.mode_ = typexpr
	x.typ_ = logosComptimeAny
	return true
}

// logosFieldTypePend reports whether a type read through owner's field
// variable is unknown to this check: owner has no binding on the stack
// (the generic single check). Any bound field — of the innermost loop
// or of an outer one — resolves to a concrete type for this iteration.
func (check *Checker) logosFieldTypePend(owner *LogosConstFor) bool {
	if owner == nil {
		return false
	}
	for b := check.logosFieldBind; b != nil; b = b.outer {
		if owner == b.info {
			return false
		}
	}
	return true
}

// logosFieldTypeVary reports whether a bound field variable belongs to
// an outer loop of the switch being checked: its type is concrete for
// this iteration but varies across outer iterations, so static case
// coverage defers to each iteration while dead-case detection may use
// the concrete type on hand.
func (check *Checker) logosFieldTypeVary(owner *LogosConstFor) bool {
	if owner == nil || check.logosFieldBind == nil {
		return false
	}
	return owner != check.logosFieldBind.info
}

func (check *Checker) logosFieldProjExpr(x *operand, e *syntax.FieldProjExpr) {
	x.invalidate()
	x.expr = e
	_, obj := check.lookupScope(e.Field.Value)
	v, ok := obj.(*Var)
	info := check.logosFieldVars[v]
	if !ok || info == nil {
		check.errorf(e.Field.Pos(), MissingFieldOrMethod, "[comptime::const_for_projection] %s is not a const for field variable", e.Field.Value)
		return
	}
	delete(check.logosFieldPend, e.Field)
	check.recordUse(e.Field, v)
	check.usedVars[v] = true
	var xop operand
	check.expr(nil, &xop, e.X)
	if !xop.isValid() {
		return
	}
	xt := Unalias(xop.typ())
	mode := operandMode(value)
	if xop.mode() == variable {
		mode = variable
	}
	if pt, ok := xt.(*Pointer); ok {
		xt = Unalias(pt.Elem())
		mode = variable
	}
	if _, ok := xt.(*TypeParam); ok {
		if info.Generic {
			x.mode_ = mode
			x.typ_ = check.logosFieldNarrowOr(info)
			return
		}
		check.errorf(e.Pos(), MissingFieldOrMethod, "[comptime::const_for_projection] cannot project a field of %s", xop.typ())
		return
	}
	if info.Struct != nil && !Identical(xt, info.Target) && check.logosCtSubst != nil {
		if b := check.logosFieldBindingOf(v); b != nil {
			check.logosFieldProjByName(x, &xop, mode, b.field)
			return
		}
	}
	if info.Struct == nil || !Identical(xt, info.Target) {
		check.errorf(e.Pos(), MissingFieldOrMethod, "[comptime::const_for_projection] cannot project a field of %s", xop.typ())
		return
	}
	x.mode_ = mode
	if b := check.logosFieldBindingOf(v); b != nil {
		x.typ_ = b.field.Type()
		return
	}
	x.typ_ = check.logosFieldNarrowOr(info)
}

func (check *Checker) logosFieldProjByName(x, xop *operand, mode operandMode, field *Var) {
	obj, _, indirect := lookupFieldOrMethod(xop.typ(), false, check.pkg, field.Name(), false)
	fld, ok := obj.(*Var)
	if !ok {
		check.errorf(x.expr.Pos(), MissingFieldOrMethod, "[comptime::const_for_projection] cannot project field %s of %s", field.Name(), xop.typ())
		return
	}
	if indirect {
		mode = variable
	}
	x.mode_ = mode
	x.typ_ = fld.Type()
}

// logosFieldNarrowOr answers the field's type as narrowed by the
// const-switch case under check, or the loop's shared field type when no
// case narrowed it.
func (check *Checker) logosFieldNarrowOr(info *LogosConstFor) Type {
	if n, ok := check.logosFieldNarrow[info]; ok && n != nil {
		return n
	}
	return check.logosFieldProjType(info)
}

func (check *Checker) logosFieldProjType(info *LogosConstFor) Type {
	if info.Struct == nil {
		return logosComptimeAny
	}
	var shared Type
	for i := range info.Struct.NumFields() {
		t := info.Struct.Field(i).Type()
		if shared == nil {
			shared = t
		} else if !Identical(shared, t) {
			return logosComptimeAny
		}
	}
	if shared == nil {
		return logosComptimeAny
	}
	return shared
}

func (check *Checker) logosConstForExecutes(s *syntax.ConstForStmt) bool {
	info := check.Info.LogosConstFor[s]
	if info == nil {
		return false
	}
	if info.Generic && info.Struct == nil {
		return true
	}
	if info.Struct == nil {
		return false
	}
	for i := range info.Struct.NumFields() {
		f := info.Struct.Field(i)
		if f.Exported() || f.Pkg() == check.pkg {
			return true
		}
	}
	return false
}

func (check *Checker) logosFieldBindingOf(v *Var) *logosFieldBinding {
	for b := check.logosFieldBind; b != nil; b = b.outer {
		if b.info.FieldVar == v {
			return b
		}
	}
	return nil
}

func (check *Checker) logosTagGetFold(x *operand, call *syntax.CallExpr, sig *Signature, args []*operand) bool {
	if check.logosFieldBind == nil || sig.recvold == nil {
		return false
	}
	fun, ok := syntax.Unparen(call.Fun).(*syntax.SelectorExpr)
	if !ok || fun.Sel.Value != "Get" || sig.recvold.Type() != check.logosComptimeObj("StructTag").Type() {
		return false
	}
	sel, ok := syntax.Unparen(fun.X).(*syntax.SelectorExpr)
	if !ok || sel.Sel.Value != "Tag" {
		return false
	}
	name, ok := sel.X.(*syntax.Name)
	if !ok {
		return false
	}
	_, obj := check.lookupScope(name.Value)
	v, _ := obj.(*Var)
	b := check.logosFieldBindingOf(v)
	if b == nil {
		return false
	}
	if hasDots(call) || len(args) != 1 || !args[0].isValid() {
		x.invalidate()
		return true
	}
	fact := LogosTagGet{Receiver: sel, Tag: b.tag}
	if arg := args[0]; arg.mode() == constant_ && arg.val.Kind() == constant.String {
		tag := reflect.StructTag(b.tag)
		fact.Value = constant.MakeString(tag.Get(constant.StringVal(arg.val)))
	}
	if check.Info.LogosTagGet == nil {
		check.Info.LogosTagGet = make(map[*syntax.CallExpr]LogosTagGet)
	}
	check.Info.LogosTagGet[call] = fact
	if fact.Value == nil {
		return false
	}
	x.mode_, x.val = constant_, fact.Value
	return true
}

// logosTerminatingConstFor reports whether a const for is terminating by
// its expansion: over a concrete struct the statement stands for the
// sequence of its per-field bodies, so it terminates when some visible
// field's body terminates under that field's predicate values, and a
// struct with no visible fields expands to nothing and never terminates.
// When no per-field verdict was recorded (a generic const for or a
// rejected range), it falls back to the shared body, the previous rule.
// logosConstForConcrete records Term per visible field.
func (check *Checker) logosTerminatingConstFor(s *syntax.ConstForStmt) bool {
	info := check.Info.LogosConstFor[s]
	if info == nil {
		return false
	}
	if len(info.Term) == 0 && info.Struct == nil {
		return check.isTerminatingList(s.Body.List, "")
	}
	for _, term := range info.Term {
		if term {
			return true
		}
	}
	return false
}

// logosHasBreakConstFor reports whether some visible field's live body
// holds a break that leaves the const for for the label-ed statement or,
// when implicit is set, the closest outer breakable statement. Only the
// emitted prefix counts: unrolling stops after the first terminating
// field, so later fields never run. A struct with no visible fields holds
// no break. When no per-field verdict was recorded, it falls back to the
// shared body, the previous rule. logosConstForConcrete records Breaks
// per visible field.
func (check *Checker) logosHasBreakConstFor(s *syntax.ConstForStmt, label string, implicit bool) bool {
	info := check.Info.LogosConstFor[s]
	if info == nil {
		return false
	}
	if len(info.Breaks) == 0 && info.Struct == nil {
		return check.hasBreak(s.Body, label, implicit)
	}
	for i, brk := range info.Breaks {
		if implicit && brk.Implicit {
			return true
		}
		if label != "" && slices.Contains(brk.Labels, label) {
			return true
		}
		if i < len(info.Term) && info.Term[i] {
			break
		}
	}
	return false
}

// LogosConstForBreaks names the breaks of one field's live body that can
// leave the const for: Implicit is an unlabeled break of the enclosing
// breakable statement, Labels are labels not bound inside the body.
type LogosConstForBreaks struct {
	Implicit bool
	Labels   []string
}

// logosLiveBreaks collects the breaks in a const for body that can leave
// the statement: an unlabeled break of the enclosing breakable statement,
// and a labeled break whose label is not bound to a breakable nested in
// the body (labels are unique per function, so a label not bound inside
// names a statement outside). Dead const code, which this field's
// expansion does not run, holds no break.
func (check *Checker) logosLiveBreaks(s syntax.Stmt) LogosConstForBreaks {
	var brk LogosConstForBreaks
	check.logosLiveBreakWalk(s, false, nil, &brk)
	return brk
}

// logosLiveBreakWalk walks s collecting leaving breaks into brk. inner
// reports whether an unlabeled break under s binds to a breakable nested
// in the const for body; bound holds the labels of those breakables.
func (check *Checker) logosLiveBreakWalk(s syntax.Stmt, inner bool, bound map[string]bool, brk *LogosConstForBreaks) {
	if check.logosDead(s) {
		return
	}
	switch s := s.(type) {
	case *syntax.BranchStmt:
		if s.Tok != syntax.Break {
			return
		}
		if s.Label == nil {
			if !inner {
				brk.Implicit = true
			}
			return
		}
		if !bound[s.Label.Value] && !slices.Contains(brk.Labels, s.Label.Value) {
			brk.Labels = append(brk.Labels, s.Label.Value)
		}

	case *syntax.LabeledStmt:
		if logosBreakableStmt(s.Stmt) {
			bound = logosBindLabel(bound, s.Label.Value)
		}
		check.logosLiveBreakWalk(s.Stmt, inner, bound, brk)

	case *syntax.BlockStmt:
		check.logosLiveBreakList(s.List, inner, bound, brk)

	case *syntax.IfStmt:
		check.logosLiveBreakWalk(s.Then, inner, bound, brk)
		if s.Else != nil {
			check.logosLiveBreakWalk(s.Else, inner, bound, brk)
		}

	case *syntax.ForStmt:
		check.logosLiveBreakList(s.Body.List, true, bound, brk)

	case *syntax.SwitchStmt:
		for _, cc := range s.Body {
			check.logosLiveBreakList(cc.Body, true, bound, brk)
		}

	case *syntax.SelectStmt:
		for _, cc := range s.Body {
			check.logosLiveBreakList(cc.Body, true, bound, brk)
		}

	case *syntax.ConstIfStmt:
		if v, ok := check.logosConstIfKnown(s.Cond); ok {
			if v {
				check.logosLiveBreakWalk(s.Then, inner, bound, brk)
			} else if s.Else != nil {
				check.logosLiveBreakWalk(s.Else, inner, bound, brk)
			}
			return
		}
		check.logosLiveBreakWalk(s.Then, inner, bound, brk)
		if s.Else != nil {
			check.logosLiveBreakWalk(s.Else, inner, bound, brk)
		}

	case *syntax.ConstSwitchStmt:
		for _, c := range s.Cases {
			check.logosLiveBreakList(c.Body, inner, bound, brk)
		}

	case *syntax.GroupStmt:
		check.logosLiveBreakList(s.Body.List, inner, bound, brk)

	case *syntax.ConstForStmt:
		// A nested const for keeps the enclosing label namespace; its own
		// recorded per-field breaks are the breaks its expansion runs.
		// Without a record (a generic or rejected nested range) the walk
		// falls back to the nested body, the previous rule.
		if ninfo := check.Info.LogosConstFor[s]; ninfo != nil && (len(ninfo.Breaks) > 0 || ninfo.Struct != nil) {
			for _, nbrk := range ninfo.Breaks {
				if nbrk.Implicit && !inner {
					brk.Implicit = true
				}
				for _, l := range nbrk.Labels {
					if !bound[l] && !slices.Contains(brk.Labels, l) {
						brk.Labels = append(brk.Labels, l)
					}
				}
			}
		} else {
			check.logosLiveBreakWalk(s.Body, inner, bound, brk)
		}
	}
}

func (check *Checker) logosLiveBreakList(list []syntax.Stmt, inner bool, bound map[string]bool, brk *LogosConstForBreaks) {
	for _, s := range list {
		check.logosLiveBreakWalk(s, inner, bound, brk)
	}
}

func logosBreakableStmt(s syntax.Stmt) bool {
	switch s.(type) {
	case *syntax.ForStmt, *syntax.SwitchStmt, *syntax.SelectStmt:
		return true
	}
	return false
}

func logosBindLabel(bound map[string]bool, label string) map[string]bool {
	if bound[label] {
		return bound
	}
	next := make(map[string]bool, len(bound)+1)
	for l := range bound {
		next[l] = true
	}
	next[label] = true
	return next
}

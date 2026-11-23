// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	"cmd/internal/hash"
	"encoding/hex"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	. "internal/types/errors"
)

func (check *Checker) logosCtAnalyze(pos syntax.Pos, expr syntax.Expr, xlist []syntax.Expr, typ *Signature, targs []Type, inst *Signature) *Signature {
	if check.logosCtImplicitTargs(pos, expr, xlist, targs) {
		return inst
	}
	if !check.conf.Logos || check.logosCtDeclCheck || check.logosCtTParamDecl || check.logosCtSubst != nil {
		return nil
	}
	if check.sig != nil && (check.sig.TypeParams().Len() != 0 || check.sig.RecvTypeParams().Len() != 0) {
		return nil
	}
	for _, targ := range targs {
		if logosContainsTypeParam(targ, nil) {
			check.errorf(pos, InvalidPkgUse, "[comptime::generic_targ] cannot instantiate %s with a type parameter", typ)
			return inst
		}
	}
	return nil
}

func (check *Checker) logosCtImplicitTargs(pos syntax.Pos, expr syntax.Expr, xlist []syntax.Expr, targs []Type) bool {
	if !check.conf.Logos || len(xlist) >= len(targs) || check.logosCtSubst == nil && check.logosFieldBind == nil {
		return false
	}
	fn := check.logosCallee(expr)
	if fn == nil || !check.logosIsComptimeGeneric(fn) {
		return false
	}
	at := pos
	if n := len(check.logosCtPos); check.logosCtSubst != nil && n > 0 {
		at = check.logosCtPos[n-1]
	}
	check.errorf(at, InvalidPkgUse, "[comptime::generic_targ] comptime generic %s needs explicit type arguments here", fn.Name())
	return true
}

func (check *Checker) logosCallee(expr syntax.Expr) *Func {
	e := syntax.Unparen(expr)
	if index, ok := e.(*syntax.IndexExpr); ok {
		e = syntax.Unparen(index.X)
	}
	var name *syntax.Name
	switch e := e.(type) {
	case *syntax.Name:
		name = e
	case *syntax.SelectorExpr:
		name = e.Sel
	default:
		return nil
	}
	fn, _ := check.Uses[name].(*Func)
	return fn
}

func (check *Checker) logosIsComptimeGeneric(fn *Func) bool {
	origin := fn.Origin()
	if di := check.objMap[origin]; di != nil && di.fdecl != nil {
		return di.fdecl.Body != nil && LogosUsesComptime(di.fdecl, di.file)
	}
	imported := check.LogosCtImport
	if check.logosDeclInfo != nil {
		imported = check.logosDeclInfo.LogosCtImport
	}
	if imported == nil {
		return false
	}
	generic, ok := imported(origin)
	return ok && generic.Decl != nil
}

func (check *Checker) logosRejectGenericMethod(decl *declInfo, body *syntax.BlockStmt) {
	if !check.conf.Logos || decl == nil || decl.fdecl == nil || decl.fdecl.Recv == nil || body != decl.fdecl.Body {
		return
	}
	if check.logosHasGenericConstFor(body) {
		check.errorf(decl.fdecl.Pos(), InvalidPkgUse, "[comptime::generic_method] method %s of a generic type cannot be a comptime generic", decl.fdecl.Name.Value)
	}
}

func (check *Checker) logosHasGenericConstFor(body *syntax.BlockStmt) bool {
	found := false
	syntax.Inspect(body, func(n syntax.Node) bool {
		if loop, ok := n.(*syntax.ConstForStmt); ok && check.Info.LogosConstFor[loop] != nil {
			found = found || check.Info.LogosConstFor[loop].Generic
		}
		return !found
	})
	return found
}

func (check *Checker) logosCtDrain() {
	if !check.conf.Logos || check.firstErr != nil || check.conf.IgnoreFuncBodies || check.Instances == nil || check.Uses == nil {
		return
	}
	work := logosCtWork{
		check:  check,
		seen:   make(map[string]string),
		active: make(map[string]int),
		graph:  make(map[string][]string),
	}
	decls := make(map[*syntax.FuncDecl]bool)
	for obj, di := range check.objMap {
		fn, ok := obj.(*Func)
		if !ok || di == nil || di.fdecl == nil || di.fdecl.Body == nil || fn.Signature().Recv() != nil || fn.Signature().TypeParams().Len() == 0 {
			continue
		}
		decl := di.fdecl
		var generic LogosCtGeneric
		if LogosUsesComptime(decl, di.file) {
			decls[decl] = true
			generic = LogosCtGeneric{Decl: decl, Scope: di.file, GoVersion: string(di.version)}
		} else {
			generic.Edges = logosCtEdges(check.Info, decl.Body, nil)
			if len(generic.Edges) == 0 {
				continue
			}
		}
		if check.LogosCtGenerics == nil {
			check.LogosCtGenerics = make(map[*Func]LogosCtGeneric)
		}
		check.LogosCtGenerics[fn] = generic
	}
	for _, file := range check.files {
		for _, edge := range logosCtEdges(check.Info, file, decls) {
			work.bind(edge, edge.Pos)
		}
	}
	work.finish()
	check.cleanup()
}

type logosCtFrame struct {
	comptime bool
	targs    []Type
}

type logosCtWork struct {
	check         *Checker
	seen          map[string]string
	active        map[string]int
	stack         []logosCtFrame
	graph         map[string][]string
	sites         []logosCtUse
	comptimeDepth int
}

type logosCtUse struct {
	info *Info
	name *syntax.Name
	id   string
}

func (w *logosCtWork) bind(edge LogosCtEdge, pos syntax.Pos) {
	if id := w.visit(edge, pos); id != "" {
		w.sites = append(w.sites, logosCtUse{edge.info, edge.Site, id})
	}
}

func (w *logosCtWork) finish() {
	check := w.check
	if len(check.LogosCtInst) == 0 {
		return
	}
	parents := make(map[string][]string, len(w.graph))
	for parent, children := range w.graph {
		for _, child := range children {
			if child != "" {
				parents[child] = append(parents[child], parent)
			}
		}
	}
	work := make([]string, 0, len(check.LogosCtInst))
	exact := make(map[string]bool, len(check.LogosCtInst))
	for _, inst := range check.LogosCtInst {
		work = append(work, inst.ID)
		exact[inst.ID] = true
	}
	graph := make(map[string][]string)
	for i := 0; i < len(work); i++ {
		for _, parent := range parents[work[i]] {
			if _, ok := graph[parent]; !ok {
				graph[parent] = w.graph[parent]
				work = append(work, parent)
			}
		}
	}
	for _, children := range graph {
		for i, child := range children {
			if _, ok := graph[child]; !ok && !exact[child] {
				children[i] = ""
			}
		}
	}
	check.LogosCtGraph = graph
	for _, site := range w.sites {
		if _, ok := graph[site.id]; !ok && !exact[site.id] {
			continue
		}
		if site.info.LogosCtSiteKeys == nil {
			site.info.LogosCtSiteKeys = make(map[*syntax.Name]string)
		}
		site.info.LogosCtSiteKeys[site.name] = site.id
	}
}

func (w *logosCtWork) visit(edge LogosCtEdge, pos syntax.Pos) string {
	check := w.check
	origin, targs := edge.Origin, edge.Targs
	sig := origin.Signature()
	if sig.Recv() != nil || sig.TypeParams().Len() != len(targs) {
		return ""
	}
	generic, ok := check.LogosCtGenerics[origin]
	if !ok && origin.Pkg() != check.pkg && check.LogosCtImport != nil {
		generic, ok = check.LogosCtImport(origin)
	}
	if !ok {
		return ""
	}
	key := LogosCtKey(origin, targs)
	if key == "" {
		return ""
	}
	if i, ok := w.active[key]; ok {
		var labels []string
		for _, frame := range w.stack[i:] {
			if frame.comptime {
				labels = append(labels, logosCtLabel(frame.targs))
			}
		}
		if len(labels) != 0 {
			labels = append(labels, labels[0])
			check.errorf(pos, InvalidPkgUse, "[comptime::circular_instantiation] %s", strings.Join(labels, " -> "))
		}
		return w.seen[key]
	}
	if id, ok := w.seen[key]; ok {
		return id
	}
	comptime := generic.Decl != nil
	if comptime && w.comptimeDepth >= check.logosNodeLimit() {
		check.errorf(pos, InvalidPkgUse, "[comptime::node_limit] comptime instantiation exceeds depth limit %d", check.logosNodeLimit())
		return ""
	}
	sum := hash.Sum32([]byte(key))
	id := hex.EncodeToString(sum[:])
	w.seen[key] = id
	w.active[key] = len(w.stack)
	w.stack = append(w.stack, logosCtFrame{comptime: comptime, targs: targs})
	if comptime {
		w.comptimeDepth++
	}
	defer func() {
		if comptime {
			w.comptimeDepth--
		}
		w.stack = w.stack[:len(w.stack)-1]
		delete(w.active, key)
	}()

	subst := makeSubstMap(sig.TypeParams().list(), targs)
	if comptime {
		rec := check.logosCtAnalyzeFunc(pos, origin, targs, id, subst, generic)
		if rec == nil {
			return ""
		}
		if check.LogosCtInst == nil {
			check.LogosCtInst = make(map[string]*LogosCtInst)
		}
		check.LogosCtInst[key] = rec
		for _, edge := range logosCtEdges(rec.Info, rec.Decl.Body, nil) {
			w.bind(edge, pos)
		}
		return id
	}
	children := make([]string, len(generic.Edges))
	for slot, edge := range generic.Edges {
		var concrete []Type
		for i, t := range edge.Targs {
			newType := check.subst(edge.Pos, t, subst, nil, check.context())
			if newType != t {
				if concrete == nil {
					concrete = slices.Clone(edge.Targs)
				}
				concrete[i] = newType
			}
		}
		if concrete != nil {
			edge.Targs = concrete
		}
		children[slot] = w.visit(edge, pos)
	}
	w.graph[id] = children
	return id
}

func logosCtEdges(info *Info, root syntax.Node, generics map[*syntax.FuncDecl]bool) []LogosCtEdge {
	var edges []LogosCtEdge
	var visit func(*Info, syntax.Node)
	visit = func(info *Info, root syntax.Node) {
		syntax.Inspect(root, func(n syntax.Node) bool {
			switch n := n.(type) {
			case *syntax.FuncDecl:
				return !generics[n]
			case syntax.Stmt:
				if info.LogosConstIfDead[n] {
					return false
				}
				if loop := info.LogosConstFor[n]; loop != nil {
					for _, iteration := range loop.Iterations {
						if iteration != nil {
							visit(iteration, n.(*syntax.ConstForStmt).Body)
						}
					}
					return false
				}
			case *syntax.Name:
				fn, ok := info.Uses[n].(*Func)
				inst, found := info.Instances[n]
				if !ok || !found {
					return true
				}
				sig := fn.Signature()
				if sig.Recv() == nil && sig.TypeParams().Len() != 0 && sig.TypeParams().Len() == inst.TypeArgs.Len() {
					edges = append(edges, LogosCtEdge{Origin: fn.Origin(), Targs: inst.TypeArgs.list(), Pos: n.Pos(), Site: n, info: info})
				}
			}
			return true
		})
	}
	visit(info, root)
	return edges
}

func (check *Checker) logosCtAnalyzeFunc(pos syntax.Pos, origin *Func, targs []Type, id string, subst substMap, generic LogosCtGeneric) *LogosCtInst {
	decl := generic.Decl
	if nodes := logosCountBodyNodes(decl); nodes > check.logosNodeLimit() {
		check.errorf(pos, InvalidRangeExpr, "[comptime::node_limit] comptime generic %s has %d nodes above the limit %d", origin.Name(), nodes, check.logosNodeLimit())
		return nil
	}
	decl = syntax.CloneLogosCtSyntax(decl)
	savedSubst, savedPkg, savedVersion := check.logosCtSubst, check.pkg, check.version
	check.logosCtSubst, check.pkg, check.version = subst, origin.Pkg(), asGoVersion(generic.GoVersion)
	check.logosCtPos = append(check.logosCtPos, pos)
	defer func() {
		check.logosCtSubst, check.pkg, check.version = savedSubst, savedPkg, savedVersion
		check.logosCtPos = check.logosCtPos[:len(check.logosCtPos)-1]
	}()
	failed := false
	defer check.logosCtTrackFailure(&failed)()
	sig, facts := check.logosCtInstanceBody(origin, origin.Signature(), targs, decl, generic.Scope)
	if failed {
		return nil
	}
	return &LogosCtInst{
		Origin: origin,
		Targs:  targs,
		Decl:   decl,
		Sig:    sig,
		Sym:    origin.Name() + ".logosct." + id,
		ID:     id,
		Pkg:    origin.Pkg(),
		Info:   facts,
	}
}

func (check *Checker) logosCtTrackFailure(failed *bool) func() {
	report := check.conf.Error
	if report == nil {
		return func() {}
	}
	check.conf.Error = func(err error) {
		if e, ok := err.(Error); !ok || !check.logosWarnOnly(e) {
			*failed = true
		}
		report(err)
	}
	return func() { check.conf.Error = report }
}

func (check *Checker) logosCtErrorPos(pos syntax.Pos) syntax.Pos {
	n := len(check.logosCtPos)
	if check.logosCtSubst == nil || check.logosFieldBind == nil || n == 0 {
		return pos
	}
	return check.logosCtPos[n-1]
}

func logosIsolatedInfo(parent *Info) *Info {
	return &Info{
		Types:                      make(map[syntax.Expr]TypeAndValue),
		Uses:                       make(map[*syntax.Name]Object),
		Defs:                       make(map[*syntax.Name]Object),
		Instances:                  make(map[*syntax.Name]Instance),
		Selections:                 make(map[*syntax.SelectorExpr]*Selection),
		Implicits:                  make(map[syntax.Node]Object),
		Scopes:                     make(map[syntax.Node]*Scope),
		StoreTypesInSyntax:         parent.StoreTypesInSyntax,
		FileVersions:               parent.FileVersions,
		LogosFieldDefaults:         maps.Clone(parent.LogosFieldDefaults),
		LogosCtImportNamedDefaults: parent.LogosCtImportNamedDefaults,
		LogosCtImportConstMap:      parent.LogosCtImportConstMap,
		LogosCtDefaultOrigin:       parent,
		LogosCTFE:                  maps.Clone(parent.LogosCTFE),
		LogosConstFuncs:            maps.Clone(parent.LogosConstFuncs),
		LogosImporter:              parent.LogosImporter,
		LogosContext:               parent.LogosContext,
		LogosCTFEMaxSteps:          parent.LogosCTFEMaxSteps,
	}
}

// LogosCtNamedRHS returns the declaration RHS with the instance's arguments
// substituted. Imported types only retain their ordinary Go underlying type.
func LogosCtNamedRHS(t *Named) Type {
	return t.unpack().rhs()
}

// LogosCtNamedRHS recovers the imported derived declaration and substitutes
// the supplied type arguments; Go export data retains only its underlying type.
func (info *Info) LogosCtNamedRHS(t *Named) Type {
	if info.LogosCtImportNamedDefaults != nil {
		if rhs := info.LogosCtImportNamedDefaults(t.Origin()).RHS; rhs != nil {
			return LogosCtSubstituteNamed(rhs, t, info.LogosContext)
		}
	}
	return LogosCtNamedRHS(t)
}

// LogosCtImportedNamedDefaults returns the declaration's original factories.
func (info *Info) LogosCtImportedNamedDefaults(t *Named) []LogosCtNamedDefault {
	if info.LogosCtImportNamedDefaults == nil {
		return nil
	}
	return info.LogosCtImportNamedDefaults(t.Origin()).Fields
}

// LogosCtSubstituteNamed replaces a named declaration's type parameters in t
// with their arguments from inst, using ctxt to preserve named type identity.
func LogosCtSubstituteNamed(t Type, inst *Named, ctxt *Context) Type {
	args := inst.TypeArgs().list()
	if len(args) == 0 {
		return t
	}
	if ctxt == nil {
		ctxt = NewContext()
	}
	return (*Checker)(nil).subst(inst.Obj().Pos(), t, makeSubstMap(inst.Origin().TypeParams().list(), args), nil, ctxt)
}

func (check *Checker) logosCtInstanceBody(origin *Func, sig *Signature, targs []Type, decl *syntax.FuncDecl, scope *Scope) (*Signature, *Info) {
	parent, declInfo := check.Info, check.logosDeclInfo
	untyped, bind, pred := check.untyped, check.logosFieldBind, check.logosPred
	typeOf, deferred := check.logosTypeOfFact, check.logosTypeOfDeferred
	depth, budget := check.logosConstForDepth, check.logosConstForBudget
	switchCases := check.logosCSwitch
	abstract := check.logosCtAbstract
	defer func() {
		check.Info, check.logosDeclInfo = parent, declInfo
		check.untyped, check.logosFieldBind, check.logosPred = untyped, bind, pred
		check.logosTypeOfFact, check.logosTypeOfDeferred = typeOf, deferred
		check.logosConstForDepth, check.logosConstForBudget = depth, budget
		check.logosCSwitch = switchCases
		check.logosCtAbstract = abstract
	}()
	check.Info = logosIsolatedInfo(parent)
	check.Info.FileVersions = map[*syntax.PosBase]string{decl.Pos().FileBase(): string(check.version)}
	if check.logosDeclInfo == nil {
		check.logosDeclInfo = parent
	}
	check.untyped, check.logosFieldBind = nil, nil
	check.logosPred = logosPredState{}
	check.logosTypeOfFact, check.logosTypeOfDeferred = nil, nil
	check.logosConstForDepth, check.logosConstForBudget = 0, 0
	check.logosCSwitch = nil
	check.logosCtAbstract = nil
	bodySig := check.logosCtCheckBody(origin, sig, targs, decl, scope)
	check.recordUntyped()
	return bodySig, check.Info
}

func logosCtLabel(targs []Type) string {
	parts := make([]string, len(targs))
	for i, t := range targs {
		parts[i] = TypeString(t, func(*Package) string { return "" })
	}
	return strings.Join(parts, ", ")
}

// LogosCtKey is the stable identity of an instance of a package-level
// function. It returns an empty key if the arguments are not concrete.
func LogosCtKey(origin *Func, targs []Type) string {
	if origin.Signature().Recv() != nil || len(targs) != origin.Signature().TypeParams().Len() {
		return ""
	}
	for _, t := range targs {
		if !isValid(t) || logosContainsTypeParam(t, nil) {
			return ""
		}
	}
	var key logosCtTypeKey
	key.name(origin)
	key.list(targs)
	return key.String()
}

type logosCtTypeKey struct {
	strings.Builder
	interfaces []Type
}

func (k *logosCtTypeKey) number(n int64) {
	k.WriteString(strconv.FormatInt(n, 10))
	k.WriteByte(':')
}

func (k *logosCtTypeKey) text(s string) {
	k.number(int64(len(s)))
	k.WriteString(s)
}

func (k *logosCtTypeKey) name(obj Object) {
	pkg := obj.Pkg()
	path := ""
	if pkg != nil {
		path = pkg.Path()
	}
	k.text(path)
	k.text(obj.Name())
}

func (k *logosCtTypeKey) list(ts []Type) {
	k.number(int64(len(ts)))
	for _, t := range ts {
		k.typ(t)
	}
}

func (k *logosCtTypeKey) vars(ts *Tuple) {
	k.number(int64(ts.Len()))
	for i := range ts.Len() {
		k.typ(ts.At(i).Type())
	}
}

func (k *logosCtTypeKey) typ(typ Type) {
	switch t := Unalias(typ).(type) {
	case *Basic:
		k.WriteByte('b')
		k.number(int64(t.Kind()))
	case *Named:
		obj := t.Obj()
		origin, captures, explicit := LogosCtLocalNamed(t)
		if origin != nil {
			obj = origin.Obj()
		}
		k.WriteByte('n')
		k.name(obj)
		if pkg := obj.Pkg(); origin != nil || pkg != nil && obj.Parent() != pkg.Scope() {
			pos := obj.Pos()
			file := ""
			k.WriteByte('l')
			if base := pos.FileBase(); base != nil {
				file = filepath.Base(base.Filename())
			}
			k.text(file)
			k.number(int64(pos.Line()))
			k.number(int64(pos.Col()))
			k.text(LogosCtLocalDiscriminator(t))
			k.list(captures)
		} else {
			k.WriteByte('g')
		}
		if origin == nil {
			explicit = t.TypeArgs().list()
		}
		k.list(explicit)
	case *Array:
		k.WriteByte('a')
		k.number(t.Len())
		k.typ(t.Elem())
	case *Slice:
		k.WriteByte('s')
		k.typ(t.Elem())
	case *Pointer:
		k.WriteByte('p')
		k.typ(t.Elem())
	case *Map:
		k.WriteByte('m')
		k.typ(t.Key())
		k.typ(t.Elem())
	case *Chan:
		k.WriteByte('c')
		k.number(int64(t.Dir()))
		k.typ(t.Elem())
	case *Signature:
		k.WriteByte('f')
		k.WriteString(strconv.FormatBool(t.Variadic()))
		k.vars(t.Params())
		k.vars(t.Results())
	case *Struct:
		k.WriteByte('t')
		k.number(int64(t.NumFields()))
		for i := range t.NumFields() {
			f := t.Field(i)
			k.text(f.Id())
			k.text(t.Tag(i))
			k.WriteString(strconv.FormatBool(f.Embedded()))
			k.typ(f.Type())
		}
	case *Interface:
		if i := slices.Index(k.interfaces, Type(t)); i >= 0 {
			k.WriteByte('r')
			k.number(int64(i))
			return
		}
		k.interfaces = append(k.interfaces, t)
		k.WriteByte('i')
		ts := t.typeSet()
		k.WriteString(strconv.FormatBool(ts.comparable))
		k.number(int64(len(ts.methods)))
		for _, m := range ts.methods {
			k.text(m.Id())
			k.WriteString(strconv.FormatBool(m.LogosReadonly()))
			k.typ(m.Type())
		}
		if ts.terms.isAll() {
			k.WriteByte('*')
		} else {
			var terms []string
			for _, term := range ts.terms {
				if term == nil {
					continue
				}
				termKey := logosCtTypeKey{interfaces: k.interfaces}
				termKey.WriteString(strconv.FormatBool(term.tilde))
				termKey.typ(term.typ)
				terms = append(terms, termKey.String())
			}
			slices.Sort(terms)
			k.number(int64(len(terms)))
			for _, term := range terms {
				k.text(term)
			}
		}
		k.interfaces = k.interfaces[:len(k.interfaces)-1]
	case *ConstView:
		k.WriteByte('v')
		k.typ(t.Elem)
	case *ReadonlyDescendants:
		k.WriteByte('d')
		k.typ(t.Elem)
	case *Option:
		k.WriteByte('o')
		k.typ(t.Elem)
	case *LogosTuple:
		k.typ(t.underlyingStruct())
	default:
		panic("non-concrete comptime type argument")
	}
}

// LogosUsesComptime identifies a comptime generic body without relying on
// checked names from deferred branches.
func LogosUsesComptime(fdecl *syntax.FuncDecl, fileScope *Scope) bool {
	found := false
	syntax.Inspect(fdecl.Body, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.ConstForStmt, *syntax.ConstIfStmt, *syntax.FieldProjExpr:
			found = true
		case *syntax.SelectorExpr:
			if name, ok := n.X.(*syntax.Name); ok && fileScope != nil {
				if pn, ok := fileScope.Lookup(name.Value).(*PkgName); ok && pn.Imported().Path() == "comptime" {
					found = true
				}
			}
		}
		return !found
	})
	return found
}

// logosCtCheckBody type-checks one concrete instance of a comptime
// generic: a fresh scope under the defining file's scope binds each
// type parameter to its concrete argument and each parameter and
// result to its substituted type, then the body is checked as ordinary
// Go with the comptime fact folds active.
func (check *Checker) logosCtCheckBody(origin *Func, sig *Signature, targs []Type, fdecl *syntax.FuncDecl, fileScope *Scope) *Signature {
	tp := sig.TypeParams().list()
	for _, tpar := range tp {
		if logosPropertyInterface(tpar.Constraint()) != nil {
			check.logosCtAbstract = make(map[Object]Type)
			break
		}
	}
	paramScope := &Scope{parent: fileScope, pos: fdecl.Pos(), end: syntax.EndPos(fdecl), comment: "comptime instance"}
	for i, tpar := range tp {
		if name := tpar.Obj().Name(); name != "" {
			binder := NewTypeName(fdecl.Pos(), origin.Pkg(), name, targs[i])
			paramScope.insert(name, binder)
			check.logosCtKeepType(binder, tpar)
		}
	}
	smap := check.logosCtSubst
	bodySig := *sig
	bodySig.tparams = nil
	bodySig.scope = paramScope
	bodySig.params = NewTuple(substTupleVars(check, sig.params, smap)...)
	bodySig.results = NewTuple(substTupleVars(check, sig.results, smap)...)
	for _, tuple := range []*Tuple{bodySig.params, bodySig.results} {
		if tuple == nil {
			continue
		}
		for i := range tuple.Len() {
			if v := tuple.At(i); v != nil && v.Name() != "" {
				paramScope.insert(v.Name(), v)
			}
		}
	}

	saved := check.environment
	check.environment = environment{
		decl:    nil,
		scope:   paramScope,
		version: check.version,
		iota:    nil,
		sig:     &bodySig,
	}
	defer func() { check.environment = saved }()
	check.logosStmtEnterFunc(origin.Name(), fdecl.Body)
	defer check.logosExitFunc()
	defer check.processDelayed(len(check.delayed))
	check.logosPushFuncSig(&bodySig)
	defer check.logosPopFuncSig()

	savedScope := check.scope
	check.scope = paramScope
	check.openScope(fdecl, "comptime instance")
	check.stmtList(0, fdecl.Body.List)
	if check.hasLabel && !check.conf.IgnoreBranchErrors {
		check.labels(fdecl.Body)
	}
	bodyScope := check.scope
	check.closeScope()
	check.scope = savedScope
	check.usage(bodyScope)
	if bodySig.results.Len() > 0 && !check.logosGeneratorSig(&bodySig) && !check.isTerminating(fdecl.Body, "") {
		pos := fdecl.Body.Rbrace
		if n := len(check.logosCtPos); n > 0 {
			pos = check.logosCtPos[n-1]
		}
		check.error(pos, MissingReturn, "missing return")
	}
	return &bodySig
}

func substTupleVars(check *Checker, t *Tuple, smap substMap) []*Var {
	if t == nil {
		return nil
	}
	out := make([]*Var, 0, t.Len())
	for i := range t.Len() {
		v := t.At(i)
		if v == nil {
			continue
		}
		sub := check.subst(v.Pos(), v.Type(), smap, nil, check.context())
		concrete := newVar(v.Kind(), v.Pos(), v.Pkg(), v.Name(), sub)
		check.logosCtKeepType(concrete, v.Type())
		out = append(out, concrete)
	}
	return out
}

// logosCtStopAfter ends the declaration check of a comptime generic after a
// statement that holds a const switch: the declaration cannot decide
// fallthrough from a deferred switch, so it leaves the continuation for the
// concrete check without marking it dead. An instance checks the code after
// a terminating statement like any other code: that code is
// termination-dead, not const-dead, so it is type-checked and a
// comptime.Error in it fails.
func (check *Checker) logosCtStopAfter(s syntax.Stmt, tail []syntax.Stmt) bool {
	if len(tail) == 0 || !check.logosCtDeclCheck || !check.logosCtHasDeferredSwitch(s) {
		return false
	}
	// A label in this list can be reached by a jump instead of falling
	// through s. Do not prune through that alternate entry.
	for _, st := range tail {
		if _, ok := st.(*syntax.LabeledStmt); ok {
			return false
		}
	}
	w := logosDeadUseWalk{check: check, checked: check.scope}
	w.stmts(tail)
	return true
}

func (check *Checker) logosCtMarkDead(list ...syntax.Stmt) {
	if len(list) == 0 {
		return
	}
	if check.Info.LogosConstIfDead == nil {
		check.Info.LogosConstIfDead = make(map[syntax.Stmt]bool)
	}
	for _, s := range list {
		markDeadStmts(check.Info.LogosConstIfDead, s)
	}
}

func (check *Checker) logosCtHasDeferredSwitch(s syntax.Stmt) bool {
	found := false
	syntax.Inspect(s, func(n syntax.Node) bool {
		if check.logosDead(n) {
			return false
		}
		switch n.(type) {
		case *syntax.FuncLit, *syntax.LambdaExpr, *syntax.GroupChild:
			return false
		case *syntax.ConstSwitchStmt:
			found = true
		}
		return !found
	})
	return found
}

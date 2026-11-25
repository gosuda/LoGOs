// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"go/constant"
	"go/token"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// This file lowers M7-S2 `const for` over concrete types (SPEC Sec 2.5,
// T-205): each const for unrolls once per field of its struct type in
// declaration order. Lowering is text-primary: the expansion text is
// built from source slices with leaf substitutions, the native tree is
// re-parsed from that text, and every new node takes the const for
// position so traces name the .lgo line.

// cfField is one unrolled struct field.
type cfField struct {
	name     string
	typ      types2.Type
	tag      string
	index    int
	exported bool
}

// cfFieldsOf lists checked, reachable fields in declaration order.
func cfFieldsOf(loop *types2.LogosConstFor) []cfField {
	var out []cfField
	st := loop.Struct
	for i, iteration := range loop.Iterations {
		if iteration == nil {
			continue
		}
		f := st.Field(i)
		out = append(out, cfField{
			name:     f.Name(),
			typ:      f.Type(),
			tag:      st.Tag(i),
			index:    i,
			exported: f.Exported(),
		})
	}
	return out
}

// cfRegion is one virtual copy of expansion text. It lives at
// [start, end) of the working buffer after the original file text, on
// the virtual lines from line on, so every position inside it names a
// byte of its own text. Splices recorded inside the region apply to
// text only; the host composes the finished region texts. owner is the
// source position every node of the region takes after lowering.
type cfRegion struct {
	start   int
	end     int
	line    int
	owner   syntax.Pos
	splices []splice
	host    *cfHost
}

// cfHost is the outer splice that stands for the regions of one
// expansion: its text is head, then each region's finished text followed
// by sep, then tail. A host is retired once its text is consumed
// (composed into an enclosing span) or discarded (pruned with its span),
// and its regions accept no further edit.
type cfHost struct {
	regions []*cfRegion
	head    string
	sep     string
	tail    string
	retired bool
	// compose builds a host's finished text at finalization. When nil the
	// host composes as head, regions, sep, tail. A custom composer lets a
	// lowered construct arrange its region texts itself; it runs after
	// nested hosts finalized, so nested compositions are already set.
	compose func(h *cfHost) otext
}

type cfFuncCtx struct {
	sig  *types2.Signature
	body *syntax.BlockStmt
}

// lowerConstFor unrolls every concrete const for of one file. It runs
// before the other file passes so leaf offsets stay pristine; generic
// const fors (M7-S3) are left for the expansion phase.
func (fl *fileLower) lowerConstFor() {
	for _, decl := range fl.file.DeclList {
		fn, ok := decl.(*syntax.FuncDecl)
		if !ok || fn.Body == nil {
			fl.lowerConstForLits(decl, nil)
			continue
		}
		sig := fl.cfFuncSig(fn)
		fl.lowerConstForList(&fn.Body.List, cfFuncCtx{sig: sig, body: fn.Body})
		fl.lowerConstForLits(fn.Body, sig)
	}
	fl.collectInterpKinds()
}

// lowerConstForLits unrolls the const fors of every function literal
// and block lambda under root, outer bodies before the bodies their
// copies and bodies hold: a body is expanded first, so the bodies found
// inside it include those of its fresh copies, whose types the copy
// checks recorded.
func (fl *fileLower) lowerConstForLits(root syntax.Node, enclosing *types2.Signature) {
	for _, lb := range collectLitBodies(root) {
		sig := enclosing
		if lb.funcLit != nil {
			sig = fl.cfLitSig(lb.funcLit)
		}
		fl.lowerConstForList(&lb.body.List, cfFuncCtx{sig: sig, body: lb.body})
		fl.lowerConstForLits(lb.body, sig)
	}
}

func (fl *fileLower) cfFuncSig(fn *syntax.FuncDecl) *types2.Signature {
	if obj, ok := fl.pkg.info.Defs[fn.Name]; ok {
		if fnObj, ok := obj.(*types2.Func); ok {
			if sig, ok := fnObj.Type().(*types2.Signature); ok {
				return sig
			}
		}
	}
	return nil
}

// cfLitSig returns the signature the checker recorded for a function
// literal. TypeOf reads the Types map or the syntax node, whichever the
// check populated: the compiler stores types in the nodes only, and a
// missing signature skips the check of every copy inside the literal.
func (fl *fileLower) cfLitSig(lit *syntax.FuncLit) *types2.Signature {
	sig, _ := fl.pkg.info.TypeOf(lit).(*types2.Signature)
	return sig
}

type cfLitBody struct {
	body    *syntax.BlockStmt
	funcLit *syntax.FuncLit
}

func collectLitBodies(root syntax.Node) []cfLitBody {
	var out []cfLitBody
	syntax.Inspect(root, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			out = append(out, cfLitBody{body: n.Body, funcLit: n})
			return false
		case *syntax.LambdaExpr:
			if block, ok := n.Body.(*syntax.BlockStmt); ok {
				out = append(out, cfLitBody{body: block})
				return false
			}
		}
		return true
	})
	return out
}

// lowerConstForList expands the const fors of one statement list in
// place, recursing into child lists of the statements it keeps. A
// statement of a dead `const if` branch is left alone: the pruning of
// that statement deletes it with its text.
func (fl *fileLower) lowerConstForList(list *[]syntax.Stmt, fctx cfFuncCtx) {
	for i := 0; i < len(*list); i++ {
		if fl.pkg.info.LogosConstIfDead[(*list)[i]] {
			continue
		}
		cf, ok := (*list)[i].(*syntax.ConstForStmt)
		if !ok {
			eachStmtList((*list)[i], func(child *[]syntax.Stmt) {
				fl.lowerConstForList(child, fctx)
			})
			continue
		}
		info := fl.pkg.info.LogosConstFor[cf]
		if info == nil || info.Struct == nil {
			continue
		}
		stmts, host := fl.unrollConstFor(cf, info, fl.cfRunFor(cf), fctx)
		*list = append((*list)[:i], append(stmts, (*list)[i+1:]...)...)
		fl.addHost(fl.off(syntax.StartPos(cf)), fl.off(syntax.EndPos(cf))+1, host)
		i += len(stmts) - 1
	}
}

// eachStmtList applies f to every child statement list of s.
func eachStmtList(s syntax.Stmt, f func(*[]syntax.Stmt)) {
	switch s := s.(type) {
	case *syntax.BlockStmt:
		f(&s.List)
	case *syntax.IfStmt:
		eachStmtList(s.Then, f)
		if s.Else != nil {
			if b, ok := s.Else.(*syntax.BlockStmt); ok {
				f(&b.List)
			} else {
				eachStmtList(s.Else, f)
			}
		}
	case *syntax.ConstIfStmt:
		eachStmtList(s.Then, f)
		if s.Else != nil {
			eachStmtList(s.Else, f)
		}
	case *syntax.ConstSwitchStmt:
		for _, c := range s.Cases {
			f(&c.Body)
		}
	case *syntax.GroupStmt:
		eachStmtList(s.Body, f)
	case *syntax.GroupChild:
		eachStmtList(s.Body, f)
	case *syntax.ForStmt:
		eachStmtList(s.Body, f)
	case *syntax.SwitchStmt:
		for _, c := range s.Body {
			f(&c.Body)
		}
	case *syntax.SelectStmt:
		for _, c := range s.Body {
			f(&c.Body)
		}
	case *syntax.LabeledStmt:
		eachStmtList(s.Stmt, f)
	case *syntax.RangeClause:
	}
}

// cfChain is the active field-variable chain, innermost last.
type cfChain []cfActive

// cfActive binds a checked loop variable to the current struct field.
type cfActive struct {
	obj   *types2.Var
	field cfField
	index bool
}

// unrollConstFor expands one const for over the fields of info with the
// outer chain applied inside. It returns the replacement statements and
// their text.
func (fl *fileLower) unrollConstFor(cf *syntax.ConstForStmt, info *types2.LogosConstFor, run *cfRun, fctx cfFuncCtx) ([]syntax.Stmt, *cfHost) {
	bodyOff := offAt(run.ctx.starts, syntax.StartPos(cf.Body))
	bodyEnd := offAt(run.ctx.starts, syntax.EndPos(cf.Body)) + 1
	bodySrc := run.ctx.buf.sliceO(bodyOff, bodyEnd)
	var out []syntax.Stmt
	host := &cfHost{sep: "\n"}
	outerNodes := cfBodyNodes(cf.Body)
	for _, fd := range cfFieldsOf(info) {
		if !fl.cfCharge(run, outerNodes) {
			break
		}
		active := cfPushField(nil, info, fd)
		text := fl.expandLevel(cf.Body, active, bodySrc, 0, &cfMapper{start: bodyOff}, run, info.Iterations[fd.index])
		stmts, region := fl.reparseTopCopy(text, cf, fctx)
		pairs := fl.pairNestedSiteKeys(cf, info, fd.index, stmts)
		fl.hoistNestedLocalTypes(cf, info, fd.index, stmts, pairs)
		out = append(out, stmts...)
		host.regions = append(host.regions, region)
	}
	fl.cfCountNodes(out, cf.Pos())
	return out, host
}

// nestedSitePair links one freshly unrolled comptime site to the staged
// instance the drain recorded for the corresponding original site.
type nestedSitePair struct {
	orig, fresh *syntax.Name
	id          string
}

// pairNestedSiteKeys records writer site keys for the comptime sites in
// a freshly unrolled const-for copy. The drain staged an instance for
// each original site per iteration and recorded its key against the
// original node; the copies are fresh nodes, so pair them with the
// originals in unrolled order and copy the staged ids. In eject mode each
// paired site is renamed to its printed instance in the ejected text.
func (fl *fileLower) pairNestedSiteKeys(cf *syntax.ConstForStmt, info *types2.LogosConstFor, index int, stmts []syntax.Stmt) []nestedSitePair {
	if fl.pkg.info.LogosCtImport == nil || index < 0 || index >= len(info.Iterations) || info.Iterations[index] == nil {
		return nil
	}
	pairs := cfStagedSites(cf.Body, info.Iterations[index])
	var fresh []*syntax.Name
	for _, s := range stmts {
		syntax.Inspect(s, func(n syntax.Node) bool {
			name, ok := n.(*syntax.Name)
			if !ok {
				return true
			}
			_, inst := fl.pkg.info.Instances[name]
			obj, _ := fl.pkg.info.Uses[name].(*types2.Func)
			if inst && obj != nil && fl.cfIsGeneric(obj) {
				fresh = append(fresh, name)
			}
			return true
		})
	}
	if len(pairs) != len(fresh) {
		if base.Errors() == 0 {
			base.Fatalf("logos: const for at %s unrolls %d comptime sites for %d staged instances", cf.Pos().String(), len(fresh), len(pairs))
		}
		return nil
	}
	if len(pairs) == 0 {
		return nil
	}
	if fl.pkg.info.LogosCtSiteKeys == nil {
		fl.pkg.info.LogosCtSiteKeys = make(map[*syntax.Name]string)
	}
	for i := range pairs {
		pairs[i].fresh = fresh[i]
		fl.pkg.info.LogosCtSiteKeys[fresh[i]] = pairs[i].id
	}
	if base.LogosEject != "" {
		parents := make(map[syntax.Node]syntax.Node)
		for _, s := range stmts {
			maps.Copy(parents, cfParents(s))
		}
		for _, p := range pairs {
			fl.ejectCtSite(p.fresh, parents)
		}
	}
	return pairs
}

// cfStagedSites lists in unrolled order the comptime sites that one
// iteration of body expands to, each with the instance the drain staged
// for it: the live sites of body under info, and each nested const for
// once per visible field under that field's iteration.
func cfStagedSites(body syntax.Node, info *types2.Info) []nestedSitePair {
	var out []nestedSitePair
	syntax.Inspect(body, func(n syntax.Node) bool {
		if st, ok := n.(syntax.Stmt); ok && info.LogosConstIfDead[st] {
			return false
		}
		switch n := n.(type) {
		case *syntax.ConstForStmt:
			if loop := info.LogosConstFor[n]; loop != nil && loop.Struct != nil {
				for _, fd := range cfFieldsOf(loop) {
					out = append(out, cfStagedSites(n.Body, loop.Iterations[fd.index])...)
				}
			}
			return false
		case *syntax.Name:
			if id := info.LogosCtSiteKeys[n]; id != "" {
				out = append(out, nestedSitePair{orig: n, id: id})
			}
		}
		return true
	})
	return out
}

// cfDirectLocalType reports the declared name when t is directly a
// function-local named or alias type.
func cfDirectLocalType(t types2.Type) (*types2.TypeName, bool) {
	var obj *types2.TypeName
	switch t := t.(type) {
	case *types2.Named:
		obj = t.Obj()
	case *types2.Alias:
		obj = t.Obj()
	default:
		return nil, false
	}
	if obj == nil || obj.Pkg() == nil || obj.Parent() == nil || obj.Parent() == obj.Pkg().Scope() {
		return nil, false
	}
	return obj, true
}

// hoistNestedLocalTypes moves function-local type declarations referenced
// by transferred nested comptime calls out of unrolled const-for copies
// to package level with fresh names, so staged owner-package instances
// can name them. Only direct local type arguments with in-copy
// declarations and fully covered references are moved; anything else is
// left in place for the finish check to report.
func (fl *fileLower) hoistNestedLocalTypes(cf *syntax.ConstForStmt, info *types2.LogosConstFor, index int, stmts []syntax.Stmt, pairs []nestedSitePair) {
	if len(pairs) == 0 {
		return
	}
	if index < 0 || index >= len(info.Iterations) {
		return
	}
	iter := info.Iterations[index]
	if iter == nil {
		return
	}
	// Link fresh local type arguments back to the original
	// iteration-local types, positionally per call.
	type link struct {
		fresh, orig *types2.TypeName
		id          string
	}
	var links []link
	seen := make(map[*types2.TypeName]bool)
	for _, p := range pairs {
		oinst, ok1 := iter.Instances[p.orig]
		finst, ok2 := fl.pkg.info.Instances[p.fresh]
		if !ok1 || !ok2 {
			continue
		}
		ot, ft := typeArgsOf(oinst.TypeArgs), typeArgsOf(finst.TypeArgs)
		if len(ot) != len(ft) {
			continue
		}
		for i := range ot {
			oo, ook := cfDirectLocalType(ot[i])
			fo, fok := cfDirectLocalType(ft[i])
			if !ook || !fok || seen[fo] {
				continue
			}
			seen[fo] = true
			links = append(links, link{fresh: fo, orig: oo, id: p.id})
		}
	}
	if len(links) == 0 {
		return
	}
	// Find each linked fresh type's declaration in this iteration's copies.
	declOf := make(map[*types2.TypeName]*syntax.TypeDecl)
	for _, s := range stmts {
		syntax.Inspect(s, func(n syntax.Node) bool {
			if _, ok := n.(*syntax.ConstForStmt); ok {
				return false
			}
			if td, ok := n.(*syntax.TypeDecl); ok && td.Name != nil {
				if obj, ok := fl.pkg.info.Defs[td.Name].(*types2.TypeName); ok && obj.Parent() != nil && obj.Parent() != fl.pkg.tpkg.Scope() {
					declOf[obj] = td
				}
			}
			return true
		})
	}
	hoisted := make(map[*types2.TypeName]bool)
	for _, l := range links {
		if hoisted[l.fresh] {
			continue
		}
		td, ok := declOf[l.fresh]
		if !ok {
			continue
		}
		if !fl.cfHoistableUnderlying(td, declOf) {
			continue
		}
		if fl.hoistNestedLocalDecl(stmts, td, l.fresh, l.orig, l.id) {
			hoisted[l.fresh] = true
		}
	}
}

// cfHoistableUnderlying reports whether every function-local type named
// in td is declared by an in-copy declaration, so moving those
// declarations to package level leaves no dangling reference.
func (fl *fileLower) cfHoistableUnderlying(td *syntax.TypeDecl, declOf map[*types2.TypeName]*syntax.TypeDecl) bool {
	if td.Type == nil {
		return false
	}
	ok := true
	seen := make(map[*syntax.TypeDecl]bool)
	var visit func(d *syntax.TypeDecl)
	visit = func(d *syntax.TypeDecl) {
		if !ok || seen[d] {
			return
		}
		seen[d] = true
		if d.Type == nil {
			ok = false
			return
		}
		syntax.Inspect(d.Type, func(n syntax.Node) bool {
			if !ok {
				return false
			}
			id, isName := n.(*syntax.Name)
			if !isName {
				return true
			}
			obj, isTN := fl.pkg.info.Uses[id].(*types2.TypeName)
			if !isTN {
				return true
			}
			if obj.Pkg() == nil || obj.Parent() == nil || obj.Parent() == obj.Pkg().Scope() {
				return true
			}
			sub, hasDecl := declOf[obj]
			if !hasDecl {
				ok = false
				return false
			}
			visit(sub)
			return true
		})
	}
	visit(td)
	return ok
}

// hoistNestedLocalDecl moves one fresh local type declaration out of an
// unrolled const-for copy to package level under a fresh name and
// rewrites its uses within this iteration's copies, recording the
// original iteration-local type for the finish pass. The ejected text
// follows the tree: the declaration leaves the copy, its uses take the
// fresh name, and the file ends with the package-level declaration. It
// returns false leaving everything untouched when the declaration cannot
// be moved.
func (fl *fileLower) hoistNestedLocalDecl(stmts []syntax.Stmt, td *syntax.TypeDecl, fresh, orig *types2.TypeName, id string) bool {
	// Remove the declaration from its enclosing statement list first so
	// a failure below leaves the copies unchanged.
	removed := false
	var cut syntax.Node = td
	var excise func(list *[]syntax.Stmt)
	excise = func(list *[]syntax.Stmt) {
		if removed {
			return
		}
		for i := 0; i < len(*list); i++ {
			if ds, ok := (*list)[i].(*syntax.DeclStmt); ok {
				for j, d := range ds.DeclList {
					if d != syntax.Decl(td) {
						continue
					}
					if len(ds.DeclList) == 1 {
						*list = append((*list)[:i], (*list)[i+1:]...)
						i--
						cut = ds
					} else {
						ds.DeclList = append(ds.DeclList[:j], ds.DeclList[j+1:]...)
					}
					removed = true
				}
			}
		}
		if !removed {
			for _, s := range *list {
				eachStmtList(s, func(child *[]syntax.Stmt) {
					excise(child)
				})
				if removed {
					return
				}
			}
		}
	}
	for _, s := range stmts {
		if blk, ok := s.(*syntax.BlockStmt); ok {
			excise(&blk.List)
			if removed {
				break
			}
		}
	}
	if !removed {
		return false
	}
	suffix := id
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	name := td.Name.Value + "_" + suffix
	for i := 2; fl.pkg.tpkg.Scope().Lookup(name) != nil; i++ {
		name = fmt.Sprintf("%s_%s_%d", td.Name.Value, suffix, i)
	}
	ctx := cfCtx{buf: fl.srcMap, starts: fl.start}
	fl.add(fl.off(syntax.StartPos(cut)), cfExactEnd(ctx, cut), "")
	width := len(td.Name.Value)
	td.Name.Value = name
	for _, s := range stmts {
		parents := cfParents(s)
		syntax.Inspect(s, func(n syntax.Node) bool {
			if id, ok := n.(*syntax.Name); ok && id != td.Name {
				if obj, ok := fl.pkg.info.Uses[id]; ok && obj == types2.Object(fresh) {
					id.Value = name
					if !fl.cfInEjectSite(id, parents) {
						fl.add(fl.off(id.Pos()), fl.off(id.Pos())+width, name)
					}
				}
			}
			return true
		})
	}
	var decl strings.Builder
	if _, err := syntax.Fprint(&decl, ctEjectUngrouped(td), 0); err != nil {
		base.Fatalf("logos: print hoisted local type %s: %v", name, err)
	}
	fl.constText += "\n" + decl.String() + "\n"
	fl.file.DeclList = append(fl.file.DeclList, td)
	fl.changed = true
	if fl.pkg.ctLocalHoists == nil {
		fl.pkg.ctLocalHoists = make(map[*syntax.Name]*types2.TypeName)
	}
	fl.pkg.ctLocalHoists[td.Name] = orig
	return true
}

// cfInEjectSite reports whether n lies inside a comptime call site whose
// ejected text is already replaced by its instance name.
func (fl *fileLower) cfInEjectSite(n syntax.Node, parents map[syntax.Node]syntax.Node) bool {
	for ; n != nil; n = parents[n] {
		if fl.ejectSites[n] {
			return true
		}
	}
	return false
}

func cfPushField(active cfChain, loop *types2.LogosConstFor, field cfField) cfChain {
	active = append(active, cfActive{obj: loop.FieldVar, field: field})
	if loop.IndexVar != nil {
		active = append(active, cfActive{obj: loop.IndexVar, field: field, index: true})
	}
	return active
}

// addHost records the outer splice of one expansion over [start, end).
func (fl *fileLower) addHost(start, end int, host *cfHost) {
	for _, r := range host.regions {
		r.host = host
	}
	fl.splices = append(fl.splices, splice{start: start, end: end, host: host})
	fl.hosts = append(fl.hosts, host)
	fl.changed = true
}

// regionAt returns the region holding the span [start, end), boundaries
// included, or nil for a span of the original text. Every offset past
// the original text belongs to exactly one region, so an edit never
// lands in the outer list at a buffer offset the original text lacks.
func (fl *fileLower) regionAt(start, end int) *cfRegion {
	for _, r := range fl.regions {
		if start >= r.start && end <= r.end {
			return r
		}
	}
	return nil
}

// reparseTopCopy parses one unrolled iteration as statements, checks
// them on their own, and moves them into a region of their own. The
// parse wraps the text so that it begins on line 3.
func (fl *fileLower) reparseTopCopy(text otext, cf *syntax.ConstForStmt, fctx cfFuncCtx) ([]syntax.Stmt, *cfRegion) {
	fbase := syntax.NewFileBase(fl.baseName + ".cf")
	var errs []error
	f, _ := syntax.Parse(fbase, strings.NewReader("package cfreparse\nfunc cfreparse() {\n"+text.s+"\n}\n"), func(err error) {
		errs = append(errs, err)
	}, nil, syntax.LogosMode)
	fn := cfFirstFunc(f)
	if len(errs) > 0 || fn == nil || fn.Body == nil {
		base.Fatalf("logos: cannot parse const for expansion at %s: %v", cf.Pos().String(), errs)
	}
	var blocks []*syntax.BlockStmt
	var roots []syntax.Node
	for _, s := range fn.Body.List {
		b, ok := s.(*syntax.BlockStmt)
		if !ok {
			base.Fatalf("logos: cannot parse const for expansion at %s", cf.Pos().String())
		}
		blocks = append(blocks, b)
		roots = append(roots, s)
	}
	fl.miniCheckCopies(blocks, cf, fctx)
	return fn.Body.List, fl.adoptRegion(text, cf.Pos(), fbase, 3, roots)
}

// miniCheckCopies types the copies of one const for under the scope and
// signature the const for sits in, so the lowering of each copy reads
// the types of its own nodes.
func (fl *fileLower) miniCheckCopies(blocks []*syntax.BlockStmt, cf *syntax.ConstForStmt, fctx cfFuncCtx) {
	ctx := types2.LogosCopyContext{
		Sig:       fctx.sig,
		Disqual:   fl.pkg.info.LogosDisqual[fctx.body],
		Aliases:   fl.cfAliases(),
		MaxSteps:  fl.pkg.info.LogosCTFEMaxSteps,
		NodeLimit: base.LogosComptimeNodes,
		Importer:  fl.pkg.info.LogosImporter,
		Context:   fl.pkg.info.LogosContext,
		Bind:      fl.cfRuntimeBindings(),
	}
	if enclosing := fl.pkg.info.Scopes[cf]; enclosing != nil {
		ctx.Scope = enclosing.Parent()
	}
	if recorded := fl.pkg.info.LogosConstFor[cf]; recorded != nil {
		ctx.Version = recorded.GoVersion
	}
	types2.LogosCheckCopy(fl.pkg.tpkg, fl.pkg.info, ctx, blocks)
}

// cfAliases returns the synthesized package aliases of the file so far,
// each keyed by the package object it names. A copy keeps the comptime
// selections its source spells, such as comptime.ViewAs, so comptime
// binds as well when the file does not bind that name itself.
func (fl *fileLower) cfAliases() map[*types2.Package]string {
	out := make(map[*types2.Package]string)
	if reg := fl.getGenImports(); reg != nil {
		for path, q := range reg.qual {
			if pkg := reg.pkgs[path]; pkg != nil && q != "" {
				out[pkg] = q
			}
		}
	}
	if fs := fl.pkg.info.Scopes[fl.file]; fs != nil && fs.Lookup("comptime") == nil && fl.pkg.tpkg.Scope().Lookup("comptime") == nil {
		out[types2.LogosComptimePackage()] = "comptime"
	}
	return out
}

// adoptRegion appends text to the working buffer as a new region and
// moves every position of roots, parsed from a wrapper in which text
// starts on line textLine of the parsed file base, onto the region's own
// virtual lines. owner is the position the region's nodes take back
// after lowering.
func (fl *fileLower) adoptRegion(text otext, owner syntax.Pos, parsed *syntax.PosBase, textLine int, roots []syntax.Node) *cfRegion {
	fl.src = append(fl.src, '\n')
	r := &cfRegion{start: len(fl.src), line: len(lineStarts(fl.src)), owner: owner}
	fl.src = append(fl.src, text.s...)
	r.end = len(fl.src)
	fl.src = append(fl.src, '\n')
	fl.keywords.Append(fl.src, r.start-1)
	fl.srcMap.b = fl.src
	for _, c := range text.chunks {
		if c.n == 0 {
			continue
		}
		fl.srcMap.chunks = append(fl.srcMap.chunks, chunk{out: r.start + c.out, orig: c.orig, n: c.n})
	}
	fl.start = lineStarts(fl.src)
	fl.regions = append(fl.regions, r)
	virtual := func(p syntax.Pos) syntax.Pos {
		if !p.IsKnown() || p.FileBase() != parsed {
			return p
		}
		return syntax.MakePos(fl.base, uint(r.line)+p.Line()-uint(textLine), p.Col())
	}
	for _, root := range roots {
		cfMapPositions(root, virtual)
	}
	return r
}

var (
	cfPosType     = reflect.TypeFor[syntax.Pos]()
	cfPosListType = reflect.TypeFor[[]syntax.Pos]()
)

// cfMapPositions maps every position held by a node under root through
// fn: the node position and every exported Pos or []Pos field (block and
// switch closers, clause colons, interpolation operand braces, lambda
// arrows, and every other delimiter a node records outside SetPos).
func cfMapPositions(root syntax.Node, fn func(syntax.Pos) syntax.Pos) {
	syntax.Inspect(root, func(n syntax.Node) bool {
		if n == nil {
			return true
		}
		n.SetPos(fn(n.Pos()))
		if s, ok := n.(*syntax.ConstSwitchStmt); ok {
			for _, c := range s.Cases {
				c.Case = fn(c.Case)
				c.Colon = fn(c.Colon)
				for i := range c.Elems {
					c.Elems[i].TildePos = fn(c.Elems[i].TildePos)
				}
			}
		}
		v := reflect.ValueOf(n).Elem()
		for i := range v.NumField() {
			f := v.Field(i)
			if !f.CanSet() {
				continue
			}
			switch f.Type() {
			case cfPosType:
				f.Set(reflect.ValueOf(fn(f.Interface().(syntax.Pos))))
			case cfPosListType:
				for j := range f.Len() {
					e := f.Index(j)
					e.Set(reflect.ValueOf(fn(e.Interface().(syntax.Pos))))
				}
			}
		}
		return true
	})
}

// composeHost returns the finished text of a host: its head, each
// region's text with the region's edits applied followed by sep, and its
// tail. The region bytes live in the working buffer with their origin
// mapping, so the composed text keeps the origin of every copied byte.
func (fl *fileLower) composeHost(h *cfHost) otext {
	parts := []otext{gen(h.head)}
	for _, r := range h.regions {
		adj := make([]splice, len(r.splices))
		for i, sp := range r.splices {
			adj[i] = sp
			adj[i].start -= r.start
			adj[i].end -= r.start
		}
		out, err := applyChunks(fl.srcMap.sliceO(r.start, r.end), adj)
		if err != nil {
			base.Fatalf("logos: overlapping edits at %s", r.owner.String())
		}
		parts = append(parts, out, gen(h.sep))
	}
	parts = append(parts, gen(h.tail))
	return cat(parts...)
}

// retireHost composes the text of h for the enclosing span that
// consumes it and closes its regions to further edits.
func (fl *fileLower) retireHost(h *cfHost) otext {
	h.retired = true
	return fl.composeHost(h)
}

// finalizeConstFor stores the finished text in every host still live.
func (fl *fileLower) finalizeConstFor() {
	for _, h := range fl.hosts {
		if !h.retired {
			if h.compose != nil {
				fl.setHostText(h, h.compose(h))
			} else {
				fl.setHostText(h, fl.composeHost(h))
			}
		}
	}
}

// setHostText sets the text of the outer splice that stands for h.
func (fl *fileLower) setHostText(h *cfHost, text otext) {
	for i := range fl.splices {
		if fl.splices[i].host == h {
			fl.splices[i].text, fl.splices[i].chunks = text.s, text.chunks
			return
		}
	}
	base.Fatalf("logos: host splice missing at %s", h.regions[0].owner.String())
}

// remapRegions gives every node that lowering left on a virtual line the
// position of the source construct its region expands, so the lowered
// tree, the recheck, and the line tables see the original file.
func (fl *fileLower) remapRegions() {
	if len(fl.regions) == 0 {
		return
	}
	cfMapPositions(fl.file, fl.remapPos)
}

// remapPos returns the owner position of the region p lies in, or p.
func (fl *fileLower) remapPos(p syntax.Pos) syntax.Pos {
	if r := fl.regionFor(p); r != nil {
		return r.owner
	}
	return p
}

// regionFor returns the region that holds the virtual position p, or nil
// for a position of the original text.
func (fl *fileLower) regionFor(p syntax.Pos) *cfRegion {
	if !p.IsKnown() || p.FileBase() != fl.base {
		return nil
	}
	line := int(p.Line())
	if line > len(fl.start) {
		base.Fatalf("logos: copy position outside any region at %s", p.String())
	}
	off := fl.start[line-1] + int(p.Col()) - 1
	if off <= fl.orig {
		return nil
	}
	for _, r := range fl.regions {
		if off >= r.start && off <= r.end {
			return r
		}
	}
	base.Fatalf("logos: copy position outside any region at %s", p.String())
	return nil
}

// cfRun owns a single expansion's source coordinates and checked facts.
type cfRun struct {
	ctx     cfCtx
	info    *types2.Info
	parents map[syntax.Node]syntax.Node
	budget  int
	labels  map[string]bool
	labelID int
}

// cfRunFor builds the run of one const for of the file being lowered.
func (fl *fileLower) cfRunFor(cf *syntax.ConstForStmt) *cfRun {
	return &cfRun{
		ctx:     cfCtx{buf: fl.srcMap, starts: fl.start, stamp: cf.Pos()},
		info:    fl.pkg.info,
		parents: cfParents(cf),
	}
}

// offAt converts a valid file position to a byte offset.
func offAt(starts []int, pos syntax.Pos) int {
	return starts[pos.Line()-1] + int(pos.Col()) - 1
}

// cfCtx bundles the source coordinates one expansion works with.
type cfCtx struct {
	buf    srcBuf
	starts []int
	stamp  syntax.Pos
}

// cfSub is one leaf replacement in base coordinates.
type cfSub struct {
	start, end int
	text       string
}

// cfMapper maps base offsets to current-text offsets through the
// applied substitutions.
type cfMapper struct {
	start int
	subs  []cfSub
}

// mapPos maps a base offset to the current text.
func (m *cfMapper) mapPos(p int) int {
	out := p - m.start
	for _, s := range m.subs {
		if s.end <= p {
			out += len(s.text) - (s.end - s.start)
		}
	}
	return out
}

// mapSpan maps a base span to the current text.
func (m *cfMapper) mapSpan(s, e int) (int, int) {
	return m.mapPos(s), m.mapPos(e)
}

// cfParents maps every node under root to its parent.
func cfParents(root syntax.Node) map[syntax.Node]syntax.Node {
	parents := make(map[syntax.Node]syntax.Node)
	var stack []syntax.Node
	syntax.Walk(root, cfParentVisitor{parents: parents, stack: &stack})
	return parents
}

// cfParentVisitor tracks the node stack through Visit(nil) exits.
type cfParentVisitor struct {
	parents map[syntax.Node]syntax.Node
	stack   *[]syntax.Node
}

// Visit records parents on entry and pops on exit.
func (v cfParentVisitor) Visit(n syntax.Node) syntax.Visitor {
	if n == nil {
		*v.stack = (*v.stack)[:len(*v.stack)-1]
		return nil
	}
	if len(*v.stack) > 0 {
		v.parents[n] = (*v.stack)[len(*v.stack)-1]
	}
	*v.stack = append(*v.stack, n)
	return v
}

func (fl *fileLower) cfLabelSubs(body *syntax.BlockStmt, covered []span, run *cfRun) []cfSub {
	var declarations []*syntax.Name
	syntax.Inspect(body, func(n syntax.Node) bool {
		if st, ok := n.(syntax.Stmt); ok && run.info.LogosConstIfDead[st] {
			return false
		}
		switch n := n.(type) {
		case *syntax.ConstForStmt, *syntax.FuncLit:
			return false
		case *syntax.LabeledStmt:
			if n.Label.Value != "_" {
				declarations = append(declarations, n.Label)
			}
		}
		return true
	})
	if len(declarations) == 0 {
		return nil
	}
	if run.labels == nil {
		run.labels = make(map[string]bool)
		syntax.Inspect(fl.file, func(n syntax.Node) bool {
			if s, ok := n.(*syntax.LabeledStmt); ok {
				run.labels[s.Label.Value] = true
			}
			return true
		})
		scanSegment(run.ctx.buf.b, 0, len(run.ctx.buf.b), func(t tok) bool {
			if t.kind == token.IDENT {
				run.labels[string(run.ctx.buf.b[t.start:t.end])] = true
			}
			return true
		})
	}
	names := make(map[string]string)
	for _, label := range declarations {
		for {
			run.labelID++
			name := fmt.Sprintf("_logos_cf_label_%d_%d_%d", run.ctx.stamp.Line(), run.ctx.stamp.Col(), run.labelID)
			if !run.labels[name] {
				run.labels[name] = true
				names[label.Value] = name
				break
			}
		}
	}
	var subs []cfSub
	syntax.Inspect(body, func(n syntax.Node) bool {
		if st, ok := n.(syntax.Stmt); ok && run.info.LogosConstIfDead[st] {
			return false
		}
		var label *syntax.Name
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.LabeledStmt:
			label = n.Label
		case *syntax.BranchStmt:
			label = n.Label
		}
		if label != nil {
			if name, ok := names[label.Value]; ok {
				start := offAt(run.ctx.starts, label.Pos())
				end := start + len(label.Value)
				if !insideAny(span{start: start, end: end}, covered) {
					subs = append(subs, cfSub{start: start, end: end, text: name})
				}
			}
		}
		return true
	})
	return subs
}

// expandLevel substitutes the active bindings in the body text slice and
// recursively expands nested const fors right to left. sliceStart is the
// mapped offset of text[0]; m maps base offsets to the evolving text.
func (fl *fileLower) expandLevel(body *syntax.BlockStmt, active cfChain, text otext, sliceStart int, m *cfMapper, run *cfRun, info *types2.Info) otext {
	saved := run.info
	run.info = info
	defer func() { run.info = saved }()
	ctx := run.ctx
	ls := fl.cfLeafSubs(body, active, m.covered(), run)
	// Labels inside regions the leaf subs already removed (dead const
	// branches, pruned switch cases) must not be renamed: their sub
	// would overlap the removal span. Feed them in as covered.
	lspans := m.covered()
	for _, x := range ls {
		lspans = append(lspans, span{x.start, x.end})
	}
	ls = append(ls, fl.cfLabelSubs(body, lspans, run)...)
	rel := make([]splice, 0, len(ls))
	for _, x := range ls {
		ms, me := m.mapSpan(x.start, x.end)
		rel = append(rel, splice{start: ms - sliceStart, end: me - sliceStart, text: x.text})
	}
	out, err := applyChunks(text, rel)
	if err != nil {
		base.Fatalf("logos: overlapping const for substitutions")
	}
	text = out
	m.subs = append(m.subs, ls...)
	inners := cfInnerConstFors(body, info)
	slices.Reverse(inners)
	for _, inner := range inners {
		istart := offAt(ctx.starts, syntax.StartPos(inner))
		iend := offAt(ctx.starts, syntax.EndPos(inner)) + 1
		if insideAny(span{start: istart, end: iend}, m.covered()) {
			continue
		}
		loop := info.LogosConstFor[inner]
		if loop == nil || loop.Struct == nil {
			base.Fatalf("logos: missing nested const for facts at %s", inner.Pos().String())
		}
		s, e := m.mapSpan(istart, iend)
		bstart := offAt(ctx.starts, syntax.StartPos(inner.Body))
		bend := offAt(ctx.starts, syntax.EndPos(inner.Body)) + 1
		bs, be := m.mapSpan(bstart, bend)
		var innerParts []otext
		innerNodes := cfBodyNodes(inner.Body)
		for _, fd := range cfFieldsOf(loop) {
			if !fl.cfCharge(run, innerNodes) {
				return gen("")
			}
			sub := cfPushField(active, loop, fd)
			iterMap := &cfMapper{start: m.start, subs: slices.Clone(m.subs)}
			one := fl.expandLevel(inner.Body, sub, text.slice(bs-sliceStart, be-sliceStart), bs, iterMap, run, loop.Iterations[fd.index])
			innerParts = append(innerParts, one, gen("\n"))
		}
		text = cat(text.slice(0, s-sliceStart), cat(innerParts...), text.slice(e-sliceStart, len(text.s)))
	}
	return text
}

// covered reports the covered spans of a mapper.
func (m *cfMapper) covered() []span {
	out := make([]span, 0, len(m.subs))
	for _, s := range m.subs {
		out = append(out, span{s.start, s.end})
	}
	return out
}

// reparseStmts parses text as a statement list for the native tree and
// stamps every node and every scope boundary with stamp so traces name
// the .lgo const for line and debug scopes stay ordered.
func (fl *fileLower) reparseStmts(text string, stamp syntax.Pos) []syntax.Stmt {
	src := "package cfreparse\nfunc cfreparse() {\n" + text + "\n}\n"
	fbase := syntax.NewFileBase(fl.baseName + ".cf")
	var errs []error
	f, _ := syntax.Parse(fbase, strings.NewReader(src), func(err error) {
		errs = append(errs, err)
	}, nil, syntax.LogosMode)
	if len(errs) > 0 || f == nil {
		base.Fatalf("logos: cannot parse const for expansion at %s: %v", stamp.String(), errs)
	}
	var body *syntax.BlockStmt
	for _, decl := range f.DeclList {
		if fn, ok := decl.(*syntax.FuncDecl); ok && fn.Name.Value == "cfreparse" {
			body = fn.Body
		}
	}
	if body == nil {
		base.Fatalf("logos: cannot parse const for expansion at %s", stamp.String())
	}
	for _, s := range body.List {
		normalizeChainPos(s, stamp)
	}
	return body.List
}

// cfCountNodes enforces the expanded-node limit on one expansion: a
// count above the selected limit fails, a count at the limit passes.
func (fl *fileLower) cfCountNodes(stmts []syntax.Stmt, pos syntax.Pos) {
	n := 0
	for _, s := range stmts {
		syntax.Inspect(s, func(x syntax.Node) bool {
			n++
			return true
		})
	}
	if n > base.LogosComptimeNodes {
		fl.cfError(pos, "[comptime::node_limit] expansion has %d nodes above the selected limit %d", n, base.LogosComptimeNodes)
	}
}

func (fl *fileLower) cfCharge(run *cfRun, n int) bool {
	if run.budget > 8*base.LogosComptimeNodes {
		return false
	}
	run.budget += n
	if run.budget > 8*base.LogosComptimeNodes {
		fl.cfError(run.ctx.stamp, "[comptime::node_limit] expansion has %d copies above the selected limit %d", run.budget, base.LogosComptimeNodes)
		return false
	}
	return true
}

func cfBodyNodes(root syntax.Node) int {
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

// cfBareSkip reports whether a bare Name occurrence is a declaration,
// label, or selector element rather than a value use. Assignment
// targets are not skipped: writing a loop variable is meaningless and
// fails loudly at substitution.
func (fl *fileLower) cfBareSkip(n *syntax.Name, p syntax.Node) bool {
	switch p := p.(type) {
	case *syntax.SelectorExpr:
		return p.Sel == n
	case *syntax.OptSelectExpr:
		return p.Sel == n
	case *syntax.ConstForStmt:
		return p.Index == n || p.Field == n
	case *syntax.LambdaExpr:
		return slices.Contains(p.Params, n)
	case *syntax.Field:
		return p.Name == n
	case *syntax.FuncDecl:
		return p.Name == n
	case *syntax.LabeledStmt:
		return p.Label == n
	case *syntax.KeyValueExpr:
		if p.Key == n {
			field, _ := fl.pkg.info.ObjectOf(n).(*types2.Var)
			return field != nil && field.IsField()
		}
	case *syntax.AssignStmt:
		if p.Op == syntax.Def {
			for _, e := range syntax.UnpackListExpr(p.Lhs) {
				if en, ok := e.(*syntax.Name); ok && en == n {
					return true
				}
			}
		}
		return false
	case *syntax.VarDecl:
		for _, name := range p.NameList {
			if name == n {
				return true
			}
		}
		return false
	case *syntax.ConstDecl:
		for _, name := range p.NameList {
			if name == n {
				return true
			}
		}
		return false
	case *syntax.TypeDecl:
		return p.Name == n
	case *syntax.BranchStmt:
		return p.Label == n
	case *syntax.TypeSwitchGuard:
		return p.Lhs == n
	case *syntax.RangeClause:
		if !p.Def {
			return false
		}
		for _, e := range syntax.UnpackListExpr(p.Lhs) {
			if en, ok := e.(*syntax.Name); ok && en == n {
				return true
			}
		}
		return false
	}
	return false
}

func cfAddressTaken(p syntax.Node, parents map[syntax.Node]syntax.Node) bool {
	for {
		switch q := p.(type) {
		case *syntax.ParenExpr:
			p = parents[p]
		case *syntax.Operation:
			return q.Op == syntax.And && q.Y == nil
		default:
			return false
		}
	}
}

// cfError reports one user-facing comptime diagnostic at pos. The run
// fails once lowering finishes, so one defect never cascades.
func (fl *fileLower) cfError(pos syntax.Pos, format string, args ...any) {
	base.Errorf("%s: %s", pos.String(), fmt.Sprintf(format, args...))
}

// cfExactEnd returns the exclusive end offset of node n in ctx. EndPos
// stops short of the closing brackets of calls, indexes, and literals
// (and of the brackets enclosing them), short of the tag of the last
// field of a struct type, and at the keyword of a struct type with no
// field: the tokens between the node start and its approximate end
// are balanced up, and the scan then continues past that end, still
// counting the brackets that open there, until the brackets still open
// close. A field tag, or the braces of an empty struct, no longer end
// the node short of its closers.
func cfExactEnd(ctx cfCtx, n syntax.Node) int {
	start := offAt(ctx.starts, syntax.StartPos(n))
	end := offAt(ctx.starts, syntax.EndPos(n))
	open := 0
	scanSegment(ctx.buf.b, start, end, func(t tok) bool {
		switch t.kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			open++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			open--
		}
		return true
	})
	if open <= 0 {
		return end
	}
	scanSegment(ctx.buf.b, end, len(ctx.buf.b), func(t tok) bool {
		switch t.kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			open++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			open--
			end = t.end
		}
		return open > 0
	})
	return end
}

type cfLeaf struct {
	fl      *fileLower
	run     *cfRun
	active  cfChain
	covered []span
	parents map[syntax.Node]syntax.Node
	out     []cfSub
	deadEnd int
}

// cfLeafSubs collects the leaf substitutions for active bindings in
// body. Spans are base coordinates of ctx.
func (fl *fileLower) cfLeafSubs(root syntax.Node, active cfChain, covered []span, run *cfRun) []cfSub {
	c := &cfLeaf{fl: fl, run: run, active: active, covered: covered, parents: run.parents}
	syntax.Inspect(root, c.visit)
	return c.out
}

func (c *cfLeaf) at(n syntax.Node) (int, int) {
	return offAt(c.run.ctx.starts, syntax.StartPos(n)), cfExactEnd(c.run.ctx, n)
}

func (c *cfLeaf) add(s, e int, text string) {
	if e <= s || insideAny(span{start: s, end: e}, c.covered) {
		return
	}
	for _, o := range c.out {
		if o.start <= s && e <= o.end {
			return
		}
	}
	c.out = append(c.out, cfSub{s, e, text})
}

func cfStmtStart(ctx cfCtx, s syntax.Stmt) int {
	var expr syntax.Expr
	switch s := s.(type) {
	case *syntax.ExprStmt:
		expr = s.X
	case *syntax.AssignStmt:
		expr = s.Lhs
	case *syntax.SendStmt:
		expr = s.Chan
	default:
		return offAt(ctx.starts, stmtStartPos(s))
	}
	return spanStartBack(ctx.buf.b, offAt(ctx.starts, cfExprStart(expr)))
}

func cfExprStart(e syntax.Expr) syntax.Pos {
	for {
		switch x := e.(type) {
		case *syntax.CallExpr:
			e = x.Fun
		case *syntax.SelectorExpr:
			e = x.X
		case *syntax.IndexExpr:
			e = x.X
		case *syntax.SliceExpr:
			e = x.X
		case *syntax.AssertExpr:
			e = x.X
		case *syntax.FieldProjExpr:
			e = x.X
		case *syntax.OptSelectExpr:
			e = x.X
		case *syntax.TryExpr:
			e = x.X
		case *syntax.CoalesceExpr:
			e = x.X
		case *syntax.Operation:
			if x.Y == nil {
				return x.Pos()
			}
			e = x.X
		case *syntax.ListExpr:
			e = x.ElemList[0]
		default:
			return syntax.StartPos(e)
		}
	}
}

func cfNextClause[C interface {
	syntax.Node
	comparable
}](clause C, clauses []C, close syntax.Pos) syntax.Pos {
	if next := slices.Index(clauses, clause) + 1; next < len(clauses) {
		return clauses[next].Pos()
	}
	return close
}

func (c *cfLeaf) deadStmtList(st syntax.Stmt, start int) ([]syntax.Stmt, syntax.Pos) {
	switch p := c.parents[st].(type) {
	case *syntax.BlockStmt:
		return p.List, p.Rbrace
	case *syntax.CaseClause:
		s := c.parents[p].(*syntax.SwitchStmt)
		return p.Body, cfNextClause(p, s.Body, s.Rbrace)
	case *syntax.CommClause:
		s := c.parents[p].(*syntax.SelectStmt)
		return p.Body, cfNextClause(p, s.Body, s.Rbrace)
	case *syntax.ConstSwitchStmt:
		starts := c.run.ctx.starts
		for i, arm := range p.Cases {
			if i+1 == len(p.Cases) {
				return arm.Body, p.Rbrace
			}
			next := p.Cases[i+1].Case
			if start < offAt(starts, next) {
				return arm.Body, next
			}
		}
	}
	base.Fatalf("logos: dead statement at %s is not in a statement list", st.Pos().String())
	return nil, syntax.Pos{}
}

func (c *cfLeaf) dropDeadStmts(st syntax.Stmt) {
	ctx := c.run.ctx
	if offAt(ctx.starts, st.Pos()) < c.deadEnd {
		return
	}
	start := cfStmtStart(ctx, st)
	list, boundary := c.deadStmtList(st, start)
	i := slices.Index(list, st)
	if i < 0 {
		base.Fatalf("logos: dead statement at %s missing from its statement list", st.Pos().String())
	}
	// Use the next live statement or the clause closer as the boundary.
	// EndPos cannot distinguish the last operand from the end of a statement.
	end := offAt(ctx.starts, boundary)
	for _, next := range list[i+1:] {
		if !c.run.info.LogosConstIfDead[next] {
			end = cfStmtStart(ctx, next)
			break
		}
	}
	c.deadEnd = end
	c.add(start, c.deadEnd, "")
}

func (c *cfLeaf) visit(n syntax.Node) bool {
	if st, ok := n.(syntax.Stmt); ok && c.run.info.LogosConstIfDead[st] {
		c.dropDeadStmts(st)
		return false
	}
	switch n := n.(type) {
	case *syntax.ConstForStmt:
		return false
	case *syntax.ConstIfStmt:
		if live := c.fl.cfPruneConstIf(n, c.run, c.add); live != nil {
			syntax.Inspect(live, c.visit)
		}
		return false
	case *syntax.FieldProjExpr:
		c.subFieldProj(n)
		return true
	case *syntax.SelectorExpr:
		c.subSelector(n)
		return true
	case *syntax.CallExpr:
		return c.subCall(n)
	case *syntax.Name:
		c.subName(n)
		return true
	case *syntax.AssignStmt:
		c.subBlankAssign(n)
		return true
	}
	return true
}

func (c *cfLeaf) subFieldProj(n *syntax.FieldProjExpr) {
	a := c.fieldBinding(n.Field)
	if a == nil {
		return
	}
	_, xe := c.at(n.X)
	_, e := c.at(n)
	c.add(xe, e, "."+a.field.name)
}

func (c *cfLeaf) subSelector(n *syntax.SelectorExpr) {
	a := c.fieldBinding(n.X)
	if a == nil {
		return
	}
	s, e := c.at(n)
	switch n.Sel.Value {
	case "Name":
		c.add(s, e, strconv.Quote(a.field.name))
	case "Index":
		c.add(s, e, strconv.Itoa(a.field.index))
	case "Exported":
		c.add(s, e, strconv.FormatBool(a.field.exported))
	case "Tag":
		c.add(s, e, strconv.Quote(a.field.tag))
	case "Type":
		spelled, ok := c.fl.spellTypeAs(a.field.typ, copyTypeSpelling)
		if !ok {
			base.Fatalf("logos: cannot spell field type at %s", n.Pos().String())
		}
		c.add(s, e, spelled)
	}
}

func (c *cfLeaf) subCall(n *syntax.CallExpr) bool {
	ctx := c.run.ctx
	if matched, descend := c.subTagGet(n); matched {
		return descend
	}
	if name := cfComptimeCall(n, c.run.info); name != "" {
		switch name {
		case "SizeOf", "AlignOf", "HasPointers":
			if v, ok := typeConst(c.run.info, n); ok {
				s, e := cfCallSpan(ctx, n)
				c.add(s, e, keepComments(ctx.buf.b, s, e, v.String()))
				return false
			}
		case "TypeOf":
			s, e := cfCallSpan(ctx, n)
			c.add(s, e, keepComments(ctx.buf.b, s, e, "struct{}{}"))
			return false
		}
	}
	return !c.subComptimeError(n)
}

// subComptimeError answers a live comptime.Error call of an expansion:
// the message is a compile-time constant per instantiation, so the
// lowering reports it here and removes the call from the copy. It
// reports whether n was such a call.
func (c *cfLeaf) subComptimeError(n *syntax.CallExpr) bool {
	if len(n.ArgList) != 1 || cfComptimeCall(n, c.run.info) != "Error" {
		return false
	}
	s, e := cfCallSpan(c.run.ctx, n)
	if insideAny(span{start: s, end: e}, c.covered) {
		return false
	}
	msg, ok := cfComptimeErrorMsg(c.run.info, n.ArgList[0])
	if !ok {
		c.fl.cfError(n.ArgList[0].Pos(), "[comptime::error_not_constant] comptime.Error needs a constant message")
	} else {
		c.fl.cfError(n.ArgList[0].Pos(), "[comptime::error] %s", msg)
	}
	c.add(s, e, "")
	return true
}

// cfComptimeErrorMsg evaluates the comptime.Error message argument: a
// recorded constant string, or a string literal when the check left no
// record.
func cfComptimeErrorMsg(info *types2.Info, e syntax.Expr) (string, bool) {
	if tv, ok := typeConst(info, e); ok && tv.Kind() == constant.String {
		return constant.StringVal(tv), true
	}
	if lit, ok := syntax.Unparen(e).(*syntax.BasicLit); ok && lit.Kind == syntax.StringLit {
		if msg, err := strconv.Unquote(lit.Value); err == nil {
			return msg, true
		}
	}
	return "", false
}

func (c *cfLeaf) subName(n *syntax.Name) {
	s, e := c.at(n)
	if insideAny(span{start: s, end: e}, c.covered) {
		return
	}
	a := c.binding(n)
	if a == nil || !a.index {
		return
	}
	p, ok := c.parents[n]
	if !ok {
		return
	}
	if cfAddressTaken(p, c.parents) {
		c.fl.cfError(n.Pos(), "[comptime::const_for_assign] cannot take the address of const for variable %s", n.Value)
		return
	}
	if c.fl.cfBareSkip(n, p) {
		return
	}
	c.add(s, e, strconv.Itoa(a.field.index))
}

func (c *cfLeaf) binding(n *syntax.Name) *cfActive {
	obj, ok := c.run.info.Uses[n].(*types2.Var)
	if !ok {
		return nil
	}
	for i := len(c.active) - 1; i >= 0; i-- {
		if c.active[i].obj == obj {
			return &c.active[i]
		}
	}
	return nil
}

func (c *cfLeaf) fieldBinding(e syntax.Expr) *cfActive {
	n, ok := e.(*syntax.Name)
	if !ok {
		return nil
	}
	if a := c.binding(n); a != nil && !a.index {
		return a
	}
	return nil
}

func (c *cfLeaf) subTagGet(call *syntax.CallExpr) (matched, descend bool) {
	fact, ok := c.run.info.LogosTagGet[call]
	if !ok {
		return false, false
	}
	p := c.parents[call]
	for {
		if _, ok := p.(*syntax.ParenExpr); !ok {
			break
		}
		p = c.parents[p]
	}
	if fact.Value != nil {
		text := strconv.Quote(constant.StringVal(fact.Value))
		switch p := p.(type) {
		case *syntax.CallStmt:
			// Keep a call in go and defer statements.
		case *syntax.ExprStmt:
			s, e := c.at(p.X)
			c.add(s, e, keepComments(c.run.ctx.buf.b, s, e, "_ = "+text))
			return true, false
		default:
			s, e := c.at(call)
			c.add(s, e, keepComments(c.run.ctx.buf.b, s, e, text))
			return true, false
		}
	}
	s, e := c.at(fact.Receiver)
	if !c.fl.needReflect {
		pkg, err := c.fl.cfLoadPackage("reflect")
		if err != nil {
			c.fl.cfError(call.Pos(), "[comptime::generic_import] cannot load reflect for a dynamic struct tag lookup: %v", err)
			return true, false
		}
		c.fl.pkg.ctExp.pkgs["reflect"] = pkg
	}
	c.add(s, e, reflectAlias+".StructTag("+strconv.Quote(fact.Tag)+")")
	c.fl.needReflect = true
	return true, true
}

func cfComptimeCall(call *syntax.CallExpr, info *types2.Info) string {
	fun := syntax.Unparen(call.Fun)
	if idx, ok := fun.(*syntax.IndexExpr); ok {
		fun = idx.X
	}
	sel, ok := fun.(*syntax.SelectorExpr)
	if !ok {
		return ""
	}
	name, ok := sel.X.(*syntax.Name)
	if !ok {
		return ""
	}
	if pkg, ok := info.Uses[name].(*types2.PkgName); ok && pkg.Imported().Path() == "comptime" {
		return sel.Sel.Value
	}
	return ""
}

func (c *cfLeaf) subBlankAssign(s *syntax.AssignStmt) {
	if s.Op != 0 {
		return
	}
	lhs := syntax.UnpackListExpr(s.Lhs)
	rhs := syntax.UnpackListExpr(s.Rhs)
	if len(lhs) != 1 || len(rhs) != 1 {
		return
	}
	ln, ok := lhs[0].(*syntax.Name)
	if !ok || ln.Value != "_" {
		return
	}
	if c.fieldBinding(rhs[0]) == nil {
		return
	}
	ss, se := c.at(s)
	c.add(ss, se, "")
}

func (r *ctReducer) constFor(s *syntax.ConstForStmt, f *ctFrame) *syntax.BlockStmt {
	out := at(&syntax.BlockStmt{Rbrace: s.Body.Rbrace}, s.Pos())
	loop := f.info.LogosConstFor[s]
	if loop == nil || loop.Struct == nil {
		r.invalid(s, "missing concrete const for iterations")
		return out
	}
	fields := cfFieldsOf(loop)
	out.List = make([]syntax.Stmt, 0, len(fields))
	for _, field := range fields {
		iteration := newCtFrame(loop.Iterations[field.index], f)
		iteration.active = cfPushField(nil, loop, field)
		r.ctIterationLabels(s.Body, iteration)
		out.List = append(out.List, r.clone(s.Body, iteration).(syntax.Stmt))
	}
	return out
}

func (r *ctReducer) ctIterationLabels(body *syntax.BlockStmt, f *ctFrame) {
	syntax.Inspect(body, func(n syntax.Node) bool {
		if s, ok := n.(syntax.Stmt); ok && f.info.LogosConstIfDead[s] {
			return false
		}
		switch n := n.(type) {
		case *syntax.ConstForStmt, *syntax.FuncLit, *syntax.LambdaExpr, *syntax.GroupChild:
			return false
		case *syntax.LabeledStmt:
			if n.Label.Value != "_" {
				if f.labels == nil {
					f.labels = make(map[string]string)
				}
				r.nextLabel++
				f.labels[n.Label.Value] = "_logos_ct_label" + strconv.Itoa(r.nextLabel)
			}
		}
		return true
	})
}

func (r *ctReducer) renameLabel(label *syntax.Name, f *ctFrame) {
	if label == nil {
		return
	}
	for f != nil {
		if name, ok := f.labels[label.Value]; ok {
			label.Value = name
			return
		}
		f = f.outer
	}
}

func (f *ctFrame) binding(n *syntax.Name) *cfActive {
	obj, ok := f.info.Uses[n].(*types2.Var)
	if !ok {
		return nil
	}
	for f != nil {
		for i := range f.active {
			if f.active[i].obj == obj {
				return &f.active[i]
			}
		}
		f = f.outer
	}
	return nil
}

func (f *ctFrame) fieldBinding(e syntax.Expr) *cfActive {
	if name, ok := syntax.Unparen(e).(*syntax.Name); ok {
		if b := f.binding(name); b != nil && !b.index {
			return b
		}
	}
	return nil
}

func (r *ctReducer) projectField(e *syntax.FieldProjExpr, f *ctFrame) syntax.Expr {
	binding := f.fieldBinding(e.Field)
	if binding == nil {
		r.invalid(e, "missing concrete field projection")
		return nil
	}
	pos := e.Pos()
	name := nameOf(e.Field.Pos(), binding.field.name)
	x := r.clone(e.X, f).(syntax.Expr)
	sel := at(&syntax.SelectorExpr{X: x, Sel: name}, pos)
	if typ := f.info.TypeOf(e.X); typ != nil {
		if s, ok := types2.LookupSelection(typ, true, r.owner, binding.field.name); ok {
			r.info.Selections[sel] = &s
			r.info.Uses[name] = s.Obj()
		}
	}
	return sel
}

func (r *ctReducer) blankFieldAssign(s *syntax.AssignStmt, f *ctFrame) bool {
	lhs := syntax.UnpackListExpr(s.Lhs)
	rhs := syntax.UnpackListExpr(s.Rhs)
	if s.Op != 0 || len(lhs) != 1 || len(rhs) != 1 {
		return false
	}
	name, ok := lhs[0].(*syntax.Name)
	return ok && name.Value == "_" && f.fieldBinding(rhs[0]) != nil
}

func (r *ctReducer) checkIndexStore(lhs syntax.Expr, f *ctFrame) {
	for _, e := range syntax.UnpackListExpr(lhs) {
		name, ok := syntax.Unparen(e).(*syntax.Name)
		if !ok {
			continue
		}
		if binding := f.binding(name); binding != nil && binding.index {
			r.invalid(name, "[comptime::const_for_assign] cannot assign to const for variable "+name.Value)
		}
	}
}

func (r *ctReducer) reduceTagGet(e *syntax.CallExpr, fact types2.LogosTagGet, f *ctFrame) syntax.Expr {
	pos := e.Pos()
	tag := litOf(fact.Receiver.Pos(), strconv.Quote(fact.Tag), syntax.StringLit)
	recv := callOf(pos, r.fl.ctPkgSelector(pos, "reflect", "StructTag"), []syntax.Expr{tag})
	var args []syntax.Expr
	for _, a := range e.ArgList {
		args = append(args, r.clone(a, f).(syntax.Expr))
	}
	return callOf(pos, selOf(pos, recv, "Get"), args)
}

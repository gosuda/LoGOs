// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// ctEjectLocal plans the helpers of one instance that name a type declared
// inside the instance body. A package-level declaration cannot name such a
// type, so the ejected text declares each of these helpers in the body,
// right after the declaration of the last body type it names. The native
// build keeps the helpers at package level.
type ctEjectLocal struct {
	sites map[*types2.TypeName]ctEjectTypeSite
	bound map[int]*types2.TypeName // helper index to the body type it follows
	refs  [][]int                  // helper index to the helpers it names
}

// ctEjectTypeSite is the statement that declares a body type and the
// statement list that holds it.
type ctEjectTypeSite struct {
	list *[]syntax.Stmt
	stmt syntax.Stmt
}

func ctEjectPlanLocal(rec *types2.LogosCtInst) (ctEjectLocal, error) {
	facts := rec.Info
	plan := ctEjectLocal{sites: make(map[*types2.TypeName]ctEjectTypeSite)}
	var walk func(list *[]syntax.Stmt)
	walk = func(list *[]syntax.Stmt) {
		for _, s := range *list {
			if ds, ok := s.(*syntax.DeclStmt); ok {
				for _, d := range ds.DeclList {
					if td, ok := d.(*syntax.TypeDecl); ok {
						if obj, ok := facts.Defs[td.Name].(*types2.TypeName); ok {
							plan.sites[obj] = ctEjectTypeSite{list, s}
						}
					}
				}
			}
			eachStmtList(s, walk)
		}
	}
	walk(&rec.Decl.Body.List)
	if len(plan.sites) == 0 {
		return plan, nil
	}
	helperOf := make(map[types2.Object]int)
	for i, helper := range rec.Helpers {
		for _, name := range ctEjectDeclNames(helper) {
			if obj := facts.Defs[name]; obj != nil {
				helperOf[obj] = i
			}
		}
	}
	needs := make([]map[*types2.TypeName]bool, len(rec.Helpers))
	plan.refs = make([][]int, len(rec.Helpers))
	for i, helper := range rec.Helpers {
		needs[i] = make(map[*types2.TypeName]bool)
		syntax.Inspect(helper, func(n syntax.Node) bool {
			name, ok := n.(*syntax.Name)
			if !ok {
				return true
			}
			if name.Value == "_logos_ct_type" {
				ctEjectMentions(name.GetTypeInfo().Type, plan.sites, needs[i], make(map[*types2.Named]bool))
				return true
			}
			obj := facts.Uses[name]
			if tn, ok := obj.(*types2.TypeName); ok {
				if _, local := plan.sites[tn]; local {
					needs[i][tn] = true
				}
			}
			if j, ok := helperOf[obj]; ok && j != i {
				plan.refs[i] = append(plan.refs[i], j)
			}
			return true
		})
	}
	for changed := true; changed; {
		changed = false
		for i := range rec.Helpers {
			for _, j := range plan.refs[i] {
				for tn := range needs[j] {
					if !needs[i][tn] {
						needs[i][tn] = true
						changed = true
					}
				}
			}
		}
	}
	for i, helper := range rec.Helpers {
		var last *types2.TypeName
		for tn := range needs[i] {
			if last == nil || syntax.StartPos(plan.sites[tn].stmt).Cmp(syntax.StartPos(plan.sites[last].stmt)) > 0 {
				last = tn
			}
		}
		if last == nil {
			continue
		}
		switch d := helper.(type) {
		case *syntax.FuncDecl:
			if d.Recv != nil || len(d.TParamList) != 0 {
				return plan, fmt.Errorf("%s: comptime instance helper %s names body type %s but is not a plain function", d.Pos(), d.Name.Value, last.Name())
			}
		case *syntax.VarDecl:
			if len(d.NameList) != 1 {
				return plan, fmt.Errorf("%s: comptime instance helper declares %d variables that name body type %s", d.Pos(), len(d.NameList), last.Name())
			}
		}
		if plan.bound == nil {
			plan.bound = make(map[int]*types2.TypeName)
		}
		plan.bound[i] = last
	}
	return plan, nil
}

// ctEjectMentions adds to out every body type that t names.
func ctEjectMentions(t types2.Type, sites map[*types2.TypeName]ctEjectTypeSite, out map[*types2.TypeName]bool, seen map[*types2.Named]bool) {
	switch t := t.(type) {
	case *types2.Alias:
		ctEjectMentions(types2.Unalias(t), sites, out, seen)
	case *types2.Named:
		if _, local := sites[t.Obj()]; local {
			out[t.Obj()] = true
		}
		if seen[t] {
			return
		}
		seen[t] = true
		for i := range t.TypeArgs().Len() {
			ctEjectMentions(t.TypeArgs().At(i), sites, out, seen)
		}
	case *types2.Pointer:
		ctEjectMentions(t.Elem(), sites, out, seen)
	case *types2.Slice:
		ctEjectMentions(t.Elem(), sites, out, seen)
	case *types2.Array:
		ctEjectMentions(t.Elem(), sites, out, seen)
	case *types2.Chan:
		ctEjectMentions(t.Elem(), sites, out, seen)
	case *types2.Map:
		ctEjectMentions(t.Key(), sites, out, seen)
		ctEjectMentions(t.Elem(), sites, out, seen)
	case *types2.Struct:
		for i := range t.NumFields() {
			ctEjectMentions(t.Field(i).Type(), sites, out, seen)
		}
	case *types2.Tuple:
		for i := range t.Len() {
			ctEjectMentions(t.At(i).Type(), sites, out, seen)
		}
	case *types2.Signature:
		ctEjectMentions(t.Params(), sites, out, seen)
		ctEjectMentions(t.Results(), sites, out, seen)
	case *types2.Interface:
		for i := range t.NumExplicitMethods() {
			ctEjectMentions(t.ExplicitMethod(i).Type(), sites, out, seen)
		}
		for i := range t.NumEmbeddeds() {
			ctEjectMentions(t.EmbeddedType(i), sites, out, seen)
		}
	case *types2.Union:
		for i := range t.Len() {
			ctEjectMentions(t.Term(i).Type(), sites, out, seen)
		}
	case *types2.Option:
		ctEjectMentions(t.Elem, sites, out, seen)
	case *types2.ConstView:
		ctEjectMentions(t.Elem, sites, out, seen)
	case *types2.ReadonlyDescendants:
		ctEjectMentions(t.Elem, sites, out, seen)
	case *types2.LogosTuple:
		for i := range t.Len() {
			ctEjectMentions(t.Elem(i), sites, out, seen)
		}
	}
}

// placeLocal moves the planned helpers into the instance body, after the
// statement that declares the body type each one follows. A function
// becomes a function literal. A variable keeps one value per instance, as
// natively: it lives in a package-level box that the first call fills, and
// the body reads it through a pointer. placeLocal returns the box
// declarations and a function that puts every statement list back.
func (p *ctEjectPrinter) placeLocal(rec *types2.LogosCtInst, plan ctEjectLocal, renames map[types2.Object]string) ([]string, func(), error) {
	var undo []func()
	restore := func() {
		for i := len(undo) - 1; i >= 0; i-- {
			undo[i]()
		}
	}
	if len(plan.bound) == 0 {
		return nil, restore, nil
	}
	facts := rec.Info
	var sites []syntax.Stmt
	groups := make(map[syntax.Stmt][]int)
	for i := range rec.Helpers {
		tn, ok := plan.bound[i]
		if !ok {
			continue
		}
		site := plan.sites[tn].stmt
		if groups[site] == nil {
			sites = append(sites, site)
		}
		groups[site] = append(groups[site], i)
	}
	var boxes []string
	inserts := make(map[syntax.Stmt][]syntax.Stmt)
	for _, site := range sites {
		order, err := ctEjectLocalOrder(groups[site], plan.refs)
		if err != nil {
			return nil, restore, err
		}
		for _, i := range order {
			stmts, box := p.localHelper(rec.Helpers[i], facts, renames)
			inserts[site] = append(inserts[site], stmts...)
			if box != "" {
				boxes = append(boxes, box)
			}
		}
	}
	done := make(map[*[]syntax.Stmt]bool)
	for _, site := range sites {
		list := plan.sites[plan.bound[groups[site][0]]].list
		if done[list] {
			continue
		}
		done[list] = true
		old := *list
		grown := make([]syntax.Stmt, 0, len(old))
		for _, s := range old {
			grown = append(grown, s)
			grown = append(grown, inserts[s]...)
		}
		*list = grown
		undo = append(undo, func() { *list = old })
	}
	return boxes, restore, nil
}

// ctEjectLocalOrder orders one group of helpers so that each follows the
// helpers of the group it names.
func ctEjectLocalOrder(group []int, refs [][]int) ([]int, error) {
	member := make(map[int]bool, len(group))
	for _, i := range group {
		member[i] = true
	}
	const (
		visiting = 1
		visited  = 2
	)
	state := make(map[int]int, len(group))
	var order []int
	var visit func(i int) error
	visit = func(i int) error {
		switch state[i] {
		case visiting:
			return fmt.Errorf("comptime instance helpers that name a body type refer to each other in a cycle")
		case visited:
			return nil
		}
		state[i] = visiting
		for _, j := range refs[i] {
			if member[j] {
				if err := visit(j); err != nil {
					return err
				}
			}
		}
		state[i] = visited
		order = append(order, i)
		return nil
	}
	for _, i := range group {
		if err := visit(i); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// localHelper returns the body statements that declare one helper, and for
// a variable the package-level box that holds its value.
func (p *ctEjectPrinter) localHelper(helper syntax.Decl, facts *types2.Info, renames map[types2.Object]string) ([]syntax.Stmt, string) {
	pos := helper.Pos()
	use := func(name string) syntax.Stmt {
		return &syntax.AssignStmt{Lhs: syntax.NewName(pos, "_"), Rhs: syntax.NewName(pos, name)}
	}
	switch d := helper.(type) {
	case *syntax.VarDecl:
		obj := facts.Defs[d.NameList[0]]
		name := renames[obj]
		typ := types2.TypeString(obj.Type(), p.qualify)
		box := name + "_box"
		set := &syntax.VarDecl{NameList: []*syntax.Name{syntax.NewName(pos, name)}, Type: syntax.NewName(pos, typ), Values: d.Values}
		fill := &syntax.FuncLit{Type: &syntax.FuncType{}, Body: &syntax.BlockStmt{List: []syntax.Stmt{
			&syntax.DeclStmt{DeclList: []syntax.Decl{set}},
			&syntax.AssignStmt{Lhs: syntax.NewName(pos, box+".value"), Rhs: syntax.NewName(pos, "&"+name)},
		}}}
		once := &syntax.ExprStmt{X: &syntax.CallExpr{Fun: syntax.NewName(pos, box+".once.Do"), ArgList: []syntax.Expr{fill}}}
		get := &syntax.AssignStmt{Op: syntax.Def, Lhs: syntax.NewName(pos, name), Rhs: syntax.NewName(pos, box+".value.(*"+typ+")")}
		sync := p.alias(types2.NewPackage("sync", "sync"))
		decl := fmt.Sprintf("var %s struct {\n\tonce  %s.Once\n\tvalue any\n}", box, sync)
		return []syntax.Stmt{once, get, use(name)}, decl
	case *syntax.FuncDecl:
		name := renames[facts.Defs[d.Name]]
		lit := &syntax.FuncLit{Type: d.Type, Body: d.Body}
		decl := &syntax.VarDecl{NameList: []*syntax.Name{syntax.NewName(pos, name)}, Values: lit}
		return []syntax.Stmt{&syntax.DeclStmt{DeclList: []syntax.Decl{decl}}, use(name)}, ""
	}
	return []syntax.Stmt{&syntax.DeclStmt{DeclList: []syntax.Decl{ctEjectUngrouped(helper).(syntax.Decl)}}}, ""
}

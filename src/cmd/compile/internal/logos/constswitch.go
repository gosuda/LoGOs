// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"go/token"
	"reflect"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

type cswKind int

const (
	cswExact cswKind = iota
	cswTilde
	cswIface
)

type cswCase struct {
	syn  *syntax.ConstSwitchCase
	kind cswKind
	typ  types2.Type

	spell otext

	lo, hi int

	close syntax.Pos

	sub types2.LogosConstCase
}

type cswLower struct {
	fc    *funcLower
	fl    *fileLower
	info  *types2.Info
	stmt  *syntax.ConstSwitchStmt
	start int
	end   int
	tag   types2.Type
	cases []cswCase
}

type cswRebind struct {
	obj   *types2.Var
	name  string
	spell string
}

func (fc *funcLower) lowerConstSwitchStmt(s *syntax.ConstSwitchStmt) []syntax.Stmt {
	c := newCswLower(fc, s)
	if _, ok := c.tag.(*types2.TypeParam); ok {
		return c.lowerRuntime()
	}
	return c.lowerFold()
}

func newCswLower(fc *funcLower, s *syntax.ConstSwitchStmt) *cswLower {
	fl := fc.fl
	c := &cswLower{
		fc:    fc,
		fl:    fl,
		info:  fl.pkg.info,
		stmt:  s,
		start: fl.off(s.Pos()),
		end:   fl.off(s.Rbrace) + 1,
		tag:   checkedTypeOf(fl.pkg.info, s.Targ),
	}
	for i, syn := range s.Cases {
		typ := checkedTypeOf(fl.pkg.info, syn.Type)
		cs := cswCase{
			syn:   syn,
			kind:  cswExact,
			typ:   typ,
			spell: c.spell(fl.off(syn.Case), fl.off(syn.Colon)),
			lo:    fl.off(syn.Colon) + 1,
			hi:    fl.off(s.Rbrace),
			sub:   fl.pkg.info.LogosConstCases[syn],
		}
		cs.close = s.Rbrace
		if i+1 < len(s.Cases) {
			cs.hi = fl.off(s.Cases[i+1].Case)
			cs.close = s.Cases[i+1].Case
		}
		switch {
		case syn.Tilde:
			cs.kind = cswTilde
		default:
			if _, ok := types2.Unalias(typ).Underlying().(*types2.Interface); ok {
				cs.kind = cswIface
			}
		}
		c.cases = append(c.cases, cs)
	}
	return c
}

func checkedTypeOf(info *types2.Info, e syntax.Expr) types2.Type {
	var name *syntax.Name
	switch e := e.(type) {
	case *syntax.Name:
		name = e
	case *syntax.SelectorExpr:
		name = e.Sel
	}
	if name != nil {
		if tn, ok := info.Uses[name].(*types2.TypeName); ok {
			return tn.Type()
		}
	}
	if typ := info.TypeOf(e); typ != nil {
		return typ
	}
	base.Fatalf("logos: const switch type at %s has no checked type", e.Pos().String())
	return nil
}

func (c *cswLower) spell(from, bound int) otext {
	start := typeStart(c.fl.src, from, bound)
	return c.fl.textOf(span{start, typeEnd(c.fl.src, start, bound)})
}

func typeStart(src []byte, from, bound int) int {
	start := bound
	scanSegment(src, from, bound, func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		switch t.kind {
		case token.CASE, token.CONST, token.SWITCH, token.TILDE:
			return true
		}
		start = t.start
		return false
	})
	return start
}

func typeEnd(src []byte, start, bound int) int {
	end := start
	depth := 0
	scanSegment(src, start, bound, func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		switch t.kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			if depth == 0 {
				return false
			}
			depth--
		}
		end = t.end
		return true
	})
	return end
}

func (c *cswLower) lowerFold() []syntax.Stmt {
	sel, static := types2.LogosConstSelectedCase(c.info, c.stmt)
	if !static {
		base.Fatalf("logos: const switch at %s has no compile-time selection", c.stmt.Pos().String())
	}
	var match *cswCase
	for i := range c.cases {
		if c.cases[i].syn == sel {
			match = &c.cases[i]
			break
		}
	}
	if match == nil {
		c.fc.deleteConstSpan(c.start, c.end)
		return nil
	}
	stmts, text := c.caseBody(match, "")
	block := closeAt(c.stmt.Pos(), stmts, c.stmt.Rbrace)
	kept := span{match.lo, match.hi}
	c.fl.addT(c.start, c.end, cat(leftoverComments(c.fl, span{c.start, c.end}, []span{kept}), gen("{"), text, gen("}")))
	return []syntax.Stmt{block}
}

func (c *cswLower) runs() [][]*cswCase {
	var out [][]*cswCase
	for i := range c.cases {
		cs := &c.cases[i]
		n := len(out)
		if n > 0 && (out[n-1][0].kind == cswExact) == (cs.kind == cswExact) {
			out[n-1] = append(out[n-1], cs)
			continue
		}
		out = append(out, []*cswCase{cs})
	}
	return out
}

func (c *cswLower) lowerRuntime() []syntax.Stmt {
	runs := c.runs()
	if len(runs) <= 1 || (len(runs) == 2 && runs[0][0].kind == cswExact) {
		var exacts, rest []*cswCase
		for _, run := range runs {
			if run[0].kind == cswExact {
				exacts = run
			} else {
				rest = run
			}
		}
		stmt, text := c.typeSwitch(exacts, rest, "")
		c.fl.addT(c.start, c.end, text)
		return []syntax.Stmt{stmt}
	}
	pos := c.stmt.Pos()
	done := fmt.Sprintf("_logos_csw%d", c.start)
	block := at(&syntax.BlockStmt{Rbrace: c.stmt.Rbrace}, pos)
	block.List = append(block.List, defineOf(pos, []syntax.Expr{nameOf(pos, done)}, []syntax.Expr{nameOf(pos, "false")}))
	text := gen("{" + done + " := false\n")
	for _, run := range runs {
		first := run[0].syn.Case
		close := run[len(run)-1].close
		var inner syntax.Stmt
		var innerText otext
		if run[0].kind == cswExact {
			inner, innerText = c.typeSwitch(run, nil, done)
			if sw, ok := inner.(*syntax.SwitchStmt); ok {
				sw.Rbrace = close
			}
		} else {
			inner, innerText = c.chainIf(run, done)
		}
		guard := at(&syntax.Operation{Op: syntax.Not, X: nameOf(first, done)}, first)
		block.List = append(block.List, at(&syntax.IfStmt{
			Cond: guard,
			Then: closeAt(first, []syntax.Stmt{inner}, close),
		}, first))
		text = cat(text, gen("if !"+done+" {"), innerText, gen("}\n"))
	}
	text = cat(text, gen("}"))
	c.fl.addT(c.start, c.end, text)
	return []syntax.Stmt{block}
}

func (c *cswLower) typeSwitch(exacts, rest []*cswCase, done string) (syntax.Stmt, otext) {
	pos := c.stmt.Pos()
	if len(exacts) > 0 {
		pos = exacts[0].syn.Case
	} else if len(rest) > 0 {
		pos = rest[0].syn.Case
	}
	tag := c.spell(c.fl.off(c.stmt.Pos()), c.fl.off(c.stmt.Lbrace))
	guard, ok := parseExprAt("any((*"+tag.s+")(nil))", pos)
	if !ok {
		base.Fatalf("logos: const switch tag %s at %s cannot be respelled", tag.s, pos.String())
	}
	sw := at(&syntax.SwitchStmt{
		Tag:    at(&syntax.TypeSwitchGuard{X: guard}, pos),
		Rbrace: c.stmt.Rbrace,
	}, pos)
	text := cat(gen("switch any((*"), tag, gen(")(nil)).(type) {"))
	for _, cs := range exacts {
		stmts, bodyText := c.caseBody(cs, done)
		ptr, ok := parseExprAt("*"+cs.spell.s, cs.syn.Case)
		if !ok {
			base.Fatalf("logos: const switch case %s at %s cannot be respelled", cs.spell.s, cs.syn.Case.String())
		}
		sw.Body = append(sw.Body, at(&syntax.CaseClause{Cases: ptr, Body: stmts}, cs.syn.Case))
		text = cat(text, gen("\ncase *"), cs.spell, gen(":"), bodyText)
	}
	if len(rest) > 0 {
		chain, chainText := c.chainIf(rest, done)
		sw.Body = append(sw.Body, at(&syntax.CaseClause{Body: []syntax.Stmt{chain}}, rest[0].syn.Case))
		text = cat(text, gen("\ndefault:\n"), chainText)
	}
	text = cat(text, gen("\n}"))
	return sw, text
}

func (c *cswLower) chainIf(run []*cswCase, done string) (syntax.Stmt, otext) {
	var next syntax.Stmt
	var nextText otext
	for i := len(run) - 1; i >= 0; i-- {
		cs := run[i]
		cond, condText := c.cond(cs)
		stmts, bodyText := c.caseBody(cs, done)
		link := at(&syntax.IfStmt{
			Cond: cond,
			Then: closeAt(cs.syn.Case, stmts, cs.close),
			Else: next,
		}, cs.syn.Case)
		text := cat(gen("if "), condText, gen(" {"), bodyText, gen("}"))
		if next != nil {
			text = cat(text, gen(" else "), nextText)
		}
		next, nextText = link, text
	}
	return next, nextText
}

func closeAt(pos syntax.Pos, list []syntax.Stmt, close syntax.Pos) *syntax.BlockStmt {
	return at(&syntax.BlockStmt{List: list, Rbrace: close}, pos)
}

func (c *cswLower) cond(cs *cswCase) (syntax.Expr, otext) {
	c.fl.needRT = true
	tag := c.spell(c.fl.off(c.stmt.Pos()), c.fl.off(c.stmt.Lbrace))
	helper := "Implements"
	if cs.kind == cswTilde {
		helper = "TildeIs"
	}
	text := cat(gen(rtAlias+"."+helper+"["), tag, gen(", "), cs.spell, gen("]()"))
	expr, ok := parseExprAt(text.s, cs.syn.Case)
	if !ok {
		base.Fatalf("logos: const switch case %s at %s cannot be respelled", cs.spell.s, cs.syn.Case.String())
	}
	return expr, text
}

func (c *cswLower) caseBody(cs *cswCase, done string) ([]syntax.Stmt, otext) {
	if cs.kind == cswIface {
		c.wrapSelectors(cs.syn.Body, cs)
		c.wrapConvs(cs.syn.Body, cs)
	} else if cs.sub.Target != nil {
		c.rewriteRebound(cs)
	}
	lowered := c.fc.lowerList(cs.syn.Body)
	var head []syntax.Stmt
	var headText otext
	if done != "" {
		head = append(head, at(&syntax.AssignStmt{Lhs: nameOf(cs.syn.Case, done), Rhs: nameOf(cs.syn.Case, "true")}, cs.syn.Case))
		headText = gen(done + " = true\n")
	}
	body := c.fl.textOf(span{cs.lo, cs.hi})
	return append(head, lowered...), cat(headText, body)
}

// rewriteRebound respells the switched type's name to the case type and
// rewrites every use of a variable bound to the switched type as a
// dereference of a type-correct pointer into the variable's own storage,
// taken at the use site so the address is always the variable visible there:
// reads see the current value and writes land directly, on every exit path.
func (c *cswLower) rewriteRebound(cs *cswCase) {
	targets := make(map[types2.Object]cswTarget)
	spell := cs.spell
	targets[cs.sub.Target] = cswTarget{
		repl: func(pos syntax.Pos) syntax.Expr {
			e, ok := parseExprAt(spell.s, pos)
			if !ok {
				base.Fatalf("logos: const switch case %s at %s cannot be respelled", spell.s, pos.String())
			}
			return e
		},
		splice: func(start, end int) { c.fl.addT(start, end, spell) },
	}
	pun := "PtrAs"
	if cs.kind == cswTilde {
		// The rebound type keeps the layout but not the identity of the
		// type argument (Port vs int), so an identity assert cannot see
		// through it; the tilde match guarantees the layouts agree.
		pun = "UnderPtrAs"
	}
	for _, r := range c.rebound(cs) {
		c.fl.needRT = true
		replText := "*(" + rtAlias + "." + pun + "[" + r.spell + "](&" + r.name + "))"
		targets[r.obj] = cswTarget{
			repl: func(pos syntax.Pos) syntax.Expr {
				e, ok := parseExprAt(replText, pos)
				if !ok {
					base.Fatalf("logos: const switch rebinding of %s at %s cannot be respelled", r.name, pos.String())
				}
				return e
			},
			splice: func(start, end int) { c.fl.addT(start, end, gen(replText)) },
		}
	}
	w := cswWalker{info: c.info, off: c.fl.off, targets: targets}
	w.list(cs.syn.Body)
}

func (c *cswLower) rebound(cs *cswCase) []cswRebind {
	var out []cswRebind
	seen := make(map[*types2.Var]bool)
	for _, s := range cs.syn.Body {
		syntax.Inspect(s, func(n syntax.Node) bool {
			name, ok := n.(*syntax.Name)
			if !ok {
				return true
			}
			v, ok := c.info.Uses[name].(*types2.Var)
			if !ok || seen[v] {
				return true
			}
			sub, ok := cs.sub.Vars[v]
			if !ok {
				return true
			}
			seen[v] = true
			out = append(out, cswRebind{obj: v, name: v.Name(), spell: c.cswSpell(sub)})
			return true
		})
	}
	return out
}

func (c *cswLower) cswSpell(typ types2.Type) string {
	names := c.fl.importNames()
	return types2.TypeString(typ, func(p *types2.Package) string {
		if p == c.fl.pkg.tpkg {
			return ""
		}
		q, ok := names[p]
		if !ok {
			base.Fatalf("logos: const switch rebinding of type %s needs package %s, which the file does not import", types2.TypeString(typ, nil), p.Path())
		}
		return q
	})
}

// cswTarget is one name the walker rewrites: the AST replacement for the
// node and the source-text splice for the emitted file.
type cswTarget struct {
	repl   func(syntax.Pos) syntax.Expr
	splice func(start, end int)
}

type cswWalker struct {
	info    *types2.Info
	off     func(syntax.Pos) int
	targets map[types2.Object]cswTarget
	conv    map[syntax.Expr]syntax.Expr
	wrap    func(x, r syntax.Expr)
}

func (w cswWalker) list(stmts []syntax.Stmt) {
	for _, s := range stmts {
		w.node(s)
	}
}

func (w cswWalker) node(root syntax.Node) {
	syntax.Inspect(root, func(n syntax.Node) bool {
		if n == nil {
			return true
		}
		if sw, ok := n.(*syntax.ConstSwitchStmt); ok {
			for _, cc := range sw.Cases {
				w.list(cc.Body)
			}
			return false
		}
		w.replaceChildren(n)
		return true
	})
}

func (w cswWalker) replaceChildren(n syntax.Node) {
	v := reflect.ValueOf(n)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return
	}
	v = v.Elem()
	if v.Kind() != reflect.Struct {
		return
	}
	for i := range v.NumField() {
		if v.Type().Field(i).IsExported() {
			w.replaceNames(v.Field(i))
		}
	}
}

func (w cswWalker) replaceNames(f reflect.Value) {
	switch f.Kind() {
	case reflect.Interface:
		if f.IsNil() || !f.CanSet() {
			return
		}
		x, ok := f.Elem().Interface().(syntax.Expr)
		if !ok {
			return
		}
		if r, ok := w.conv[x]; ok {
			delete(w.conv, x) // the replacement embeds x; consume the entry so it is not wrapped again
			if w.wrap != nil {
				w.wrap(x, r)
			}
			f.Set(reflect.ValueOf(r))
			return
		}
		if name, ok := x.(*syntax.Name); ok {
			w.replaceName(f, name)
		}
	case reflect.Slice, reflect.Array:
		for i := range f.Len() {
			w.replaceNames(f.Index(i))
		}
	}
}

func (w cswWalker) replaceName(f reflect.Value, x *syntax.Name) {
	t, ok := w.targets[w.info.Uses[x]]
	if !ok {
		return
	}
	t.splice(w.off(x.Pos()), w.off(x.Pos())+len(x.Value))
	f.Set(reflect.ValueOf(t.repl(x.Pos())))
}

// wrapConvs boxes each operand the checker recorded under the interface case
// as rt.Box[I](e): the value keeps its concrete layout at storage sites and
// widens to the case interface where the checker admitted it (SPEC Sec 2.5).
// A recorded expression nested inside another one is composed inside the
// outer replacement so the splices never overlap.
func (c *cswLower) wrapConvs(body []syntax.Stmt, cs *cswCase) {
	type rec struct {
		e          syntax.Expr
		start, end int
	}
	var recs []rec // Inspect is a preorder walk, so recs arrive in source order.
	for _, s := range body {
		syntax.Inspect(s, func(n syntax.Node) bool {
			e, ok := n.(syntax.Expr)
			if !ok || c.info.LogosConstConv[e] != cs.syn {
				return true
			}
			start := c.fl.off(leftmostOf(e))
			recs = append(recs, rec{e: e, start: start, end: c.fl.exprEndNested(start)})
			return true
		})
	}
	if len(recs) == 0 {
		return
	}
	c.fl.needRT = true
	box := func(text string) string {
		return rtAlias + ".Box[" + cs.spell.s + "](" + text + ")"
	}
	// A recorded operand in multi-result position — the sole right side of a
	// two-result assignment, declaration, or return — keeps its arity through
	// a two-result closure that boxes the first value: `p, ok = m[k]` becomes
	// `p, ok = func() (I, bool) { v, ok := m[k]; return Box[I](v), ok }()`.
	multi := make(map[syntax.Expr]bool)
	for _, s := range body {
		syntax.Inspect(s, func(n syntax.Node) bool {
			var rhs syntax.Expr
			two := false
			switch st := n.(type) {
			case *syntax.AssignStmt:
				rhs = st.Rhs
				if lhs, ok := st.Lhs.(*syntax.ListExpr); ok {
					two = len(lhs.ElemList) == 2
				}
			case *syntax.VarDecl:
				rhs = st.Values
				two = len(st.NameList) == 2
			case *syntax.ReturnStmt:
				rhs = st.Results
				two = c.resultCount() == 2
			}
			if two && rhs != nil && c.info.LogosConstConv[rhs] == cs.syn {
				multi[rhs] = true
			}
			return true
		})
	}
	secondOf := func(e syntax.Expr) (string, bool) {
		switch x := syntax.Unparen(e).(type) {
		case *syntax.IndexExpr, *syntax.AssertExpr:
			return "bool", true
		case *syntax.Operation:
			if x.Op == syntax.Recv {
				return "bool", true
			}
		}
		if tup, ok := c.info.TypeOf(e).(*types2.Tuple); ok && tup.Len() == 2 {
			return c.cswSpell(tup.At(1).Type()), true
		}
		return "", false
	}
	// expand renders the span's source text with every recorded expression
	// inside it boxed; nested recordings compose inside their outer boxes.
	var expand func(start, end int) string
	expand = func(start, end int) string {
		var inner []rec
		for _, r := range recs {
			if r.start >= start && r.end <= end && (r.start != start || r.end != end) {
				inner = append(inner, r)
			}
		}
		var out strings.Builder
		pos := start
		for _, r := range inner {
			nested := false
			for _, o := range inner {
				if o != r && o.start <= r.start && r.end <= o.end {
					nested = true
					break
				}
			}
			if nested {
				continue
			}
			if r.start > pos {
				out.WriteString(c.fl.textOf(span{pos, r.start}).s)
			}
			out.WriteString(box(expand(r.start, r.end)))
			pos = r.end
		}
		if pos < end {
			out.WriteString(c.fl.textOf(span{pos, end}).s)
		}
		return out.String()
	}
	conv := make(map[syntax.Expr]syntax.Expr)
	texts := make(map[syntax.Expr]otext)
	for _, r := range recs {
		contained := false
		for _, o := range recs {
			if o != r && o.start <= r.start && r.end <= o.end {
				contained = true
				break
			}
		}
		if contained {
			continue // emitted inside the outer box's composed text
		}
		text := box(expand(r.start, r.end))
		if multi[r.e] {
			second, ok := secondOf(r.e)
			if !ok {
				base.Fatalf("logos: const switch boxing of a multi-result at %s cannot name its second type", r.e.Pos().String())
			}
			text = "func() (" + cs.spell.s + ", " + second + ") { _logos_v, _logos_s := " + expand(r.start, r.end) + "; return " + box("_logos_v") + ", _logos_s }()"
		}
		node, ok := parseExprAt(text, r.e.Pos())
		if !ok {
			base.Fatalf("logos: const switch interface boxing at %s cannot be respelled", r.e.Pos().String())
		}
		conv[r.e] = node
		texts[r.e] = gen(text)
	}
	w := cswWalker{
		info: c.info,
		off:  c.fl.off,
		conv: conv,
		wrap: func(x, r syntax.Expr) {
			start := c.fl.off(leftmostOf(x))
			c.fl.addT(start, c.fl.exprEndNested(start), texts[x])
		},
	}
	w.list(body)
}

// resultCount reports how many results the enclosing function declares:
// `return e` is multi-result only when the signature takes two.
func (c *cswLower) resultCount() int {
	if c.fc.ft == nil {
		return 0
	}
	return len(c.fc.ft.ResultList)
}

func (c *cswLower) wrapSelectors(body []syntax.Stmt, cs *cswCase) {
	for _, s := range body {
		syntax.Inspect(s, func(n syntax.Node) bool {
			sel, ok := n.(*syntax.SelectorExpr)
			if !ok || c.info.LogosConstSel[sel] != cs.syn {
				return true
			}
			xpos := leftmostOf(sel.X)
			iface, ok := typeExprAt(cs.spell.s, xpos)
			if !ok {
				base.Fatalf("logos: const switch interface %s at %s cannot be respelled", cs.spell.s, xpos.String())
			}
			xstart := c.fl.off(xpos)
			xend := typeEnd(c.fl.src, xstart, c.fl.off(sel.Sel.Pos()))
			if xend > xstart && c.fl.src[xend-1] == '.' {
				xend--
			}
			xspan := span{xstart, xend}
			xtext := c.fl.textOf(xspan)
			sel.X = at(&syntax.AssertExpr{
				X: at(&syntax.CallExpr{
					Fun:     at(nameOf(xpos, "any"), xpos),
					ArgList: []syntax.Expr{sel.X},
				}, xpos),
				Type: iface,
			}, xpos)
			c.fl.addT(xspan.start, xspan.end, cat(gen("any("), xtext, gen(").("), cs.spell, gen(")")))
			return true
		})
	}
}

func parseExprAt(text string, pos syntax.Pos) (syntax.Expr, bool) {
	file, err := syntax.Parse(syntax.NewFileBase("logos-clone"), strings.NewReader("package logosclone\nvar _ = "+text), nil, nil, syntax.LogosMode)
	if err != nil || file == nil || len(file.DeclList) != 1 {
		return nil, false
	}
	decl, ok := file.DeclList[0].(*syntax.VarDecl)
	if !ok || decl.Values == nil {
		return nil, false
	}
	syntax.Inspect(decl.Values, func(n syntax.Node) bool {
		if n != nil {
			n.SetPos(pos)
		}
		return true
	})
	return decl.Values, true
}

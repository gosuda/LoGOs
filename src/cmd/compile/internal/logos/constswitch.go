// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"go/token"
	"reflect"
	"slices"
	"strconv"
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
	elem int // element index; -1 for the default arm
	body []syntax.Stmt
	kind cswKind

	spell otext

	lo, hi int

	close syntax.Pos

	sub types2.LogosConstCase

	alias string // case-body alias for spell, immune to inner shadowing
}

type cswLower struct {
	fc    *funcLower
	fl    *fileLower
	info  *types2.Info
	stmt  *syntax.ConstSwitchStmt
	start int
	end   int
	cases []cswCase
	// Default arm lowering, when the switch has one. Lowered once per
	// switch; the clauses below splice its statements and text in place.
	defStmts  []syntax.Stmt
	defText   otext
	hasDef    bool
	defPos    syntax.Pos
	defClose  syntax.Pos
	hostTexts map[*syntax.ConstSwitchCase]otext
}
type cswRebind struct {
	obj   *types2.Var
	name  string
	spell string
}

func (fl *fileLower) snapshotConstSwitchCases() {
	var cases map[*syntax.ConstSwitchStmt]ctSwitchSel
	info := fl.pkg.info
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		if stmt, ok := n.(syntax.Stmt); ok && info.LogosConstIfDead[stmt] {
			return false
		}
		s, ok := n.(*syntax.ConstSwitchStmt)
		if !ok {
			return true
		}
		sel, elem, static := types2.LogosConstSelectedElem(info, s)
		if !static {
			return true
		}
		if cases == nil {
			cases = make(map[*syntax.ConstSwitchStmt]ctSwitchSel)
		}
		cases[s] = ctSwitchSel{sel: sel, elem: elem}
		return true
	})
	fl.pkg.ctExp.switches = cases
}

func (fc *funcLower) lowerConstSwitchStmt(s *syntax.ConstSwitchStmt) []syntax.Stmt {
	fl := fc.fl
	c := &cswLower{
		fc:    fc,
		fl:    fl,
		info:  fl.pkg.info,
		stmt:  s,
		start: fl.off(s.Pos()),
		end:   fl.off(s.Rbrace) + 1,
	}
	if sel, static := fl.pkg.ctExp.switches[s]; static {
		if sel.sel == nil {
			c.replaceFolded(otext{})
			return nil
		}
		if sel.elem < 0 {
			match := c.defaultCase(sel.sel)
			return c.lowerFold(&match)
		}
		match := c.caseAtElem(sel.sel, sel.elem, sel.sel.Body)
		return c.lowerFold(&match)
	}
	// Fresh copies from region lowering are not snapshotted: select
	// directly. Their tags resolve through the copy's bindings, so a
	// nested switch inside a copy selects statically.
	if sel, elem, static := types2.LogosConstSelectedElem(c.info, s); static {
		if sel == nil {
			c.replaceFolded(otext{})
			return nil
		}
		if elem < 0 {
			match := c.defaultCase(sel)
			return c.lowerFold(&match)
		}
		match := c.caseAtElem(sel, elem, sel.Body)
		return c.lowerFold(&match)
	}
	if _, ok := types2.Unalias(checkedTypeOf(c.info, s.Targ)).(*types2.TypeParam); !ok {
		base.Fatalf("logos: const switch at %s has no compile-time selection", s.Pos().String())
	}
	return c.lowerRuntime()
}

// defaultArm answers the switch's default arm, or nil.
func defaultArm(s *syntax.ConstSwitchStmt) *syntax.ConstSwitchCase {
	for _, c := range s.Cases {
		if c.Default {
			return c
		}
	}
	return nil
}

func (c *cswLower) caseAtElem(syn *syntax.ConstSwitchCase, elem int, body []syntax.Stmt) cswCase {
	fl, s := c.fl, c.stmt
	e := &syn.Elems[elem]
	typ := checkedTypeOf(c.info, e.Type)
	cs := cswCase{
		syn:   syn,
		elem:  elem,
		body:  body,
		kind:  cswExact,
		spell: c.elemSpell(e),
		lo:    fl.off(syn.Colon) + 1,
		hi:    fl.off(s.Rbrace),
		close: s.Rbrace,
		sub:   c.info.LogosConstCases[types2.LogosConstCaseKey{Case: syn, Elem: elem}],
	}
	if i := slices.Index(s.Cases, syn); i+1 < len(s.Cases) {
		cs.hi = fl.off(s.Cases[i+1].Case)
		cs.close = s.Cases[i+1].Case
	}
	if _, ok := reparseType(cs.spell.s); !ok {
		// The case type spells LoGOs-only syntax (an option suffix):
		// the respell sites emit this text into a plain recheck, so
		// spell the checked type in its lowered plain form instead.
		cs.spell = gen(fl.plainTypeSpell(typ))
	}
	if e.Tilde {
		cs.kind = cswTilde
	} else if _, ok := types2.Unalias(typ).Underlying().(*types2.Interface); ok {
		cs.kind = cswIface
	}
	return cs
}

// defaultCase builds the fold case for a default arm: no alias, no
// rebinding, just the arm body in place of the switch.
func (c *cswLower) defaultCase(syn *syntax.ConstSwitchCase) cswCase {
	fl, s := c.fl, c.stmt
	cs := cswCase{
		syn:   syn,
		elem:  -1,
		body:  syn.Body,
		kind:  cswExact,
		lo:    fl.off(syn.Colon) + 1,
		hi:    fl.off(s.Rbrace),
		close: s.Rbrace,
	}
	if i := slices.Index(s.Cases, syn); i+1 < len(s.Cases) {
		cs.hi = fl.off(s.Cases[i+1].Case)
		cs.close = s.Cases[i+1].Case
	}
	return cs
}

func (c *cswLower) elemSpell(e *syntax.ConstSwitchCaseElem) otext {
	// StartPos, not Pos: a qualified type names its selector position
	// at the dot, and the spell must keep the qualifier.
	return c.fl.textOf(span{c.fl.off(syntax.StartPos(e.Type)), c.fl.exprEndOf(e.Type)})
}

func checkedTypeOf(info *types2.Info, e syntax.Expr) types2.Type {
	if typ := info.TypeOf(e); typ != nil {
		return typ
	}
	if sel, ok := e.(*syntax.SelectorExpr); ok {
		if tn, ok := info.Uses[sel.Sel].(*types2.TypeName); ok {
			return tn.Type()
		}
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

func (c *cswLower) lowerFold(match *cswCase) []syntax.Stmt {
	stmts, text := c.caseBody(match, "")
	block := closeAt(c.stmt.Pos(), stmts, c.stmt.Rbrace)
	kept := span{match.lo, match.hi}
	c.replaceFolded(cat(leftoverComments(c.fl, span{c.start, c.end}, []span{kept}), gen("{"), text, gen("}")))
	return []syntax.Stmt{block}
}

func (c *cswLower) replaceFolded(text otext) {
	whole := span{c.start, c.end}
	if move := c.fl.moveAbove(whole); move != nil {
		move.inner, _ = partitionSplices(move.inner, whole)
	} else {
		c.fl.dropInner(whole)
	}
	c.fl.addT(c.start, c.end, text)
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
	// A switch with a multi-element case lowers through regions: each arm
	// is reparsed from the transformed body text and checked and lowered
	// on its own, so per-element spellings never share spans.
	for _, syn := range c.stmt.Cases {
		if len(syn.Elems) > 1 {
			return c.lowerRuntimeHost()
		}
	}
	return c.lowerRuntimeSync()
}

// cswHostArm is one lowered arm of a region-based switch: an if-chain
// link whose body lives in its own region until finalization. Head and
// condition texts are fixed at lowering; the body composes at finalize,
// after nested hosts finalize.
type cswHostArm struct {
	init      syntax.SimpleStmt
	initText  otext
	hasInit   bool
	cond      syntax.Expr
	condText  otext
	head      []syntax.Stmt
	headText  otext
	body      []syntax.Stmt
	region    *cfRegion
	pos       syntax.Pos
	close     syntax.Pos
	isDefault bool
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

func (c *cswLower) lowerRuntimeHost() []syntax.Stmt {
	var arms []cswHostArm
	for _, syn := range c.stmt.Cases {
		if syn.Default {
			arms = append(arms, c.lowerHostArm(syn, -1))
			continue
		}
		for i := range syn.Elems {
			if !c.info.LogosConstCases[types2.LogosConstCaseKey{Case: syn, Elem: i}].Live {
				continue
			}
			arms = append(arms, c.lowerHostArm(syn, i))
		}
	}
	// Nodes assemble now: an if-chain over the lowered arms. The default
	// arm, if present, is the final else: shadowing already rejects any
	// arm after it, so it is last.
	var root syntax.Stmt
	for i := len(arms) - 1; i >= 0; i-- {
		arm := arms[i]
		// Each arm block closes where the next arm opens. A block that
		// ran to the switch end (or the next case) would close after
		// the next arm opens, and scope marks must stay monotonic.
		close := arm.close
		if i+1 < len(arms) {
			close = arms[i+1].pos
		}
		if arm.isDefault {
			root = closeAt(arm.pos, append(arm.head, arm.body...), close)
			continue
		}
		root = at(&syntax.IfStmt{
			Init: arm.init,
			Cond: arm.cond,
			Then: closeAt(arm.pos, append(arm.head, arm.body...), close),
			Else: root,
		}, arm.pos)
	}
	if root == nil {
		root = closeAt(c.stmt.Pos(), nil, c.stmt.Rbrace)
	}
	// Text assembles at finalization, when every region (including nested
	// hosts inside the copies) is finished.
	host := &cfHost{compose: func(h *cfHost) otext { return c.composeHostSwitch(arms) }}
	for _, arm := range arms {
		host.regions = append(host.regions, arm.region)
	}
	c.fl.addHost(c.start, c.end, host)
	return []syntax.Stmt{root}
}

// composeHostSwitch assembles the eject text of a region-based switch:
// the if-chain skeleton with each arm's head text and composed region.
func (c *cswLower) composeHostSwitch(arms []cswHostArm) otext {
	var out otext
	for i, arm := range arms {
		if arm.isDefault {
			out = cat(out, gen(" else {"), arm.headText, c.fl.composeRegionNow(arm.region), gen("}"))
			continue
		}
		if i == 0 {
			out = gen("if ")
		} else {
			out = cat(out, gen(" else if "))
		}
		if arm.hasInit {
			out = cat(out, arm.initText, gen("; "))
		}
		out = cat(out, arm.condText, gen(" {"), arm.headText, c.fl.composeRegionNow(arm.region), gen("}"))
	}
	return out
}

// lowerHostArm lowers one arm of a region-based switch into its own
// region: the body text is reparsed from the transformed source, checked
// under the arm's bindings, adopted into a fresh region, and lowered
// there, so per-element spellings never share spans.
func (c *cswLower) lowerHostArm(syn *syntax.ConstSwitchCase, elem int) cswHostArm {
	fl := c.fl
	if syn.Default {
		bodyText := c.hostBodyText(syn)
		fnBody, fbase := reparseCswBody(bodyText.s, syn.Case)
		c.miniCheckCopy(fnBody, syn, elem, nil, false)
		region := fl.adoptRegion(bodyText, syn.Case, fbase, 3, []syntax.Node{fnBody})
		fnBody.List = c.fc.lowerConstIfList(fnBody.List)
		lowered := c.fc.lowerList(fnBody.List)
		return cswHostArm{body: lowered, region: region, pos: syn.Case, close: c.armClose(syn), isDefault: true}
	}
	e := &syn.Elems[elem]
	typ := checkedTypeOf(c.info, e.Type)
	var kind cswKind
	kind = cswExact
	if e.Tilde {
		kind = cswTilde
	} else if _, ok := types2.Unalias(typ).Underlying().(*types2.Interface); ok {
		kind = cswIface
	}
	bodyText := c.hostBodyText(syn)
	fnBody, fbase := reparseCswBody(bodyText.s, e.Type.Pos())
	c.miniCheckCopy(fnBody, syn, elem, typ, kind == cswIface)
	region := fl.adoptRegion(bodyText, syn.Case, fbase, 3, []syntax.Node{fnBody})
	fnBody.List = c.fc.lowerConstIfList(fnBody.List)
	cs := cswCase{syn: syn, elem: elem, body: fnBody.List, kind: kind}
	cs.spell = c.elemSpell(e)
	cs.sub = c.info.LogosConstCases[types2.LogosConstCaseKey{Case: syn, Elem: elem}]
	cs.lo, cs.hi = region.start, region.end
	cs.close = syn.Case
	head, headText := c.caseHead(&cs)
	lowered := c.fc.lowerList(fnBody.List)
	arm := cswHostArm{head: head, headText: headText, body: lowered, region: region, pos: syn.Case, close: c.armClose(syn)}
	if kind == cswExact {
		tag := c.spell(fl.off(c.stmt.Pos()), fl.off(c.stmt.Lbrace))
		tagExpr, ok := parseExprAt("any((*"+tag.s+")(nil))", syn.Case)
		if !ok {
			base.Fatalf("logos: const switch tag %s at %s cannot be respelled", tag.s, syn.Case.String())
		}
		ptr, ok := parseExprAt("*"+cs.spell.s, syn.Case)
		if !ok {
			base.Fatalf("logos: const switch case %s at %s cannot be respelled", cs.spell.s, syn.Case.String())
		}
		asrt := at(&syntax.AssertExpr{X: tagExpr, Type: ptr}, syn.Case)
		okName := c.fl.cswName("ok")
		arm.init = defineOf(syn.Case, []syntax.Expr{nameOf(syn.Case, "_"), nameOf(syn.Case, okName)}, []syntax.Expr{asrt})
		arm.initText = gen("_, " + okName + " := any((*" + tag.s + ")(nil)).(*" + cs.spell.s + ")")
		arm.hasInit = true
		arm.cond = nameOf(syn.Case, okName)
		arm.condText = gen(okName)
	} else {
		cond, condText := c.cond(&cs)
		arm.cond, arm.condText = cond, condText
	}
	return arm
}

// armEnd answers the source end offset of a case body: the next case's
// start, or the switch's closing brace.
func (c *cswLower) armEnd(syn *syntax.ConstSwitchCase) int {
	if i := slices.Index(c.stmt.Cases, syn); i+1 < len(c.stmt.Cases) {
		return c.fl.off(c.stmt.Cases[i+1].Case)
	}
	return c.fl.off(c.stmt.Rbrace)
}

func (c *cswLower) armClose(syn *syntax.ConstSwitchCase) syntax.Pos {
	if i := slices.Index(c.stmt.Cases, syn); i+1 < len(c.stmt.Cases) {
		return c.stmt.Cases[i+1].Case
	}
	return c.stmt.Rbrace
}

// hostBodyText returns the transformed source of one case body, memoized
// per case: textOf drains the span's splices, so reparsing every element
// from one call would starve the later copies.
func (c *cswLower) hostBodyText(syn *syntax.ConstSwitchCase) otext {
	if c.hostTexts == nil {
		c.hostTexts = make(map[*syntax.ConstSwitchCase]otext)
	}
	if text, ok := c.hostTexts[syn]; ok {
		return text
	}
	text := c.fl.textOf(span{c.fl.off(syn.Colon) + 1, c.armEnd(syn)})
	c.hostTexts[syn] = text
	return text
}

// miniCheckCopy checks one freshly parsed arm body under that arm's
// bindings and records the facts for lowering it. Diagnostics stay with
// the declaration check, which already reported them on the shared body.
func (c *cswLower) miniCheckCopy(fnBody *syntax.BlockStmt, syn *syntax.ConstSwitchCase, elem int, typ types2.Type, isIface bool) {
	fl, info := c.fl, c.info
	scopeRec, ok := info.LogosConstSwitchScope[c.stmt]
	if !ok || scopeRec.Scope == nil || scopeRec.Sig == nil {
		base.Fatalf("logos: const switch at %s has no recorded scope for region lowering", c.stmt.Pos().String())
	}
	ctx := types2.LogosCopyContext{
		Scope:     scopeRec.Scope,
		Sig:       scopeRec.Sig,
		Version:   scopeRec.Version,
		Disqual:   info.LogosDisqual[c.fc.body],
		Aliases:   fl.cfAliases(),
		MaxSteps:  info.LogosCTFEMaxSteps,
		NodeLimit: base.LogosComptimeNodes,
		Importer:  info.LogosImporter,
		Context:   info.LogosContext,
		Bind:      fl.cfRuntimeBindings(),
	}
	key := types2.LogosConstCaseKey{Case: syn, Elem: elem}
	shadow := types2.LogosConstSwitchShadow{
		Rec:     info.LogosConstCases[key],
		Tag:     scopeRec.Tag,
		Elem:    key,
		Typ:     typ,
		IsIface: isIface,
	}
	types2.LogosCheckSwitchElem(fl.pkg.tpkg, info, ctx, shadow, []*syntax.BlockStmt{fnBody})
}

// reparseCswBody parses one arm body for isolated lowering. The text
// already carries every file-pass transform; the fresh base keeps the
// copy's nodes distinct from the shared originals.
func reparseCswBody(text string, pos syntax.Pos) (*syntax.BlockStmt, *syntax.PosBase) {
	fbase := syntax.NewFileBase("csw")
	var errs []error
	f, _ := syntax.Parse(fbase, strings.NewReader("package cswparse\nfunc cswparse() {\n"+text+"\n}\n"), func(err error) {
		errs = append(errs, err)
	}, nil, syntax.LogosMode)
	if len(errs) > 0 || f == nil {
		base.Fatalf("logos: cannot reparse const switch body at %s: %v", pos.String(), errs)
	}
	for _, decl := range f.DeclList {
		if fn, ok := decl.(*syntax.FuncDecl); ok && fn.Body != nil {
			return fn.Body, fbase
		}
	}
	base.Fatalf("logos: cannot reparse const switch body at %s", pos.String())
	return nil, nil
}

// composeRegionNow composes one adopted region's finished text: the
// region bytes with the region's edits applied, plus any nested host
// text whose splice landed inside the region. Consumed nested host
// splices are dropped so the final composition does not repeat them.
func (fl *fileLower) composeRegionNow(r *cfRegion) otext {
	inner := make([]splice, 0, len(r.splices)+4)
	inner = append(inner, r.splices...)
	// Fast path: regions without nested hosts need no file-wide copy.
	// Partitioning fl.splices here copies the whole edit list for every
	// unrolled const-for iteration, so skip it unless this region
	// actually contains a nested host.
	hasNested := false
	for _, sp := range fl.splices {
		if sp.host != nil && sp.start >= r.start && sp.end <= r.end {
			hasNested = true
			break
		}
	}
	if hasNested {
		var nested, kept []splice
		for _, sp := range fl.splices {
			if sp.host != nil && sp.start >= r.start && sp.end <= r.end {
				nested = append(nested, sp)
			} else {
				kept = append(kept, sp)
			}
		}
		// Remove these hosts before composing them: recursive composition may
		// consume their descendants from the remaining global splice list.
		fl.splices = kept
		for _, sp := range nested {
			text := fl.retireHost(sp.host)
			inner = append(inner, splice{start: sp.start, end: sp.end, text: text.s, chunks: text.chunks})
		}
	}
	adj := make([]splice, len(inner))
	for i, sp := range inner {
		adj[i] = sp
		adj[i].start -= r.start
		adj[i].end -= r.start
	}
	out, err := applyChunks(fl.srcMap.sliceO(r.start, r.end), adj)
	if err != nil {
		base.Fatalf("logos: %v", err)
	}
	return out
}
func (c *cswLower) lowerRuntimeSync() []syntax.Stmt {
	c.cases = make([]cswCase, 0, len(c.stmt.Cases))
	for _, syn := range c.stmt.Cases {
		if syn.Default {
			continue
		}
		c.cases = append(c.cases, c.caseAtElem(syn, 0, syn.Body))
	}
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
		c.lowerDefault("")
		stmt, text := c.typeSwitch(exacts, rest, "")
		c.fl.addT(c.start, c.end, text)
		return []syntax.Stmt{stmt}
	}
	pos := c.stmt.Pos()
	done := fmt.Sprintf("_logos_csw%d", c.start)
	c.lowerDefault(done)
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
	if c.hasDef {
		guard := at(&syntax.Operation{Op: syntax.Not, X: nameOf(c.defPos, done)}, c.defPos)
		block.List = append(block.List, at(&syntax.IfStmt{
			Cond: guard,
			Then: closeAt(c.defPos, c.defStmts, c.defClose),
		}, c.defPos))
		text = cat(text, gen("if !"+done+" {"), c.defText, gen("}\n"))
	}
	text = cat(text, gen("}"))
	c.fl.addT(c.start, c.end, text)
	return []syntax.Stmt{block}
}

// lowerDefault lowers the switch's default arm once, if present. The arm
// carries no alias and no rebinding: it is the body as written, plus the
// done assignment when the switch dispatches under a done guard.
func (c *cswLower) lowerDefault(done string) {
	c.hasDef = false
	defSyn := defaultArm(c.stmt)
	if defSyn == nil {
		return
	}
	dc := c.defaultCase(defSyn)
	c.defStmts, c.defText = c.caseBody(&dc, done)
	c.hasDef = true
	c.defPos = defSyn.Case
	c.defClose = dc.close
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
		if c.hasDef && done == "" {
			chain, chainText = c.attachDefault(chain, chainText)
		}
		sw.Body = append(sw.Body, at(&syntax.CaseClause{Body: []syntax.Stmt{chain}}, rest[0].syn.Case))
		text = cat(text, gen("\ndefault:\n"), chainText)
	} else if c.hasDef && done == "" {
		sw.Body = append(sw.Body, at(&syntax.CaseClause{Body: c.defStmts}, c.defPos))
		text = cat(text, gen("\ndefault:\n"), c.defText)
	}
	text = cat(text, gen("\n}"))
	return sw, text
}

// attachDefault sets the final else of a chainIf chain to the switch's
// default arm. The chain nests IfStmt nodes only, so the walk always lands
// on the innermost link.
func (c *cswLower) attachDefault(chain syntax.Stmt, chainText otext) (syntax.Stmt, otext) {
	last, ok := chain.(*syntax.IfStmt)
	if !ok {
		base.Fatalf("logos: const switch chain at %s is not an if chain", chain.Pos().String())
	}
	for last.Else != nil {
		inner, ok := last.Else.(*syntax.IfStmt)
		if !ok {
			base.Fatalf("logos: const switch chain at %s is not an if chain", chain.Pos().String())
		}
		last = inner
	}
	last.Else = closeAt(c.defPos, c.defStmts, c.defClose)
	return chain, cat(chainText, gen(" else {"), c.defText, gen("}"))
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
func (c *cswLower) caseBody(cs *cswCase, done string) ([]syntax.Stmt, otext) {
	head, headText := c.caseHead(cs)
	if done != "" {
		head = append(head, at(&syntax.AssignStmt{Lhs: nameOf(cs.syn.Case, done), Rhs: nameOf(cs.syn.Case, "true")}, cs.syn.Case))
		headText = cat(headText, gen(done+" = true\n"))
	}
	lowered, bodyText := c.caseTail(cs)
	return append(head, lowered...), cat(headText, bodyText)
}

func (c *cswLower) caseHead(cs *cswCase) ([]syntax.Stmt, otext) {
	var head []syntax.Stmt
	var headText otext
	if cs.kind == cswIface || cs.sub.Target != nil {
		// A type alias spelled at the top of the case body names the case
		// type: every respelled name resolves to it even inside blocks
		// that redeclare the case type's own name.
		cs.alias = c.fl.cswName("caseT")
		typ, ok := parseExprAt(cs.spell.s, cs.syn.Case)
		if !ok {
			base.Fatalf("logos: const switch case %s at %s cannot be respelled", cs.spell.s, cs.syn.Case.String())
		}
		head = append(head, at(&syntax.DeclStmt{DeclList: []syntax.Decl{
			at(&syntax.TypeDecl{Name: nameOf(cs.syn.Case, cs.alias), Alias: true, Type: typ}, cs.syn.Case),
		}}, cs.syn.Case))
		headText = gen("type " + cs.alias + " = " + cs.spell.s + "\n")
	}
	if cs.kind == cswIface {
		selWraps := c.wrapSelectors(cs.body, cs)
		decls, dtext := c.wrapConvs(cs.body, cs, selWraps)
		head = append(head, decls...)
		headText = cat(headText, dtext)
	} else if cs.sub.Target != nil {
		decls, dtext := c.rewriteRebound(cs)
		head = append(head, decls...)
		headText = cat(headText, dtext)
	}
	return head, headText
}

func (c *cswLower) caseTail(cs *cswCase) ([]syntax.Stmt, otext) {
	lowered := c.fc.lowerList(cs.body)
	return lowered, c.fl.lazyText(span{cs.lo, cs.hi})
}

// rewriteRebound respells the switched type's name to the case type and
// rewrites every use of a variable bound to the switched type as a
// dereference of a type-correct pointer into the variable's own storage,
// taken at the use site so the address is always the variable visible there:
// reads see the current value and writes land directly, on every exit path.
func (c *cswLower) rewriteRebound(cs *cswCase) ([]syntax.Stmt, otext) {
	var decls []syntax.Stmt
	var dtext otext
	targets := make(map[types2.Object]cswTarget)
	spell := gen(cs.alias) // the case-body alias declared by caseBody
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
	// aliasFor emits a case-body type alias for a substituted spell that
	// differs from the case type: the spell may name a shadowable identifier.
	varAlias := map[string]string{cs.spell.s: cs.alias}
	aliasFor := func(spellText string) string {
		if a, ok := varAlias[spellText]; ok {
			return a
		}
		a := c.fl.cswName("caseV")
		varAlias[spellText] = a
		typ, ok := parseExprAt(spellText, cs.syn.Case)
		if !ok {
			base.Fatalf("logos: const switch rebind type %s at %s cannot be respelled", spellText, cs.syn.Case.String())
		}
		decls = append(decls, at(&syntax.DeclStmt{DeclList: []syntax.Decl{
			at(&syntax.TypeDecl{Name: nameOf(cs.syn.Case, a), Alias: true, Type: typ}, cs.syn.Case),
		}}, cs.syn.Case))
		dtext = cat(dtext, gen("type "+a+" = "+spellText+"\n"))
		return a
	}
	// len/cap of a rebound array variable is a constant expression; a
	// pointer into its storage is not. Rewrite such arguments to a nil
	// pointer dereference, which len/cap accept without evaluation. The
	// dereference spells the variable's own substituted type: it may carry
	// a different length than the case type.
	for _, s := range cs.body {
		syntax.Inspect(s, func(n syntax.Node) bool {
			call, ok := n.(*syntax.CallExpr)
			if !ok {
				return true
			}
			fn, ok := call.Fun.(*syntax.Name)
			if !ok || (fn.Value != "len" && fn.Value != "cap") {
				return true
			}
			if _, ok := c.info.Uses[fn].(*types2.Builtin); !ok {
				return true
			}
			if len(call.ArgList) != 1 {
				return true
			}
			arg, ok := call.ArgList[0].(*syntax.Name)
			if !ok {
				return true
			}
			v, ok := c.info.Uses[arg].(*types2.Var)
			if !ok {
				return true
			}
			sub, ok := cs.sub.Vars[v]
			if !ok {
				return true
			}
			if _, ok := types2.Unalias(sub).Underlying().(*types2.Array); !ok {
				return true
			}
			text := "(*(*" + aliasFor(c.cswSpell(sub)) + ")(nil))"
			e, ok := parseExprAt(text, arg.Pos())
			if !ok {
				base.Fatalf("logos: const switch len/cap argument at %s cannot be respelled", arg.Pos().String())
			}
			call.ArgList[0] = e
			c.fl.addT(c.fl.off(arg.Pos()), c.fl.off(arg.Pos())+len(arg.Value), gen(text))
			return true
		})
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
		// A derived substitution (*F -> *int) gets its own alias:
		// the name may spell a shadowable identifier.
		spell := aliasFor(r.spell)
		replText := "(*(" + rtAlias + "." + pun + "[" + spell + "](&" + r.name + ")))"
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
	c.fixReboundDef(cs, targets)
	w := cswWalker{info: c.info, off: c.fl.off, targets: targets}
	w.list(cs.body)
	return decls, dtext
}

// fixReboundDef rewrites `x, y := ...` statements whose left side mixes new
// names with names that reuse a rebound variable. types2 records such a name
// as a use of the existing variable, so the rebind pass would respell it into
// a dereference — which := cannot carry. Each such statement becomes an
// ordinary assignment (a dereference is a legal = operand) and the genuinely
// new names are declared by var statements hoisted before the statement that
// heads the enclosing clause (the if/for/switch/select holding the :=).
func (c *cswLower) fixReboundDef(cs *cswCase, targets map[types2.Object]cswTarget) {
	parents := map[syntax.Node]*[]syntax.Stmt{}
	owner := map[syntax.Node]syntax.Node{}
	for i := range cs.body {
		parents[cs.body[i]] = &cs.body
	}
	var assigns []*syntax.AssignStmt
	var ranges []*syntax.RangeClause
	for _, s := range cs.body {
		syntax.Inspect(s, func(n syntax.Node) bool {
			switch t := n.(type) {
			case *syntax.BlockStmt:
				for i := range t.List {
					parents[t.List[i]] = &t.List
				}
			case *syntax.CaseClause:
				for i := range t.Body {
					parents[t.Body[i]] = &t.Body
				}
			case *syntax.CommClause:
				for i := range t.Body {
					parents[t.Body[i]] = &t.Body
				}
				if t.Comm != nil {
					owner[t.Comm] = t
				}
			case *syntax.IfStmt:
				if t.Init != nil {
					owner[t.Init] = t
				}
				if t.Then != nil {
					owner[t.Then] = t
				}
				if t.Else != nil {
					owner[t.Else] = t
				}
			case *syntax.ForStmt:
				if t.Init != nil {
					owner[t.Init] = t
				}
				if t.Post != nil {
					owner[t.Post] = t
				}
				if t.Body != nil {
					owner[t.Body] = t
				}
			case *syntax.SwitchStmt:
				if t.Init != nil {
					owner[t.Init] = t
				}
				for _, cc := range t.Body {
					owner[cc] = t
				}
			case *syntax.SelectStmt:
				for _, cc := range t.Body {
					owner[cc] = t
				}
			case *syntax.LabeledStmt:
				if t.Stmt != nil {
					owner[t.Stmt] = t
				}
			case *syntax.FuncLit:
				if t.Body != nil {
					owner[t.Body] = t
				}
			case *syntax.AssignStmt:
				if t.Op == syntax.Def && c.reboundLhs(t.Lhs, cs) {
					assigns = append(assigns, t)
				}
			case *syntax.RangeClause:
				if t.Def && t.Lhs != nil && c.reboundLhs(t.Lhs, cs) {
					ranges = append(ranges, t)
				}
			}
			return true
		})
	}
	if len(assigns) == 0 && len(ranges) == 0 {
		return
	}

	hoist := func(head syntax.Node, n *syntax.Name) {
		for owner[head] != nil {
			head = owner[head]
		}
		pl, ok := parents[head]
		if !ok {
			return
		}
		nv, ok := c.info.Defs[n].(*types2.Var)
		if !ok {
			return
		}
		// The declaration always hoists under a fresh temp: a same-named var
		// placed before the statement would shadow an outer variable that the
		// initializer itself may read.
		orig := n.Value
		name := c.fl.cswName("rv")
		pos := n.Pos()
		text := gen(name)
		targets[nv] = cswTarget{
			repl: func(atp syntax.Pos) syntax.Expr {
				e, ok := parseExprAt(text.s, atp)
				if !ok {
					base.Fatalf("logos: const switch temp name at %s cannot be respelled", atp.String())
				}
				return e
			},
			splice: func(start, end int) { c.fl.addT(start, end, text) },
		}
		n.Value = name
		c.fl.addT(c.fl.off(pos), c.fl.off(pos)+len(orig), text)
		spell := c.cswSpell(nv.Type())
		typ, ok := typeExprAt(spell, n.Pos())
		if !ok {
			base.Fatalf("logos: const switch hoisted type %s at %s cannot be respelled", spell, n.Pos().String())
		}
		decl := at(&syntax.DeclStmt{DeclList: []syntax.Decl{
			at(&syntax.VarDecl{NameList: []*syntax.Name{nameOf(n.Pos(), name)}, Type: typ}, n.Pos()),
		}}, n.Pos())
		idx := -1
		for i, s := range *pl {
			if s == head {
				idx = i
				break
			}
		}
		if idx < 0 {
			return
		}
		*pl = append(*pl, nil)
		copy((*pl)[idx+1:], (*pl)[idx:])
		(*pl)[idx] = decl
		headStart := c.fl.off(syntax.StartPos(head))
		c.fl.addT(headStart, headStart, gen("var "+name+" "+spell+"\n"))
	}

	defToAssign := func(lo, hi int) {
		i := strings.IndexByte(string(c.fl.src[lo:hi]), ':')
		if i >= 0 {
			c.fl.addT(lo+i, lo+i+1, gen(""))
		}
	}
	for _, as := range assigns {
		// Take the left side's end and the initializer's start before hoist
		// renames any declared name: EndPos follows the mutated Value length.
		lo := c.fl.off(syntax.EndPos(as.Lhs))
		hi := c.fl.off(as.Rhs.Pos())
		for _, n := range lhsNames(as.Lhs) {
			if c.info.Defs[n] != nil {
				hoist(as, n)
			}
		}
		as.Op = 0
		defToAssign(lo, hi)
	}
	for _, rc := range ranges {
		lo := c.fl.off(syntax.EndPos(rc.Lhs))
		hi := c.fl.off(rc.X.Pos())
		for _, n := range lhsNames(rc.Lhs) {
			if c.info.Defs[n] != nil {
				hoist(rc, n)
			}
		}
		rc.Def = false
		defToAssign(lo, hi)
	}
}

// reboundLhs reports whether a := left side reuses at least one variable the
// case rebinds: the checker put it in Uses, not Defs.
func (c *cswLower) reboundLhs(lhs syntax.Expr, cs *cswCase) bool {
	for _, n := range lhsNames(lhs) {
		if c.info.Defs[n] != nil {
			continue
		}
		if v, ok := c.info.Uses[n].(*types2.Var); ok {
			if _, ok := cs.sub.Vars[v]; ok {
				return true
			}
		}
	}
	return false
}

func lhsNames(e syntax.Expr) []*syntax.Name {
	if l, ok := e.(*syntax.ListExpr); ok {
		var out []*syntax.Name
		for _, x := range l.ElemList {
			if n, ok := x.(*syntax.Name); ok {
				out = append(out, n)
			}
		}
		return out
	}
	if n, ok := e.(*syntax.Name); ok {
		return []*syntax.Name{n}
	}
	return nil
}

func (c *cswLower) rebound(cs *cswCase) []cswRebind {
	var out []cswRebind
	seen := make(map[*types2.Var]bool)
	for _, s := range cs.body {
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

// cswSpell renders typ as the plain Go text the lowered file spells: an
// option becomes the element type itself when the option is bare and
// logosrt.Option[E] otherwise, and composite types spell their parts the
// same way. Every respell site parses this text as plain Go, so a LoGOs
// spelling here would fail the recheck.
func (c *cswLower) cswSpell(typ types2.Type) string {
	return c.fl.plainTypeSpell(typ)
}

func (fl *fileLower) plainTypeSpell(typ types2.Type) string {
	if a, ok := typ.(*types2.Alias); ok && a.Obj() != nil {
		if q := fl.typeQualifier()(a.Obj().Pkg()); q != "" {
			var b strings.Builder
			b.WriteString(q)
			b.WriteString(".")
			b.WriteString(a.Obj().Name())
			if ta := a.TypeArgs(); ta != nil {
				b.WriteString("[")
				for i := range ta.Len() {
					if i > 0 {
						b.WriteString(", ")
					}
					b.WriteString(fl.plainTypeSpell(ta.At(i)))
				}
				b.WriteString("]")
			}
			return b.String()
		}
		return fl.plainTypeSpell(types2.Unalias(typ))
	}
	switch t := unaliasConstView(typ).(type) {
	case *types2.Option:
		if types2.LogosOptionIsBare(t.Elem) {
			return fl.plainTypeSpell(t.Elem)
		}
		fl.needRT = true
		return rtAlias + ".Option[" + fl.plainTypeSpell(t.Elem) + "]"
	case *types2.Pointer:
		return "*" + fl.plainTypeSpell(t.Elem())
	case *types2.Slice:
		return "[]" + fl.plainTypeSpell(t.Elem())
	case *types2.Array:
		return "[" + strconv.FormatInt(t.Len(), 10) + "]" + fl.plainTypeSpell(t.Elem())
	case *types2.Map:
		return "map[" + fl.plainTypeSpell(t.Key()) + "]" + fl.plainTypeSpell(t.Elem())
	case *types2.Chan:
		switch t.Dir() {
		case types2.RecvOnly:
			return "<-chan " + fl.plainTypeSpell(t.Elem())
		case types2.SendOnly:
			return "chan<- " + fl.plainTypeSpell(t.Elem())
		}
		if c, ok := types2.Unalias(t.Elem()).(*types2.Chan); ok && c.Dir() == types2.RecvOnly {
			return "chan (" + fl.plainTypeSpell(t.Elem()) + ")"
		}
		return "chan " + fl.plainTypeSpell(t.Elem())
	case *types2.Named:
		ta := t.TypeArgs()
		if ta == nil {
			return fl.typeSpell(t)
		}
		var b strings.Builder
		if q := fl.typeQualifier()(t.Obj().Pkg()); q != "" {
			b.WriteString(q)
			b.WriteByte('.')
		}
		b.WriteString(t.Obj().Name())
		b.WriteByte('[')
		for i := range ta.Len() {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(fl.plainTypeSpell(ta.At(i)))
		}
		b.WriteByte(']')
		return b.String()
	case *types2.Signature:
		var b strings.Builder
		b.WriteString("func(")
		b.WriteString(fl.paramsText(t.Params(), t.Variadic()))
		b.WriteByte(')')
		if res := t.Results(); res != nil && res.Len() > 0 {
			if res.Len() == 1 && res.At(0).Name() == "" {
				b.WriteByte(' ')
				b.WriteString(fl.plainTypeSpell(res.At(0).Type()))
			} else {
				b.WriteString(" (")
				b.WriteString(fl.paramsText(res, false))
				b.WriteByte(')')
			}
		}
		return b.String()
	}
	return fl.typeSpell(typ)
}

func (fl *fileLower) paramsText(ps *types2.Tuple, variadic bool) string {
	var b strings.Builder
	for i := range ps.Len() {
		if i > 0 {
			b.WriteString(", ")
		}
		t := ps.At(i).Type()
		if variadic && i == ps.Len()-1 {
			if s, ok := t.(*types2.Slice); ok {
				b.WriteString("...")
				b.WriteString(fl.plainTypeSpell(s.Elem()))
				continue
			}
			b.WriteString(fl.plainTypeSpell(t))
			b.WriteString("...")
			continue
		}
		b.WriteString(fl.plainTypeSpell(t))
	}
	return b.String()
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
	if w.info.Defs[x] != nil {
		return // a declaration site (e.g. `v, n := f()`) declares a new name
	}
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
func (c *cswLower) wrapConvs(body []syntax.Stmt, cs *cswCase, selWraps []selWrap) ([]syntax.Stmt, otext) {
	var decls []syntax.Stmt
	var dtext otext
	type rec struct {
		e          syntax.Expr
		start, end int
	}
	var recs []rec // Inspect is a preorder walk, so recs arrive in source order.
	// A recorded operand in a receive clause cannot be boxed in place — a
	// comm clause admits only a receive, never a call — so those operands
	// are respelled by the clause rewrite at the end instead. A send's
	// value is an ordinary operand and boxes in place.
	clauseOf := make(map[syntax.Expr]*syntax.CommClause)
	for _, s := range body {
		syntax.Inspect(s, func(n syntax.Node) bool {
			if cc, ok := n.(*syntax.CommClause); ok && cc.Comm != nil {
				pinned := []syntax.Node{cc.Comm}
				if send, ok := cc.Comm.(*syntax.SendStmt); ok {
					pinned = []syntax.Node{send.Chan}
				}
				for _, root := range pinned {
					syntax.Inspect(root, func(m syntax.Node) bool {
						if e, ok := m.(syntax.Expr); ok {
							clauseOf[e] = cc
						}
						return true
					})
				}
			}
			e, ok := n.(syntax.Expr)
			if !ok || c.info.LogosConstConv[e].Case != cs.syn {
				return true
			}
			start := c.fl.off(leftmostOf(e))
			recs = append(recs, rec{e: e, start: start, end: c.fl.exprEndNested(start)})
			return true
		})
	}
	if len(recs) == 0 {
		return decls, dtext
	}
	// clauses lists each comm clause holding a recorded operand, in
	// record order; the alias pre-mint and the render loop share it.
	var clauses []*syntax.CommClause
	clauseSeen := make(map[*syntax.CommClause]bool)
	for _, r := range recs {
		if cc := clauseOf[r.e]; cc != nil && !clauseSeen[cc] {
			clauseSeen[cc] = true
			clauses = append(clauses, cc)
		}
	}
	c.fl.needRT = true
	recOf := make(map[syntax.Expr]rec, len(recs))
	for _, r := range recs {
		recOf[r.e] = r
	}
	// localDecls maps the name position of each type declared inside the
	// case body to the offset just past its declaration and to the list
	// holding it. A spell that resolves to such a type can't go in a
	// case-top alias — the alias would precede the declaration — and a
	// bare spell at the use site could re-resolve to a nested block's
	// shadow of the name. Its alias is instead spliced right after the
	// declaration it spells: after the name is in scope, before every
	// use that the declaration permits, and past any shadow of either
	// name.
	type localTypeDecl struct {
		end  int
		list *[]syntax.Stmt
		stmt *syntax.DeclStmt
	}
	localDecls := map[int]localTypeDecl{}
	var scanTypeDecls func(list *[]syntax.Stmt)
	scanTypeDecls = func(list *[]syntax.Stmt) {
		for _, s := range *list {
			if d, ok := s.(*syntax.DeclStmt); ok {
				// stmtEnd scans past balanced brackets to the declaration's
				// line end: EndPos stops at a grouped declaration's closing
				// token or a generic alias's last type argument on some
				// shapes, and exprEndNested stops at an alias `=` — the later
				// offset is the one past the whole declaration.
				end := max(stmtEnd(c.fl.src, c.fl.off(syntax.StartPos(d))), c.fl.off(syntax.EndPos(d)))
				for _, decl := range d.DeclList {
					if td, ok := decl.(*syntax.TypeDecl); ok && td.Name != nil {
						localDecls[c.fl.off(td.Name.Pos())] = localTypeDecl{end, list, d}
					}
				}
			}
			syntax.Inspect(s, func(n syntax.Node) bool {
				switch x := n.(type) {
				case *syntax.BlockStmt:
					scanTypeDecls(&x.List)
				case *syntax.CaseClause:
					scanTypeDecls(&x.Body)
				case *syntax.CommClause:
					scanTypeDecls(&x.Body)
				default:
					return true
				}
				return false
			})
		}
	}
	scanTypeDecls(&cs.body)
	// localTypeOf reports the last case-body declaration that any object
	// inside t names, by object identity — a same-named declaration of
	// another scope never matches, so a package type shadowed inside the
	// case body still gets the ordinary case-top alias.
	localTypeOf := func(t types2.Type) (localTypeDecl, bool) {
		var out localTypeDecl
		found := false
		seen := map[types2.Type]bool{}
		var walk func(t types2.Type)
		walk = func(t types2.Type) {
			if t == nil || seen[t] {
				return
			}
			seen[t] = true
			var obj *types2.TypeName
			switch x := t.(type) {
			case *types2.Alias:
				obj = x.Obj()
				if args := x.TypeArgs(); args != nil {
					for i := 0; i < args.Len(); i++ {
						walk(args.At(i))
					}
				}
			case *types2.Named:
				obj = x.Obj()
				if args := x.TypeArgs(); args != nil {
					for i := 0; i < args.Len(); i++ {
						walk(args.At(i))
					}
				}
			case *types2.Pointer:
				walk(x.Elem())
			case *types2.Slice:
				walk(x.Elem())
			case *types2.Array:
				walk(x.Elem())
			case *types2.Map:
				walk(x.Key())
				walk(x.Elem())
			case *types2.Chan:
				walk(x.Elem())
			case *types2.Struct:
				for i := 0; i < x.NumFields(); i++ {
					walk(x.Field(i).Type())
				}
			case *types2.Tuple:
				for i := 0; i < x.Len(); i++ {
					walk(x.At(i).Type())
				}
			case *types2.Signature:
				walk(x.Params())
				walk(x.Results())
			case *types2.Interface:
				for i := 0; i < x.NumEmbeddeds(); i++ {
					walk(x.EmbeddedType(i))
				}
				for i := 0; i < x.NumMethods(); i++ {
					walk(x.Method(i).Type())
				}
			}
			if obj != nil && obj.Pos().IsKnown() &&
				obj.Pos().FileBase() == c.fl.file.Pos().FileBase() {
				if lt, ok := localDecls[c.fl.off(obj.Pos())]; ok && (!found || lt.end > out.end) {
					out, found = lt, true
				}
			}
		}
		walk(t)
		return out, found
	}
	// spellFor names each destination type through a case-body alias: a
	// declared or inferred type may spell an identifier a nested block
	// shadows. A spell naming a type declared inside the case body gets
	// its alias right after that declaration instead of at the case top.
	// The alias text is spliced at mint time: a use inside a boxed
	// operand's span is composed into that operand's replacement by
	// the next textOf, so the splice must exist before composition
	// begins. Only the declaration node waits for the body walk:
	// inserting it into a statement list mid-walk shifts the slice
	// the walk iterates.
	type pinnedAlias struct {
		d    *syntax.DeclStmt
		list *[]syntax.Stmt
		stmt *syntax.DeclStmt
	}
	var pinned []pinnedAlias
	// secAlias dedupes by type, not spell: two types can spell the same
	// name when a nested declaration shadows a package name.
	secAlias := map[types2.Type]string{}
	spellFor := func(t types2.Type) string {
		s := c.cswSpell(t)
		if a, ok := secAlias[t]; ok {
			return a
		}
		a := c.fl.cswName("caseV")
		secAlias[t] = a
		typ, ok := parseExprAt(s, cs.syn.Case)
		if !ok {
			base.Fatalf("logos: const switch conv type %s at %s cannot be respelled", s, cs.syn.Case.String())
		}
		d := at(&syntax.DeclStmt{DeclList: []syntax.Decl{
			at(&syntax.TypeDecl{Name: nameOf(cs.syn.Case, a), Alias: true, Type: typ}, cs.syn.Case),
		}}, cs.syn.Case)
		if lt, ok := localTypeOf(t); ok {
			c.fl.addT(lt.end, lt.end, gen("\ntype "+a+" = "+s))
			pinned = append(pinned, pinnedAlias{d, lt.list, lt.stmt})
			return a
		}
		decls = append(decls, d)
		dtext = cat(dtext, gen("type "+a+" = "+s+"\n"))
		return a
	}
	box := func(text string) string {
		return rtAlias + ".Box[" + cs.alias + "](" + text + ")"
	}
	secondOf := func(e syntax.Expr) (types2.Type, bool) {
		switch x := syntax.Unparen(e).(type) {
		case *syntax.IndexExpr, *syntax.AssertExpr:
			return types2.Typ[types2.Bool], true
		case *syntax.Operation:
			if x.Op == syntax.Recv {
				return types2.Typ[types2.Bool], true
			}
		}
		if tup, ok := c.info.TypeOf(e).(*types2.Tuple); ok && tup.Len() == 2 {
			return tup.At(1).Type(), true
		}
		return nil, false
	}
	// `return e` is multi-result only when the innermost enclosing
	// function's signature takes two results: a literal inside a
	// two-result declaration has its own arity, and vice versa.
	type fnSpan struct {
		start, end int
		ft         *syntax.FuncType
	}
	var spans []fnSpan
	for _, s := range body {
		syntax.Inspect(s, func(n syntax.Node) bool {
			if lit, ok := n.(*syntax.FuncLit); ok {
				spans = append(spans, fnSpan{c.fl.off(lit.Pos()), c.fl.off(syntax.EndPos(lit)), lit.Type})
			}
			return true
		})
	}
	enclosing := func(pos int) *syntax.FuncType {
		best := c.fc.ft
		bestSize := int(^uint(0) >> 1)
		for _, s := range spans {
			if s.start <= pos && pos < s.end && s.end-s.start < bestSize {
				best, bestSize = s.ft, s.end-s.start
			}
		}
		return best
	}
	// A recorded operand in multi-result position — the sole right side of
	// a two-result assignment, declaration, or return — keeps its arity
	// through a two-result closure that boxes the first value and converts
	// the second to the left side's declared type: `p, ok = m[k]` becomes
	// `p, ok = func() (I, bool) { v, ok := m[k]; return Box[I](v), ok }()`.
	// The second type stays a types2.Type until render actually needs it:
	// an inferred type that spells a package the file does not import
	// aborts cswSpell, and assignments needing no rewrite must never ask.
	multi := make(map[syntax.Expr]types2.Type)
	for _, s := range body {
		syntax.Inspect(s, func(n syntax.Node) bool {
			var rhs syntax.Expr
			var secondT types2.Type
			two := false
			switch st := n.(type) {
			case *syntax.AssignStmt:
				rhs = st.Rhs
				if lhs, ok := st.Lhs.(*syntax.ListExpr); ok && len(lhs.ElemList) == 2 {
					if t := c.info.TypeOf(lhs.ElemList[1]); t != nil {
						two = true
						secondT = t
					}
				}
			case *syntax.VarDecl:
				rhs = st.Values
				if len(st.NameList) == 2 {
					if t := c.info.TypeOf(st.NameList[1]); t != nil {
						two = true
						secondT = t
					}
				}
			case *syntax.ReturnStmt:
				rhs = st.Results
				if ft := enclosing(c.fl.off(st.Pos())); ft != nil && len(ft.ResultList) == 2 {
					if t := c.info.TypeOf(ft.ResultList[1].Type); t != nil {
						two = true
						secondT = t
					}
				}
			}
			if two && rhs != nil && c.info.LogosConstConv[rhs].Case == cs.syn {
				if secondT == nil {
					secondT, two = secondOf(rhs)
				}
				if !two {
					base.Fatalf("logos: const switch boxing of a multi-result at %s cannot name its second type", rhs.Pos().String())
				}
				multi[rhs] = secondT
			}
			return true
		})
	}
	// Mint every alias the rewrites below will spell before the first
	// expand: a pinned alias whose declaration sits inside a boxed
	// operand's span must already be a pending splice so textOf drains
	// it into the composed replacement. Minted later, it would survive
	// as a sibling edit the covering replacement overlaps.
	for _, r := range recs {
		if clauseOf[r.e] != nil {
			continue
		}
		if secondT, ok := multi[r.e]; ok {
			spellFor(secondT)
		}
	}
	for _, cc := range clauses {
		as, ok := cc.Comm.(*syntax.AssignStmt)
		if !ok || as.Op != 0 {
			continue
		}
		var elems []syntax.Expr
		if le, ok := as.Lhs.(*syntax.ListExpr); ok {
			elems = le.ElemList
		} else if as.Lhs != nil {
			elems = []syntax.Expr{as.Lhs}
		}
		for _, l := range elems[1:] {
			if t := c.info.TypeOf(l); t != nil {
				spellFor(t)
			}
		}
	}
	// expand renders the span's source text with every recorded expression
	// inside it boxed; nested recordings compose inside their outer boxes.
	var expand func(start, end int) string
	// render emits the operand's source text boxed, or wrapped in a
	// two-result closure when the operand keeps a comma-ok arity.
	render := func(r rec) string {
		if secondT, ok := multi[r.e]; ok {
			second := spellFor(secondT)
			return "func() (" + cs.alias + ", " + second + ") { _logos_v, _logos_s := " + expand(r.start, r.end) + "; return " + box("_logos_v") + ", " + second + "(_logos_s) }()"
		}
		return box(expand(r.start, r.end))
	}
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
			if clauseOf[r.e] != nil {
				base.Fatalf("logos: const switch select receive inside a boxed operand at %s is not supported", r.e.Pos().String())
			}
			if r.start > pos {
				out.WriteString(c.fl.textOf(span{pos, r.start}).s)
			}
			out.WriteString(render(r))
			pos = r.end
		}
		if pos < end {
			out.WriteString(c.fl.textOf(span{pos, end}).s)
		}
		return out.String()
	}
	// wrapTextOf returns the composed cast text of the smallest selector wrap
	// containing [start, end). The wrap already drained every lowering splice
	// inside it, so only its text still holds their lowered forms.
	wrapTextOf := func(start, end int) (otext, bool) {
		best := -1
		for i, w := range selWraps {
			if w.start <= start && end <= w.end {
				if best < 0 || w.end-w.start < selWraps[best].end-selWraps[best].start {
					best = i
				}
			}
		}
		if best < 0 {
			return otext{}, false
		}
		return selWraps[best].text, true
	}
	// expandAt renders [start, end) from a composed wrap text the way expand
	// renders it from the working buffer: gaps are located through the wrap's
	// origin chunks so a make-length check the wrap absorbed stays in the
	// temp, and recorded operands inside compose through renderAt.
	var expandAt func(wt otext, start, end int) string
	renderAt := func(wt otext, r rec) string {
		if secondT, ok := multi[r.e]; ok {
			second := spellFor(secondT)
			return "func() (" + cs.alias + ", " + second + ") { _logos_v, _logos_s := " + expandAt(wt, r.start, r.end) + "; return " + box("_logos_v") + ", " + second + "(_logos_s) }()"
		}
		return box(expandAt(wt, r.start, r.end))
	}
	expandAt = func(wt otext, start, end int) string {
		var inner []rec
		for _, r := range recs {
			if r.start >= start && r.end <= end && (r.start != start || r.end != end) {
				inner = append(inner, r)
			}
		}
		var out strings.Builder
		pos := start
		at := func(from, to int, what string) string {
			aS, aE, ok := c.fl.loweredAt(wt.chunks, from, to)
			if !ok {
				base.Fatalf("logos: const switch select target temp cannot be read back from the cast text: frozen operand [%d,%d) %s is not inside it", from, to, what)
			}
			if from == start && to == end {
				// Only the whole operand may absorb a generated wrap; gap
				// and tail slices end inside it.
				aS, aE = gapWrapExtend(wt.chunks, wt.s, aS, aE)
			}
			return wt.s[aS:aE]
		}
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
			if clauseOf[r.e] != nil {
				base.Fatalf("logos: const switch select receive inside a boxed operand at %s is not supported", r.e.Pos().String())
			}
			if r.start > pos {
				out.WriteString(at(pos, r.start, "gap"))
			}
			out.WriteString(renderAt(wt, r))
			pos = r.end
		}
		if pos < end {
			out.WriteString(at(pos, end, "tail"))
		}
		return out.String()
	}
	conv := make(map[syntax.Expr]syntax.Expr)
	texts := make(map[syntax.Expr]otext)
	for _, r := range recs {
		if clauseOf[r.e] != nil {
			continue
		}
		contained := false
		for _, o := range recs {
			if o != r && clauseOf[o.e] == nil && o.start <= r.start && r.end <= o.end {
				contained = true
				break
			}
		}
		if contained {
			continue // emitted inside the outer box's composed text
		}
		text := render(r)
		node, ok := parseExprAt(text, r.e.Pos())
		if !ok {
			base.Fatalf("logos: const switch interface boxing at %s cannot be respelled", r.e.Pos().String())
		}
		conv[r.e] = node
		texts[r.e] = gen(text)
	}
	// `p, ok = <-ch` inside a select cannot name the box call in the comm
	// clause: receive into fresh variables and assign the boxed value in
	// the clause body.
	for _, cc := range clauses {
		as, ok := cc.Comm.(*syntax.AssignStmt)
		if !ok || as.Op != 0 {
			continue // a bare `case <-ch` discards the value; `:=` needs no box
		}
		var elems []syntax.Expr
		if le, ok := as.Lhs.(*syntax.ListExpr); ok {
			elems = le.ElemList
		} else if as.Lhs != nil {
			elems = []syntax.Expr{as.Lhs}
		}
		var temps []syntax.Expr
		var tempNames []string
		svbase := c.fl.cswName("sv")
		for i, l := range elems {
			t := fmt.Sprintf("%s_%d", svbase, i)
			temps = append(temps, nameOf(l.Pos(), t))
			tempNames = append(tempNames, t)
		}
		if len(temps) == 1 {
			as.Lhs = temps[0]
		} else {
			as.Lhs = at(&syntax.ListExpr{ElemList: temps}, as.Lhs.Pos())
		}
		as.Op = syntax.Def
		// A multi-target receive assigns in phases: index operands on the
		// left are evaluated before any store. The split assigns below run
		// in turn, so freeze those reads into temps first — an earlier
		// target may be the variable an index operand reads.
		var freezeStmts []syntax.Stmt
		var freezeText strings.Builder
		freezeN := 0
		// lhsSubs records the span of each frozen operand inside its left-hand
		// element so the emitted store text names the temp; the left side's own
		// span is replaced wholesale by the receive temps, so no source splice
		// may cover it.
		type lhsSub struct {
			start, end int
			name       string
		}
		lhsSubs := map[syntax.Expr][]lhsSub{}
		// freeze emits `t := <src of [start,end)>` early. When the operand
		// sits inside a selector wrap, the wrap already drained the lowering
		// splices (a make-length check) into its cast text, so the temp is
		// built from that composed text: reading the raw source would drop
		// the check from both the native tree and the ejected output.
		freeze := func(e syntax.Expr, start, end int, t string) {
			var src string
			if wt, ok := wrapTextOf(start, end); ok {
				if r, ok := recOf[e]; ok {
					src = renderAt(wt, r)
				} else {
					src = expandAt(wt, start, end)
				}
			} else if r, ok := recOf[e]; ok {
				src = render(r)
			} else {
				src = expand(start, end)
			}
			rhs, ok := parseExprAt(src, e.Pos())
			if !ok {
				base.Fatalf("logos: const switch select target temp at %s cannot be respelled: %q", e.Pos().String(), src)
			}
			freezeStmts = append(freezeStmts, at(&syntax.AssignStmt{Op: syntax.Def, Lhs: nameOf(e.Pos(), t), Rhs: rhs}, e.Pos()))
			freezeText.WriteString("\n" + t + " := " + src)
		}
		if len(elems) > 1 {
			for _, l := range elems {
				// Candidates are collected as the walk goes so a frozen X's
				// replacement name prunes the reads nested inside it; the
				// statements are emitted in source order afterwards because
				// a left-hand operand evaluates left to right (m[i()][j()]
				// reads i() before j()).
				type cand struct {
					e          syntax.Expr
					start, end int
					name       string
				}
				var cands []cand
				var visit func(syntax.Node) bool
				visit = func(n syntax.Node) bool {
					ix, ok := n.(*syntax.IndexExpr)
					if !ok {
						return true
					}
					if ix.X != nil {
						frozen := false
						if _, ok := recOf[ix.X]; ok {
							// A recorded conversion inside a left-hand target must be
							// detached here: the clause's left side is replaced by
							// receive temps, so the use-site splice would overlap it.
							frozen = true
						} else if u := c.info.TypeOf(ix.X); u != nil {
							switch types2.Unalias(u).Underlying().(type) {
							case *types2.Slice, *types2.Map:
								// A slice or map read yields a header value that
								// must be taken early; an array or pointer base is
								// an address read and stays stable.
								frozen = true
							}
						}
						if frozen {
							// X ends where the parent's '[' opens; exprEndNested
							// would swallow the index brackets.
							st := c.fl.off(leftmostOf(ix.X))
							en := c.fl.off(ix.Pos())
							t := fmt.Sprintf("%s_lv%d", svbase, freezeN)
							freezeN++
							cands = append(cands, cand{ix.X, st, en, t})
							lhsSubs[l] = append(lhsSubs[l], lhsSub{st, en, t})
							// The temp takes X's leftmost position so the
							// element's recomputed start still reaches it —
							// a selector's own Pos sits on the field name.
							ix.X = nameOf(leftmostOf(ix.X), t)
						}
					}
					if ix.Index != nil {
						// Literals, constants, and type arguments are not
						// runtime reads: leaving them keeps the map's own
						// key conversion and never freezes a type as a value.
						idx := syntax.Unparen(ix.Index)
						_, lit := idx.(*syntax.BasicLit)
						tv := c.info.Types[idx]
						freezable := !lit && !tv.IsType() && tv.Value == nil
						if freezable {
							if name, ok := idx.(*syntax.Name); ok {
								switch c.info.ObjectOf(name).(type) {
								case *types2.TypeName, *types2.Const:
									freezable = false
								}
							}
						}
						if freezable {
							// The index operand ends where the parent's ']'
							// closes at nesting depth zero.
							st := c.fl.off(leftmostOf(ix.Index))
							en := c.fl.exprEndNested(st)
							t := fmt.Sprintf("%s_lv%d", svbase, freezeN)
							freezeN++
							cands = append(cands, cand{ix.Index, st, en, t})
							lhsSubs[l] = append(lhsSubs[l], lhsSub{st, en, t})
							ix.Index = nameOf(leftmostOf(ix.Index), t)
						}
					}
					// Keep descending into X: an unfrozen X on the address path
					// may hold another index whose operand still needs freezing,
					// and a frozen X was already replaced by its temp name. A
					// skipped index operand is a compile-time expression whose
					// calls never run — unsafe.Sizeof does not evaluate its
					// argument — so the walk must not descend into it.
					syntax.Inspect(ix.X, visit)
					return false
				}
				syntax.Inspect(l, visit)
				slices.SortFunc(cands, func(a, b cand) int { return a.start - b.start })
				for _, cd := range cands {
					freeze(cd.e, cd.start, cd.end, cd.name)
				}
			}
		}
		var assigns []syntax.Stmt
		var texts strings.Builder
		for i, l := range elems {
			// l.Pos() is not the element's leftmost byte for every shape
			// (IndexExpr.Pos() is the '['), and syntax.EndPos follows the
			// mutated children, so take the span from the source: leftmost
			// token through the expression's own nesting.
			lstart := c.fl.off(leftmostOf(l))
			lend := c.fl.exprEndNested(lstart)
			lhsText := c.fl.srcUser(lstart, lend)
			subs := lhsSubs[l]
			// A matched-selector rewrite inside a surviving part of the
			// target folds into the store text here: leaving it as a splice
			// would overlap the receive-target replacement. A rewrite
			// inside a frozen operand's span already reached the emitted
			// temp through the wrap's composed text.
			for _, w := range selWraps {
				if w.start < lstart || w.end > lend {
					continue
				}
				inside := false
				var inner []lhsSub
				for _, s := range subs {
					if w.start >= s.start && w.end <= s.end {
						inside = true
						break
					}
					if w.start <= s.start && s.end <= w.end {
						inner = append(inner, s)
					}
				}
				if inside {
					continue
				}
				if len(inner) == 0 {
					subs = append(subs, lhsSub{w.start, w.end, c.fl.textOf(w.span).s})
					continue
				}
				// The wrapped receiver holds frozen operands: substitute the
				// temps into its composed text, so the temps — not the
				// original operands — appear inside any(...). The composed
				// text keeps every drain already folded into it — an option
				// fallback's `??` lowering, a conversion, a make-length
				// check — so their lowered forms survive; reading the raw
				// gaps here would leak logos-only syntax into the store
				// text, because wrapSelectors drained those splices into
				// the cast when it built it.
				wt := c.fl.textOf(w.span)
				slices.SortFunc(inner, func(a, b lhsSub) int { return b.start - a.start })
				for _, s := range inner {
					aS, aE, ok := c.fl.loweredAt(wt.chunks, s.start, s.end)
					if !ok {
						base.Fatalf("logos: const switch select target at %s: frozen operand [%d,%d) is not verbatim inside the cast text of [%d,%d)", l.Pos().String(), s.start, s.end, w.start, w.end)
					}
					aS, aE = gapWrapExtend(wt.chunks, wt.s, aS, aE)
					wt.s = wt.s[:aS] + s.name + wt.s[aE:]
				}
				kept := subs[:0]
				for _, s := range subs {
					if !(w.start <= s.start && s.end <= w.end) {
						kept = append(kept, s)
					}
				}
				subs = append(kept, lhsSub{w.start, w.end, wt.s})
			}
			// Other pending splices inside the target — an option
			// fallback's `??` lowering, a spread literal's closure, a
			// chain hoist — would overlap the receive replacement that
			// covers it, so drain and fold them into the store text.
			// Inside a covering splice, a frozen operand's span still
			// spells its temp: the splice's text holds the original
			// expression, which would be evaluated a second time. Where
			// the operand's bytes live decides how the temp replaces
			// them: a chainMove owns the copy of a moved region, so the
			// substitution rides on the move; embedded source is located
			// through the splice's origin chunks; only a
			// length-preserving splice keeps byte-parallel offsets.
			// Operands fold from the last to the first, so an offset
			// taken from the chunks stays valid.
			slices.SortFunc(subs, func(a, b lhsSub) int { return b.start - a.start })
			for _, sp := range c.fl.drainInner(span{lstart, lend}) {
				text := sp.text
				var kept []lhsSub
				prevStart := -1
				for _, s := range subs {
					if s.start == s.end {
						// A zero-width operand at a splice boundary covers
						// no source bytes: it is adjacent to, not inside, the
						// splice, the boundary rule spanInside and
						// moveContaining use. It rides along for the store
						// substitution below, where its text lands at
						// exactly this point.
						kept = append(kept, s)
						continue
					}
					if sp.start > s.start || s.end > sp.end {
						kept = append(kept, s)
						continue
					}
					if prevStart >= 0 && s.end > prevStart {
						base.Fatalf("logos: const switch select target at %s: overlapping frozen operands inside [%d,%d)", l.Pos().String(), sp.start, sp.end)
					}
					prevStart = s.start
					if c.fl.moveContaining(span{s.start, s.end}) != nil {
						c.fl.addT(s.start, s.end, gen(s.name))
						continue
					}
					if aS, aE, ok := c.fl.loweredAt(sp.chunks, s.start, s.end); ok {
						aS, aE = gapWrapExtend(sp.chunks, text, aS, aE)
						text = text[:aS] + s.name + text[aE:]
						continue
					}
					if len(sp.text) == sp.end-sp.start {
						text = text[:s.start-sp.start] + s.name + text[s.end-sp.start:]
						continue
					}
					base.Fatalf("logos: const switch select target at %s: frozen operand [%d,%d) cannot fold into the lowered text of [%d,%d)", l.Pos().String(), s.start, s.end, sp.start, sp.end)
				}
				subs = append(kept, lhsSub{sp.start, sp.end, text})
				slices.SortFunc(subs, func(a, b lhsSub) int { return b.start - a.start })
			}
			for _, s := range subs {
				if s.start-lstart < 0 || s.end-lstart > len(lhsText.s) {
					base.Fatalf("logos: lhs sub [%d,%d) outside element %T span [%d,%d) text %q", s.start, s.end, l, lstart, lstart+len(lhsText.s), lhsText.s)
				}
				lhsText.s = lhsText.s[:s.start-lstart] + s.name + lhsText.s[s.end-lstart:]
			}
			rhsText := tempNames[i]
			var rhs syntax.Expr
			if i == 0 {
				rhsText = box(rhsText)
			} else if t := c.info.TypeOf(l); t != nil {
				rhsText = "(" + spellFor(t) + ")(" + rhsText + ")"
			}
			rhs, _ = parseExprAt(rhsText, l.Pos())
			assigns = append(assigns, at(&syntax.AssignStmt{Lhs: l, Rhs: rhs}, l.Pos()))
			texts.WriteString("\n" + lhsText.s + " = " + rhsText)
		}
		cc.Body = append(append(freezeStmts, assigns...), cc.Body...)
		lhsStart := c.fl.off(leftmostOf(as.Lhs))
		recvStart := c.fl.off(leftmostOf(as.Rhs))
		// The temps declaration replaces the whole receive target,
		// including the gaps between elements that no per-element
		// drain reaches (a wrap crossing an element boundary). Any
		// splice still inside the span is dead text.
		c.fl.dropInner(span{lhsStart, recvStart})
		var tempsText strings.Builder
		for i, t := range tempNames {
			if i > 0 {
				tempsText.WriteString(", ")
			}
			tempsText.WriteString(t)
		}
		tempsText.WriteString(" := ")
		c.fl.addT(lhsStart, recvStart, gen(tempsText.String()))
		texts.WriteString("\n")
		c.fl.addT(c.fl.off(cc.Colon)+1, c.fl.off(cc.Colon)+1, gen(freezeText.String()+texts.String()))
	}
	// Pinned alias declarations insert after the store walk: inserting a
	// node into a statement list mid-walk shifts the slice the walk
	// iterates. Their text splices already went out at mint time.
	emitPinned := func() {
		for _, p := range pinned {
			if i := slices.Index(*p.list, syntax.Stmt(p.stmt)); i >= 0 {
				*p.list = slices.Insert(*p.list, i+1, syntax.Stmt(p.d))
			}
		}
	}
	if len(conv) == 0 {
		emitPinned()
		return decls, dtext
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
	emitPinned()
	return decls, dtext
}

// selWrap is one matched-selector receiver rewrite: the replaced span and
// the composed cast text built for it. The text keeps every lowering splice
// the wrap drained (a make-length check, an option fallback), so a frozen
// operand inside the span is read back from it instead of the raw source.
type selWrap struct {
	span
	text otext
}

// wrapSelectors rewrites a matched selector's receiver to
// any(x).(caseT) and returns each replaced receiver span so the
// select-receive rewrite can fold the same edit into a replaced target's
// store text instead of overlapping it.
func (c *cswLower) wrapSelectors(body []syntax.Stmt, cs *cswCase) []selWrap {
	var sels []*syntax.SelectorExpr
	for _, s := range body {
		syntax.Inspect(s, func(n syntax.Node) bool {
			sel, ok := n.(*syntax.SelectorExpr)
			if !ok || c.info.LogosConstSel[sel].Case != cs.syn {
				return true
			}
			sels = append(sels, sel)
			return true
		})
	}
	recvSpan := func(sel *syntax.SelectorExpr) span {
		xstart := c.fl.off(leftmostOf(sel.X))
		xend := typeEnd(c.fl.src, xstart, c.fl.off(sel.Sel.Pos()))
		if xend > xstart && c.fl.src[xend-1] == '.' {
			xend--
		}
		return span{xstart, xend}
	}
	slices.SortStableFunc(sels, func(a, b *syntax.SelectorExpr) int {
		as := recvSpan(a)
		bs := recvSpan(b)
		return (as.end - as.start) - (bs.end - bs.start)
	})
	var wraps []selWrap
	for _, sel := range sels {
		xpos := leftmostOf(sel.X)
		iface, ok := typeExprAt(cs.alias, xpos)
		if !ok {
			base.Fatalf("logos: const switch interface %s at %s cannot be respelled", cs.alias, xpos.String())
		}
		xspan := recvSpan(sel)
		xtext := c.fl.textOf(xspan)
		sel.X = at(&syntax.AssertExpr{
			X: at(&syntax.CallExpr{
				Fun:     at(nameOf(xpos, "any"), xpos),
				ArgList: []syntax.Expr{sel.X},
			}, xpos),
			Type: iface,
		}, xpos)
		cast := cat(gen("any("), xtext, gen(").("), gen(cs.alias), gen(")"))
		c.fl.addT(xspan.start, xspan.end, cast)
		wraps = append(wraps, selWrap{span: xspan, text: cast})
	}
	return wraps
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

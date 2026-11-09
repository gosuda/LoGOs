// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"go/token"
	"slices"
	"strconv"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// lowerArenaEject rewrites the logos/arena calls of one file into plain
// Go eject text. The native tree keeps the calls untouched.
func (fl *fileLower) lowerArenaEject() {
	if base.LogosEject == "" {
		return
	}
	a := &arenaEject{fl: fl}
	a.findRTImport()
	var calls []*syntax.CallExpr
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		c, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		if fl.arenaCallName(c) == "" {
			return true
		}
		calls = append(calls, c)
		return false
	})
	for _, c := range calls {
		a.rewriteCall(c)
	}
	if len(a.spans) == 0 {
		return
	}
	a.insertBlankUses()
	a.fixImports()
}

// arenaEject carries one file's arena rewrite: the replaced spans, the
// rt spelling, and the surviving arena references.
type arenaEject struct {
	fl        *fileLower
	spans     []span
	needRT    bool
	rtAlias   string
	rtPresent bool
	arenaRefs int
}

// arenaCallName names the logos/arena function c calls, or "".
func (fl *fileLower) arenaCallName(c *syntax.CallExpr) string {
	fun := c.Fun
	if ix, ok := syntax.Unparen(fun).(*syntax.IndexExpr); ok {
		fun = ix.X
	}
	sel, ok := syntax.Unparen(fun).(*syntax.SelectorExpr)
	if !ok {
		return ""
	}
	obj, ok := fl.pkg.info.Uses[sel.Sel].(*types2.Func)
	if !ok || obj.Pkg() == nil || obj.Pkg().Path() != "logos/arena" {
		return ""
	}
	switch obj.Name() {
	case "Open", "New", "Make", "Append":
		return obj.Name()
	}
	return ""
}

// rewriteCall replaces one arena call in the eject text. A call inside an
// already replaced span is gone with that span.
func (a *arenaEject) rewriteCall(c *syntax.CallExpr) {
	s := a.callSpan(c)
	if a.covered(s) {
		return
	}
	if a.fl.arenaCallName(c) == "Open" {
		a.needRT = true
		a.replace(s, a.rtAlias+".Region{}")
		return
	}
	a.rewriteBuiltin(c, s)
}

// callSpan returns the whole source span of one call, through its
// closing parenthesis.
func (a *arenaEject) callSpan(c *syntax.CallExpr) span {
	split, ok := a.splitOf(c)
	if !ok {
		base.Fatalf("logos eject: %s: cannot locate call arguments", a.fl.baseName)
	}
	return span{a.fl.off(syntax.StartPos(c)), split.end}
}

// splitOf splits one call's argument list.
func (a *arenaEject) splitOf(c *syntax.CallExpr) (callSplit, bool) {
	start := a.fl.off(syntax.StartPos(c))
	lparen := -1
	scanSegment(a.fl.src, start, len(a.fl.src), func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		switch t.kind {
		case token.LPAREN:
			lparen = t.start
			return false
		case token.EOF:
			return false
		}
		return true
	})
	if lparen < 0 {
		return callSplit{}, false
	}
	return splitCall(a.fl.src, lparen)
}

// rewriteBuiltin lowers New, Make, and Append. The region argument of the
// plain Go builtin drops, so a call whose region argument is neither a
// name, an Open call, nor a Region literal wraps the builtin in a
// closure: the closure evaluates the argument, keeps the evaluation
// order, and discards the value.
func (a *arenaEject) rewriteBuiltin(c *syntax.CallExpr, s span) {
	kept := c.ArgList[1:]
	for _, arg := range kept {
		a.rewriteInner(arg)
	}
	if a.plainRegion(c.ArgList[0]) {
		a.replace(s, a.builtinText(c, kept))
		return
	}
	a.replace(s, a.wrappedText(c, kept))
}

// text renders the current text of one span.
func (a *arenaEject) text(s span) string {
	return a.fl.textOf(s).s
}

// argSpan returns the exact source span of argument i.
func (a *arenaEject) argSpan(c *syntax.CallExpr, i int) span {
	split, ok := a.splitOf(c)
	if !ok || i >= len(split.args) {
		base.Fatalf("logos eject: %s: cannot locate argument %d", a.fl.baseName, i+1)
	}
	return trimSpan(a.fl.src, split.args[i])
}

// rewriteInner lowers the arena calls inside one kept argument before its
// text renders.
func (a *arenaEject) rewriteInner(n syntax.Expr) {
	syntax.Inspect(n, func(m syntax.Node) bool {
		c, ok := m.(*syntax.CallExpr)
		if !ok {
			return true
		}
		if a.fl.arenaCallName(c) == "" {
			return true
		}
		a.rewriteCall(c)
		return false
	})
}

// plainRegion reports whether the region argument drops without a
// closure: a name, an Open call, or a Region composite literal.
func (a *arenaEject) plainRegion(arg syntax.Expr) bool {
	switch x := syntax.Unparen(arg).(type) {
	case *syntax.Name:
		return true
	case *syntax.CallExpr:
		return a.fl.arenaCallName(x) == "Open"
	case *syntax.CompositeLit:
		return logosRegionIsType(a.fl.pkg.info.TypeOf(x))
	}
	return false
}

// builtinName maps the arena call to its plain Go builtin.
func (a *arenaEject) builtinName(c *syntax.CallExpr) string {
	switch a.fl.arenaCallName(c) {
	case "New":
		return "new"
	case "Make":
		return "make"
	default:
		return "append"
	}
}

// builtinText spells the plain builtin call.
func (a *arenaEject) builtinText(c *syntax.CallExpr, kept []syntax.Expr) string {
	switch a.fl.arenaCallName(c) {
	case "New":
		return "new(" + a.elemSpell(c) + ")"
	case "Make":
		return "make([]" + a.elemSpell(c) + ", " + a.argText(c, 0) + ")"
	default:
		parts := make([]string, len(kept))
		for i := range kept {
			parts[i] = a.argText(c, i)
		}
		return "append(" + strings.Join(parts, ", ") + ")"
	}
}

// wrappedText spells the closure form: the region argument evaluates at
// the call, and the builtin body reads parameter names.
func (a *arenaEject) wrappedText(c *syntax.CallExpr, kept []syntax.Expr) string {
	a.needRT = true
	sig, _ := types2.Unalias(a.fl.pkg.info.TypeOf(c.Fun)).(*types2.Signature)
	params := []string{"_ " + a.rtType()}
	names := make([]string, len(kept))
	for i := range kept {
		name := "_logos_e" + strconv.Itoa(i)
		typ := a.paramSpell(c, i)
		if c.HasDots && i == len(kept)-1 {
			names[i] = name + "..."
			typ = a.variadicSpell(c, sig, i)
		} else {
			names[i] = name
		}
		params = append(params, name+" "+typ)
	}
	args := []string{a.text(a.argSpan(c, 0))}
	for i := range kept {
		args = append(args, a.argText(c, i))
	}
	return "func(" + strings.Join(params, ", ") + ") " + a.resultSpell(c) + " { return " +
		a.bodyText(c, names) + " }(" + strings.Join(args, ", ") + ")"
}

// bodyText spells the closure body, which reads the parameter names.
func (a *arenaEject) bodyText(c *syntax.CallExpr, names []string) string {
	switch a.fl.arenaCallName(c) {
	case "New":
		return "new(" + a.elemSpell(c) + ")"
	case "Make":
		return "make([]" + a.elemSpell(c) + ", " + names[0] + ")"
	default:
		return "append(" + strings.Join(names, ", ") + ")"
	}
}

// argText renders kept argument i, with its spread dots on the last one
// and its untyped nil spelled as a conversion the plain builtin accepts.
func (a *arenaEject) argText(c *syntax.CallExpr, i int) string {
	t := a.text(a.argSpan(c, 1+i))
	if n, ok := syntax.Unparen(c.ArgList[1+i]).(*syntax.Name); ok && n.Value == "nil" {
		t = "(" + a.paramSpell(c, i) + ")(" + t + ")"
	}
	if c.HasDots && i == len(c.ArgList)-2 {
		t += "..."
	}
	return t
}

// elemSpell spells the element type T: the written type argument, or the
// element of the checked result type.
func (a *arenaEject) elemSpell(c *syntax.CallExpr) string {
	if _, ok := syntax.Unparen(c.Fun).(*syntax.IndexExpr); ok {
		return a.typeArgText(c)
	}
	res := a.fl.pkg.info.TypeOf(c)
	if p, ok := types2.Unalias(res).(*types2.Pointer); ok {
		return a.spell(p.Elem())
	}
	if sl, ok := types2.Unalias(res).(*types2.Slice); ok {
		return a.spell(sl.Elem())
	}
	return a.spell(res)
}

// resultSpell spells the checked result type of the call.
func (a *arenaEject) resultSpell(c *syntax.CallExpr) string {
	return a.spell(a.fl.pkg.info.TypeOf(c))
}

// paramSpell spells the checked type of the kept argument at index i.
func (a *arenaEject) paramSpell(c *syntax.CallExpr, i int) string {
	if sig, _ := types2.Unalias(a.fl.pkg.info.TypeOf(c.Fun)).(*types2.Signature); sig != nil && 1+i < sig.Params().Len() {
		return a.spell(sig.Params().At(1 + i).Type())
	}
	return a.spell(types2.Default(a.fl.pkg.info.TypeOf(c.ArgList[1+i])))
}

// variadicSpell spells the spread parameter type of the kept argument at
// index i: three dots and the element of the checked slice.
func (a *arenaEject) variadicSpell(c *syntax.CallExpr, sig *types2.Signature, i int) string {
	arg := c.ArgList[1+i]
	if sig != nil && sig.Variadic() && 1+i < sig.Params().Len() {
		if sl, ok := types2.Unalias(sig.Params().At(1 + i).Type()).(*types2.Slice); ok {
			return "..." + a.spell(sl.Elem())
		}
	}
	return "..." + a.spell(types2.Default(a.fl.pkg.info.TypeOf(arg)))
}

// spell prints t relative to this file's package.
func (a *arenaEject) spell(t types2.Type) string {
	if t == nil {
		base.Fatalf("logos eject: %s: missing type", a.fl.baseName)
	}
	return types2.TypeString(t, func(p *types2.Package) string {
		if p == a.fl.pkg.tpkg {
			return ""
		}
		return p.Name() + "."
	})
}

// typeArgText renders the type argument between the instantiation
// brackets of one call.
func (a *arenaEject) typeArgText(c *syntax.CallExpr) string {
	start := a.fl.off(syntax.StartPos(c))
	s := span{-1, -1}
	depth := 0
	done := false
	scanSegment(a.fl.src, start, len(a.fl.src), func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		switch t.kind {
		case token.LBRACK:
			depth++
			if depth == 1 {
				s = span{t.end, t.end}
				return true
			}
		case token.RBRACK:
			if depth == 1 {
				s.end = t.start
				done = true
				return false
			}
			depth--
		case token.EOF:
			return false
		}
		if depth >= 1 {
			s = extend(s, t.span)
		}
		return true
	})
	if !done {
		base.Fatalf("logos eject: %s: cannot locate type argument", a.fl.baseName)
	}
	return a.fl.textOf(trimSpan(a.fl.src, s)).s
}

// rtType spells the region handle type the ejected text denotes.
func (a *arenaEject) rtType() string {
	return a.rtAlias + ".Region"
}

// spanOf converts one node to its byte span.
func (a *arenaEject) spanOf(n syntax.Expr) span {
	return span{a.fl.off(syntax.StartPos(n)), a.fl.endOff(n)}
}

// replace records the replacement of one whole span.
func (a *arenaEject) replace(s span, text string) {
	a.spans = append(a.spans, s)
	a.fl.add(s.start, s.end, text)
}

// covered reports whether s lies inside a span replaced earlier.
func (a *arenaEject) covered(s span) bool {
	for _, p := range a.spans {
		if p.start <= s.start && s.end <= p.end {
			return true
		}
	}
	return false
}

// findRTImport resolves the alias the file already gives logos/rt.
func (a *arenaEject) findRTImport() {
	a.rtAlias = rtAlias
	for _, d := range a.fl.file.DeclList {
		imp, ok := d.(*syntax.ImportDecl)
		if !ok || imp.Path == nil || strings.Trim(imp.Path.Value, `"`) != rtImportPath {
			continue
		}
		a.rtPresent = true
		if imp.LocalPkgName != nil && imp.LocalPkgName.Value != "_" && imp.LocalPkgName.Value != "." {
			a.rtAlias = imp.LocalPkgName.Value
		}
	}
}

// insertBlankUses adds `_ = name` after every local declaration that
// declares a Region: the rewrites drop its uses from the ejected text.
func (a *arenaEject) insertBlankUses() {
	syntax.Inspect(a.fl.file, func(n syntax.Node) bool {
		switch d := n.(type) {
		case *syntax.AssignStmt:
			if d.Op == syntax.Def {
				a.blankNames(syntax.UnpackListExpr(d.Lhs))
			}
		case *syntax.VarDecl:
			names := make([]syntax.Expr, len(d.NameList))
			for i, name := range d.NameList {
				names[i] = name
			}
			a.blankNames(names)
		}
		return true
	})
}

// blankNames adds one blank use per declared Region variable.
func (a *arenaEject) blankNames(names []syntax.Expr) {
	for _, e := range names {
		name, ok := e.(*syntax.Name)
		if !ok {
			continue
		}
		v, ok := a.fl.pkg.info.Defs[name].(*types2.Var)
		if !ok || !logosRegionIsType(v.Type()) || !a.isLocal(v) {
			continue
		}
		a.insertBlankUse(name)
	}
}

// isLocal reports whether v is declared inside a function.
func (a *arenaEject) isLocal(v *types2.Var) bool {
	return v.Parent() != nil && v.Parent() != a.fl.pkg.tpkg.Scope()
}

// insertBlankUse adds `_ = name` on the line after the declaration.
func (a *arenaEject) insertBlankUse(name *syntax.Name) {
	start := a.fl.off(syntax.StartPos(name))
	end := a.fl.endOff(name)
	nl := a.lineEnd(end)
	if nl == len(a.fl.src) {
		return
	}
	lineStart := start
	for lineStart > 0 && a.fl.src[lineStart-1] != '\n' {
		lineStart--
	}
	indent := ""
	for _, r := range string(a.fl.src[lineStart:start]) {
		if r != ' ' && r != '\t' {
			break
		}
		indent += string(r)
	}
	a.fl.add(nl, nl, "\n"+indent+"_ = "+name.Value)
}

// fixImports drops the arena import when no reference survives, and adds
// the rt import when the rewrites spell it.
func (a *arenaEject) fixImports() {
	a.countArenaRefs()
	var removed []span
	lastSurvivor := -1
	for i, d := range a.fl.file.DeclList {
		imp, ok := d.(*syntax.ImportDecl)
		if !ok || imp.Path == nil || strings.Trim(imp.Path.Value, `"`) != "logos/arena" {
			continue
		}
		name := ""
		if imp.LocalPkgName != nil {
			name = imp.LocalPkgName.Value
		}
		if a.arenaRefs == 0 && name != "_" && name != "." {
			removed = append(removed, a.lineSpan(imp))
			continue
		}
		lastSurvivor = i
	}
	for _, r := range removed {
		a.fl.add(r.start, r.end, "")
	}
	if a.needRT && !a.rtPresent {
		a.addRTImport(lastSurvivor, removed)
	}
}

// countArenaRefs counts the arena-package selectors outside replaced
// spans.
func (a *arenaEject) countArenaRefs() {
	syntax.Inspect(a.fl.file, func(n syntax.Node) bool {
		sel, ok := n.(*syntax.SelectorExpr)
		if !ok {
			return true
		}
		name, ok := sel.X.(*syntax.Name)
		if !ok {
			return true
		}
		pkg, ok := a.fl.pkg.info.Uses[name].(*types2.PkgName)
		if !ok || pkg.Imported() == nil || pkg.Imported().Path() != "logos/arena" {
			return true
		}
		if !a.covered(a.spanOf(sel)) {
			a.arenaRefs++
		}
		return true
	})
}

// addRTImport spells the rt import after the last surviving import: a
// new spec line inside a grouped decl, else a new decl line.
func (a *arenaEject) addRTImport(lastSurvivor int, removed []span) {
	off := -1
	text := "\nimport " + rtAlias + ` "` + rtImportPath + `"`
	for i := len(a.fl.file.DeclList) - 1; i >= 0; i-- {
		imp, ok := a.fl.file.DeclList[i].(*syntax.ImportDecl)
		if !ok || imp.Path == nil {
			continue
		}
		if i == lastSurvivor || !removedHas(removed, a.lineSpan(imp)) {
			off = a.lineEnd(a.fl.off(syntax.EndPos(imp)))
			if imp.Group != nil {
				text = "\n\t" + rtAlias + ` "` + rtImportPath + `"`
			}
			break
		}
	}
	if off < 0 {
		off = a.lineEnd(a.fl.off(a.fl.file.PkgName.Pos()))
	}
	a.fl.add(off, off, text)
}

// lineSpan covers one node's whole source line.
func (a *arenaEject) lineSpan(n syntax.Node) span {
	s := a.fl.off(syntax.StartPos(n))
	for s > 0 && a.fl.src[s-1] != '\n' {
		s--
	}
	e := a.lineEnd(a.fl.off(syntax.EndPos(n)))
	if e < len(a.fl.src) {
		e++
	}
	return span{s, e}
}

// removedHas reports whether removed already covers one whole line span.
func removedHas(removed []span, s span) bool {
	return slices.Contains(removed, s)
}

// lineEnd returns the offset of the newline at or after off.
func (a *arenaEject) lineEnd(off int) int {
	for off < len(a.fl.src) && a.fl.src[off] != '\n' {
		off++
	}
	return off
}

// logosRegionIsType reports whether t denotes logos/arena.Region.
func logosRegionIsType(t types2.Type) bool {
	n, _ := types2.Unalias(t).(*types2.Named)
	if n == nil || n.Obj() == nil || n.Obj().Pkg() == nil {
		return false
	}
	return n.Obj().Pkg().Path() == "logos/arena" && n.Obj().Name() == "Region"
}

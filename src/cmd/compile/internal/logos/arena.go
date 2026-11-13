// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"go/token"
	"strconv"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

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

type arenaEject struct {
	fl        *fileLower
	spans     []span
	needRT    bool
	rtAlias   string
	rtPresent bool
	arenaRefs int
}

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

func (a *arenaEject) rewriteCall(c *syntax.CallExpr) {
	s := a.callSpan(c)
	if a.covered(s) {
		return
	}
	if a.fl.arenaCallName(c) == "Open" {
		a.needRT = true
		a.replace(s, "("+a.rtAlias+".Region{})")
		return
	}
	a.rewriteBuiltin(c, s)
}

func (a *arenaEject) callSpan(c *syntax.CallExpr) span {
	split, ok := a.splitOf(c)
	if !ok {
		base.Fatalf("logos eject: %s: cannot locate call arguments", a.fl.baseName)
	}
	return span{a.fl.off(syntax.StartPos(c)), split.end}
}

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

func (a *arenaEject) text(s span) string {
	return a.fl.textOf(s).s
}

func (a *arenaEject) argSpan(c *syntax.CallExpr, i int) span {
	split, ok := a.splitOf(c)
	if !ok || i >= len(split.args) {
		base.Fatalf("logos eject: %s: cannot locate argument %d", a.fl.baseName, i+1)
	}
	return trimSpan(a.fl.src, split.args[i])
}

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

func (a *arenaEject) resultSpell(c *syntax.CallExpr) string {
	return a.spell(a.fl.pkg.info.TypeOf(c))
}

func (a *arenaEject) paramSpell(c *syntax.CallExpr, i int) string {
	if sig, _ := types2.Unalias(a.fl.pkg.info.TypeOf(c.Fun)).(*types2.Signature); sig != nil && 1+i < sig.Params().Len() {
		return a.spell(sig.Params().At(1 + i).Type())
	}
	return a.spell(types2.Default(a.fl.pkg.info.TypeOf(c.ArgList[1+i])))
}

func (a *arenaEject) variadicSpell(c *syntax.CallExpr, sig *types2.Signature, i int) string {
	arg := c.ArgList[1+i]
	if sig != nil && sig.Variadic() && 1+i < sig.Params().Len() {
		if sl, ok := types2.Unalias(sig.Params().At(1 + i).Type()).(*types2.Slice); ok {
			return "..." + a.spell(sl.Elem())
		}
	}
	return "..." + a.spell(types2.Default(a.fl.pkg.info.TypeOf(arg)))
}

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

func (a *arenaEject) rtType() string {
	return a.rtAlias + ".Region"
}

func (a *arenaEject) spanOf(n syntax.Expr) span {
	return span{a.fl.off(syntax.StartPos(n)), a.fl.endOff(n)}
}

func (a *arenaEject) replace(s span, text string) {
	a.spans = append(a.spans, s)
	a.fl.add(s.start, s.end, text)
}

func (a *arenaEject) covered(s span) bool {
	for _, p := range a.spans {
		if p.start <= s.start && s.end <= p.end {
			return true
		}
	}
	return false
}

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

func (a *arenaEject) insertBlankUses() {
	inits := make(map[*syntax.AssignStmt]bool)
	syntax.Inspect(a.fl.file, func(n syntax.Node) bool {
		switch s := n.(type) {
		case *syntax.IfStmt:
			a.blankInit(s.Init, a.fl.off(s.Then.Pos())+1, "", inits)
		case *syntax.ForStmt:
			a.blankInit(s.Init, a.fl.off(s.Body.Pos())+1, "", inits)
		case *syntax.SwitchStmt:
			a.blankSwitchInit(s, inits)
		case *syntax.AssignStmt:
			if s.Op == syntax.Def && !inits[s] {
				a.insertBlankUse(stmtEnd(a.fl.src, a.fl.off(s.Pos())), ";", a.blankNames(syntax.UnpackListExpr(s.Lhs)))
			}
		case *syntax.VarDecl:
			a.blankVarDecl(s)
		}
		return true
	})
}

func (a *arenaEject) blankInit(init syntax.SimpleStmt, off int, prefix string, inits map[*syntax.AssignStmt]bool) {
	s, ok := init.(*syntax.AssignStmt)
	if !ok || s.Op != syntax.Def {
		return
	}
	inits[s] = true
	a.insertBlankUse(off, prefix, a.blankNames(syntax.UnpackListExpr(s.Lhs)))
}

func (a *arenaEject) blankSwitchInit(s *syntax.SwitchStmt, inits map[*syntax.AssignStmt]bool) {
	if len(s.Body) == 0 {
		a.blankInit(s.Init, a.fl.off(s.Rbrace), "default:", inits)
		return
	}
	a.blankInit(s.Init, a.fl.off(s.Body[0].Colon)+1, "", inits)
}

func (a *arenaEject) blankVarDecl(d *syntax.VarDecl) {
	end := stmtEnd(a.fl.src, a.fl.off(d.Pos()))
	if d.Group != nil {
		end = groupEnd(a.fl.src, a.fl.off(d.Pos()))
	}
	var text strings.Builder
	for _, name := range d.NameList {
		text.WriteString(a.blankName(name))
	}
	a.insertBlankUse(end, ";", text.String())
}

func (a *arenaEject) blankNames(names []syntax.Expr) string {
	var text strings.Builder
	for _, e := range names {
		if name, ok := e.(*syntax.Name); ok {
			text.WriteString(a.blankName(name))
		}
	}
	return text.String()
}

func (a *arenaEject) blankName(name *syntax.Name) string {
	v, ok := a.fl.pkg.info.Defs[name].(*types2.Var)
	if !ok || !logosRegionIsType(v.Type()) || !a.isLocal(v) {
		return ""
	}
	return "_ = " + name.Value + ";"
}

func (a *arenaEject) isLocal(v *types2.Var) bool {
	return v.Parent() != nil && v.Parent() != a.fl.pkg.tpkg.Scope()
}

func (a *arenaEject) insertBlankUse(off int, prefix, text string) {
	if text != "" {
		a.fl.add(off, off, prefix+text)
	}
}

func (a *arenaEject) fixImports() {
	a.countArenaRefs()
	removed := make(map[*syntax.ImportDecl]bool)
	for _, d := range a.fl.file.DeclList {
		imp, ok := d.(*syntax.ImportDecl)
		if !ok || imp.Path == nil || strings.Trim(imp.Path.Value, `"`) != "logos/arena" {
			continue
		}
		name := ""
		if imp.LocalPkgName != nil {
			name = imp.LocalPkgName.Value
		}
		if a.arenaRefs == 0 && name != "_" && name != "." {
			removed[imp] = true
			a.fl.removeImportSpec(imp)
		}
	}
	if a.needRT && !a.rtPresent {
		a.addRTImport(removed)
	}
}

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

func (a *arenaEject) addRTImport(removed map[*syntax.ImportDecl]bool) {
	spec := rtAlias + ` "` + rtImportPath + `"`
	for i := len(a.fl.file.DeclList) - 1; i >= 0; i-- {
		imp, ok := a.fl.file.DeclList[i].(*syntax.ImportDecl)
		if !ok || imp.Path == nil || removed[imp] {
			continue
		}
		off := a.fl.off(syntax.EndPos(imp))
		if imp.Group != nil {
			a.fl.add(off, off, "; "+spec)
		} else {
			a.fl.add(off, off, "\nimport "+spec)
		}
		return
	}
	off := a.fl.off(syntax.EndPos(a.fl.file.PkgName))
	a.fl.add(off, off, "\nimport "+spec)
}

func (a *arenaEject) lineEnd(off int) int {
	for off < len(a.fl.src) && a.fl.src[off] != '\n' {
		off++
	}
	return off
}

func logosRegionIsType(t types2.Type) bool {
	n, _ := types2.Unalias(t).(*types2.Named)
	if n == nil || n.Obj() == nil || n.Obj().Pkg() == nil {
		return false
	}
	return n.Obj().Pkg().Path() == "logos/arena" && n.Obj().Name() == "Region"
}

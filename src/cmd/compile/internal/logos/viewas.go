// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"slices"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

func (fl *fileLower) lowerViewAs() {
	var sels []*syntax.SelectorExpr
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		sel, ok := n.(*syntax.SelectorExpr)
		if ok && logosComptimeViewAs(fl.pkg.info, sel) {
			sels = append(sels, sel)
		}
		return true
	})
	if len(sels) == 0 {
		return
	}
	done := make(map[*syntax.Name]bool, len(sels))
	for _, sel := range sels {
		qn := sel.X.(*syntax.Name)
		start := fl.off(qn.Pos())
		fl.add(start, start+len(qn.Value), rtAlias)
		qn.Value = rtAlias
		done[qn] = true
	}
	fl.needRT = true
	fl.dropComptimeImport(done)
}

func logosComptimeViewAs(info *types2.Info, sel *syntax.SelectorExpr) bool {
	qn, ok := sel.X.(*syntax.Name)
	if !ok || sel.Sel.Value != "ViewAs" {
		return false
	}
	pn, ok := info.Uses[qn].(*types2.PkgName)
	if !ok || pn.Imported().Path() != "comptime" {
		return false
	}
	fn, ok := info.Uses[sel.Sel].(*types2.Func)
	return ok && fn.Name() == "ViewAs" && fn.Pkg() != nil && fn.Pkg().Path() == "comptime"
}

func (fl *fileLower) dropComptimeImport(done map[*syntax.Name]bool) {
	keep := false
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		sel, ok := n.(*syntax.SelectorExpr)
		if !ok {
			return true
		}
		qn, ok := sel.X.(*syntax.Name)
		if !ok || done[qn] {
			return true
		}
		if pn, ok := fl.pkg.info.Uses[qn].(*types2.PkgName); ok && pn.Imported().Path() == "comptime" {
			keep = true
			return false
		}
		return true
	})
	if keep {
		return
	}
	var at []int
	for i, decl := range fl.file.DeclList {
		imp, ok := decl.(*syntax.ImportDecl)
		if !ok || imp.Path == nil || !isComptimePath(imp.Path.Value) {
			continue
		}
		at = append(at, i)
		start := fl.off(imp.Pos())
		if kw, ok := importKeywordStart(fl.src, start); ok {
			start = kw
		}
		if line := int(imp.Pos().Line()) - 1; line >= 0 && line < len(fl.start) {
			blank := true
			for _, c := range fl.src[fl.start[line]:start] {
				if c != ' ' && c != '\t' {
					blank = false
					break
				}
			}
			if blank {
				start = fl.start[line]
			}
		}
		end := swallowSep(fl.src, fl.off(imp.Path.Pos())+len(imp.Path.Value))
		fl.add(start, end, "")
	}
	for i := len(at) - 1; i >= 0; i-- {
		fl.file.DeclList = slices.Delete(fl.file.DeclList, at[i], at[i]+1)
	}
}

func isComptimePath(v string) bool {
	return v == `"comptime"` || v == "`comptime`"
}

func importKeywordStart(src []byte, specOff int) (int, bool) {
	off := specOff
	for off > 0 && (src[off-1] == ' ' || src[off-1] == '\t') {
		off--
	}
	if off >= 6 && string(src[off-6:off]) == "import" &&
		(off == 6 || !isIdentChar(src[off-7]) && src[off-7] != '.') {
		return off - 6, true
	}
	return 0, false
}

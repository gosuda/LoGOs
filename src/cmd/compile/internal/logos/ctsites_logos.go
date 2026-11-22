// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"path/filepath"
	"slices"
	"strconv"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

const ctTraceRT = "_logos·ct·rt"

type ctTracePosition struct {
	file string
	line uint
}

type ctTraceUse struct {
	context *ctLowerContext
	ref     *syntax.Name
	def     *syntax.Name
}

type ctTraceSites struct {
	byLocation map[ctTracePosition]*syntax.Name
	decls      []syntax.Decl
	uses       []ctTraceUse
}

func (fl *fileLower) ctTrySite(pos syntax.Pos) *syntax.Name {
	file := fl.baseName
	if fb := pos.FileBase(); fb != nil {
		file = filepath.Base(fb.Filename())
	}
	file, line := siteDisplay(pos, file, pos.Line())
	key := ctTracePosition{file: file, line: line}
	sites := fl.ct.sites
	def := sites.byLocation[key]
	if def == nil {
		if sites.byLocation == nil {
			sites.byLocation = make(map[ctTracePosition]*syntax.Name)
		}
		def = nameOf(pos, "_logos·ct·site"+strconv.Itoa(len(sites.decls)))
		decl := siteDecl(pos, def, file, line)
		decl.Values.(*syntax.CompositeLit).Type.(*syntax.SelectorExpr).X.(*syntax.Name).Value = ctTraceRT
		sites.byLocation[key] = def
		sites.decls = append(sites.decls, decl)
	}
	ref := nameOf(pos, def.Value)
	sites.uses = append(sites.uses, ctTraceUse{context: fl.ct, ref: ref, def: def})
	return ref
}

func (sites *ctTraceSites) appendDecls(file *syntax.File) bool {
	if len(sites.decls) == 0 {
		return false
	}
	imp := rtImport(file.Pos())
	imp.LocalPkgName.Value = ctTraceRT
	decls := slices.Grow(file.DeclList, 1+len(sites.decls))
	file.DeclList = append(slices.Insert(decls, 0, syntax.Decl(imp)), sites.decls...)
	return true
}

func (sites *ctTraceSites) bindUses(pkg *types2.Package, info *types2.Info) error {
	for _, use := range sites.uses {
		obj, ok := info.Defs[use.def].(*types2.Var)
		if !ok || obj.Pkg() != pkg || obj.Parent() != pkg.Scope() {
			return fmt.Errorf("%s: native comptime trace site %q has no consumer package definition", use.def.Pos(), use.def.Value)
		}
		use.context.bindUse(use.ref, obj)
	}
	return nil
}

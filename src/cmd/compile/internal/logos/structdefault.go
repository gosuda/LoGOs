// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"go/token"
	"strconv"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/logos/facts"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

func (fl *fileLower) lowerStructDefaults() {
	stripped := make(map[*syntax.FieldDefault]bool)
	for _, s := range fl.pkg.defaults.order {
		if s.file == fl.file {
			fl.stripFieldDefaults(s.node, stripped)
		}
	}
	fl.emitDefaultHelpers()
	for _, s := range fl.pkg.defaults.order {
		if s.file == fl.file && s.captureText != "" {
			fl.addDefaultCaptures(s.stmt, s.captureText, s.node.Pos())
		}
	}
	replacements := make(map[syntax.Expr]syntax.Expr)
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.CompositeLit:
			if r := fl.fillStructDefaults(n); r != n {
				replacements[n] = r
			}
		}
		return true
	})
	// An addressed literal becomes a call. The runtime helper retains its fresh
	// variable without referring to a potentially shadowed source builtin.
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		if op, ok := n.(*syntax.Operation); ok && op.Op == syntax.And && op.Y == nil {
			if replacements[syntax.Unparen(op.X)] != nil {
				if fl.pkg.info.LogosAddrFresh == nil {
					fl.pkg.info.LogosAddrFresh = make(map[syntax.Expr]bool)
				}
				fl.pkg.info.LogosAddrFresh[op] = true
				fl.pkg.defaults.addresses[op] = true
				fl.needRT = true
			}
		}
		return true
	})
	replaceInnerFirst(fl.file, replacements)
	fl.insertDefaultCaptures()
}

func (fl *fileLower) stripFieldDefaults(st *syntax.StructType, stripped map[*syntax.FieldDefault]bool) {
	for i, f := range st.FieldList {
		fd, ok := f.Type.(*syntax.FieldDefault)
		if !ok {
			continue
		}
		f.Type = fd.Type
		if stripped[fd] {
			continue
		}
		stripped[fd] = true
		typeEnd := fl.exprEndNested(fl.off(syntax.StartPos(fd.Type)))
		fl.add(typeEnd, fl.fieldDefaultEnd(st, i, fd, typeEnd), "")
	}
}

func (fl *fileLower) fieldDefaultEnd(st *syntax.StructType, i int, fd *syntax.FieldDefault, from int) int {
	if i < len(st.TagList) && st.TagList[i] != nil {
		return lastTokenEnd(fl.src, from, fl.off(st.TagList[i].Pos()))
	}
	return fl.exprEndOf(fd.Default)
}

type structDefault struct {
	field string
	text  string
}

func (fl *fileLower) fillStructDefaults(lit *syntax.CompositeLit) syntax.Expr {
	t := fl.pkg.info.TypeOf(lit)
	if t == nil {
		return lit
	}
	ptr, isPtr := types2.Unalias(t).(*types2.Pointer)
	valueType := t
	if isPtr {
		valueType = ptr.Elem()
	}
	st, ok := valueType.Underlying().(*types2.Struct)
	if !ok {
		return lit
	}
	defs, helper := fl.structDefaultsFor(valueType, lit.Pos())
	if len(defs) == 0 {
		return lit
	}
	present := make(map[string]bool)
	for _, e := range lit.ElemList {
		if _, ok := e.(*syntax.SpreadExpr); ok {
			continue
		}
		kv, ok := syntax.Unparen(e).(*syntax.KeyValueExpr)
		if !ok {
			return lit
		} // a positional literal supplies every field
		if key, ok := syntax.Unparen(kv.Key).(*syntax.Name); ok {
			present[key.Value] = true
		}
	}
	for _, spread := range fl.pkg.info.LogosSpreads[lit] {
		for _, field := range spread.Fields {
			present[field] = true
		}
	}
	var missing []structDefault
	for _, d := range defs {
		if d.field != "_" && !present[d.field] {
			missing = append(missing, d)
		}
	}
	if len(missing) == 0 {
		return lit
	}
	if helper != "" {
		var mask []string
		for i := range st.NumFields() {
			if present[st.Field(i).Name()] {
				mask = append(mask, strconv.Itoa(i)+": (0 == 0)")
			}
		}
		maskText := fl.defaultMaskType(st.NumFields()) + "{" + strings.Join(mask, ", ") + "}"
		pos := lit.Pos()
		start, end := fl.off(syntax.StartPos(lit)), fl.off(lit.Rbrace)+1
		prefix, suffix := helper+"(", ", "+maskText+")"
		if lit.Type == nil {
			typeText := fl.defaultLiteralType(valueType, pos)
			lit.Type = fl.reparseDefault(typeText, pos)
			prefix += typeText
		}
		tv := fl.pkg.info.Types[lit]
		valueTV := tv
		valueTV.Type = valueType
		fl.pkg.info.Types[lit] = valueTV
		result := syntax.Expr(callOf(pos, fl.reparseDefault(helper, pos), []syntax.Expr{lit, fl.reparseDefault(maskText, pos)}))
		fl.pkg.info.Types[result] = valueTV
		if isPtr {
			prefix = rtAlias + ".Logos_DefaultAddr(" + prefix
			suffix += ")"
			result = callOf(pos, rtSel(pos, "Logos_DefaultAddr"), []syntax.Expr{result})
		}
		fl.addFirst(start, prefix)
		fl.add(end, end, suffix)
		fl.pkg.info.Types[result] = tv
		fl.wrapSpan[result] = span{start: start, end: end}
		return fl.retyped(lit, result)
	}
	var text strings.Builder
	var native []syntax.Expr
	for _, d := range missing {
		value := fl.reparseDefault(d.text, lit.Pos())
		native = append(native, at(&syntax.KeyValueExpr{
			Key:   nameOf(lit.Pos(), d.field),
			Value: value,
		}, lit.Pos()))
		text.WriteString(d.field + ": " + d.text + ", ")
	}
	lit.ElemList = append(native, lit.ElemList...)
	lit.NKeys += len(native)
	brace := fl.literalBrace(lit)
	fl.add(brace+1, brace+1, text.String())
	return lit
}

func (fl *fileLower) structDefaultsFor(t types2.Type, pos syntax.Pos) ([]structDefault, string) {
	obj, args := defaultTypeObject(t)
	if obj != nil {
		var defs []structDefault
		helper := ""
		if h, ok := fl.pkg.defaults.byType[obj]; ok {
			for _, d := range h.Fields {
				defs = append(defs, structDefault{field: d.Field})
			}
			helper = facts.DefaultHelperName(obj.Name())
		} else if obj.Pkg() != nil && obj.Pkg() != fl.pkg.tpkg {
			if imported, ok := facts.Lookup(obj.Pkg().Path()); ok {
				for _, d := range imported.Defaults {
					if d.Type == obj.Name() {
						defs = append(defs, structDefault{field: d.Field})
					}
				}
				for _, h := range imported.DefaultHelpers {
					if h.Type == obj.Name() {
						helper = h.Helper
						break
					}
				}
				if len(defs) > 0 {
					if err := checkDefaultHelper(obj.Pkg(), t, helper, args); err != nil {
						base.Errorf("%s: logos: %v; rebuild dependency", pos.String(), err)
						return nil, ""
					}
					helper = fl.defaultQualifier(obj.Pkg()) + "." + helper
				}
			}
		}
		if len(defs) > 0 {
			// A named argument carries its exact type arguments, including
			// private types and shadowed local type parameters, into inference.
			_, named := t.(*types2.Named)
			if args.Len() > 0 && !named {
				var text []string
				for i := range args.Len() {
					text = append(text, fl.defaultTypeText(args.At(i)))
				}
				helper += "[" + strings.Join(text, ", ") + "]"
			}
			return defs, helper
		}
		if a, ok := t.(*types2.Alias); ok {
			return fl.structDefaultsFor(a.Rhs(), pos)
		}
		if decl := fl.pkg.defaults.decls[obj]; decl != nil {
			if _, direct := fl.pkg.info.TypeOf(decl.node.Type).(*types2.Struct); !direct {
				return fl.localDerivedDefault(t.(*types2.Named), decl, pos)
			}
		}
	}
	st, _ := t.Underlying().(*types2.Struct)
	if n, ok := t.(*types2.Named); ok {
		st, _ = n.Origin().Underlying().(*types2.Struct)
	}
	s := fl.pkg.defaults.sources[st]
	if s == nil {
		return nil, ""
	}
	if !s.planned {
		base.Fatalf("logos: local defaults used without a capture plan")
	}
	defs := make([]structDefault, len(s.values))
	for i, d := range s.values {
		text := d.text
		for j := range args.Len() {
			text = strings.ReplaceAll(text, "_logos_default_arg"+strconv.Itoa(j)+"_", fl.defaultTypeText(args.At(j)))
		}
		defs[i] = structDefault{field: d.field, text: text}
	}
	return defs, ""
}

func checkDefaultHelper(pkg *types2.Package, typ types2.Type, helper string, args *types2.TypeList) error {
	fn, ok := pkg.Scope().Lookup(helper).(*types2.Func)
	if !ok {
		return fmt.Errorf("default helper %s.%s is not a function", pkg.Path(), helper)
	}
	sig := fn.Type().(*types2.Signature)
	if sig.TypeParams().Len() != args.Len() {
		return fmt.Errorf("default helper %s.%s has incompatible type parameters", pkg.Path(), helper)
	}
	if args.Len() > 0 {
		var ts []types2.Type
		for i := range args.Len() {
			ts = append(ts, args.At(i))
		}
		inst, err := types2.Instantiate(nil, sig, ts, true)
		if err != nil {
			return fmt.Errorf("default helper %s.%s: %w", pkg.Path(), helper, err)
		}
		sig = inst.(*types2.Signature)
	}
	st := typ.Underlying().(*types2.Struct)
	mask := types2.NewArray(types2.Typ[types2.Bool], int64(st.NumFields()))
	if sig.Variadic() || sig.Params().Len() != 2 || sig.Results().Len() != 1 ||
		!types2.Identical(sig.Params().At(0).Type(), typ) ||
		!types2.Identical(sig.Params().At(1).Type(), mask) ||
		!types2.Identical(sig.Results().At(0).Type(), typ) {
		return fmt.Errorf("default helper %s.%s has incompatible signature", pkg.Path(), helper)
	}
	return nil
}

func (fl *fileLower) literalBrace(lit *syntax.CompositeLit) int {
	if lit.Type == nil {
		return fl.off(lit.Pos())
	}
	brace := -1
	depth := 0
	typeBrace := false
	scanSegment(fl.src, fl.off(syntax.StartPos(lit.Type)), len(fl.src), func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		switch t.kind {
		case token.STRUCT, token.INTERFACE:
			typeBrace = true
			return true
		case token.LBRACE:
			if !typeBrace && depth == 0 {
				brace = t.start
				return false
			}
			depth++
		case token.RBRACE:
			depth--
		}
		typeBrace = false
		return true
	})
	if brace < 0 {
		base.Fatalf("logos: composite literal at %s has no {", lit.Pos().String())
	}
	return brace
}

func (fl *fileLower) reparseDefault(text string, pos syntax.Pos) syntax.Expr {
	file, err := syntax.Parse(syntax.NewFileBase("logos-default"), strings.NewReader("package logosdefault\nvar _ = "+text), nil, nil, 0)
	if err != nil || file == nil || len(file.DeclList) != 1 {
		base.Fatalf("logos: cannot reparse default %q", text)
	}
	decl, ok := file.DeclList[0].(*syntax.VarDecl)
	if !ok || decl.Values == nil {
		base.Fatalf("logos: cannot reparse default %q", text)
	}
	value := decl.Values
	syntax.Inspect(value, func(n syntax.Node) bool {
		if n != nil {
			n.SetPos(pos)
		}
		return true
	})
	return value
}

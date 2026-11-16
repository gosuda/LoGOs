// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"strconv"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/logos/facts"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// A package default executes in a producer helper. Local defaults capture
// only constants and types at their declaration: adding a closure variable
// there would make a previously legal goto jump over a variable declaration.
type defaultSource struct {
	node        *syntax.StructType
	file        *syntax.File
	stmt        *syntax.DeclStmt
	params      []*syntax.Field
	fields      []types2.LogosFieldDefault
	values      []structDefault
	captureText string
	planned     bool
	helper      bool
	inline      bool
}

type defaultBindings struct {
	sources   map[*types2.Struct]*defaultSource
	order     []*defaultSource
	helpers   []facts.DefaultBinding
	byType    map[*types2.TypeName]facts.DefaultBinding
	decls     map[*types2.TypeName]*defaultLocalDecl
	literals  map[*types2.TypeName]string
	locals    map[*syntax.DeclStmt][]syntax.Decl
	addresses map[*syntax.Operation]bool
	seq       int
}

type defaultLocalDecl struct {
	node   *syntax.TypeDecl
	stmt   *syntax.DeclStmt
	base   string
	result string
}

func collectDefaultBindings(pkg *types2.Package, info *types2.Info, files []*syntax.File) *defaultBindings {
	b := &defaultBindings{
		sources:   make(map[*types2.Struct]*defaultSource),
		helpers:   facts.DefaultBindings(pkg, info, files),
		byType:    make(map[*types2.TypeName]facts.DefaultBinding),
		decls:     make(map[*types2.TypeName]*defaultLocalDecl),
		literals:  make(map[*types2.TypeName]string),
		locals:    make(map[*syntax.DeclStmt][]syntax.Decl),
		addresses: make(map[*syntax.Operation]bool),
	}
	for _, h := range b.helpers {
		b.byType[h.Type] = h
	}
	for _, file := range files {
		var stack []syntax.Node
		syntax.Inspect(file, func(n syntax.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return false
			}
			if td, ok := n.(*syntax.TypeDecl); ok {
				if obj, ok := info.Defs[td.Name].(*types2.TypeName); ok && obj.Parent() != pkg.Scope() {
					for i := len(stack) - 1; i >= 0; i-- {
						if stmt, ok := stack[i].(*syntax.DeclStmt); ok {
							b.decls[obj] = &defaultLocalDecl{node: td, stmt: stmt}
							break
						}
					}
				}
			}
			if st, ok := n.(*syntax.StructType); ok {
				t, _ := info.TypeOf(st).(*types2.Struct)
				if fields := info.LogosFieldDefaults[t]; len(fields) > 0 && b.sources[t] == nil {
					s := &defaultSource{node: st, file: file, fields: fields}
					for i := len(stack) - 1; i >= 0; i-- {
						switch p := stack[i].(type) {
						case *syntax.FuncDecl, *syntax.FuncLit:
							s.inline = true
						case *syntax.DeclStmt:
							if s.stmt == nil {
								s.stmt = p
							}
						case *syntax.TypeDecl:
							if s.params == nil {
								s.params = p.TParamList
							}
						}
					}
					s.inline = s.inline && s.stmt == nil
					if s.stmt != nil {
						if _, typeDecl := s.stmt.DeclList[0].(*syntax.TypeDecl); !typeDecl {
							s.inline = true
						}
					}
					b.sources[t] = s
					b.order = append(b.order, s)
				}
			}
			stack = append(stack, n)
			return true
		})
	}
	for _, s := range b.order {
		for _, h := range b.helpers {
			if h.Fields[0].Default == s.fields[0].Default {
				s.helper = true
				break
			}
		}
		if !s.helper {
			b.planDefaultCaptures(s, info)
		}
	}
	return b
}

func defaultTypeParams(t types2.Type) *types2.TypeParamList {
	switch t := t.(type) {
	case *types2.Named:
		return t.TypeParams()
	case *types2.Alias:
		return t.TypeParams()
	}
	return nil
}

func defaultTypeObject(t types2.Type) (*types2.TypeName, *types2.TypeList) {
	switch t := t.(type) {
	case *types2.Named:
		return t.Obj(), t.TypeArgs()
	case *types2.Alias:
		return t.Obj(), t.TypeArgs()
	}
	return nil, nil
}

func (fl *fileLower) emitDefaultHelpers() {
	for _, h := range fl.pkg.defaults.helpers {
		if h.Fields[0].Default.Pos().FileBase() != fl.base {
			continue
		}
		pos := h.Fields[0].Default.Pos()
		renames, params, args := fl.defaultTypeScope(defaultTypeParams(h.Type.Type()), pos)
		typ := h.Type.Name() + args
		var text strings.Builder
		fmt.Fprintf(&text, "\nfunc %s%s", facts.DefaultHelperName(h.Type.Name()), params)
		st := h.Type.Type().Underlying().(*types2.Struct)
		fmt.Fprintf(&text, "(_logos_default_value %s, _logos_default_present %s) %s {\n", typ, fl.defaultMaskType(st.NumFields()), typ)
		for _, d := range h.Fields {
			if d.Field == "_" {
				continue
			}
			index := defaultFieldIndex(st, d.Field)
			expr := fl.defaultHelperExpr(d.Default, h.TypeArgs, renames)
			fmt.Fprintf(&text, "if !_logos_default_present[%d] { _logos_default_value.%s = %s\n}\n", index, d.Field, expr)
		}
		text.WriteString("return _logos_default_value\n}\n")
		fl.constDecls = append(fl.constDecls, parseGeneratedDecls(text.String(), pos)...)
		fl.constText += text.String()
	}
}

// A generated generic scope must not let its parameters hide names bound by
// the source declaration, including the declared type's own name.
func (fl *fileLower) defaultTypeScope(params *types2.TypeParamList, pos syntax.Pos) (*typeSubst, string, string) {
	renames := &typeSubst{pkg: fl.pkg.tpkg, tparams: make(map[*types2.TypeParam]*types2.TypeParam)}
	for i := range params.Len() {
		p := params.At(i)
		renamed := types2.NewTypeName(pos, fl.pkg.tpkg, fmt.Sprintf("_logos_default_type%d", i), nil)
		renames.tparams[p] = types2.NewTypeParam(renamed, nil)
	}
	var parameters, arguments []string
	for i := range params.Len() {
		p := params.At(i)
		r := renames.tparams[p]
		r.SetConstraint(substType(renames, p.Constraint()))
		parameters = append(parameters, r.Obj().Name()+" "+types2.TypeString(r.Constraint(), fl.genQualifier))
		arguments = append(arguments, r.Obj().Name())
	}
	if len(parameters) == 0 {
		return renames, "", ""
	}
	return renames, "[" + strings.Join(parameters, ", ") + "]", "[" + strings.Join(arguments, ", ") + "]"
}

func (fl *fileLower) defaultMaskType(fields int) string {
	fl.needRT = true
	return fmt.Sprintf("[%d]%s.Logos_DefaultBool", fields, rtAlias)
}

// Source import names can be shadowed at a constructor. Reserve a distinct
// qualifier for generated references while leaving the source import intact.
func (fl *fileLower) defaultQualifier(p *types2.Package) string {
	reg := fl.getGenImports()
	if q := reg.qual[p.Path()]; strings.HasPrefix(q, "_logos_") {
		return q
	}
	for i := 0; ; i++ {
		q := "_logos_default_import" + strconv.Itoa(i)
		if reg.used[q] {
			continue
		}
		reg.used[q] = true
		reg.pkgs[p.Path()] = p
		reg.qual[p.Path()] = q
		reg.order = append(reg.order, p.Path())
		return q
	}
}

func (fl *fileLower) defaultTypeText(t types2.Type) string {
	return types2.TypeString(t, func(p *types2.Package) string {
		if p == fl.pkg.tpkg {
			return ""
		}
		return fl.defaultQualifier(p)
	})
}

// Wrapping an implicit element literal removes its contextual type. Bind a
// reserved alias outside constructor-local shadows before spelling that type.
func (fl *fileLower) defaultLiteralType(t types2.Type, pos syntax.Pos) string {
	obj, args := defaultTypeObject(t)
	if obj == nil || obj.Pkg() != fl.pkg.tpkg {
		return fl.defaultTypeText(t)
	}
	b := fl.pkg.defaults
	name := b.literals[obj]
	if name == "" {
		var stmt *syntax.DeclStmt
		if decl := b.decls[obj]; decl != nil {
			stmt = decl.stmt
			name = decl.result
		}
		if name == "" {
			name = fmt.Sprintf("_logos_default_literal%d", b.seq)
			b.seq++
			_, params, arguments := fl.defaultTypeScope(defaultTypeParams(obj.Type()), pos)
			text := fmt.Sprintf("type %s%s = %s%s\n", name, params, obj.Name(), arguments)
			fl.addDefaultCaptures(stmt, text, pos)
		}
		b.literals[obj] = name
	}
	if args.Len() > 0 {
		var arguments []string
		for i := range args.Len() {
			arguments = append(arguments, fl.defaultTypeText(args.At(i)))
		}
		name += "[" + strings.Join(arguments, ", ") + "]"
	}
	return name
}

func (fl *fileLower) localDerivedDefault(t *types2.Named, decl *defaultLocalDecl, pos syntax.Pos) ([]structDefault, string) {
	rhs := types2.LogosDefaultInstance(fl.pkg.info.TypeOf(decl.node.Type), t.TypeParams(), t.TypeArgs())
	defs, helper := fl.structDefaultsFor(rhs, pos)
	if helper == "" || len(defs) == 0 {
		return defs, helper
	}
	if decl.base == "" {
		seq := fl.pkg.defaults.seq
		fl.pkg.defaults.seq++
		decl.base = fmt.Sprintf("_logos_default_base%d", seq)
		decl.result = fmt.Sprintf("_logos_default_result%d", seq)
		renames, parameters, arguments := fl.defaultTypeScope(t.TypeParams(), decl.node.Pos())
		baseType := fl.defaultHelperExpr(decl.node.Type, nil, renames)
		text := fmt.Sprintf("type %s%s = %s\ntype %s%s = %s%s\n", decl.base, parameters, baseType, decl.result, parameters, decl.node.Name.Value, arguments)
		fl.addDefaultCaptures(decl.stmt, text, decl.node.Pos())
	}
	baseType, resultType := decl.base, decl.result
	if t.TypeArgs().Len() > 0 {
		var args []string
		for i := range t.TypeArgs().Len() {
			args = append(args, fl.defaultTypeText(t.TypeArgs().At(i)))
		}
		suffix := "[" + strings.Join(args, ", ") + "]"
		baseType += suffix
		resultType += suffix
	}
	st := t.Underlying().(*types2.Struct)
	call := fmt.Sprintf("func(_logos_default_derived %s, _logos_default_mask %s) %s { return %s(%s(%s(_logos_default_derived), _logos_default_mask)) }", resultType, fl.defaultMaskType(st.NumFields()), resultType, resultType, helper, baseType)
	return defs, call
}

// Only type-parameter references move between generic declaration scopes.
// Every other name remains in the original file and package scope.
func (fl *fileLower) defaultHelperExpr(e syntax.Expr, args map[*types2.TypeParam]types2.Type, renames *typeSubst) string {
	saved := make(map[*syntax.Name]string)
	syntax.Inspect(e, func(n syntax.Node) bool {
		name, ok := n.(*syntax.Name)
		if !ok {
			return true
		}
		obj, ok := fl.pkg.info.Uses[name].(*types2.TypeName)
		if !ok {
			return true
		}
		p, ok := obj.Type().(*types2.TypeParam)
		if !ok {
			return true
		}
		t := types2.Type(p)
		if a, ok := args[p]; ok {
			t = a
		}
		saved[name] = name.Value
		name.Value = types2.TypeString(substType(renames, t), fl.genQualifier)
		return true
	})
	text := renderExpr(e)
	for name, old := range saved {
		name.Value = old
	}
	return text
}

func defaultFieldIndex(st *types2.Struct, name string) int {
	for i := range st.NumFields() {
		if st.Field(i).Name() == name {
			return i
		}
	}
	base.Fatalf("logos: default for missing field %s", name)
	return -1
}

// Plan every capture before lowering any file: a constructor may precede the
// file containing an anonymous element type. Emit declarations only in their
// original file and lexical scope, where their source names are bound.
func (b *defaultBindings) planDefaultCaptures(s *defaultSource, info *types2.Info) {
	s.planned = true
	if s.inline {
		for _, d := range s.fields {
			if d.Field != "_" {
				s.values = append(s.values, structDefault{field: d.Field, text: renderDefaultExpr(d.Default)})
			}
		}
		return
	}
	var text strings.Builder
	var capture func(syntax.Expr) string
	capture = func(e syntax.Expr) string {
		e = syntax.Unparen(e)
		if info.Types[e].Value != nil {
			name := fmt.Sprintf("_logos_default_const%d", b.seq)
			b.seq++
			fmt.Fprintf(&text, "const %s = %s\n", name, renderExpr(e))
			return name
		}
		lit, ok := e.(*syntax.CompositeLit)
		if !ok {
			base.Fatalf("logos: unchecked field default %s", renderExpr(e))
		}
		typ := ""
		if lit.Type != nil {
			name := fmt.Sprintf("_logos_default_type%d", b.seq)
			b.seq++
			var params, args []string
			for i, p := range s.params {
				params = append(params, p.Name.Value+" "+renderDefaultExpr(p.Type))
				args = append(args, "_logos_default_arg"+strconv.Itoa(i)+"_")
			}
			paramText := ""
			if len(params) > 0 {
				paramText = "[" + strings.Join(params, ", ") + "]"
			}
			typeText := renderDefaultExpr(lit.Type)
			if array, ok := lit.Type.(*syntax.ArrayType); ok && array.Len == nil {
				checked := info.TypeOf(lit).Underlying().(*types2.Array)
				typeText = "[" + strconv.FormatInt(checked.Len(), 10) + "]" + renderDefaultExpr(array.Elem)
			}
			fmt.Fprintf(&text, "type %s%s = %s\n", name, paramText, typeText)
			typ = name
			if len(args) > 0 {
				typ += "[" + strings.Join(args, ", ") + "]"
			}
		}
		var elems []string
		for _, el := range lit.ElemList {
			if kv, ok := el.(*syntax.KeyValueExpr); ok {
				key := renderExpr(kv.Key)
				field := false
				if name, ok := kv.Key.(*syntax.Name); ok {
					if v, ok := info.Uses[name].(*types2.Var); ok {
						field = v.IsField()
					}
				}
				if !field {
					key = capture(kv.Key)
				}
				elems = append(elems, key+": "+capture(kv.Value))
			} else {
				elems = append(elems, capture(el))
			}
		}
		return typ + "{" + strings.Join(elems, ", ") + "}"
	}
	for _, d := range s.fields {
		if d.Field != "_" {
			s.values = append(s.values, structDefault{field: d.Field, text: capture(d.Default)})
		}
	}
	s.captureText = text.String()
}

// Capture planning runs before annotation erasure. Keep generated aliases as
// plain Go types without mutating the source annotations needed for eject edits.
func renderDefaultExpr(e syntax.Expr) string {
	saved := make(map[*syntax.Field]syntax.Expr)
	syntax.Inspect(e, func(n syntax.Node) bool {
		if field, ok := n.(*syntax.Field); ok {
			if fd, ok := field.Type.(*syntax.FieldDefault); ok {
				saved[field] = field.Type
				field.Type = fd.Type
			}
		}
		return true
	})
	text := renderExpr(e)
	for field, old := range saved {
		field.Type = old
	}
	return text
}

func (fl *fileLower) addDefaultCaptures(stmt *syntax.DeclStmt, text string, pos syntax.Pos) {
	decls := parseGeneratedDecls(text, pos)
	if stmt == nil {
		fl.constDecls = append(fl.constDecls, decls...)
		fl.constText += "\n" + text
	} else {
		fl.pkg.defaults.locals[stmt] = append(fl.pkg.defaults.locals[stmt], decls...)
		end := stmtEnd(fl.src, fl.off(stmt.Pos()))
		fl.add(end, end, "\n"+text)
	}
}

func (fl *fileLower) insertDefaultCaptures() {
	expand := func(list []syntax.Stmt) []syntax.Stmt {
		var out []syntax.Stmt
		for _, stmt := range list {
			out = append(out, stmt)
			target := stmt
			for {
				label, ok := target.(*syntax.LabeledStmt)
				if !ok {
					break
				}
				target = label.Stmt
			}
			if decl, ok := target.(*syntax.DeclStmt); ok {
				for _, capture := range fl.pkg.defaults.locals[decl] {
					out = append(out, at(&syntax.DeclStmt{DeclList: []syntax.Decl{capture}}, capture.Pos()))
				}
				delete(fl.pkg.defaults.locals, decl)
			}
		}
		return out
	}
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.BlockStmt:
			n.List = expand(n.List)
		case *syntax.CaseClause:
			n.Body = expand(n.Body)
		case *syntax.CommClause:
			n.Body = expand(n.Body)
		}
		return true
	})
}

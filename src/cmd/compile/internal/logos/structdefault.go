// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"go/constant"
	"go/token"
	"slices"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/logos/facts"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

func (fl *fileLower) lowerStructDefaults() {
	// Respell marked zero literals first: the walk reaches literals inside
	// field defaults before stripFieldDefaults orphans them, and fill/
	// factory text then renders the respelled form on its own.
	fl.lowerTypeParamLits()
	typeNames := fl.defaultTypeNames(fl.file)
	fl.defaultDepScan()
	// Fill before strip: a composite literal inside a field default gets
	// its own missing fields here, so the factory body rendered from the
	// default subtree carries the inner defaults. The fill splice inside
	// the erased default span is dropped by stripFieldDefaults while the
	// appended elements stay in the subtree that renderDefault prints.
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		if lit, ok := n.(*syntax.CompositeLit); ok {
			fl.fillStructDefaults(lit)
		}
		return true
	})
	stripped := make(map[*syntax.FieldDefault]bool)
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		if st, ok := n.(*syntax.StructType); ok {
			fl.stripFieldDefaults(st, stripped, typeNames)
		}
		return true
	})
}

// lowerTypeParamLits respells an empty composite literal the checker admitted
// as a zero value — T{} for a type parameter, int{}, or an elided {} element —
// as *new(T): a concrete substitution in a comptime instance carries the real
// literal, while a plain generic emits the equivalent allocation.
func (fl *fileLower) lowerTypeParamLits() {
	// Field defaults never emit their source: stripFieldDefaults erases
	// the ` = default` span and renderDefault respells marked literals
	// for the generated text. Respelling one in place would only corrupt
	// the default node's end position for that erasure.
	defaults := map[syntax.Node]bool{}
	// A marked literal under a unary & needs the pointer form: indexing an
	// array literal by value is not addressable.
	underAddr := map[*syntax.CompositeLit]bool{}
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		if fd, ok := n.(*syntax.FieldDefault); ok && fd.Default != nil {
			defaults[fd.Default] = true
		}
		if op, ok := n.(*syntax.Operation); ok && op.Op == syntax.And && op.Y == nil {
			if lit, ok := syntax.Unparen(op.X).(*syntax.CompositeLit); ok {
				underAddr[lit] = true
			}
		}
		return true
	})
	conv := make(map[syntax.Expr]syntax.Expr)
	texts := make(map[syntax.Expr]string)
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		if defaults[n] {
			// The default node itself is erased whole; descending still
			// respells a marked literal nested inside it ([]int{int{}}), and
			// its recorded edit inside the erased span is dropped by
			// stripFieldDefaults while the swapped node renders through
			// renderExpr for the generated factory and fill text.
			return true
		}
		lit, ok := n.(*syntax.CompositeLit)
		if !ok || len(lit.ElemList) != 0 || !fl.pkg.info.LogosZeroLit[lit] {
			return true
		}
		text := fl.zeroLitSpell(lit, underAddr[lit])
		node, ok := parseExprAt(text, lit.Pos())
		if !ok {
			base.Fatalf("logos: type parameter literal at %s cannot be respelled", lit.Pos().String())
		}
		fl.carryType(node, lit)
		conv[lit] = node
		texts[lit] = text
		// The respelled node's positions all equal the literal's, so span
		// walks (option wrap, group extents) would miscompute its extent;
		// pin the literal's real span so a later wrap covers the whole
		// respell and drains its splice into the wrap's fused text.
		litSpan := span{fl.off(syntax.StartPos(lit)), fl.off(syntax.EndPos(lit)) + 1}
		fl.wrapSpan[lit] = litSpan
		fl.wrapSpan[node] = litSpan
		return true
	})
	if len(conv) == 0 {
		return
	}
	w := cswWalker{
		info: fl.pkg.info,
		conv: conv,
		wrap: func(x, r syntax.Expr) {
			start := fl.off(leftmostOf(x))
			// EndPos of a composite literal is its `}` position, so +1 to
			// cover it.
			fl.addT(start, fl.off(syntax.EndPos(x))+1, gen(texts[x]))
		},
	}
	w.node(fl.file)
}

// zeroLitSpell renders the source form a marked zero literal rewrites to:
// *new(T), or an array-index form when a declaration in the file shadows
// the builtin new (the index names no identifier). The value form
// ([1](T){})[0] never takes the array's address, so a large element stays
// on the stack; under a surrounding & the addressable array-pointer form
// is required.
func (fl *fileLower) zeroLitSpell(lit *syntax.CompositeLit, addr bool) string {
	var spell string
	if lit.Type != nil {
		start := fl.off(syntax.StartPos(lit.Type))
		// The type ends at the last token before the literal's brace;
		// EndPos of an index type drops its `]`.
		spell = fl.textOf(span{start, lastTokenEnd(fl.src, start, fl.literalBrace(lit))}).s
	} else {
		spell = fl.typeSpell(fl.pkg.info.TypeOf(lit))
	}
	if fl.newShadowed() {
		if addr {
			// Indexing an array pointer yields an addressable element, so
			// this form still works under a surrounding & (a plain array
			// literal's element does not).
			return "(&[1](" + spell + "){})[0]"
		}
		return "([1](" + spell + "){})[0]"
	}
	return "*new(" + spell + ")"
}

// newShadowed reports whether the file declares `new`: the builtin name
// then resolves to the declaration inside its scope, so an emitted
// *new(T) would call the declaration.
func (fl *fileLower) newShadowed() bool {
	for n := range fl.pkg.info.Defs {
		if n.Value == "new" {
			return true
		}
	}
	return false
}

// renderDefault renders a field default expression, respelling a literal the
// checker admitted as a zero value to its *new(T) form so generated factory
// and fill text never carries a bare T{} to the noder.
func (fl *fileLower) renderDefault(e syntax.Expr) string {
	lit, ok := e.(*syntax.CompositeLit)
	if !ok || !fl.pkg.info.LogosZeroLit[lit] {
		return renderExpr(e)
	}
	return fl.zeroLitSpell(lit, false)
}

// typeSpell renders a checked type with the file's import qualification, for
// emitted code that must name it.
func (fl *fileLower) typeSpell(typ types2.Type) string {
	return types2.TypeString(typ, fl.typeQualifier())
}

// defaultTypeNames maps each package-level named struct type declaration in
// this file to its TypeDecl so defaults can bind to declaration-scope
// factories. Local or anonymous struct types get no entry: they keep the
// textual default path.
func (fl *fileLower) defaultTypeNames(file *syntax.File) map[*syntax.StructType]*syntax.TypeDecl {
	m := map[*syntax.StructType]*syntax.TypeDecl{}
	for _, d := range file.DeclList {
		td, ok := d.(*syntax.TypeDecl)
		if !ok || td.Alias || td.Name == nil {
			continue
		}
		var st *syntax.StructType
		switch t := td.Type.(type) {
		case *syntax.StructType:
			st = t
		case *syntax.AssertedStruct:
			st = t.Struct
		}
		if st != nil {
			m[st] = td
		}
	}
	return m
}

// defaultFactoryPlan resolves each owner.field default pair in this package
// to the emitted factory symbol. Colliding or already-declared names get a
// deterministic suffix inside facts.FactoryNamePlan, so the factory always
// exists under the same name that fact rows export.
func (pl *packageLower) defaultFactoryPlan() map[string]string {
	if pl.factoryPlan == nil {
		pl.factoryPlan = facts.FactoryNamePlan(pl.info, pl.tpkg)
	}
	return pl.factoryPlan
}

// importQualifier returns the source qualifier ("pkg." or "") that names the
// given package in this file, so generated calls survive elided, generic, and
// aliased literals that carry no selector in lit.Type. synthQualifier reuses
// the file's import spellings and registers a synthesized import when needed.
func (fl *fileLower) importQualifier(pkg *types2.Package) string {
	if pkg == nil || pkg == fl.pkg.tpkg {
		return ""
	}
	q := fl.synthQualifier(pkg)
	if q == "" || q == "." {
		return ""
	}
	return q + "."
}

// typeQualifier adapts importQualifier to a types2.Qualifier so type
// arguments rendered into generated calls use this file's import spellings.
func (fl *fileLower) typeQualifier() types2.Qualifier {
	return func(p *types2.Package) string {
		if p == nil || p == fl.pkg.tpkg {
			return ""
		}
		q := fl.synthQualifier(p)
		if q == "." {
			return ""
		}
		return q
	}
}

// ownerArgsText renders the type arguments the derived type named by obj
// instantiates its owner with, recovered from obj's own declaration:
// `type DbBox Box[string]` yields "[string]" and `type Derived[T] Box[T]`
// under a `Derived[int]` literal yields "[int]" by substituting the literal's
// arguments through Derived's parameter names. Returning "" means the
// arguments cannot be recovered and the caller must not guess.
func (fl *fileLower) ownerArgsText(obj, ownerObj types2.Object, litArgs *types2.TypeList) string {
	tn, ok := obj.(*types2.TypeName)
	if !ok {
		return ""
	}
	td, dsrc := fl.pkg.declOf(tn)
	if td == nil || dsrc == nil {
		return ""
	}
	rhs, ok := fl.pkg.info.TypeOf(td.Type).(*types2.Named)
	if !ok || rhs.Origin() == nil || rhs.Origin().Obj() != ownerObj {
		return ""
	}
	var argExprs []syntax.Expr
	if t, ok := td.Type.(*syntax.IndexExpr); ok {
		if list, ok := t.Index.(*syntax.ListExpr); ok {
			argExprs = list.ElemList
		} else {
			argExprs = []syntax.Expr{t.Index}
		}
	}
	if len(argExprs) == 0 {
		return ""
	}
	// Substitute the derived type's parameters with the literal's type
	// arguments. Uses-bound Name nodes inside the index arguments carry the
	// substitution, so composite arguments like []T rewrite to []int.
	subst := map[types2.Object]string{}
	qf := fl.typeQualifier()
	for i, f := range td.TParamList {
		if f.Name == nil || litArgs == nil || i >= litArgs.Len() {
			continue
		}
		if o := fl.pkg.info.Defs[f.Name]; o != nil {
			subst[o] = types2.TypeString(litArgs.At(i), qf)
		}
	}
	starts := lineStarts(dsrc)
	off := func(pos syntax.Pos) int {
		return starts[pos.Line()-1] + int(pos.Col()) - 1
	}
	var parts []string
	for _, arg := range argExprs {
		from, to := off(syntax.StartPos(arg)), off(syntax.EndPos(arg))
		// EndPos excludes closing delimiters (e.g. the ']' of a nested
		// IndexExpr or the '}' of a struct type); extend the slice until
		// the brackets balance. Strings and comments are skipped so
		// delimiters inside a struct tag do not count.
		depth, str := 0, byte(0)
		esc := false
		i := from
		for i < len(dsrc) {
			c := dsrc[i]
			if str != 0 {
				if esc {
					esc = false
				} else if c == '\\' && str != '`' {
					esc = true
				} else if c == str {
					str = 0
				}
				i++
				continue
			}
			if c == '/' && i+1 < len(dsrc) && dsrc[i+1] == '/' {
				for i < len(dsrc) && dsrc[i] != '\n' {
					i++
				}
				continue
			}
			if c == '/' && i+1 < len(dsrc) && dsrc[i+1] == '*' {
				for i += 2; i+1 < len(dsrc) && !(dsrc[i] == '*' && dsrc[i+1] == '/'); i++ {
				}
				i += 2
				continue
			}
			switch c {
			case '"', '\'', '`':
				str = c
			case '[', '(', '{':
				depth++
			case ']', ')', '}':
				depth--
			}
			i++
			if i >= to && depth <= 0 {
				break
			}
		}
		text := string(dsrc[from:i])
		type repl struct {
			span
			s string
		}
		var repls []repl
		syntax.Inspect(arg, func(n syntax.Node) bool {
			name, ok := n.(*syntax.Name)
			if !ok {
				return true
			}
			if s, ok := subst[fl.pkg.info.Uses[name]]; ok {
				repls = append(repls, repl{span{off(name.Pos()), off(syntax.EndPos(name))}, s})
			}
			return true
		})
		for i := len(repls) - 1; i >= 0; i-- {
			r := repls[i]
			text = text[:r.start-from] + r.s + text[r.end-from:]
		}
		parts = append(parts, text)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func (fl *fileLower) stripFieldDefaults(st *syntax.StructType, stripped map[*syntax.FieldDefault]bool, typeNames map[*syntax.StructType]*syntax.TypeDecl) {
	plan := fl.pkg.defaultFactoryPlan()
	td := typeNames[st]
	var tparams string
	if td != nil && td.Name != nil && len(td.TParamList) > 0 {
		var params []string
		for _, p := range td.TParamList {
			if p.Name != nil {
				params = append(params, p.Name.Value+" "+renderExpr(p.Type))
			} else {
				params = append(params, renderExpr(p.Type))
			}
		}
		tparams = "[" + strings.Join(params, ", ") + "]"
	}
	for i, f := range st.FieldList {
		fd, ok := f.Type.(*syntax.FieldDefault)
		if !ok {
			continue
		}
		f.Type = fd.Type
		if !stripped[fd] {
			stripped[fd] = true
			typeEnd := fl.exprEndNested(fl.off(syntax.StartPos(fd.Type)))
			end := fl.fieldDefaultEnd(st, i, fd, typeEnd)
			// The default's source span is gone: an edit recorded inside
			// it — the respell of a marked zero literal like `= int{}` —
			// would overlap the erasure. The respelled form still reaches
			// generated factory text through renderDefault.
			fl.dropInner(span{typeEnd, end})
			fl.add(typeEnd, end, "")
		}
		if td == nil || td.Name == nil || f.Name == nil || f.Name.Value == "_" {
			continue
		}
		// Grouped fields (A, B int = v) share one FieldDefault: each name
		// still needs its own factory.
		fname := plan[td.Name.Value+"."+f.Name.Value]
		if fname == "" {
			continue
		}
		text := "func " + fname + tparams +
			"() " + renderExpr(fd.Type) + " {\n\treturn " + fl.renderDefault(fd.Default) + "\n}\n"
		fl.addGeneratedDecls(fd.Pos(), text)
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
	// key is "owner.field" when text is a package factory call, so a fill
	// inside a default subtree can tell whether the call re-enters an
	// enclosing default; "" when the text is the rendered default itself.
	key string
}

// defFillBudget caps the text fills append inside field-default subtrees.
// Chained local defaults render the already-expanded earlier level again,
// so without a bound a short chain multiplies into unbounded output.
const defFillBudget = 1 << 20

// defaultDepScan records, for every composite literal sitting inside a
// field default's subtree, which enclosing defaulted fields of named
// package types contain it, and the factory keys each such field's subtree
// directly requires. A fill that would emit a factory call reaching one of
// its enclosing keys makes that factory recursive — it never terminates —
// so the literal is reported instead of filled.
func (fl *fileLower) defaultDepScan() {
	if fl.pkg.defScanned {
		return
	}
	fl.pkg.defScanned = true
	for _, file := range fl.pkg.files {
		fl.defaultDepScanFile(file, fl.defaultTypeNames(file))
	}
}

// defaultDepScanFile records the default-subtree literals and factory
// edges of one file; defEdges spans files, so a cycle through defaults
// declared in different files still closes.
func (fl *fileLower) defaultDepScanFile(file *syntax.File, typeNames map[*syntax.StructType]*syntax.TypeDecl) {
	syntax.Inspect(file, func(n syntax.Node) bool {
		st, ok := n.(*syntax.StructType)
		if !ok {
			return true
		}
		td := typeNames[st]
		for _, f := range st.FieldList {
			fd, ok := f.Type.(*syntax.FieldDefault)
			if !ok || fd.Default == nil {
				continue
			}
			key := ""
			if td != nil && td.Name != nil && f.Name != nil {
				key = td.Name.Value + "." + f.Name.Value
			}
			syntax.Inspect(fd.Default, func(m syntax.Node) bool {
				if c, ok := m.(*syntax.CallExpr); ok && fl.unsafeFoldCall(c) {
					// unsafe.Sizeof/Alignof/Offsetof fold at check:
					// their argument literals never evaluate, so they
					// create no factory dependency. They still join
					// inDefault: their text is copied into each emitted
					// default, which the expansion budget must charge.
					syntax.Inspect(c, func(u syntax.Node) bool {
						if l, ok := u.(*syntax.CompositeLit); ok {
							if fl.pkg.inDefault == nil {
								fl.pkg.inDefault = map[*syntax.CompositeLit]bool{}
							}
							fl.pkg.inDefault[l] = true
						}
						return true
					})
					return false
				}
				lit, ok := m.(*syntax.CompositeLit)
				if !ok {
					return true
				}
				if fl.pkg.inDefault == nil {
					fl.pkg.inDefault = map[*syntax.CompositeLit]bool{}
				}
				fl.pkg.inDefault[lit] = true
				if key == "" {
					return true
				}
				if fl.pkg.defInside == nil {
					fl.pkg.defInside = map[*syntax.CompositeLit][]string{}
				}
				fl.pkg.defInside[lit] = append(fl.pkg.defInside[lit], key)
				for _, d := range fl.missingDefaults(lit, true) {
					if d.key == "" {
						continue
					}
					if fl.pkg.defEdges == nil {
						fl.pkg.defEdges = map[string]map[string]bool{}
					}
					if fl.pkg.defEdges[key] == nil {
						fl.pkg.defEdges[key] = map[string]bool{}
					}
					fl.pkg.defEdges[key][d.key] = true
				}
				return true
			})
		}
		return true
	})
}

// unsafeFoldCall reports whether c is a call the checker folds to a
// constant: unsafe.Sizeof, unsafe.Alignof, and unsafe.Offsetof never
// evaluate their arguments, so literals inside them are not factory
// dependencies.
func (fl *fileLower) unsafeFoldCall(c *syntax.CallExpr) bool {
	switch f := syntax.Unparen(c.Fun).(type) {
	case *syntax.SelectorExpr:
		switch f.Sel.Value {
		case "Sizeof", "Alignof", "Offsetof":
		default:
			return false
		}
		n, ok := f.X.(*syntax.Name)
		if !ok {
			return false
		}
		pn, ok := fl.pkg.info.Uses[n].(*types2.PkgName)
		return ok && pn.Imported().Path() == "unsafe"
	case *syntax.Name:
		switch f.Value {
		case "Sizeof", "Alignof", "Offsetof":
			_, ok := fl.pkg.info.Uses[f].(*types2.Builtin)
			return ok
		}
	}
	return false
}

// defReaches reports whether the factory key can reach any of the named
// keys through the default dependency edges — a fill emitting that factory
// inside one of those defaults would make it recursive.
func (fl *fileLower) defReaches(key string, targets []string) bool {
	seen := map[string]bool{}
	var visit func(k string) bool
	visit = func(k string) bool {
		if seen[k] {
			return false
		}
		seen[k] = true
		for _, t := range targets {
			if k == t {
				return true
			}
		}
		for next := range fl.pkg.defEdges[k] {
			if visit(next) {
				return true
			}
		}
		return false
	}
	return visit(key)
}

func (fl *fileLower) fillStructDefaults(lit *syntax.CompositeLit) {
	if fl.pkg.inDefault[lit] && fl.defFillOver {
		// The expansion budget already reported: stop rendering nested
		// defaults instead of doing the remaining exponential work.
		return
	}
	if fl.hasSpread(lit) {
		// Spread lowering emits its initializer and its text together. A
		// generated default has no source span to reuse as a spread operand.
		return
	}
	missing := fl.missingDefaults(lit, false)
	if targets := fl.pkg.defInside[lit]; len(targets) > 0 {
		keep := missing[:0]
		for _, d := range missing {
			if d.key != "" && fl.defReaches(d.key, targets) {
				base.Errorf("%s: logos: field default %s of %s is recursive", lit.Pos().String(), d.field, d.key)
				continue
			}
			keep = append(keep, d)
		}
		missing = keep
	}
	if len(missing) == 0 {
		return
	}
	native, text := fl.structDefaultFields(lit.Pos(), missing)
	lit.ElemList = append(native, lit.ElemList...)
	lit.NKeys += len(native)
	brace := fl.literalBrace(lit)
	fl.add(brace+1, brace+1, text)
	if fl.pkg.inDefault[lit] {
		fl.defFillBytes += len(text)
		if fl.defFillBytes > defFillBudget && !fl.defFillOver {
			fl.defFillOver = true
			base.Errorf("%s: logos: nested field defaults exceed the %d-byte expansion budget", lit.Pos().String(), defFillBudget)
		}
	}
}

func (fl *fileLower) structDefaultFields(pos syntax.Pos, defaults []structDefault) ([]syntax.Expr, string) {
	var text strings.Builder
	var native []syntax.Expr
	for _, d := range defaults {
		value := fl.reparseDefault(d.text, pos)
		native = append(native, at(&syntax.KeyValueExpr{
			Key:   nameOf(pos, d.field),
			Value: value,
		}, pos))
		text.WriteString(d.field + ": " + d.text + ", ")
	}
	return native, text.String()
}

// missingDefaults returns the field defaults a literal omits. keysOnly
// keeps the factory keys but skips rendering default text: the
// dependency scan calls it on literals of other files, whose positions
// this file's line table cannot resolve.
func (fl *fileLower) missingDefaults(lit *syntax.CompositeLit, keysOnly bool) []structDefault {
	t := fl.pkg.info.TypeOf(lit)
	if t == nil {
		return nil
	}
	u, _ := ctLiteralType(lit, t)
	st, _ := u.Underlying().(*types2.Struct)
	if st == nil {
		return nil
	}
	named, _ := u.(*types2.Named)
	defs := fl.pkg.info.LogosFieldDefaults[st]
	if len(defs) == 0 && named != nil {
		origin, _ := named.Origin().Underlying().(*types2.Struct)
		defs = fl.pkg.info.LogosFieldDefaults[origin]
	}
	var obj *types2.TypeName
	if named != nil {
		obj = named.Obj()
	}
	var imported []facts.DefaultFact
	if len(defs) == 0 && obj != nil && obj.Pkg() != nil && obj.Pkg() != fl.pkg.tpkg {
		if f, ok := facts.Lookup(obj.Pkg().Path()); ok && f != nil {
			imported = f.Defaults
		}
	}
	if len(defs) == 0 && len(imported) == 0 {
		return nil
	}
	present := make(map[string]bool)
	for _, spread := range fl.pkg.info.LogosSpreads[lit] {
		for _, name := range spread.Fields {
			present[name] = true
		}
	}
	for _, e := range lit.ElemList {
		if _, ok := e.(*syntax.SpreadExpr); ok {
			continue
		}
		kv, ok := syntax.Unparen(e).(*syntax.KeyValueExpr)
		if !ok {
			return nil
		}
		if key, ok := syntax.Unparen(kv.Key).(*syntax.Name); ok {
			present[key.Value] = true
		}
	}
	var out []structDefault
	for _, d := range defs {
		if present[d.Field] {
			continue
		}
		sd := structDefault{field: d.Field, key: d.Owner + "." + d.Field}
		if !keysOnly {
			sd.text = fl.localDefaultValue(named, d)
		}
		out = append(out, sd)
	}
	for _, d := range imported {
		if d.Type != obj.Name() || present[d.Field] {
			continue
		}
		sd := structDefault{field: d.Field}
		if !keysOnly {
			sd.text = fl.importedDefaultValue(named, d)
		}
		out = append(out, sd)
	}
	return out
}

func (fl *fileLower) localDefaultValue(named *types2.Named, d types2.LogosFieldDefault) string {
	fname := fl.pkg.defaultFactoryPlan()[d.Owner+"."+d.Field]
	if fname == "" {
		return fl.renderDefault(d.Default)
	}
	if named == nil {
		return fname + "()"
	}
	owner, _ := fl.pkg.tpkg.Scope().Lookup(d.Owner).(*types2.TypeName)
	obj := named.Obj()
	if obj == owner {
		return fname + fl.defaultTypeArgs(named.TypeArgs()) + "()"
	}
	if owner != nil {
		if typ, ok := owner.Type().(*types2.Named); ok && typ.TypeParams().Len() != 0 {
			if args := fl.ownerArgsText(obj, owner, named.TypeArgs()); args != "" {
				return fname + args + "()"
			}
		}
	}
	return fname + "()"
}

func (fl *fileLower) importedDefaultValue(named *types2.Named, d facts.DefaultFact) string {
	obj := named.Obj()
	owner, sym, ok := strings.Cut(d.Text, "/")
	if !ok {
		owner, sym = "", owner
	}
	name := fl.importQualifier(obj.Pkg()) + sym
	if owner == obj.Name() {
		name += fl.defaultTypeArgs(named.TypeArgs())
	}
	return name + "()"
}

func (fl *fileLower) defaultTypeArgs(args *types2.TypeList) string {
	if args.Len() == 0 {
		return ""
	}
	parts := make([]string, args.Len())
	qualify := fl.typeQualifier()
	for i := range parts {
		parts[i] = types2.TypeString(args.At(i), qualify)
	}
	return "[" + strings.Join(parts, ", ") + "]"
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

func (r *ctReducer) copyCtStructDefaultFacts(st, _ *syntax.StructType, f *ctFrame) {
	typ, _ := f.info.TypeOf(st).(*types2.Struct)
	var defs []types2.LogosFieldDefault
	for _, d := range f.info.LogosFieldDefaults[typ] {
		if d.Owner != "" {
			continue
		}
		value, ok := f.nodes[d.Default].(syntax.Expr)
		if !ok {
			r.invalid(d.Default, "missing reduced field default")
			continue
		}
		d.Default = value
		defs = append(defs, d)
	}
	if len(defs) != 0 {
		if r.info.LogosFieldDefaults == nil {
			r.info.LogosFieldDefaults = make(map[*types2.Struct][]types2.LogosFieldDefault)
		}
		r.info.LogosFieldDefaults[typ] = defs
	}
}

func (w ctWalker) ctStructDefaultType(st *syntax.StructType) {
	for _, field := range st.FieldList {
		if d, ok := field.Type.(*syntax.FieldDefault); ok {
			field.Type = d.Type
		}
	}
}

func (w ctWalker) ctStructDefault(lit *syntax.CompositeLit) (syntax.Expr, []syntax.Stmt, bool) {
	info := w.fl.pkg.info
	typ := info.TypeOf(lit)
	if info.LogosZeroLit[lit] {
		return w.ctZeroLiteral(lit, typ), nil, true
	}
	if typ == nil {
		return nil, nil, false
	}
	elem, addr := ctLiteralType(lit, typ)
	defaults := w.ctStructDefaultFields(lit, elem, elem, nil)
	if w.fl.hasSpread(lit) {
		e, pre := w.ctStructSpread(lit, elem, defaults, addr)
		return e, pre, true
	}
	fields := defaults.fields
	if len(fields) == 0 {
		return nil, nil, false
	}
	pre := w.literalValues(lit)
	if len(pre) != 0 {
		st := elem.Underlying().(*types2.Struct)
		var defaults []syntax.Stmt
		for _, el := range fields {
			pair := el.(*syntax.KeyValueExpr)
			name := w.nextArg()
			pos := pair.Pos()
			d := at(&syntax.VarDecl{
				NameList: []*syntax.Name{nameOf(pos, name)},
				Type:     w.fl.ctTypeExpr(pos, ctDefaultField(st, pair.Key.(*syntax.Name).Value).Type()),
				Values:   pair.Value,
			}, pos)
			defaults = append(defaults, at(&syntax.DeclStmt{DeclList: []syntax.Decl{d}}, pos))
			pair.Value = nameOf(pos, name)
		}
		pre = append(defaults, pre...)
	}
	lit.Type = w.fl.ctTypeExpr(lit.Pos(), elem)
	out := lit
	if defaults.private {
		amended := *lit
		out = &amended
		w.fl.ct.bindOwnedLiteral(out)
	}
	out.ElemList = append(fields, lit.ElemList...)
	out.NKeys += len(fields)
	if addr {
		return at(&syntax.Operation{Op: syntax.And, X: out}, lit.Pos()), pre, true
	}
	return out, pre, true
}

func (w ctWalker) ctZeroLiteral(lit *syntax.CompositeLit, typ types2.Type) syntax.Expr {
	if typ == nil {
		w.reject(lit, "missing checked type of zero literal")
		return lit
	}
	pos := lit.Pos()
	zero := callOf(pos, w.fl.ctBuiltin(pos, "new"), []syntax.Expr{w.fl.ctTypeExpr(pos, typ)})
	return at(&syntax.Operation{Op: syntax.Mul, X: zero}, pos)
}

func ctDefaultBase(typ types2.Type) types2.Type {
	for {
		switch t := types2.Unalias(typ).(type) {
		case *types2.ConstView:
			typ = t.Elem
		case *types2.ReadonlyDescendants:
			typ = t.Elem
		default:
			return t
		}
	}
}

func ctLiteralType(lit *syntax.CompositeLit, typ types2.Type) (types2.Type, bool) {
	typ = ctDefaultBase(typ)
	if lit.Type == nil {
		if ptr, ok := typ.Underlying().(*types2.Pointer); ok {
			return ctDefaultBase(ptr.Elem()), true
		}
	}
	return typ, false
}

func ctDefaultField(st *types2.Struct, name string) *types2.Var {
	for i := range st.NumFields() {
		if f := st.Field(i); f.Name() == name {
			return f
		}
	}
	return nil
}

type ctDefaultUse struct {
	expr   syntax.Expr
	typ    types2.Type
	owner  *types2.Named
	thunk  string
	parent *ctDefaultUse
}

type ctDefaultFields struct {
	fields  []syntax.Expr
	private bool
}

func (w ctWalker) ctStructDefaultFields(lit *syntax.CompositeLit, concrete, semantic types2.Type, active *ctDefaultUse) ctDefaultFields {
	st, _ := concrete.Underlying().(*types2.Struct)
	if st == nil || (len(lit.ElemList) != 0 && lit.NKeys == 0 && !w.fl.hasSpread(lit)) {
		return ctDefaultFields{}
	}
	source := w.ctDefaultRecords(semantic)
	if source.named == nil && len(source.checked) == 0 {
		return ctDefaultFields{}
	}
	present := make(map[string]bool, lit.NKeys)
	for _, spread := range w.fl.pkg.info.LogosSpreads[lit] {
		for _, name := range spread.Fields {
			present[name] = true
		}
	}
	for _, e := range lit.ElemList {
		if pair, ok := e.(*syntax.KeyValueExpr); ok {
			present[pair.Key.(*syntax.Name).Value] = true
		}
	}
	pos := lit.Pos()
	var out ctDefaultFields
	add := func(field *types2.Var, value syntax.Expr) {
		if value == nil {
			return
		}
		out.private = out.private || w.ctDefaultForeignField(field)
		out.fields = append(out.fields, at(&syntax.KeyValueExpr{Key: nameOf(pos, field.Name()), Value: value}, pos))
	}
	missing := func(name string) *types2.Var {
		if present[name] {
			return nil
		}
		field := ctDefaultField(st, name)
		if field == nil {
			w.reject(lit, "default metadata does not name a field of the literal type: "+name)
		}
		return field
	}
	for _, d := range source.checked {
		field := missing(d.Field)
		if field == nil {
			continue
		}
		if d.Owner != "" {
			add(field, w.ctDefaultFactory(lit, source.named, d))
			continue
		}
		add(field, w.ctDefaultLocal(lit, d.Default, field.Type(), source.named, active))
	}
	for _, row := range source.archived {
		field := missing(row.Field)
		if field == nil {
			continue
		}
		factory := row.Factory
		if factory == nil || factory.Pkg() == nil {
			w.reject(lit, "missing checked default factory for "+row.Field)
			continue
		}
		add(field, w.ctDefaultFactoryCall(lit.Pos(), factory.Pkg().Path(), factory.Name(), source.named.TypeArgs()))
	}
	return out
}

func (w ctWalker) ctDefaultForeignField(field *types2.Var) bool {
	pkg := field.Pkg()
	return !field.Exported() && pkg != nil && pkg.Path() != w.fl.ct.owner.Path()
}

func (w ctWalker) ctDefaultLocal(lit *syntax.CompositeLit, expr syntax.Expr, typ types2.Type, owner *types2.Named, active *ctDefaultUse) syntax.Expr {
	pos := lit.Pos()
	for use := active; use != nil; use = use.parent {
		if expr != use.expr {
			continue
		}
		sameOwner := use.owner == owner || (use.owner != nil && owner != nil && types2.Identical(use.owner, owner))
		if !sameOwner || !types2.Identical(use.typ, typ) {
			w.reject(lit, "recursive local struct field default changes its instantiation")
			return nil
		}
		if use.thunk == "" {
			use.thunk = w.nextArg()
		}
		return callOf(pos, nameOf(pos, use.thunk), nil)
	}
	use := ctDefaultUse{expr: expr, typ: typ, owner: owner, parent: active}
	render := ctDefaultRender{info: w.fl.pkg.info, owner: owner, active: &use}
	value := w.ctDefaultExpression(expr, typ, render)
	if use.thunk == "" {
		return value
	}
	return w.ctDefaultThunk(pos, typ, use.thunk, value)
}

func (w ctWalker) ctDefaultThunk(pos syntax.Pos, typ types2.Type, name string, value syntax.Expr) syntax.Expr {
	sig := func() *syntax.FuncType {
		result := at(&syntax.Field{Type: w.fl.ctTypeExpr(pos, typ)}, pos)
		return at(&syntax.FuncType{ResultList: []*syntax.Field{result}}, pos)
	}
	decl := at(&syntax.VarDecl{NameList: []*syntax.Name{nameOf(pos, name)}, Type: sig()}, pos)
	thunk := at(&syntax.FuncLit{Type: sig(), Body: blockOf(pos, []syntax.Stmt{returnOf(pos, []syntax.Expr{value})})}, pos)
	body := []syntax.Stmt{
		at(&syntax.DeclStmt{DeclList: []syntax.Decl{decl}}, pos),
		assignOf(pos, []syntax.Expr{nameOf(pos, name)}, []syntax.Expr{thunk}),
		returnOf(pos, []syntax.Expr{callOf(pos, nameOf(pos, name), nil)}),
	}
	wrap := at(&syntax.FuncLit{Type: sig(), Body: blockOf(pos, body)}, pos)
	return callOf(pos, wrap, nil)
}

type ctDefaultSource struct {
	named    *types2.Named
	checked  []types2.LogosFieldDefault
	archived []types2.LogosCtNamedDefault
}

func (w ctWalker) ctDefaultRecords(t types2.Type) ctDefaultSource {
	info := w.fl.pkg.info
	st, _ := t.Underlying().(*types2.Struct)
	n, _ := t.(*types2.Named)
	if n == nil {
		return ctDefaultSource{checked: info.LogosFieldDefaults[st]}
	}
	for n != nil {
		if defs := info.LogosCtImportedNamedDefaults(n); len(defs) != 0 {
			return ctDefaultSource{named: n, archived: defs}
		}
		rhs, _ := types2.Unalias(info.LogosCtNamedRHS(n)).(*types2.Named)
		if rhs == nil {
			origin, _ := n.Origin().Underlying().(*types2.Struct)
			if defs := info.LogosFieldDefaults[origin]; len(defs) != 0 {
				return ctDefaultSource{named: n, checked: defs}
			}
			concrete, _ := n.Underlying().(*types2.Struct)
			if defs := info.LogosFieldDefaults[concrete]; len(defs) != 0 {
				return ctDefaultSource{named: n, checked: defs}
			}
		}
		n = rhs
	}
	return ctDefaultSource{}
}

func (w ctWalker) ctDefaultFactory(lit *syntax.CompositeLit, typ *types2.Named, d types2.LogosFieldDefault) syntax.Expr {
	if typ == nil || typ.Obj().Pkg() == nil || typ.Obj().Name() != d.Owner {
		w.reject(lit, "missing checked declaring type of default factory for "+d.Owner+"."+d.Field)
		return nil
	}
	pkg := typ.Obj().Pkg()
	symbol := w.ctDefaultFactorySymbol(pkg, d)
	if symbol == "" || typ.TypeArgs().Len() != typ.TypeParams().Len() {
		w.reject(lit, "missing checked default factory or type arguments for "+d.Owner+"."+d.Field)
		return nil
	}
	return w.ctDefaultFactoryCall(lit.Pos(), pkg.Path(), symbol, typ.TypeArgs())
}

func (w ctWalker) ctDefaultFactoryCall(pos syntax.Pos, path, symbol string, args *types2.TypeList) syntax.Expr {
	var fun syntax.Expr = w.fl.ctPkgSelector(pos, path, symbol)
	if args.Len() != 0 {
		indices := make([]syntax.Expr, args.Len())
		for i := range indices {
			indices[i] = w.fl.ctTypeExpr(pos, args.At(i))
		}
		fun = at(&syntax.IndexExpr{X: fun, Index: listOf(pos, indices)}, pos)
	}
	return callOf(pos, fun, nil)
}

func (w ctWalker) ctDefaultFactorySymbol(pkg *types2.Package, field types2.LogosFieldDefault) string {
	ct := w.fl.ct
	if ct.consumer != nil {
		// The instance is emitted into the instantiating package, so a
		// struct owned there resolves its default factory from the
		// consumer's own plan; structs owned by the defining package
		// keep their existing owner-based resolution.
		if pkg.Path() != ct.consumer.Path() && pkg.Path() != ct.owner.Path() {
			return ""
		}
	} else if pkg.Path() != ct.owner.Path() {
		return ""
	}
	if f := facts.Current(); f != nil {
		for _, d := range f.Defaults {
			owner, symbol, ok := strings.Cut(d.Text, "/")
			if ok && owner == field.Owner && d.Field == field.Field {
				return symbol
			}
		}
	}
	key := field.Owner + "." + field.Field
	if pkg == w.fl.pkg.tpkg {
		return w.fl.pkg.defaultFactoryPlan()[key]
	}
	info := w.fl.pkg.info
	for info.LogosCtDefaultOrigin != nil {
		info = info.LogosCtDefaultOrigin
	}
	return facts.FactoryNamePlan(info, pkg)[key]
}

type ctDefaultRender struct {
	info   *types2.Info
	owner  *types2.Named
	active *ctDefaultUse
}

func ctDefaultValue(info *types2.Info, e syntax.Expr) (types2.Type, constant.Value) {
	if tv, ok := info.Types[e]; ok {
		return tv.Type, tv.Value
	}
	if info.StoreTypesInSyntax {
		tv := e.GetTypeInfo()
		typ, _ := tv.Type.(types2.Type)
		return typ, tv.Value
	}
	return nil, nil
}

func (w ctWalker) ctDefaultExpression(e syntax.Expr, target types2.Type, render ctDefaultRender) syntax.Expr {
	e = syntax.Unparen(e)
	var typ types2.Type
	var val constant.Value
	for render.info != nil {
		typ, val = ctDefaultValue(render.info, e)
		if typ != nil {
			break
		}
		render.info = render.info.LogosCtDefaultOrigin
	}
	if typ == nil {
		w.reject(e, "missing checked declaration expression for field default")
		return e
	}
	semantic := typ
	if render.owner != nil {
		typ = types2.LogosCtSubstituteNamed(typ, render.owner, w.fl.pkg.info.LogosContext)
	}
	var out syntax.Expr
	switch {
	case val != nil:
		out = w.ctDefaultConstant(e, typ, val)
	default:
		lit, ok := e.(*syntax.CompositeLit)
		if !ok {
			w.reject(e, "missing checked constant for field default")
			return e
		}
		if render.info.LogosZeroLit[lit] {
			out = w.ctZeroLiteral(lit, typ)
		} else {
			out = w.ctDefaultComposite(lit, typ, semantic, render)
		}
	}
	if target != nil {
		out = w.ctOptionValueFrom(out, typ, target)
	}
	return out
}

func (w ctWalker) ctDefaultConstant(e syntax.Expr, typ types2.Type, val constant.Value) syntax.Expr {
	pos := e.Pos()
	base := ctDefaultBase(typ)
	basic, ok := base.Underlying().(*types2.Basic)
	if !ok {
		w.reject(e, "missing concrete type for field default constant")
		return e
	}
	name := nameOf(pos, "_logos_ct_default")
	w.fl.ct.bindUse(name, types2.NewConst(pos, nil, name.Value, basic, val))
	if base != basic {
		return callOf(pos, w.fl.ctTypeExpr(pos, typ), []syntax.Expr{name})
	}
	return name
}

func (w ctWalker) ctDefaultComposite(lit *syntax.CompositeLit, typ, semantic types2.Type, render ctDefaultRender) syntax.Expr {
	elem, addr := ctLiteralType(lit, typ)
	source, _ := ctLiteralType(lit, semantic)
	defaults := w.ctStructDefaultFields(lit, elem, source, render.active)
	fields := defaults.fields
	pos := lit.Pos()
	n := at(&syntax.CompositeLit{
		Type:     w.fl.ctTypeExpr(pos, elem),
		ElemList: slices.Grow(fields, len(lit.ElemList)),
		NKeys:    lit.NKeys + len(fields),
		Rbrace:   lit.Rbrace,
	}, pos)
	private := defaults.private
	u := elem.Underlying()
	for i, e := range lit.ElemList {
		pair, _ := e.(*syntax.KeyValueExpr)
		var key syntax.Expr
		if pair != nil {
			key, e = pair.Key, pair.Value
		}
		var target types2.Type
		switch u := u.(type) {
		case *types2.Struct:
			if id, ok := key.(*syntax.Name); ok {
				if fld, ok := render.info.Uses[id].(*types2.Var); ok {
					obj, _, _ := types2.LookupFieldOrMethod(u, false, fld.Pkg(), id.Value)
					if field, ok := obj.(*types2.Var); ok {
						private = private || w.ctDefaultForeignField(field)
						target = field.Type()
					}
				}
				if target == nil {
					w.reject(id, "missing checked field of default literal")
				}
				key = nameOf(id.Pos(), id.Value)
			} else {
				field := u.Field(i)
				private = private || w.ctDefaultForeignField(field)
				target = field.Type()
			}
		case *types2.Map:
			key = w.ctDefaultExpression(key, u.Key(), render)
			target = u.Elem()
		case *types2.Slice:
			target = u.Elem()
		case *types2.Array:
			target = u.Elem()
		}
		if key != nil {
			switch u.(type) {
			case *types2.Slice, *types2.Array:
				key = w.ctDefaultExpression(key, nil, render)
			}
		}
		e = w.ctDefaultExpression(e, target, render)
		if key != nil {
			e = at(&syntax.KeyValueExpr{Key: key, Value: e}, pair.Pos())
		}
		n.ElemList = append(n.ElemList, e)
	}
	if private {
		w.fl.ct.bindOwnedLiteral(n)
	}
	if addr {
		return at(&syntax.Operation{Op: syntax.And, X: n}, pos)
	}
	return n
}

func (w ctWalker) ctStructSpreadAddr(op *syntax.Operation) (syntax.Expr, []syntax.Stmt, bool) {
	if op.Op != syntax.And || op.Y != nil {
		return nil, nil, false
	}
	lit, ok := syntax.Unparen(op.X).(*syntax.CompositeLit)
	if !ok || !w.fl.hasSpread(lit) {
		return nil, nil, false
	}
	typ := w.fl.pkg.info.TypeOf(lit)
	if typ == nil {
		w.reject(lit, "missing checked type for spread literal")
		return lit, nil, true
	}
	typ = ctDefaultBase(typ)
	e, pre := w.ctStructSpread(lit, typ, w.ctStructDefaultFields(lit, typ, typ, nil), true)
	return e, pre, true
}

func (w ctWalker) ctStructSpread(lit *syntax.CompositeLit, typ types2.Type, defaults ctDefaultFields, addr bool) (syntax.Expr, []syntax.Stmt) {
	pos := lit.Pos()
	name := w.nextArg()
	tmp := func() *syntax.Name { return nameOf(pos, name) }
	fields := defaults.fields
	init := at(&syntax.CompositeLit{Type: w.fl.ctTypeExpr(pos, typ), ElemList: fields, NKeys: len(fields), Rbrace: lit.Rbrace}, pos)
	if defaults.private {
		w.fl.ct.bindOwnedLiteral(init)
	}
	body := []syntax.Stmt{defineOf(pos, []syntax.Expr{tmp()}, []syntax.Expr{init})}
	spreads := w.fl.pkg.info.LogosSpreads[lit]
	hoisted := false
	for i, el := range lit.ElemList {
		switch el := el.(type) {
		case *syntax.SpreadExpr:
			src := spreads[0]
			spreads = spreads[1:]
			value, pre := w.expr(src.Src)
			hoisted = hoisted || len(pre) != 0
			body = append(body, pre...)
			if len(src.Fields) == 0 {
				body = append(body, assignOf(pos, []syntax.Expr{nameOf(pos, "_")}, []syntax.Expr{value}))
				continue
			}
			if len(src.Fields) == 1 {
				field := src.Fields[0]
				lhs, rhs := selOf(pos, tmp(), field), selOf(pos, value, field)
				body = append(body, assignOf(pos, []syntax.Expr{lhs}, []syntax.Expr{rhs}))
				continue
			}
			from := w.nextArg()
			body = append(body, defineOf(pos, []syntax.Expr{nameOf(pos, from)}, []syntax.Expr{value}))
			for _, field := range src.Fields {
				lhs := selOf(pos, tmp(), field)
				rhs := selOf(pos, nameOf(pos, from), field)
				body = append(body, assignOf(pos, []syntax.Expr{lhs}, []syntax.Expr{rhs}))
			}
		case *syntax.KeyValueExpr:
			value, pre := w.checkedOperand(el.Value, w.ctOptionTarget(lit, el, i))
			hoisted = hoisted || len(pre) != 0
			lhs := selOf(pos, tmp(), el.Key.(*syntax.Name).Value)
			body = append(body, pre...)
			body = append(body, assignOf(pos, []syntax.Expr{lhs}, []syntax.Expr{value}))
		}
	}
	var result syntax.Expr = tmp()
	if addr {
		result = at(&syntax.Operation{Op: syntax.And, X: result}, pos)
		typ = types2.NewPointer(typ)
	}
	if hoisted {
		return result, body
	}
	body = append(body, returnOf(pos, []syntax.Expr{result}))
	ret := at(&syntax.Field{Type: w.fl.ctTypeExpr(pos, typ)}, pos)
	fn := at(&syntax.FuncLit{
		Type: at(&syntax.FuncType{ResultList: []*syntax.Field{ret}}, pos),
		Body: blockEndingAt(pos, lit.Rbrace, body),
	}, pos)
	return callOf(pos, fn, nil), nil
}

func (w ctWalker) ctStructSpreadSelfUpdate(s *syntax.AssignStmt) ([]syntax.Stmt, bool) {
	if s.Op != 0 {
		return nil, false
	}
	target, ok := s.Lhs.(*syntax.Name)
	if !ok || target.Value == "_" {
		return nil, false
	}
	lit, ok := syntax.Unparen(s.Rhs).(*syntax.CompositeLit)
	if !ok || !w.fl.hasSpread(lit) {
		return nil, false
	}
	info := w.fl.pkg.info
	obj := info.Uses[target]
	if obj == nil {
		return nil, false
	}
	for _, spread := range info.LogosSpreads[lit] {
		src, ok := syntax.Unparen(spread.Src).(*syntax.Name)
		if !ok || info.Uses[src] != obj {
			return nil, false
		}
	}
	var lhs, rhs []syntax.Expr
	var targets []types2.Type
	for i, e := range lit.ElemList {
		if kv, ok := e.(*syntax.KeyValueExpr); ok {
			lhs = append(lhs, selOf(kv.Pos(), nameOf(target.Pos(), target.Value), kv.Key.(*syntax.Name).Value))
			rhs = append(rhs, kv.Value)
			targets = append(targets, w.ctOptionTarget(lit, kv, i))
		}
	}
	pos := s.Pos()
	if len(rhs) == 0 {
		return []syntax.Stmt{assignOf(pos, []syntax.Expr{nameOf(pos, "_")}, []syntax.Expr{target})}, true
	}
	pre := w.watchExprs(len(rhs), func(i int) *syntax.Expr { return &rhs[i] },
		func(i int, old syntax.Expr) (syntax.Expr, []syntax.Stmt) {
			return w.checkedOperand(old, targets[i])
		})
	return append(pre, assignOf(pos, lhs, rhs)), true
}

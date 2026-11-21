// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"go/token"
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
// as *new(T): a concrete substitution in a comptime copy carries the real
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
					// create no factory dependency.
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
				for _, d := range fl.missingDefaults(lit) {
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
	missing := fl.missingDefaults(lit)
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

func (fl *fileLower) missingDefaults(lit *syntax.CompositeLit) []structDefault {
	all := fl.structDefaultsFor(lit)
	if len(all) == 0 {
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
	for _, d := range all {
		if !present[d.field] {
			out = append(out, d)
		}
	}
	return out
}

func (fl *fileLower) structDefaultsFor(lit *syntax.CompositeLit) []structDefault {
	t := fl.pkg.info.TypeOf(lit)
	if t == nil {
		return nil
	}
	u := types2.Unalias(t)
	st, _ := u.(*types2.Struct)
	if st == nil {
		if n, ok := u.(*types2.Named); ok {
			if s, ok := n.Underlying().(*types2.Struct); ok {
				st = s
			}
		}
	}
	if st == nil {
		return nil
	}
	named, _ := u.(*types2.Named)
	var obj *types2.TypeName
	var targs string
	var litArgs *types2.TypeList
	if named != nil {
		if o := named.Obj(); o != nil && o.Pkg() != nil {
			obj = o
			litArgs = named.TypeArgs()
			if litArgs != nil && litArgs.Len() > 0 {
				var parts []string
				for i := 0; i < litArgs.Len(); i++ {
					parts = append(parts, litArgs.At(i).String())
				}
				targs = "[" + strings.Join(parts, ", ") + "]"
			}
		}
	}
	qual := ""
	if obj != nil {
		qual = fl.importQualifier(obj.Pkg())
	}
	plan := fl.pkg.defaultFactoryPlan()
	call := func(d types2.LogosFieldDefault) string {
		fname := plan[d.Owner+"."+d.Field]
		if fname == "" {
			return fl.renderDefault(d.Default)
		}
		// The factory's type parameters belong to the owner type. A literal
		// naming the owner itself supplies matching type arguments; a type
		// defined over the owner's struct resolves the plain factory when it
		// is not generic, and otherwise falls back to the default's text.
		var ownerObj types2.Object
		if fl.pkg.tpkg != nil && fl.pkg.tpkg.Scope() != nil {
			ownerObj = fl.pkg.tpkg.Scope().Lookup(d.Owner)
		}
		if obj == ownerObj {
			return qual + fname + targs + "()"
		}
		if tn, ok := ownerObj.(*types2.TypeName); ok {
			if on, ok := tn.Type().(*types2.Named); !ok || on.TypeParams().Len() == 0 {
				return qual + fname + "()"
			}
			// The owner is generic: recover its arguments from the derived
			// type's own declaration. Rendering the default's text here would
			// rebind its names to the literal's scope; emitting the bare call
			// fails loudly instead.
			if args := fl.ownerArgsText(obj, ownerObj, litArgs); args != "" {
				return qual + fname + args + "()"
			}
		}
		return qual + fname + "()"
	}
	if defs := fl.pkg.info.LogosFieldDefaults[st]; len(defs) > 0 {
		out := make([]structDefault, 0, len(defs))
		for _, d := range defs {
			out = append(out, structDefault{field: d.Field, text: call(d), key: d.Owner + "." + d.Field})
		}
		return out
	}
	if obj == nil || obj.Pkg() == fl.pkg.tpkg {
		return nil
	}
	imp, ok := facts.Lookup(obj.Pkg().Path())
	if !ok || imp == nil {
		return nil
	}
	var out []structDefault
	for _, d := range imp.Defaults {
		if d.Type != obj.Name() {
			continue
		}
		// The factory's type parameters belong to the declaring (owner)
		// type, which rides in front of the symbol in the fact text. A
		// literal naming the owner itself supplies matching arguments;
		// a defined-over literal takes the plain factory. When the owner
		// is generic the bare call cannot infer its parameters, which
		// surfaces the unsupported defined-over-generic case as a build
		// error rather than a silent wrong value.
		owner, sym, _ := strings.Cut(d.Text, "/")
		if sym == "" {
			sym = owner
			owner = ""
		}
		var ownerObj types2.Object
		if owner != "" && obj.Pkg().Scope() != nil {
			ownerObj = obj.Pkg().Scope().Lookup(owner)
		}
		text := qual + sym + "()"
		if ownerObj == obj {
			text = qual + sym + targs + "()"
		}
		out = append(out, structDefault{field: d.Field, text: text})
	}
	return out
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

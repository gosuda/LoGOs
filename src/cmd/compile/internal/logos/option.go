// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"go/token"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

const (
	optTemp     = "_logos_v"
	optOk       = "_logos_ok"
	optWrapTemp = "_logos_w%d"
)

var exprType = reflect.TypeFor[syntax.Expr]()

func infoHasInferredOption(pkg *types2.Package, info *types2.Info, files []*syntax.File) bool {
	if info == nil {
		return false
	}
	scan := inferredOptionScan{pkg: pkg, seen: make(map[types2.Type]struct{})}
	return scan.hasObjects(info.Defs) || scan.hasObjects(info.Uses) ||
		scan.hasInfo(info) || scan.hasFiles(files)
}

type inferredOptionScan struct {
	pkg  *types2.Package
	seen map[types2.Type]struct{}
}

func (s *inferredOptionScan) hasObjects(objects map[*syntax.Name]types2.Object) bool {
	for _, obj := range objects {
		if obj != nil && s.hasOption(obj.Type()) {
			return true
		}
	}
	return false
}

func (s *inferredOptionScan) hasInfo(info *types2.Info) bool {
	for _, obj := range info.Implicits {
		if obj != nil && s.hasOption(obj.Type()) {
			return true
		}
	}
	for _, tv := range info.Types {
		if s.hasOption(tv.Type) {
			return true
		}
	}
	for _, inst := range info.Instances {
		if s.hasOption(inst.Type) || s.hasTypeArgs(inst.TypeArgs) {
			return true
		}
	}
	for _, sel := range info.Selections {
		if sel != nil && (s.hasOption(sel.Recv()) || s.hasOption(sel.Obj().Type())) {
			return true
		}
	}
	return false
}

func (s *inferredOptionScan) hasFiles(files []*syntax.File) bool {
	for _, file := range files {
		if file == nil {
			continue
		}
		found := false
		syntax.Inspect(file, func(n syntax.Node) bool {
			if found {
				return false
			}
			if expr, ok := n.(syntax.Expr); ok {
				found = s.hasOption(expr.GetTypeInfo().Type)
			}
			return !found
		})
		if found {
			return true
		}
	}
	return false
}

func (s *inferredOptionScan) hasOption(t types2.Type) bool {
	if t == nil {
		return false
	}
	if _, seen := s.seen[t]; seen {
		return false
	}
	s.seen[t] = struct{}{}
	switch t := t.(type) {
	case *types2.Option:
		return true
	case *types2.Named:
		return s.hasNamed(t)
	case *types2.Alias:
		return s.hasTypeArgs(t.TypeArgs()) ||
			(t.Obj().Pkg() == s.pkg && s.hasTypeParams(t.TypeParams())) ||
			s.hasOption(t.Rhs())
	case *types2.TypeParam:
		return s.hasOption(t.Constraint())
	case *types2.ConstView:
		return s.hasOption(t.Elem)
	case *types2.ReadonlyDescendants:
		return s.hasOption(t.Elem)
	default:
		return s.hasComposite(t)
	}
}

func (s *inferredOptionScan) hasNamed(t *types2.Named) bool {
	if s.hasTypeArgs(t.TypeArgs()) {
		return true
	}
	if t.Obj().Pkg() != s.pkg {
		return false
	}
	if origin := t.Origin(); origin != t {
		return s.hasOption(origin)
	}
	return s.hasTypeParams(t.TypeParams()) || s.hasOption(t.Underlying())
}

func (s *inferredOptionScan) hasComposite(t types2.Type) bool {
	switch t := t.(type) {
	case *types2.Map:
		return s.hasOption(t.Key()) || s.hasOption(t.Elem())
	case interface{ Elem() types2.Type }:
		return s.hasOption(t.Elem())
	case *types2.Tuple:
		return s.hasTuple(t)
	case *types2.LogosTuple:
		return s.hasLogosTuple(t)
	case *types2.Signature:
		return s.hasSignature(t)
	case *types2.Struct:
		return s.hasStruct(t)
	case *types2.Interface:
		return s.hasInterface(t)
	case *types2.Union:
		return s.hasUnion(t)
	}
	return false
}

func (s *inferredOptionScan) hasTuple(t *types2.Tuple) bool {
	for i := range t.Len() {
		if v := t.At(i); v != nil && s.hasOption(v.Type()) {
			return true
		}
	}
	return false
}

func (s *inferredOptionScan) hasLogosTuple(t *types2.LogosTuple) bool {
	for i := range t.Len() {
		if s.hasOption(t.Elem(i)) {
			return true
		}
	}
	return false
}

func (s *inferredOptionScan) hasSignature(t *types2.Signature) bool {
	if recv := t.Recv(); recv != nil && s.hasOption(recv.Type()) {
		return true
	}
	return s.hasOption(t.Params()) || s.hasOption(t.Results()) ||
		s.hasTypeParams(t.RecvTypeParams()) || s.hasTypeParams(t.TypeParams())
}

func (s *inferredOptionScan) hasStruct(t *types2.Struct) bool {
	for i := range t.NumFields() {
		if field := t.Field(i); field != nil && s.hasOption(field.Type()) {
			return true
		}
	}
	return false
}

func (s *inferredOptionScan) hasInterface(t *types2.Interface) bool {
	for i := range t.NumExplicitMethods() {
		if method := t.ExplicitMethod(i); method != nil && s.hasOption(method.Type()) {
			return true
		}
	}
	for i := range t.NumEmbeddeds() {
		if s.hasOption(t.EmbeddedType(i)) {
			return true
		}
	}
	return false
}

func (s *inferredOptionScan) hasUnion(t *types2.Union) bool {
	for i := range t.Len() {
		if term := t.Term(i); term != nil && s.hasOption(term.Type()) {
			return true
		}
	}
	return false
}

func (s *inferredOptionScan) hasTypeArgs(args *types2.TypeList) bool {
	for i := range args.Len() {
		if s.hasOption(args.At(i)) {
			return true
		}
	}
	return false
}

func (s *inferredOptionScan) hasTypeParams(params *types2.TypeParamList) bool {
	for i := range params.Len() {
		if s.hasOption(params.At(i)) {
			return true
		}
	}
	return false
}

func (fl *fileLower) lowerOptions() {
	fl.optMemo = make(map[*syntax.OptionType]syntax.Expr)
	fl.narrowDone = make(map[*syntax.Name]bool)
	fl.optBodyType = make(map[syntax.Expr]types2.Type)
	fl.collectOptReturns()
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		if n == nil {
			return true
		}
		fl.lowerOptNode(n)
		return true
	})
	fl.rejectCompositeSurvivors()
}

func (fl *fileLower) lowerOptNode(n syntax.Node) {
	switch n := n.(type) {
	case *syntax.CallStmt:
		if fl.markOptDeferGo(n) {
			return
		}
	case *syntax.CallExpr:
		if _, ok := fl.optDeferGoPend[n]; ok {
			return
		}
	case *syntax.AssignStmt:
		fl.lowerOptAssign(n)
		fl.lowerOptFields(n, "Lhs")
		return
	case *syntax.ForStmt:
		fl.lowerOptForRange(n)
	case *syntax.RangeClause:
		fl.lowerOptRange(n)
		fl.lowerOptFields(n, "Lhs")
		return
	case *syntax.ReturnStmt:
		fl.lowerOptReturn(n)
	case *syntax.VarDecl:
		n.Values = fl.lowerOptVarValues(n.NameList, n.Type, n.Values)
	case *syntax.ConstDecl:
		n.Values = fl.lowerOptVarValues(n.NameList, n.Type, n.Values)
	case *syntax.SendStmt:
		fl.lowerOptSend(n)
	case *syntax.LambdaExpr:
		fl.lowerOptLambdaBody(n)
	}
	fl.lowerOptFields(n, "")
}

func (fl *fileLower) lowerOptLambdaBody(lam *syntax.LambdaExpr) {
	body, ok := lam.Body.(syntax.Expr)
	if !ok {
		return
	}
	switch syntax.Unparen(body).(type) {
	case *syntax.CoalesceExpr, *syntax.OptSelectExpr:
	case *syntax.Name, *syntax.SelectorExpr, *syntax.IndexExpr:
		if _, ok := fl.compositeRefOf(body); !ok {
			return
		}
	default:
		return
	}
	orig := fl.pkg.info.TypeOf(body)
	lowered := fl.lowerOptExpr(body)
	lam.Body = lowered
	if lowered != body && orig != nil {
		fl.optBodyType[lowered] = orig
	}
}

func (fl *fileLower) lowerOptFields(n syntax.Node, except string) {
	v := reflect.ValueOf(n)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return
	}
	v = v.Elem()
	if v.Kind() != reflect.Struct {
		return
	}
	t := v.Type()
	for i := range v.NumField() {
		if !t.Field(i).IsExported() {
			continue
		}
		if except != "" && t.Field(i).Name == except {
			continue
		}
		fl.lowerOptField(v.Field(i), t.Field(i).Type)
	}
}

func (fl *fileLower) lowerOptField(v reflect.Value, ft reflect.Type) {
	if ft == exprType {
		if v.IsNil() {
			return
		}
		v.Set(reflect.ValueOf(fl.lowerOptExpr(v.Interface().(syntax.Expr))))
		return
	}
	k := ft.Kind()
	if (k == reflect.Slice || k == reflect.Array) && ft.Elem() == exprType {
		for i := range v.Len() {
			ev := v.Index(i)
			if ev.IsNil() {
				continue
			}
			ev.Set(reflect.ValueOf(fl.lowerOptExpr(ev.Interface().(syntax.Expr))))
		}
	}
}

func (fl *fileLower) lowerOptExpr(e syntax.Expr) (out syntax.Expr) {
	defer func() {
		if out != nil && e != nil {
			fl.carryType(out, e)
		}
	}()
	switch e := e.(type) {
	case nil:
		return nil
	case *syntax.Name:
		if out, ok := fl.lowerConstMapName(e); ok {
			return out
		}
		if out, ok := fl.compositeRewriteRoot(e); ok {
			return out
		}
		return fl.narrowAccess(e)
	case *syntax.BasicLit:
		return e
	case *syntax.OptionType:
		return fl.lowerOptionType(e)
	case *syntax.CoalesceExpr:
		return fl.lowerCoalesce(e)
	case *syntax.OptSelectExpr:
		return fl.lowerOptSelect(e)
	case *syntax.CallExpr:
		if out, ok := fl.lowerConstMapLen(e); ok {
			return out
		}
		fl.compositeShapeArg(e)
		return fl.lowerOptCallExpr(e)
	case *syntax.IndexExpr:
		if out, ok := fl.compositeRewriteRoot(e); ok {
			return out
		}
		return fl.lowerMapIndex(e)
	case *syntax.SelectorExpr:
		if out, ok := fl.compositeRewriteRoot(e); ok {
			return out
		}
		if g, ok := fl.constMapUse(e); ok {
			return fl.lowerConstMapSelector(e, g)
		}
		e.X = fl.lowerOptExpr(e.X)
		return e
	case *syntax.Operation:
		if out, ok := fl.lowerNilCompare(e); ok {
			return out
		}
		e.X = fl.lowerOptExpr(e.X)
		if e.Y != nil {
			e.Y = fl.lowerOptExpr(e.Y)
		}
		return e
	case *syntax.CompositeLit:
		return fl.lowerOptComposite(e)
	case *syntax.ParenExpr:
		e.X = fl.lowerOptExpr(e.X)
		return e
	case *syntax.ListExpr:
		for i, x := range e.ElemList {
			e.ElemList[i] = fl.lowerOptExpr(x)
		}
		return e
	case *syntax.SliceExpr:
		e.X = fl.lowerOptExpr(e.X)
		for i, x := range e.Index {
			if x != nil {
				e.Index[i] = fl.lowerOptExpr(x)
			}
		}
		return e
	case *syntax.AssertExpr:
		e.X = fl.lowerOptExpr(e.X)
		if e.Type != nil {
			e.Type = fl.lowerTypeExpr(e.Type)
		}
		return e
	case *syntax.TypeSwitchGuard:
		e.X = fl.lowerOptExpr(e.X)
		return e
	case *syntax.KeyValueExpr:
		e.Key = fl.lowerOptExpr(e.Key)
		e.Value = fl.lowerOptExpr(e.Value)
		return e
	case *syntax.FuncLit:
		return e
	default:
		return e
	}
}

func (fl *fileLower) nextWrapID() int {
	fl.wrapSeq++
	return fl.wrapSeq
}

func (fl *fileLower) optType(e syntax.Expr) types2.Type {
	if e == nil {
		return nil
	}
	return fl.pkg.info.TypeOf(e)
}

func (fl *fileLower) asOptionThroughConst(t types2.Type) *types2.Option {
	if c := constAsView(t); c != nil {
		return fl.asOptionThroughConst(c.Elem)
	}
	return fl.asOption(t)
}

func (fl *fileLower) asOption(t types2.Type) *types2.Option {
	if o, ok := types2.Unalias(t).(*types2.Option); ok {
		return o
	}
	if n, ok := types2.Unalias(t).(*types2.Named); ok {
		if u := n.Underlying(); u != nil {
			if o, ok := u.(*types2.Option); ok {
				return o
			}
		}
	}
	return nil
}

func (fl *fileLower) lowerOptionType(e *syntax.OptionType) syntax.Expr {
	if prev, ok := fl.optMemo[e]; ok {
		return cloneType(prev, e.Pos())
	}
	pos := e.Pos()
	var elemT types2.Type
	if ot := fl.asOption(fl.optType(e)); ot != nil {
		elemT = ot.Elem
	} else if et := fl.optType(e.Elem); et != nil {
		elemT = et
	}
	qoff := fl.off(pos)
	var out syntax.Expr
	if elemT != nil && types2.LogosOptionIsBare(elemT) {
		out = fl.lowerTypeExpr(e.Elem)
		fl.add(qoff, qoff+1, "")
	} else {
		es := fl.optTypeHead(e.Elem)
		le := fl.lowerTypeExpr(e.Elem)
		fl.needRT = true
		fl.add(es, es, rtAlias+".Option[")
		fl.add(qoff, qoff+1, "]")
		out = at(&syntax.IndexExpr{X: selOf(pos, nameOf(pos, rtAlias), "Option"), Index: le}, pos)
		fl.deleteGroupingParens(es, qoff)
	}
	fl.optMemo[e] = out
	return out
}

func (fl *fileLower) optTypeHead(e syntax.Expr) int {
	es := fl.off(fl.optLeftmost(e))
	if _, ok := syntax.Unparen(e).(*syntax.FuncType); !ok {
		return es
	}
	if fl.funcHeadOffs == nil {
		fl.funcHeadOffs = funcHeadOffsets(fl.src)
	}
	i := sort.SearchInts(fl.funcHeadOffs, es)
	if i == 0 {
		base.Fatalf("logos: function type at offset %d has no func keyword", es)
	}
	return fl.funcHeadOffs[i-1]
}

func funcHeadOffsets(src []byte) []int {
	var out []int
	scanSegment(src, 0, len(src), func(t tok) bool {
		if t.kind == token.FUNC {
			out = append(out, t.start)
		}
		return true
	})
	return out
}

func (fl *fileLower) deleteGroupingParens(es, qoff int) {
	depth, n := 0, 0
	scanSegment(fl.src, es, qoff, func(t tok) bool {
		switch {
		case t.comment || t.auto:
		case t.kind == token.LPAREN:
			depth++
		case t.kind == token.RPAREN:
			depth--
			if depth < 0 {
				depth = 0
				if fl.parenClaims[t.start] {
					return true
				}
				fl.add(t.start, t.end, "")
				fl.parenClaims[t.start] = true
				n++
			}
		}
		return true
	})
	off := skipBlankBack(fl.src, es)
	for n > 0 && off > 0 {
		switch {
		case fl.src[off-1] == '(':
			if !fl.parenClaims[off-1] {
				fl.add(off-1, off, "")
				fl.parenClaims[off-1] = true
				n--
			}
			off = skipBlankBack(fl.src, off-1)
		case off >= len("const") && string(fl.src[off-len("const"):off]) == "const":
			off = skipBlankBack(fl.src, off-len("const"))
		default:
			return
		}
	}
}

func (fl *fileLower) optLeftmost(e syntax.Expr) syntax.Pos {
	for {
		switch n := syntax.Unparen(e).(type) {
		case *syntax.CompositeLit:
			if n.Type == nil {
				return e.Pos()
			}
			e = n.Type
		case *syntax.OptSelectExpr:
			e = n.X
		case *syntax.CoalesceExpr:
			e = n.X
		case *syntax.OptionType:
			e = n.Elem
		case *syntax.SelectorExpr:
			e = n.X
		case *syntax.IndexExpr:
			e = n.X
		case *syntax.SliceExpr:
			e = n.X
		case *syntax.AssertExpr:
			e = n.X
		case *syntax.CallExpr:
			e = n.Fun
		case *syntax.Operation:
			if n.Y == nil {
				return e.Pos()
			}
			e = n.X
		default:
			return e.Pos()
		}
	}
}

func (fl *fileLower) optHeadStart(e syntax.Expr) syntax.Pos {
	for {
		switch n := e.(type) {
		case *syntax.ParenExpr:
			return e.Pos()
		case *syntax.CompositeLit:
			if n.Type == nil {
				return e.Pos()
			}
			e = n.Type
		case *syntax.OptSelectExpr:
			e = n.X
		case *syntax.CoalesceExpr:
			e = n.X
		case *syntax.OptionType:
			e = n.Elem
		case *syntax.SelectorExpr:
			e = n.X
		case *syntax.IndexExpr:
			e = n.X
		case *syntax.SliceExpr:
			e = n.X
		case *syntax.AssertExpr:
			e = n.X
		case *syntax.CallExpr:
			e = n.Fun
		case *syntax.Operation:
			if n.Y == nil {
				return e.Pos()
			}
			e = n.X
		default:
			return e.Pos()
		}
	}
}

func (fl *fileLower) lowerTypeExpr(e syntax.Expr) syntax.Expr {
	switch t := e.(type) {
	case *syntax.OptionType:
		return fl.lowerOptionType(t)
	case *syntax.ParenExpr:
		t.X = fl.lowerTypeExpr(t.X)
		return e
	case *syntax.Operation:
		if t.Op == syntax.Mul && t.Y == nil {
			t.X = fl.lowerTypeExpr(t.X)
		}
		return e
	case *syntax.SliceType:
		t.Elem = fl.lowerTypeExpr(t.Elem)
		return e
	case *syntax.ArrayType:
		t.Elem = fl.lowerTypeExpr(t.Elem)
		return e
	case *syntax.DotsType:
		t.Elem = fl.lowerTypeExpr(t.Elem)
		return e
	case *syntax.MapType:
		t.Key = fl.lowerTypeExpr(t.Key)
		t.Value = fl.lowerTypeExpr(t.Value)
		return e
	case *syntax.ChanType:
		t.Elem = fl.lowerTypeExpr(t.Elem)
		return e
	case *syntax.FuncType:
		for _, f := range t.ParamList {
			if f.Type != nil {
				f.Type = fl.lowerTypeExpr(f.Type)
			}
		}
		for _, f := range t.ResultList {
			if f.Type != nil {
				f.Type = fl.lowerTypeExpr(f.Type)
			}
		}
		return e
	case *syntax.StructType:
		for _, f := range t.FieldList {
			if f.Type != nil {
				f.Type = fl.lowerTypeExpr(f.Type)
			}
		}
		return e
	case *syntax.InterfaceType:
		for _, f := range t.MethodList {
			if f.Type != nil {
				f.Type = fl.lowerTypeExpr(f.Type)
			}
		}
		return e
	default:
		return e
	}
}

func (fl *fileLower) spellLoweredBoth(t types2.Type, pos syntax.Pos) (native syntax.Expr, eject string, ok bool) {
	if ownedTypeBridge != nil {
		if n, e, ok := ownedTypeBridge(fl, t, pos); ok {
			return n, e, true
		}
	}
	s, ok := fl.spellLowered(t)
	if !ok {
		return nil, "", false
	}
	n, ok := typeExprAt(s, pos)
	if !ok {
		return nil, "", false
	}
	return n, s, true
}

func (fl *fileLower) optSelectName(e *syntax.OptSelectExpr) string {
	if o := fl.coeType(e.X); o != nil {
		return tupleSelectionName(o.Elem, e.Sel.Value)
	}
	return e.Sel.Value
}

func (fl *fileLower) selectAccessBare(recv syntax.Expr, e *syntax.OptSelectExpr) (syntax.Expr, string) {
	pos := e.Pos()
	sel := fl.optSelectName(e)
	return fl.selectAccess(e, selOf(pos, recv, sel), "."+sel)
}

func (fl *fileLower) selectAccessPresent(recv syntax.Expr, e *syntax.OptSelectExpr) (syntax.Expr, string) {
	pos := e.Pos()
	sel := fl.optSelectName(e)
	return fl.selectAccess(e, selOf(pos, selOf(pos, recv, "Value"), sel), ".Value."+sel)
}

func (fl *fileLower) selectAccess(e *syntax.OptSelectExpr, native syntax.Expr, suffix string) (syntax.Expr, string) {
	pos := e.Pos()
	ot := fl.coeType(e.X)
	if ot == nil {
		return native, suffix
	}
	obj, _, _ := types2.LookupFieldOrMethod(ot.Elem, true, fl.pkg.tpkg, e.Sel.Value)
	if obj == nil {
		return native, suffix
	}
	if pkg := obj.Pkg(); pkg != nil && pkg != fl.pkg.tpkg && !obj.Exported() {
		if ownedFieldBridge != nil {
			if nacc, esuf, ok := ownedFieldBridge(fl, e, native, suffix); ok {
				return nacc, esuf
			}
		}
		base.Fatalf("logos: cannot access unexported selection %s of %s at %s without a registered private view", e.Sel.Value, ot.Elem, pos.String())
	}
	return native, suffix
}

type typeSpelling uint8

const (
	loweredTypeSpelling typeSpelling = iota
	copyTypeSpelling
)

func (fl *fileLower) spellLowered(t types2.Type) (string, bool) {
	return fl.spellTypeAs(t, loweredTypeSpelling)
}

func (fl *fileLower) spellTypeAs(t types2.Type, spelling typeSpelling) (string, bool) {
	if t == nil {
		return "", false
	}
	shape := t
	if spelling == loweredTypeSpelling {
		shape = types2.Unalias(t)
		_, named := shape.(*types2.Named)
		if o := fl.asOption(t); o != nil && !named {
			if types2.LogosOptionIsBare(o.Elem) {
				return fl.spellLowered(o.Elem)
			}
			inner, ok := fl.spellLowered(o.Elem)
			if !ok {
				return "", false
			}
			fl.needRT = true
			return rtAlias + ".Option[" + inner + "]", true
		}
	}
	switch c := shape.(type) {
	case *types2.Option:
		inner, ok := fl.spellTypeAs(c.Elem, spelling)
		if !ok {
			return "", false
		}
		return "((" + inner + ")?)", true
	case *types2.ConstView:
		inner, ok := fl.spellTypeAs(c.Elem, spelling)
		if !ok || spelling == loweredTypeSpelling {
			return inner, ok
		}
		return "(const " + inner + ")", true
	case *types2.ReadonlyDescendants:
		return fl.spellTypeAs(c.Elem, spelling)
	case *types2.Named:
		if c.TypeArgs().Len() != 0 {
			s, ok := fl.spellInstance(c.Obj(), c.TypeArgs(), spelling)
			if ok || spelling == copyTypeSpelling {
				return s, ok
			}
		}
		return fl.spellType(t)
	case *types2.Alias:
		if c.TypeArgs().Len() != 0 {
			return fl.spellInstance(c.Obj(), c.TypeArgs(), spelling)
		}
		return fl.spellType(t)
	case *types2.Basic, *types2.TypeParam:
		return fl.spellType(t)
	case *types2.Pointer:
		e, ok := fl.spellTypeAs(c.Elem(), spelling)
		if !ok {
			return "", false
		}
		return "*" + e, true
	case *types2.Slice:
		e, ok := fl.spellTypeAs(c.Elem(), spelling)
		if !ok {
			return "", false
		}
		return "[]" + e, true
	case *types2.Array:
		e, ok := fl.spellTypeAs(c.Elem(), spelling)
		if !ok {
			return "", false
		}
		return fmt.Sprintf("[%d]", c.Len()) + e, true
	case *types2.Map:
		k, ok := fl.spellTypeAs(c.Key(), spelling)
		if !ok {
			return "", false
		}
		v, ok := fl.spellTypeAs(c.Elem(), spelling)
		if !ok {
			return "", false
		}
		return "map[" + k + "]" + v, true
	case *types2.Chan:
		e, ok := fl.spellTypeAs(c.Elem(), spelling)
		if !ok {
			return "", false
		}
		switch c.Dir() {
		case types2.SendOnly:
			return "chan<- " + e, true
		case types2.RecvOnly:
			return "<-chan " + e, true
		default:
			if elem, ok := types2.Unalias(c.Elem()).(*types2.Chan); ok && elem.Dir() == types2.RecvOnly {
				return "chan (" + e + ")", true
			}
			return "chan " + e, true
		}
	case *types2.Signature:
		return fl.spellSignature(c, spelling)
	case *types2.LogosTuple:
		if spelling == copyTypeSpelling {
			return fl.spellCopyTuple(c)
		}
		if st, ok := c.Underlying().(*types2.Struct); ok {
			return fl.spellStruct(st, spelling)
		}
		return "", false
	case *types2.Struct:
		return fl.spellStruct(c, spelling)
	case *types2.Interface:
		return fl.spellInterface(c, spelling)
	default:
		return "", false
	}
}

func (fl *fileLower) spellInstance(name *types2.TypeName, args *types2.TypeList, spelling typeSpelling) (string, bool) {
	if !spellableObject(name, fl.pkg.tpkg) {
		return "", false
	}
	var b strings.Builder
	if pkg := name.Pkg(); pkg != nil && pkg != fl.pkg.tpkg {
		b.WriteString(fl.synthQualifier(pkg))
		b.WriteByte('.')
	}
	b.WriteString(name.Name())
	b.WriteByte('[')
	for i := range args.Len() {
		if i > 0 {
			b.WriteString(", ")
		}
		arg, ok := fl.spellTypeAs(args.At(i), spelling)
		if !ok {
			return "", false
		}
		b.WriteString(arg)
	}
	b.WriteByte(']')
	return b.String(), true
}

func (fl *fileLower) spellCopyTuple(tuple *types2.LogosTuple) (string, bool) {
	var b strings.Builder
	b.WriteByte('(')
	for i := range tuple.Len() {
		if i > 0 {
			b.WriteString(", ")
		}
		if name := tuple.Name(i); name != "" {
			b.WriteString(name)
			b.WriteByte(' ')
		}
		elem, ok := fl.spellTypeAs(tuple.Elem(i), copyTypeSpelling)
		if !ok {
			return "", false
		}
		b.WriteString(elem)
	}
	b.WriteByte(')')
	return b.String(), true
}

func (fl *fileLower) spellSignature(sig *types2.Signature, spelling typeSpelling) (string, bool) {
	var b strings.Builder
	b.WriteString("func(")
	params := sig.Params()
	last := -1
	if sig.Variadic() {
		last = params.Len() - 1
	}
	for i := range params.Len() {
		if i > 0 {
			b.WriteString(", ")
		}
		pt := params.At(i).Type()
		if i == last {
			if s, ok := types2.Unalias(pt).(*types2.Slice); ok {
				e, ok := fl.spellTypeAs(s.Elem(), spelling)
				if !ok {
					return "", false
				}
				b.WriteString("..." + e)
				continue
			}
			p, ok := fl.spellTypeAs(pt, spelling)
			if !ok {
				return "", false
			}
			b.WriteString(p + "...")
			continue
		}
		p, ok := fl.spellTypeAs(pt, spelling)
		if !ok {
			return "", false
		}
		b.WriteString(p)
	}
	b.WriteString(")")
	switch n := sig.Results().Len(); n {
	case 0:
	case 1:
		r, ok := fl.spellTypeAs(sig.Results().At(0).Type(), spelling)
		if !ok {
			return "", false
		}
		b.WriteByte(' ')
		b.WriteString(r)
	default:
		b.WriteString(" (")
		for i := range n {
			if i > 0 {
				b.WriteString(", ")
			}
			r, ok := fl.spellTypeAs(sig.Results().At(i).Type(), spelling)
			if !ok {
				return "", false
			}
			b.WriteString(r)
		}
		b.WriteString(")")
	}
	return b.String(), true
}

func (fl *fileLower) spellStruct(st *types2.Struct, spelling typeSpelling) (string, bool) {
	var b strings.Builder
	b.WriteString("struct{ ")
	for i := range st.NumFields() {
		if i > 0 {
			b.WriteString("; ")
		}
		f := st.Field(i)
		if !f.Exported() && f.Pkg() != fl.pkg.tpkg {
			return "", false
		}
		t, ok := fl.spellTypeAs(f.Type(), spelling)
		if !ok {
			return "", false
		}
		if !f.Embedded() {
			b.WriteString(f.Name())
			b.WriteByte(' ')
		}
		b.WriteString(t)
		if tag := st.Tag(i); tag != "" {
			b.WriteByte(' ')
			b.WriteString(strconv.Quote(tag))
		}
	}
	b.WriteString(" }")
	return b.String(), true
}

func (fl *fileLower) spellInterface(it *types2.Interface, spelling typeSpelling) (string, bool) {
	if it.NumExplicitMethods() == 0 && it.NumEmbeddeds() == 0 {
		return fl.spellType(it)
	}
	var b strings.Builder
	b.WriteString("interface{ ")
	sep := ""
	// Only explicitly declared methods need spelling: inherited ones
	// ride in through the embedding spelled by the second loop.
	for i := range it.NumExplicitMethods() {
		m := it.ExplicitMethod(i)
		if !m.Exported() && m.Pkg() != fl.pkg.tpkg {
			return "", false
		}
		sig, ok := types2.Unalias(m.Type()).(*types2.Signature)
		if !ok {
			return "", false
		}
		s, ok := fl.spellSignature(sig, spelling)
		if !ok {
			return "", false
		}
		b.WriteString(sep)
		b.WriteString(m.Name())
		b.WriteString(s)
		sep = "; "
	}
	for i := range it.NumEmbeddeds() {
		e, ok := fl.spellTypeAs(it.EmbeddedType(i), spelling)
		if !ok {
			return "", false
		}
		b.WriteString(sep)
		b.WriteString(e)
		sep = "; "
	}
	b.WriteString(" }")
	return b.String(), true
}

func isNilName(e syntax.Expr) bool {
	n, ok := syntax.Unparen(e).(*syntax.Name)
	return ok && n.Value == "nil"
}

func (fl *fileLower) narrowTarget(e *syntax.Name) *types2.Option {
	obj, ok := fl.pkg.info.Uses[e].(*types2.Var)
	if !ok {
		return nil
	}
	dOpt := fl.asOption(obj.Type())
	if dOpt == nil {
		return nil
	}
	if rec := fl.optType(e); rec == nil || !types2.Identical(rec, dOpt.Elem) {
		return nil
	}
	return dOpt
}

func (fl *fileLower) narrowAccess(e *syntax.Name) syntax.Expr {
	if fl.narrowDone[e] {
		return e
	}
	fl.narrowDone[e] = true
	dOpt := fl.narrowTarget(e)
	if dOpt == nil || types2.LogosOptionIsBare(dOpt.Elem) {
		return e
	}
	pos := e.Pos()
	off := fl.off(pos) + len(e.Value)
	acc := selOf(pos, e, "Value")
	fl.wrapOwner[acc] = fl.nextWrapID()
	fl.addOwned(fl.wrapOwner[acc], off, off, ".Value")
	return acc
}

func (fl *fileLower) coeType(e syntax.Expr) *types2.Option {
	if name, ok := syntax.Unparen(e).(*syntax.Name); ok {
		if o := fl.narrowTarget(name); o != nil {
			return o
		}
	}
	return fl.asOptionThroughConst(fl.optType(e))
}

func (fl *fileLower) lowerOptReceiver(x syntax.Expr) syntax.Expr {
	if name, ok := syntax.Unparen(x).(*syntax.Name); ok {
		if fl.narrowTarget(name) != nil {
			fl.narrowDone[name] = true
			return x
		}
	}
	return fl.lowerOptExpr(x)
}

func (fl *fileLower) needsWrap(v syntax.Expr, rec, target types2.Type) (wrap, zero bool) {
	if target == nil {
		return false, false
	}
	if _, multipleResults := rec.(*types2.Tuple); multipleResults {
		return false, false
	}
	tOpt := fl.asOption(target)
	if tOpt == nil {
		return false, false
	}
	if fl.isOptionNil(v) {
		return !types2.LogosOptionIsBare(tOpt.Elem), !types2.LogosOptionIsBare(tOpt.Elem)
	}
	if rec == nil || types2.Identical(rec, target) {
		return false, false
	}
	if types2.LogosOptionIsBare(tOpt.Elem) {
		return false, false
	}
	return true, false
}

func (fl *fileLower) wrapKnown(v syntax.Expr, rec, target types2.Type) syntax.Expr {
	wrap, zero := fl.needsWrap(v, rec, target)
	if !wrap {
		return v
	}
	pos := v.Pos()
	nativeTop, ejectTop, ok := fl.spellLoweredBoth(target, pos)
	if !ok {
		base.Fatalf("logos: cannot spell type %s of the option wrap at %s", target, pos.String())
	}
	if zero {
		n := syntax.Unparen(v).(*syntax.Name)
		off := fl.off(n.Pos())
		fl.add(off, off+3, ejectTop+"{}")
		typ := nativeTop
		out := at(&syntax.CompositeLit{Type: typ, Rbrace: pos}, pos)
		fl.wrapSpan[out] = span{start: off, end: off + 3}
		fl.wrapOwner[out] = fl.nextWrapID()
		return out
	}
	nativeNodes := []syntax.Expr{nativeTop}
	ejectTexts := []string{ejectTop}
	for cur := target; ; {
		elem := fl.asOption(cur).Elem
		if types2.Identical(rec, elem) {
			break
		}
		inner := fl.asOption(elem)
		if inner == nil || types2.LogosOptionIsBare(inner.Elem) {
			break
		}
		nt, et, ok := fl.spellLoweredBoth(elem, pos)
		if !ok {
			base.Fatalf("logos: cannot spell type %s of the option wrap at %s", elem, pos.String())
		}
		nativeNodes = append(nativeNodes, nt)
		ejectTexts = append(ejectTexts, et)
		cur = elem
	}
	vs := fl.wrapSpanOf(v)
	text := fl.lazyText(vs)
	id, ok := fl.wrapOwner[v]
	if !ok {
		id = fl.nextWrapID()
	} else {
		head, tail := fl.drainOwned(vs, id)
		text = cat(head, text, tail)
	}
	fused := text
	for i := len(ejectTexts) - 1; i >= 0; i-- {
		fused = cat(gen(ejectTexts[i]+"{Value: "), fused, gen(", Present: true}"))
	}
	fl.addT(vs.start, vs.end, fused)
	native := v
	for i := len(nativeNodes) - 1; i >= 0; i-- {
		typ := nativeNodes[i]
		native = at(&syntax.CompositeLit{
			Type: typ,
			ElemList: []syntax.Expr{
				at(&syntax.KeyValueExpr{Key: nameOf(pos, "Value"), Value: native}, pos),
				at(&syntax.KeyValueExpr{Key: nameOf(pos, "Present"), Value: nameOf(pos, "true")}, pos),
			},
			NKeys:  2,
			Rbrace: pos,
		}, pos)
	}
	fl.wrapSpan[native] = vs
	fl.wrapOwner[native] = id
	return native
}

func (fl *fileLower) wrapSpanOf(v syntax.Expr) span {
	if s, ok := fl.wrapSpan[v]; ok {
		return s
	}
	from := fl.off(leftmostOf(v))
	return span{start: from, end: fl.exprEndNested(from)}
}

func (fl *fileLower) drainOwned(vs span, id int) (head, tail otext) {
	ls := fl.ownerOfInsertion(vs.start)
	if le := fl.ownerOfInsertion(vs.end); le != ls {
		head = drainOwnedAt(ls, vs.start, id)
		tail = drainOwnedAt(le, vs.end, id)
		return head, tail
	}
	var rest []splice
	for _, sp := range *ls {
		if sp.owner != id || sp.start != sp.end || (sp.start != vs.start && sp.start != vs.end) {
			rest = append(rest, sp)
			continue
		}
		if sp.start == vs.start {
			head = cat(head, otext{s: sp.text, chunks: sp.chunks})
		} else {
			tail = cat(tail, otext{s: sp.text, chunks: sp.chunks})
		}
	}
	*ls = rest
	return head, tail
}

func drainOwnedAt(list *[]splice, off, id int) otext {
	var text otext
	var rest []splice
	for _, sp := range *list {
		if sp.owner != id || sp.start != sp.end || sp.start != off {
			rest = append(rest, sp)
			continue
		}
		text = cat(text, otext{s: sp.text, chunks: sp.chunks})
	}
	*list = rest
	return text
}

func (fl *fileLower) drainBoundaries(vs span) (head, tail otext) {
	return fl.drainBoundariesSince(vs, 1)
}

// drainEnclosing pulls the owned boundary inserts of wraps that enclose vs —
// a `??` closure's head and `}()` tail — so an inner construct's own wrap of
// vs fuses inside the enclosure rather than after its closing text. An owned
// boundary insert is enclosing only when its owner also holds an insert
// outside vs; an insert whose wrap lies wholly inside vs (like a narrowed
// operand's `.Value` suffix) stays pending so it lands inside the inner
// construct's text where it belongs.
func (fl *fileLower) drainEnclosing(vs span) (head, tail otext) {
	ls := fl.ownerOfInsertion(vs.start)
	le := fl.ownerOfInsertion(vs.end)
	outside := map[int]bool{}
	mark := func(list *[]splice) {
		for _, sp := range *list {
			if sp.owner != 0 && (sp.start < vs.start || sp.end > vs.end) {
				outside[sp.owner] = true
			}
		}
	}
	mark(ls)
	if le != ls {
		mark(le)
	}
	if len(outside) == 0 {
		return head, tail
	}
	take := func(list *[]splice, off int) otext {
		var text otext
		var rest []splice
		for _, sp := range *list {
			if sp.start != sp.end || sp.start != off || !outside[sp.owner] {
				rest = append(rest, sp)
				continue
			}
			text = cat(text, otext{s: sp.text, chunks: sp.chunks})
		}
		*list = rest
		return text
	}
	if ls != le {
		return take(ls, vs.start), take(le, vs.end)
	}
	var rest []splice
	for _, sp := range *ls {
		if sp.start != sp.end || !outside[sp.owner] || (sp.start != vs.start && sp.start != vs.end) {
			rest = append(rest, sp)
			continue
		}
		if sp.start == vs.start {
			head = cat(head, otext{s: sp.text, chunks: sp.chunks})
		} else {
			tail = cat(tail, otext{s: sp.text, chunks: sp.chunks})
		}
	}
	*ls = rest
	return head, tail
}

func (fl *fileLower) drainBoundariesSince(vs span, since int) (head, tail otext) {
	take := func(off int) otext {
		list := fl.ownerOfInsertion(off)
		var text otext
		var rest []splice
		for _, sp := range *list {
			if sp.owner < since || sp.start != sp.end || sp.start != off {
				rest = append(rest, sp)
				continue
			}
			text = cat(text, otext{s: sp.text, chunks: sp.chunks})
		}
		*list = rest
		return text
	}
	return take(vs.start), take(vs.end)
}

// drainBodyWrapper fuses the boundary inserts of the wrapper that owns
// body around the copied text. An insert strictly inside copied already
// composes with the region's own bytes; draining it around the copy
// would apply that wrapper text twice.
func (fl *fileLower) drainBodyWrapper(body syntax.Node, text otext, copied span) otext {
	expr, ok := body.(syntax.Expr)
	if !ok {
		return text
	}
	top, ok := syntax.Unparen(expr).(*syntax.CallExpr)
	if !ok {
		return text
	}
	_, ok = fl.wrapOwner[top]
	if !ok {
		return text
	}
	vs, ok := fl.wrapSpan[top]
	if !ok {
		return text
	}
	var head, tail otext
	for _, e := range []struct {
		off int
		dst *otext
	}{{vs.start, &head}, {vs.end, &tail}} {
		if copied.start < e.off && e.off < copied.end {
			continue
		}
		list := fl.ownerOfInsertion(e.off)
		var rest []splice
		for _, sp := range *list {
			if sp.owner == 0 || sp.start != sp.end || sp.start != e.off {
				rest = append(rest, sp)
				continue
			}
			*e.dst = cat(*e.dst, otext{s: sp.text, chunks: sp.chunks})
		}
		*list = rest
	}
	return cat(head, text, tail)
}

func (fl *fileLower) wrapperTail(e syntax.Expr) otext {
	top, ok := syntax.Unparen(e).(*syntax.CallExpr)
	if !ok {
		return otext{}
	}
	id, ok := fl.wrapOwner[top]
	if !ok {
		return otext{}
	}
	vs, ok := fl.wrapSpan[top]
	if !ok {
		return otext{}
	}
	// A parenthesized operand keeps the wrapper's closing splice
	// inside its copied span; drain only a boundary insertion.
	if vs.end != fl.exprSpan(e).end {
		return otext{}
	}
	return drainOwnedAt(fl.ownerOfInsertion(vs.end), vs.end, id)
}

// wrapperHead drains the opening zero-width splice a wrapper (such as
// the coalescing function) owned at the start of e's span, the mirror
// of wrapperTail at the end. A parenthesized operand keeps the splice
// inside its copied span, so only a boundary insertion is drained.
func (fl *fileLower) wrapperHead(e syntax.Expr) otext {
	top, ok := syntax.Unparen(e).(*syntax.CallExpr)
	if !ok {
		return otext{}
	}
	id, ok := fl.wrapOwner[top]
	if !ok {
		return otext{}
	}
	vs, ok := fl.wrapSpan[top]
	if !ok {
		return otext{}
	}
	if vs.start != fl.exprStart(e) {
		return otext{}
	}
	return drainOwnedAt(fl.ownerOfInsertion(vs.start), vs.start, id)
}

func (fl *fileLower) isMapRead(ix *syntax.IndexExpr) bool {
	if m := fl.pkg.info.Types; m != nil {
		return m[ix].HasOk()
	}
	return ix.GetTypeInfo().HasOk()
}

func (fl *fileLower) lowerMapIndex(e *syntax.IndexExpr) syntax.Expr {
	if g, ok := fl.constMapReadUse(e.X); ok {
		return fl.lowerConstMapRead(e, g)
	}
	opt := fl.asOption(fl.optType(e))
	oneValue := fl.isMapRead(e) && opt != nil && !fl.isTypeExpr(e)
	if oneValue && types2.LogosOptionIsBare(opt.Elem) {
		// A bare read keeps its Go form, so nothing is spliced, but the
		// check recorded an option type for it. Mark the semantic-only
		// change: the plain recheck re-infers the element type, and the
		// export writer accepts Go-facing types only.
		fl.changed = true
	}
	if !oneValue || types2.LogosOptionIsBare(opt.Elem) {
		e.X = fl.lowerOptExpr(e.X)
		e.Index = fl.lowerOptExpr(e.Index)
		return e
	}
	id := fl.nextWrapID()
	mStart, lbrack, rbrack := fl.lookupSpans(e)
	fl.addOwned(id, mStart, mStart, rtAlias+".Lookup(")
	rawIdx := e.Index
	idxT := fl.optType(rawIdx)
	e.X = fl.lowerOptExpr(e.X)
	e.Index = fl.lowerOptExpr(e.Index)
	fl.convertIndexKey(e, rawIdx, e.Index, idxT)
	out := fl.lookupFinish(e, lbrack, rbrack, id)
	fl.wrapOwner[out] = id
	return out
}

func logosMapKey(t types2.Type) (types2.Type, bool) {
	if t == nil {
		return nil, false
	}
	mt, ok := t.Underlying().(*types2.Map)
	if !ok {
		return nil, false
	}
	return mt.Key(), true
}

func (fl *fileLower) convertIndexKey(ix *syntax.IndexExpr, raw, lowered syntax.Expr, idxT types2.Type) {
	keyT, ok := logosMapKey(fl.optType(ix.X))
	if !ok {
		return
	}
	if idxT == nil || types2.Identical(idxT, keyT) || !types2.AssignableTo(idxT, keyT) {
		return
	}
	kn, kt, ok := fl.spellLoweredBoth(keyT, raw.Pos())
	if !ok {
		base.Fatalf("logos: cannot spell map key type %s at %s", keyT, raw.Pos().String())
	}
	kStart := fl.off(leftmostOf(raw))
	if cl, ok := syntax.Unparen(raw).(*syntax.CompositeLit); ok && cl.Type != nil {
		kStart = fl.off(cl.Type.Pos())
	}
	ix.Index = callOf(raw.Pos(), kn, []syntax.Expr{lowered})
	kEnd := fl.exprEndKey(kStart)
	fl.add(kStart, kStart, kt+"(")
	fl.add(kEnd, kEnd, ")")
}

func (fl *fileLower) exprEndKey(from int) int {
	end := len(fl.src)
	depth := 0
	seen := false
	scanSegment(fl.src, from, len(fl.src), func(t tok) bool {
		if t.comment {
			return true
		}
		if t.auto {
			if depth == 0 && seen {
				end = t.start
				return false
			}
			return true
		}
		seen = true
		switch t.kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			if depth == 0 {
				end = t.start
				return false
			}
			depth--
		case token.COMMA, token.SEMICOLON, token.COLON, token.ASSIGN, token.DEFINE:
			if depth == 0 {
				end = t.start
				return false
			}
		case token.EOF:
			end = len(fl.src)
			return false
		}
		return true
	})
	return end
}

func (fl *fileLower) lookupSpans(e *syntax.IndexExpr) (mStart, lbrack, rbrack int) {
	mStart = fl.off(fl.optLeftmost(e.X))
	kStart := fl.off(leftmostOf(e.Index))
	lbrack = fl.findLbrack(mStart, kStart)
	rbrack = fl.findRbrack(kStart)
	return mStart, lbrack, rbrack
}

func (fl *fileLower) lookupFinish(e *syntax.IndexExpr, lbrack, rbrack int, id int) syntax.Expr {
	pos := e.Pos()
	fl.needRT = true
	call := callOf(pos, selOf(pos, nameOf(pos, rtAlias), "Lookup"), []syntax.Expr{e.X, e.Index})
	fl.add(lbrack, lbrack+1, ", ")
	fl.add(rbrack, rbrack+1, ")")
	mStart := fl.off(fl.optLeftmost(e.X))
	if s, ok := fl.wrapSpan[e.X]; ok {
		mStart = s.start
	}
	fl.wrapSpan[call] = span{start: mStart, end: rbrack + 1}
	return call
}

func (fl *fileLower) lowerCoalesce(e *syntax.CoalesceExpr) syntax.Expr {
	opt := fl.coeType(e.X)
	ux, isIndex := syntax.Unparen(e.X).(*syntax.IndexExpr)
	mapRead := isIndex && fl.isMapRead(ux)
	if mapRead {
		if _, ok := fl.constMapReadUse(ux.X); ok {
			mapRead = false
		}
	}
	if opt == nil && mapRead {
		opt = fl.asOption(fl.optType(ux))
	}
	if opt == nil {
		e.X = fl.lowerOptExpr(e.X)
		e.Y = fl.lowerOptExpr(e.Y)
		return e
	}
	closure := fl.newGroupClosure(e)
	pos := e.Pos()
	id := fl.nextWrapID()
	resultType, etext, ok := fl.spellLoweredBoth(opt.Elem, pos)
	if !ok {
		base.Fatalf("logos: cannot spell type %s of the ?? expression at %s", opt.Elem, pos.String())
	}
	signature := at(&syntax.FuncType{
		ParamList:  []*syntax.Field{},
		ResultList: []*syntax.Field{at(&syntax.Field{Type: resultType}, pos)},
	}, pos)
	qoff := fl.off(pos)
	yStart := fl.off(leftmostOf(e.Y))
	through := yStart
	if rbrace, ok := lastBraceOf(e.Y); ok {
		through = fl.off(rbrace) + 1
	}
	yEnd := fl.exprEndThrough(yStart, through)
	fl.add(qoff, qoff+2, "")
	if mapRead {
		mStart, _, rbrack := fl.lookupSpans(ux)
		fl.deleteGroupingParens(mStart, qoff)
		xStart := fl.off(fl.optHeadStart(e.X))
		xEnd := rbrack + 1
		wy := fl.wrapKnown(fl.lowerOptExpr(e.Y), fl.optType(e.Y), opt.Elem)
		rawIdx := ux.Index
		idxT := fl.optType(rawIdx)
		ux.X = fl.lowerOptExpr(ux.X)
		ux.Index = fl.lowerOptExpr(ux.Index)
		fl.convertIndexKey(ux, rawIdx, ux.Index, idxT)
		fl.addOwned(id, xStart, xStart, closure.head("func() "+etext, []string{etext})+" { "+closure.prefix(nil)+"if "+optTemp+", "+optOk+" := ")
		commaOKIndex := at(&syntax.IndexExpr{X: ux.X, Index: ux.Index}, ux.Pos())
		init := defineOf(pos, []syntax.Expr{nameOf(pos, optTemp), nameOf(pos, optOk)}, []syntax.Expr{commaOKIndex})
		body := []syntax.Stmt{
			ifOf(pos, init, nameOf(pos, optOk), []syntax.Stmt{returnOf(pos, []syntax.Expr{nameOf(pos, optTemp)})}),
			returnOf(pos, []syntax.Expr{wy}),
		}
		lit := at(&syntax.FuncLit{Type: signature, Body: blockOf(pos, body)}, pos)
		fl.addOwned(id, xEnd, xEnd, "; "+optOk+" { return "+optTemp+closure.success()+" }; "+closure.prefix(body[1])+"return ")
		out := callOf(pos, lit, nil)
		fl.wrapSpan[out] = span{start: xStart, end: yEnd}
		fl.wrapOwner[out] = id
		fl.addOwned(id, yEnd, yEnd, closure.success()+" }()")
		closure.bind(out, fl.wrapSpan[out], id)
		return out
	}
	bare := types2.LogosOptionIsBare(opt.Elem)
	xStart := fl.off(fl.optHeadStart(e.X))
	openParen, closeParen := headerParens(e.X)
	fl.addOwned(id, xStart, xStart, closure.head("func() "+etext, []string{etext})+" { "+closure.prefix(nil)+"if "+optTemp+" := "+openParen)
	nx := fl.lowerOptReceiver(e.X)
	wy := fl.wrapKnown(fl.lowerOptExpr(e.Y), fl.optType(e.Y), opt.Elem)
	xEnd := lastTokenEnd(fl.src, xStart, qoff)
	init := defineOf(pos, []syntax.Expr{nameOf(pos, optTemp)}, []syntax.Expr{nx})
	var cond syntax.Expr
	var taken syntax.Expr
	var mid string
	if bare {
		cond = notNil(pos, nameOf(pos, optTemp))
		taken = nameOf(pos, optTemp)
		mid = "; " + optTemp + " != nil { return " + optTemp + closure.success() + " }; "
	} else {
		cond = selOf(pos, nameOf(pos, optTemp), "Present")
		taken = selOf(pos, nameOf(pos, optTemp), "Value")
		mid = "; " + optTemp + ".Present { return " + optTemp + ".Value" + closure.success() + " }; "
	}
	body := []syntax.Stmt{
		ifOf(pos, init, cond, []syntax.Stmt{returnOf(pos, []syntax.Expr{taken})}),
		returnOf(pos, []syntax.Expr{wy}),
	}
	lit := at(&syntax.FuncLit{Type: signature, Body: blockOf(pos, body)}, pos)
	fl.addOwned(id, xEnd, xEnd, closeParen+mid+closure.prefix(body[1])+"return ")
	out := callOf(pos, lit, nil)
	fl.wrapSpan[out] = span{start: xStart, end: yEnd}
	fl.wrapOwner[out] = id
	fl.addOwned(id, yEnd, yEnd, closure.success()+" }()")
	closure.bind(out, fl.wrapSpan[out], id)
	return out
}

func headerParens(e syntax.Expr) (string, string) {
	found := false
	syntax.Inspect(e, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit:
			return false
		case *syntax.CompositeLit:
			if n.Type != nil {
				found = true
			}
		}
		return !found
	})
	if found {
		return "(", ")"
	}
	return "", ""
}

func lastBraceOf(e syntax.Expr) (syntax.Pos, bool) {
	switch n := e.(type) {
	case *syntax.CompositeLit:
		return n.Rbrace, true
	case *syntax.FuncLit:
		return n.Body.Rbrace, true
	case *syntax.SelectorExpr:
		return lastBraceOf(n.X)
	case *syntax.IndexExpr:
		return lastBraceOf(n.X)
	case *syntax.SliceExpr:
		return lastBraceOf(n.X)
	case *syntax.AssertExpr:
		return lastBraceOf(n.X)
	case *syntax.CallExpr:
		return lastBraceOf(n.Fun)
	case *syntax.SwitchExpr:
		return n.Rbrace, true
	case *syntax.LambdaExpr:
		if block, ok := n.Body.(*syntax.BlockStmt); ok {
			return block.Rbrace, true
		}
		if body, ok := n.Body.(syntax.Expr); ok {
			return lastBraceOf(body)
		}
	case *syntax.CoalesceExpr:
		if pos, ok := lastBraceOf(n.Y); ok {
			return pos, true
		}
		return lastBraceOf(n.X)
	case *syntax.OptSelectExpr:
		return lastBraceOf(n.X)
	case *syntax.Operation:
		if n.Y != nil {
			if pos, ok := lastBraceOf(n.Y); ok {
				return pos, true
			}
		}
		return lastBraceOf(n.X)
	}
	return syntax.Pos{}, false
}

func (fl *fileLower) lowerOptSelect(e *syntax.OptSelectExpr) syntax.Expr {
	pos := e.Pos()
	id := fl.nextWrapID()
	ox := fl.coeType(e.X)
	ro := fl.asOption(fl.optType(e))
	if ox == nil || ro == nil {
		e.X = fl.lowerOptExpr(e.X)
		return e
	}
	closure := fl.newGroupClosure(e)
	xStart := fl.off(fl.optHeadStart(e.X))
	nativeR, ejectR, ok := fl.spellLoweredBoth(fl.optType(e), pos)
	if !ok {
		base.Fatalf("logos: cannot spell type %s of the ?. expression at %s", fl.optType(e), pos.String())
	}
	rText := ejectR
	openParen, closeParen := headerParens(e.X)
	fl.addOwned(id, xStart, xStart, closure.head("func() "+ejectR, []string{ejectR})+" { "+closure.prefix(nil)+"if "+optTemp+" := "+openParen)
	nx := fl.lowerOptReceiver(e.X)
	bareRecv := types2.LogosOptionIsBare(ox.Elem)
	accessRec := ro.Elem
	obj, _, _ := types2.LookupFieldOrMethod(optElemBase(ox.Elem), true, fl.pkg.tpkg, fl.optSelectName(e))
	if obj == nil {
		obj, _, _ = types2.LookupFieldOrMethod(tupleUnderlying(optElemBase(ox.Elem)).Underlying(), true, fl.pkg.tpkg, fl.optSelectName(e))
	}
	if obj != nil && fl.asOption(obj.Type()) != nil {
		accessRec = ro
	}
	recv := nameOf(pos, optTemp)
	var acc syntax.Expr
	var accSuffix string
	if bareRecv {
		acc, accSuffix = fl.selectAccessBare(recv, e)
	} else {
		acc, accSuffix = fl.selectAccessPresent(recv, e)
	}
	some, someHead, someTail := fl.wrapParts(acc, accessRec, ro, pos)
	if obj != nil && accessRec == ro && !types2.LogosOptionIsBare(ro.Elem) {
		if ft, ok := fl.spellLowered(obj.Type()); ok && ft != rText {
			some = at(&syntax.CompositeLit{
				Type: nativeR,
				ElemList: []syntax.Expr{
					at(&syntax.KeyValueExpr{Key: nameOf(pos, "Value"), Value: selOf(pos, acc, "Value")}, pos),
					at(&syntax.KeyValueExpr{Key: nameOf(pos, "Present"), Value: selOf(pos, acc, "Present")}, pos),
				},
				NKeys:  2,
				Rbrace: pos,
			}, pos)
			someHead = rText + "{Value: "
			someTail = ".Value, Present: " + optTemp + accSuffix + ".Present}"
		}
	}
	var none syntax.Expr
	var noneT string
	if types2.LogosOptionIsBare(ro.Elem) {
		none = nilOf(pos)
		noneT = "nil"
	} else {
		noneT = rText + "{}"
		none = at(&syntax.CompositeLit{Type: nativeR, Rbrace: pos}, pos)
	}
	var cond syntax.Expr
	var condT string
	if bareRecv {
		cond = notNil(pos, nameOf(pos, optTemp))
		condT = optTemp + " != nil"
	} else {
		cond = selOf(pos, nameOf(pos, optTemp), "Present")
		condT = optTemp + ".Present"
	}
	resultType := nativeR
	signature := at(&syntax.FuncType{
		ParamList:  []*syntax.Field{},
		ResultList: []*syntax.Field{at(&syntax.Field{Type: resultType}, pos)},
	}, pos)
	init := defineOf(pos, []syntax.Expr{nameOf(pos, optTemp)}, []syntax.Expr{nx})
	body := []syntax.Stmt{
		ifOf(pos, init, cond, []syntax.Stmt{returnOf(pos, []syntax.Expr{some})}),
		returnOf(pos, []syntax.Expr{none}),
	}
	lit := at(&syntax.FuncLit{Type: signature, Body: blockOf(pos, body)}, pos)
	xEnd := lastTokenEnd(fl.src, xStart, fl.off(pos))
	selEnd := fl.off(e.Sel.Pos()) + len(e.Sel.Value)
	fl.add(xEnd, selEnd, closeParen+"; "+condT+" { return "+someHead+optTemp+accSuffix+someTail+closure.success()+" }; return "+noneT+closure.success()+" }()")
	out := callOf(pos, lit, nil)
	fl.wrapSpan[out] = span{start: xStart, end: selEnd}
	fl.wrapOwner[out] = id
	closure.bind(out, fl.wrapSpan[out], id)
	return out
}

func (fl *fileLower) wrapParts(acc syntax.Expr, rec, target types2.Type, pos syntax.Pos) (some syntax.Expr, head, tail string) {
	wrap, _ := fl.needsWrap(acc, rec, target)
	if !wrap {
		return acc, "", ""
	}
	typ, tText, ok := fl.spellLoweredBoth(target, pos)
	if !ok {
		base.Fatalf("logos: cannot spell type %s of the ?. value at %s", target, pos.String())
	}
	return at(&syntax.CompositeLit{
		Type: typ,
		ElemList: []syntax.Expr{
			at(&syntax.KeyValueExpr{Key: nameOf(pos, "Value"), Value: acc}, pos),
			at(&syntax.KeyValueExpr{Key: nameOf(pos, "Present"), Value: nameOf(pos, "true")}, pos),
		},
		NKeys:  2,
		Rbrace: pos,
	}, pos), tText + "{Value: ", ", Present: true}"
}

func (fl *fileLower) lowerOptCallExpr(e *syntax.CallExpr) syntax.Expr {
	if osel, ok := syntax.Unparen(e.Fun).(*syntax.OptSelectExpr); ok {
		return fl.lowerOptCall(e, osel)
	}
	e.Fun = fl.lowerOptExpr(e.Fun)
	sig := fl.callSig(e)
	var targets []types2.Type
	if sig != nil {
		targets = fl.callTargets(e, sig)
	}
	if len(e.ArgList) == 1 && sig != nil && fl.methodExprCallee(e.Fun) == nil {
		// A lone multi-result argument fills every parameter; per-element
		// targets decide which results need an option wrap.
		if tup, ok := types2.Unalias(fl.optType(e.ArgList[0])).(*types2.Tuple); ok && tup.Len() > 1 {
			params := sig.Params()
			ts := make([]types2.Type, tup.Len())
			for i := range ts {
				ts[i] = fl.paramTarget(params, sig.Variadic(), e.HasDots, tup.Len(), i)
			}
			e.ArgList[0] = fl.wrapTuple(fl.lowerOptExpr(e.ArgList[0]), tupleTypes(tup), ts)
			return e
		}
	}
	for i, a := range e.ArgList {
		la := fl.lowerOptExpr(a)
		if targets != nil {
			la = fl.wrapKnown(la, fl.optType(a), targets[i])
		}
		e.ArgList[i] = la
	}
	return e
}

// optSigType strips the const-view and readonly-descendants wrappers the
// checker records on a callback's type so the signature underneath is
// visible: calling through a view is unqualified at the call site, so the
// argument wrapping needs the same unqualified signature.
func optSigType(t types2.Type) *types2.Signature {
	for {
		u := types2.Unalias(t)
		switch q := u.(type) {
		case *types2.ConstView:
			t = q.Elem
		case *types2.ReadonlyDescendants:
			t = q.Elem
		default:
			sig, _ := u.(*types2.Signature)
			return sig
		}
	}
}

func (fl *fileLower) callSig(e *syntax.CallExpr) *types2.Signature {
	if sig := fl.instCallSig(e.Fun); sig != nil {
		return sig
	}
	switch fun := syntax.Unparen(e.Fun).(type) {
	case *syntax.Name:
		if obj := fl.pkg.info.Uses[fun]; obj != nil {
			if fn, ok := obj.(*types2.Func); ok {
				if sig := optSigType(fn.Type()); sig != nil {
					return sig
				}
			}
		}
		// Builtins record their effective signature on the Name (append
		// carries ...E, delete carries the map's key), and a func-typed
		// variable names its own signature.
		if sig, ok := types2.Unalias(fl.optType(fun)).(*types2.Signature); ok {
			return sig
		}
		return nil
	case *syntax.SelectorExpr:
		if sel := fl.pkg.info.Selections[fun]; sel != nil {
			if fn, ok := sel.Obj().(*types2.Func); ok {
				if sig := optSigType(fn.Type()); sig != nil {
					return sig
				}
			}
		}
		return optSigType(fl.optType(fun))
	default:
		return optSigType(fl.optType(fun))
	}
}

// unindexedCallee strips parentheses and explicit type arguments from a
// callee expression, leaving the name or method selector that identifies it.
// Every generic call form, indexed or not, method value or method expression,
// reduces through this one place, so the receiver offset and the
// instantiation lookup cannot disagree.
func unindexedCallee(fun syntax.Expr) syntax.Expr {
	for {
		indexed, ok := syntax.Unparen(fun).(*syntax.IndexExpr)
		if !ok {
			break
		}
		fun = indexed.X
	}
	return fun
}

// methodExprCallee returns the method selector of a callee written as a
// method expression, Type.Method with or without explicit type arguments,
// whose receiver is the first call argument. A method value call (x.M) or a
// plain selector call returns nil: its argument list has no receiver.
func (fl *fileLower) methodExprCallee(fun syntax.Expr) *syntax.SelectorExpr {
	sel, ok := syntax.Unparen(unindexedCallee(fun)).(*syntax.SelectorExpr)
	if !ok || !fl.isTypeExpr(sel.X) {
		return nil
	}
	return sel
}

// instCallSig returns the instantiated signature of a generic callee. The
// generic signature names the callee's type parameters, which are not in
// scope at the call, and argument lowering spells the parameter type into
// the call site, so only this call's instantiation gives spellable types.
func (fl *fileLower) instCallSig(fun syntax.Expr) *types2.Signature {
	var name *syntax.Name
	switch callee := syntax.Unparen(unindexedCallee(fun)).(type) {
	case *syntax.Name:
		name = callee
	case *syntax.SelectorExpr:
		name = callee.Sel
	}
	if name == nil {
		return nil
	}
	inst, ok := fl.pkg.info.Instances[name]
	if !ok {
		return nil
	}
	sig, _ := inst.Type.(*types2.Signature)
	return sig
}

func (fl *fileLower) callTargets(e *syntax.CallExpr, sig *types2.Signature) []types2.Type {
	params := sig.Params()
	n := len(e.ArgList)
	targets := make([]types2.Type, n)
	shift := 0
	if fl.methodExprCallee(e.Fun) != nil {
		if recv := sig.Recv(); recv != nil && n > 0 {
			targets[0] = recv.Type()
			shift = 1
		}
	}
	for i := range targets {
		targets[i] = fl.paramTarget(params, sig.Variadic(), e.HasDots, n, i-shift)
	}
	return targets
}

func (fl *fileLower) isTypeExpr(e syntax.Expr) bool {
	if tv, ok := fl.pkg.info.Types[e]; ok {
		return tv.IsType()
	}
	return e.GetTypeInfo().IsType()
}

func (fl *fileLower) paramTarget(params *types2.Tuple, variadic, dots bool, nargs, i int) types2.Type {
	if i < 0 {
		return nil
	}
	np := params.Len()
	if !variadic {
		if i < np {
			return params.At(i).Type()
		}
		return nil
	}
	if np == 0 {
		return nil
	}
	if dots {
		if i < np {
			return params.At(i).Type()
		}
		return nil
	}
	if i < np-1 {
		return params.At(i).Type()
	}
	if s, ok := types2.Unalias(params.At(np - 1).Type()).(*types2.Slice); ok {
		return s.Elem()
	}
	return nil
}

func (fl *fileLower) lowerOptCall(e *syntax.CallExpr, osel *syntax.OptSelectExpr) syntax.Expr {
	pos := osel.Pos()
	id := fl.nextWrapID()
	ox := fl.coeType(osel.X)
	if ox == nil {
		e.Fun = fl.lowerOptExpr(e.Fun)
		for i, a := range e.ArgList {
			e.ArgList[i] = fl.lowerOptExpr(a)
		}
		return e
	}
	sig := fl.optCallSig(ox, osel.Sel.Value)
	if sig == nil {
		osel.X = fl.lowerOptReceiver(osel.X)
		e.Fun = osel
		for i, a := range e.ArgList {
			e.ArgList[i] = fl.lowerOptExpr(a)
		}
		return e
	}
	closure := fl.newGroupClosure(e)
	xStart := fl.off(fl.optLeftmost(osel.X))
	rs := sig.Results()
	n := rs.Len()
	rTexts := make([]string, n)
	nrNodes := make([]syntax.Expr, n)
	roList := make([]types2.Type, n)
	recList := make([]types2.Type, n)
	for i := range n {
		r := rs.At(i).Type()
		ro := fl.resultOption(r)
		recList[i] = r
		roList[i] = ro
		nt, t, ok := fl.spellLoweredBoth(ro, pos)
		if !ok {
			base.Fatalf("logos: cannot spell type %s of the ?. call at %s", ro, pos.String())
		}
		nrNodes[i] = nt
		rTexts[i] = t
	}
	var headText string
	switch n {
	case 0:
		headText = "func()"
	case 1:
		headText = "func() " + rTexts[0]
	default:
		headText = "func() (" + strings.Join(rTexts, ", ") + ")"
	}
	fl.addOwned(id, xStart, xStart, closure.head(headText, rTexts)+" { "+closure.prefix(nil)+"if "+optTemp+" := ")
	nx := fl.lowerOptReceiver(osel.X)
	params := sig.Params()
	for i, a := range e.ArgList {
		la := fl.lowerOptExpr(a)
		e.ArgList[i] = fl.wrapKnown(la, fl.optType(a), fl.paramTarget(params, sig.Variadic(), e.HasDots, len(e.ArgList), i))
	}
	bareRecv := types2.LogosOptionIsBare(ox.Elem)
	recv := nameOf(pos, optTemp)
	var racc syntax.Expr
	var accSuffix string
	if bareRecv {
		racc, accSuffix = fl.selectAccessBare(recv, osel)
	} else {
		racc, accSuffix = fl.selectAccessPresent(recv, osel)
	}
	var cond syntax.Expr
	var condT string
	if bareRecv {
		cond = notNil(pos, nameOf(pos, optTemp))
		condT = optTemp + " != nil"
	} else {
		cond = selOf(pos, nameOf(pos, optTemp), "Present")
		condT = optTemp + ".Present"
	}
	xEnd := lastTokenEnd(fl.src, xStart, fl.off(pos))
	selEnd := fl.off(osel.Sel.Pos()) + len(osel.Sel.Value)
	lparen, rparen := fl.findCallParens(selEnd)
	argsText := fl.lazyText(span{start: lparen, end: rparen + 1})
	init := defineOf(pos, []syntax.Expr{nameOf(pos, optTemp)}, []syntax.Expr{nx})
	call := callOf(pos, racc, e.ArgList)
	switch rs.Len() {
	case 0:
		invoke := exprStmtOf(pos, call)
		lit := at(&syntax.FuncLit{
			Type: at(&syntax.FuncType{}, pos),
			Body: blockOf(pos, []syntax.Stmt{
				ifOf(pos, init, cond, []syntax.Stmt{invoke}),
			}),
		}, pos)
		fl.addT(xEnd, rparen+1, cat(gen("; "+condT+" { "+closure.prefix(invoke)+optTemp+accSuffix), argsText, gen(" }; "+closure.voidReturn()+"}()")))
		out := callOf(pos, lit, nil)
		fl.carryType(out, e)
		fl.wrapSpan[out] = span{start: xStart, end: rparen + 1}
		fl.wrapOwner[out] = id
		closure.bind(out, fl.wrapSpan[out], id)
		return out
	case 1:
		some, someHead, someTail := fl.wrapParts(call, recList[0], roList[0], pos)
		var none syntax.Expr
		var noneT string
		if fl.bareResult(roList[0]) {
			none = nilOf(pos)
			noneT = "nil"
		} else {
			noneT = rTexts[0] + "{}"
			none = at(&syntax.CompositeLit{Type: nrNodes[0], Rbrace: pos}, pos)
		}
		resultType := nrNodes[0]
		signature := at(&syntax.FuncType{
			ParamList:  []*syntax.Field{},
			ResultList: []*syntax.Field{at(&syntax.Field{Type: resultType}, pos)},
		}, pos)
		body := []syntax.Stmt{
			ifOf(pos, init, cond, []syntax.Stmt{returnOf(pos, []syntax.Expr{some})}),
			returnOf(pos, []syntax.Expr{none}),
		}
		lit := at(&syntax.FuncLit{Type: signature, Body: blockOf(pos, body)}, pos)
		fl.addT(xEnd, rparen+1, cat(
			gen("; "+condT+" { "+closure.prefix(body[0].(*syntax.IfStmt).Then.List[0])+"return "+someHead+optTemp+accSuffix),
			argsText,
			gen(someTail+closure.success()+" }; return "+noneT+closure.success()+" }()"),
		))
		out := callOf(pos, lit, nil)
		fl.carryType(out, e)
		fl.wrapSpan[out] = span{start: xStart, end: rparen + 1}
		fl.wrapOwner[out] = id
		closure.bind(out, fl.wrapSpan[out], id)
		return out
	default:
		fields := make([]*syntax.Field, n)
		for i := range n {
			fields[i] = at(&syntax.Field{Type: nrNodes[i]}, pos)
		}
		signature := at(&syntax.FuncType{
			ParamList:  []*syntax.Field{},
			ResultList: fields,
		}, pos)
		tmps := make([]syntax.Expr, n)
		rets := make([]syntax.Expr, n)
		noneNodes := make([]syntax.Expr, n)
		noneList := make([]string, n)
		var headNames, retWraps []string
		for i := range n {
			tmp := fmt.Sprintf(optWrapTemp, i)
			tmps[i] = nameOf(pos, tmp)
			some, sh, st := fl.wrapParts(tmps[i], recList[i], roList[i], pos)
			rets[i] = some
			headNames = append(headNames, tmp)
			retWraps = append(retWraps, sh+tmp+st)
			if fl.bareResult(roList[i]) {
				noneNodes[i] = nilOf(pos)
				noneList[i] = "nil"
			} else {
				noneNodes[i] = at(&syntax.CompositeLit{Type: nrNodes[i], Rbrace: pos}, pos)
				noneList[i] = rTexts[i] + "{}"
			}
		}
		then := []syntax.Stmt{
			defineOf(pos, tmps, []syntax.Expr{call}),
			returnOf(pos, rets),
		}
		body := []syntax.Stmt{
			ifOf(pos, init, cond, then),
			returnOf(pos, noneNodes),
		}
		lit := at(&syntax.FuncLit{Type: signature, Body: blockOf(pos, body)}, pos)
		fl.addT(xEnd, rparen+1, cat(
			gen("; "+condT+" { "+closure.prefix(then[0])+strings.Join(headNames, ", ")+" := "+optTemp+accSuffix),
			argsText,
			gen("; return "+strings.Join(retWraps, ", ")+closure.success()+" }; return "+strings.Join(noneList, ", ")+closure.success()+" }()"),
		))
		out := callOf(pos, lit, nil)
		fl.carryType(out, e)
		fl.wrapSpan[out] = span{start: xStart, end: rparen + 1}
		fl.wrapOwner[out] = id
		closure.bind(out, fl.wrapSpan[out], id)
		return out
	}
}

type optDeferGoWork struct {
	osel *syntax.OptSelectExpr
	ox   *types2.Option
	nx   syntax.Expr
	lv   string
	rpos syntax.Pos
}

func (fl *fileLower) markOptDeferGo(s *syntax.CallStmt) bool {
	e, ok := syntax.Unparen(s.Call).(*syntax.CallExpr)
	if !ok {
		return false
	}
	osel, ok := syntax.Unparen(e.Fun).(*syntax.OptSelectExpr)
	if !ok {
		return false
	}
	ox := fl.coeType(osel.X)
	if ox == nil {
		return false
	}
	sig := fl.optCallSig(ox, osel.Sel.Value)
	if sig == nil {
		return false
	}
	lv := fmt.Sprintf("_logos_d%d", fl.nextWrapID())
	rpos := fl.optLeftmost(osel.X)
	osel.X = fl.lowerOptReceiver(osel.X)
	for i, a := range e.ArgList {
		target := fl.paramTarget(sig.Params(), sig.Variadic(), e.HasDots, len(e.ArgList), i)
		e.ArgList[i] = fl.wrapKnown(fl.lowerOptExpr(a), fl.optType(a), target)
	}
	if fl.optDeferGoPend == nil {
		fl.optDeferGoPend = make(map[*syntax.CallExpr]optDeferGoWork)
	}
	fl.optDeferGoPend[e] = optDeferGoWork{osel: osel, ox: ox, nx: osel.X, lv: lv, rpos: rpos}
	return true
}

func optElemBase(t types2.Type) types2.Type {
	if r, ok := types2.Unalias(t).(*types2.ReadonlyDescendants); ok {
		t = r.Elem
	}
	if c, ok := types2.Unalias(t).(*types2.ConstView); ok {
		t = c.Elem
	}
	return t
}

func (fl *fileLower) optCallSig(ox *types2.Option, name string) *types2.Signature {
	obj, _, _ := types2.LookupFieldOrMethod(optElemBase(ox.Elem), true, fl.pkg.tpkg, name)
	if obj == nil {
		return nil
	}
	sig, _ := obj.Type().Underlying().(*types2.Signature)
	return sig
}

func (fl *fileLower) lowerOptDeferGo(s *syntax.CallStmt, lowerCall func(syntax.Stmt) []syntax.Stmt) ([]syntax.Stmt, bool) {
	return fl.lowerOptDeferGoWith(s, lowerCall, fl.moveOptReceiver)
}

func (fl *fileLower) lowerOptDeferGoCopied(s *syntax.CallStmt) ([]syntax.Stmt, bool) {
	keep := func(c syntax.Stmt) []syntax.Stmt { return []syntax.Stmt{c} }
	return fl.lowerOptDeferGoWith(s, keep, fl.copyOptReceiver)
}

func (fl *fileLower) moveOptReceiver(r span, stub string) otext {
	return gen(fl.moveRegion(r, stub))
}

func (fl *fileLower) copyOptReceiver(r span, stub string) otext {
	text := fl.textOf(r)
	fl.add(r.start, r.end, stub)
	return text
}

func (fl *fileLower) lowerOptDeferGoWith(s *syntax.CallStmt, lowerCall func(syntax.Stmt) []syntax.Stmt, captureRecv func(span, string) otext) ([]syntax.Stmt, bool) {
	e, ok := syntax.Unparen(s.Call).(*syntax.CallExpr)
	if !ok {
		return nil, false
	}
	w, ok := fl.optDeferGoPend[e]
	if !ok {
		return nil, false
	}
	delete(fl.optDeferGoPend, e)
	pos := w.osel.Pos()
	rpos := w.rpos
	recvName := nameOf(rpos, w.lv)
	var cond, callee syntax.Expr
	var condT, accSuffix string
	if types2.LogosOptionIsBare(w.ox.Elem) {
		cond, condT = notNil(pos, nameOf(pos, w.lv)), w.lv+" != nil"
		callee, accSuffix = fl.selectAccessBare(recvName, w.osel)
	} else {
		cond, condT = selOf(pos, nameOf(pos, w.lv), "Present"), w.lv+".Present"
		callee, accSuffix = fl.selectAccessPresent(recvName, w.osel)
	}
	xStart := fl.off(rpos)
	xEnd := lastTokenEnd(fl.src, xStart, fl.off(pos))
	selEnd := fl.off(w.osel.Sel.Pos()) + len(w.osel.Sel.Value)
	_, rparen := fl.findCallParens(selEnd)
	recv := span{start: xStart, end: xEnd}
	if vs, ok := fl.wrapSpan[w.nx]; ok {
		recv = vs
	}
	var head, tail otext
	if id, ok := fl.wrapOwner[w.nx]; ok {
		head, tail = fl.drainOwned(recv, id)
	}
	captured := captureRecv(recv, w.lv)
	fl.add(xEnd, selEnd, accSuffix)
	stmtStart := fl.off(s.Pos())
	fl.addT(stmtStart, stmtStart, cat(gen(w.lv+" := "), head, captured, tail, gen("; if "+condT+" { ")))
	define := defineOf(pos, []syntax.Expr{nameOf(pos, w.lv)}, []syntax.Expr{w.nx})
	e.Fun = callee
	guarded := lowerCall(s)
	fl.add(rparen+1, rparen+1, " }")
	scanForLiterals(fl, define)
	return []syntax.Stmt{define, ifOf(s.Pos(), nil, cond, guarded)}, true
}

func (fl *fileLower) lowerOptDeferGoIn(block *syntax.BlockStmt) {
	lower := func(list []syntax.Stmt) []syntax.Stmt {
		var out []syntax.Stmt
		for _, st := range list {
			if cs, ok := st.(*syntax.CallStmt); ok {
				if lowered, ok := fl.lowerOptDeferGoCopied(cs); ok {
					out = append(out, lowered...)
					continue
				}
			}
			out = append(out, st)
		}
		return out
	}
	syntax.Inspect(block, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncLit, *syntax.LambdaExpr:
			return false
		case *syntax.BlockStmt:
			n.List = lower(n.List)
		case *syntax.CaseClause:
			n.Body = lower(n.Body)
		case *syntax.CommClause:
			n.Body = lower(n.Body)
		}
		return true
	})
}

func (fl *fileLower) resultOption(r types2.Type) types2.Type {
	if fl.asOption(r) != nil {
		return r
	}
	return &types2.Option{Elem: r}
}

func (fl *fileLower) bareResult(ro types2.Type) bool {
	return types2.LogosOptionIsBare(fl.asOption(ro).Elem)
}

func (fl *fileLower) lowerNilCompare(e *syntax.Operation) (syntax.Expr, bool) {
	if e.Op != syntax.Eql && e.Op != syntax.Neq {
		return nil, false
	}
	if e.X == nil || e.Y == nil {
		return nil, false
	}
	lx, rx := syntax.Unparen(e.X), syntax.Unparen(e.Y)
	lnil := fl.isOptionNil(lx)
	rnil := fl.isOptionNil(rx)
	if lnil == rnil {
		return nil, false
	}
	var xside, nilside syntax.Expr
	if rnil {
		xside, nilside = e.X, rx
	} else {
		xside, nilside = e.Y, lx
	}
	ox := fl.asOption(fl.optType(xside))
	if ox == nil || types2.LogosOptionIsBare(ox.Elem) {
		return nil, false
	}
	pos := e.Pos()
	present := selOf(pos, xside, "Present")
	var out syntax.Expr = present
	if e.Op == syntax.Eql {
		out = at(&syntax.Operation{Op: syntax.Not, X: present}, pos)
	}
	bang := ""
	if e.Op == syntax.Eql {
		bang = "!"
	}
	xs := fl.off(syntax.StartPos(xside))
	if rnil {
		op := fl.findCmpOp(xs, fl.off(leftmostOf(nilside)))
		fl.add(xs, xs, bang)
		fl.add(op, fl.nilEnd(op, fl.off(syntax.EndPos(nilside))), ".Present")
	} else {
		fl.add(fl.off(e.X.Pos()), xs, bang)
		fl.add(fl.operandEnd(xs), fl.operandEnd(xs), ".Present")
	}
	return out, true
}

func (fl *fileLower) nilEnd(op, inner int) int {
	end := inner
	depth := 0
	scanSegment(fl.src, op, end, func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		if t.kind == token.LPAREN {
			depth++
		}
		return true
	})
	for range depth {
		matched := false
		scanSegment(fl.src, end, len(fl.src), func(t tok) bool {
			if t.comment || t.auto {
				return true
			}
			if t.kind == token.RPAREN {
				end = t.span.end
				matched = true
			}
			return false
		})
		if !matched {
			base.Fatalf("logos: parenthesized nil at offset %d has no )", op)
		}
	}
	return end
}

func (fl *fileLower) findCmpOp(from, to int) int {
	op := -1
	depth := 0
	scanSegment(fl.src, from, to, func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		switch t.kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			if depth > 0 {
				depth--
			}
		case token.EQL, token.NEQ:
			if depth == 0 {
				op = t.start
				return false
			}
		}
		return true
	})
	if op < 0 {
		base.Fatalf("logos: nil comparison at offset %d has no == or !=", from)
	}
	return op
}

func unlist(e syntax.Expr) ([]syntax.Expr, bool) {
	if l, ok := syntax.Unparen(e).(*syntax.ListExpr); ok {
		return l.ElemList, true
	}
	return []syntax.Expr{e}, false
}

func (fl *fileLower) lowerOptAssign(s *syntax.AssignStmt) {
	if s.Op == syntax.Def {
		fl.lowerOptLHS(s.Lhs)
		if s.Rhs != nil {
			s.Rhs = fl.lowerOptExpr(s.Rhs)
		}
		return
	}
	if s.Rhs == nil || s.Op != 0 {
		if name, ok := syntax.Unparen(s.Lhs).(*syntax.Name); ok {
			s.Lhs = fl.narrowAccess(name)
		} else {
			fl.lowerOptLHS(s.Lhs)
		}
		if s.Rhs != nil {
			s.Rhs = fl.lowerOptExpr(s.Rhs)
		}
		return
	}
	lhs, _ := unlist(s.Lhs)
	rhs, rhsList := unlist(s.Rhs)
	if len(lhs) > 1 && len(rhs) == 1 {
		if tup, ok := types2.Unalias(fl.optType(s.Rhs)).(*types2.Tuple); ok && tup.Len() == len(lhs) {
			targets := make([]types2.Type, len(lhs))
			for i := range lhs {
				targets[i] = fl.targetOfLHS(lhs[i])
			}
			fl.lowerOptLHS(s.Lhs)
			s.Rhs = fl.wrapTuple(fl.lowerOptExpr(s.Rhs), tupleTypes(tup), targets)
			return
		}
		fl.lowerOptLHS(s.Lhs)
		s.Rhs = fl.lowerOptExpr(s.Rhs)
		return
	}
	if len(lhs) == len(rhs) {
		for i := range lhs {
			nr := fl.lowerOptExpr(rhs[i])
			nr = fl.wrapKnown(nr, fl.optType(rhs[i]), fl.targetOfLHS(lhs[i]))
			if rhsList {
				s.Rhs.(*syntax.ListExpr).ElemList[i] = nr
			} else {
				s.Rhs = nr
			}
		}
		fl.lowerOptLHS(s.Lhs)
		return
	}
	fl.lowerOptLHS(s.Lhs)
	s.Rhs = fl.lowerOptExpr(s.Rhs)
}

func (fl *fileLower) lowerOptLHS(e syntax.Expr) {
	switch e := e.(type) {
	case *syntax.Name:
	case *syntax.ListExpr:
		for _, x := range e.ElemList {
			fl.lowerOptLHS(x)
		}
	case *syntax.ParenExpr:
		fl.lowerOptLHS(e.X)
	case *syntax.IndexExpr:
		e.X = fl.lowerOptExpr(e.X)
		e.Index = fl.lowerOptExpr(e.Index)
	case *syntax.SelectorExpr:
		e.X = fl.lowerOptExpr(e.X)
	case *syntax.Operation:
		if e.Op == syntax.Mul && e.Y == nil {
			e.X = fl.lowerOptExpr(e.X)
		}
	}
}

func (fl *fileLower) targetOfLHS(e syntax.Expr) types2.Type {
	switch e := syntax.Unparen(e).(type) {
	case *syntax.Name:
		if obj, ok := fl.pkg.info.Uses[e].(*types2.Var); ok {
			return obj.Type()
		}
		return fl.optType(e)
	case *syntax.SelectorExpr:
		if sel := fl.pkg.info.Selections[e]; sel != nil {
			return sel.Obj().Type()
		}
		return fl.optType(e)
	case *syntax.IndexExpr:
		switch c := types2.Unalias(fl.optType(e.X)).(type) {
		case *types2.Slice:
			return c.Elem()
		case *types2.Array:
			return c.Elem()
		case *types2.Map:
			return c.Elem()
		}
		return nil
	case *syntax.Operation:
		if e.Op == syntax.Mul && e.Y == nil {
			if p, ok := types2.Unalias(fl.optType(e.X)).(*types2.Pointer); ok {
				return p.Elem()
			}
		}
		return nil
	}
	return nil
}

func (fl *fileLower) collectOptReturns() {
	fl.retResults = make(map[*syntax.ReturnStmt][]types2.Type)
	type fb struct {
		body    *syntax.BlockStmt
		results []types2.Type
	}
	var list []fb
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.FuncDecl:
			if n.Body != nil {
				list = append(list, fb{n.Body, fl.funcDeclResults(n)})
			}
		case *syntax.FuncLit:
			if sig, ok := types2.Unalias(fl.optType(n)).(*types2.Signature); ok {
				list = append(list, fb{n.Body, tupleTypes(sig.Results())})
			}
		}
		return true
	})
	for _, f := range list {
		if f.body == nil {
			continue
		}
		results := f.results
		syntax.Inspect(f.body, func(n syntax.Node) bool {
			if _, ok := n.(*syntax.FuncLit); ok {
				return false
			}
			if rs, ok := n.(*syntax.ReturnStmt); ok {
				fl.retResults[rs] = results
			}
			return true
		})
	}
	for rs, typ := range fl.armResult {
		fl.retResults[rs] = []types2.Type{typ}
	}
}

func (fl *fileLower) funcDeclResults(fn *syntax.FuncDecl) []types2.Type {
	if obj := fl.pkg.info.Defs[fn.Name]; obj != nil {
		if f, ok := obj.(*types2.Func); ok {
			if sig, ok := types2.Unalias(f.Type()).(*types2.Signature); ok {
				return tupleTypes(sig.Results())
			}
		}
	}
	return nil
}

func tupleTypes(t *types2.Tuple) []types2.Type {
	if t == nil {
		return nil
	}
	out := make([]types2.Type, t.Len())
	for i := range t.Len() {
		out[i] = t.At(i).Type()
	}
	return out
}

// wrapTuple wraps v, an expression evaluating to a tuple, in a call of a
// function literal that binds the tuple to temporaries and returns them
// with each option-typed target wrapped. A single multi-result expression
// cannot carry per-result wrap nodes, so the wrap moves inside the call.
func (fl *fileLower) wrapTuple(v syntax.Expr, recs, targets []types2.Type) syntax.Expr {
	n := len(recs)
	if v == nil || n < 2 || n != len(targets) {
		return v
	}
	need := false
	for i := range n {
		if wrap, _ := fl.needsWrap(v, recs[i], targets[i]); wrap {
			need = true
			break
		}
	}
	if !need {
		return v
	}
	pos := v.Pos()
	params := make([]*syntax.Field, n)
	results := make([]*syntax.Field, n)
	tmps := make([]syntax.Expr, n)
	rets := make([]syntax.Expr, n)
	retTexts := make([]string, n)
	rTexts := make([]string, n)
	pTexts := make([]string, n)
	for i := range n {
		rt := targets[i]
		if rt == nil {
			// A blank destination accepts the produced value untyped:
			// forward it under its own result type.
			rt = recs[i]
		}
		nt, et, ok := fl.spellLoweredBoth(rt, pos)
		if !ok {
			base.Fatalf("logos: cannot spell type %s of the option wrap at %s", rt, pos.String())
		}
		results[i] = at(&syntax.Field{Type: nt}, pos)
		rTexts[i] = et
		pt, pet, ok := fl.spellLoweredBoth(recs[i], pos)
		if !ok {
			base.Fatalf("logos: cannot spell type %s of the option wrap at %s", recs[i], pos.String())
		}
		tmp := fmt.Sprintf(optWrapTemp, i)
		params[i] = at(&syntax.Field{Name: nameOf(pos, tmp), Type: pt}, pos)
		pTexts[i] = tmp + " " + pet
		tmps[i] = nameOf(pos, tmp)
		some, head, tail := fl.wrapParts(tmps[i], recs[i], targets[i], pos)
		rets[i] = some
		retTexts[i] = head + tmp + tail
	}
	// Binding the tuple through parameters, not a := of the multi-result
	// call, keeps the generated body free of statements the Inspect walk
	// would lower a second time.
	sig := at(&syntax.FuncType{
		ParamList:  params,
		ResultList: results,
	}, pos)
	lit := at(&syntax.FuncLit{
		Type: sig,
		Body: blockOf(pos, []syntax.Stmt{
			returnOf(pos, rets),
		}),
	}, pos)
	out := callOf(pos, lit, []syntax.Expr{v})
	vs := fl.wrapSpanOf(v)
	fl.addT(vs.start, vs.end, cat(
		gen("func("+strings.Join(pTexts, ", ")+") ("+strings.Join(rTexts, ", ")+") { return "+strings.Join(retTexts, ", ")+" }("),
		fl.lazyText(vs),
		gen(")"),
	))
	fl.wrapSpan[out] = vs
	fl.wrapOwner[out] = fl.nextWrapID()
	// The IIFE has no recorded type: carrying v's tuple type onto it would
	// make a parent multi-result context wrap the call a second time.
	return out
}

func (fl *fileLower) lowerOptReturn(s *syntax.ReturnStmt) {
	if s.Results == nil {
		return
	}
	orig := s.Results
	low := fl.lowerOptExpr(orig)
	s.Results = low
	res := fl.retResults[s]
	if len(res) == 0 {
		return
	}
	ll, lowList := unlist(low)
	ol, _ := unlist(orig)
	if len(ol) == 1 && len(res) > 1 {
		if tup, ok := types2.Unalias(fl.optType(orig)).(*types2.Tuple); ok {
			s.Results = fl.wrapTuple(low, tupleTypes(tup), res)
		}
		return
	}
	if len(ll) > 1 && len(ol) == 1 {
		return
	}
	if lowList && len(ll) == len(res) && len(ol) == len(res) {
		for i := range ll {
			ll[i] = fl.wrapKnown(ll[i], fl.optType(ol[i]), res[i])
		}
		return
	}
	if len(ll) == 1 && len(res) == 1 && len(ol) == 1 {
		s.Results = fl.wrapKnown(low, fl.optType(orig), res[0])
	}
}

func (fl *fileLower) lowerOptVarValues(names []*syntax.Name, typ, vals syntax.Expr) syntax.Expr {
	if vals == nil || typ == nil {
		return vals
	}
	vl, isList := unlist(vals)
	if len(vl) != len(names) {
		if !isList && len(vl) == 1 && len(names) > 1 {
			if tup, ok := types2.Unalias(fl.optType(vals)).(*types2.Tuple); ok && tup.Len() == len(names) {
				targets := fl.varTargets(names)
				return fl.wrapTuple(fl.lowerOptExpr(vals), tupleTypes(tup), targets)
			}
		}
		return fl.lowerOptExpr(vals)
	}
	targets := fl.varTargets(names)
	for i := range vl {
		nv := fl.lowerOptExpr(vl[i])
		nv = fl.wrapKnown(nv, fl.optType(vl[i]), targets[i])
		if isList {
			vals.(*syntax.ListExpr).ElemList[i] = nv
		} else {
			vals = nv
		}
	}
	return vals
}

func (fl *fileLower) varTargets(names []*syntax.Name) []types2.Type {
	targets := make([]types2.Type, len(names))
	for i, name := range names {
		if obj := fl.pkg.info.Defs[name]; obj != nil {
			switch obj.(type) {
			case *types2.Var, *types2.Const:
				targets[i] = obj.Type()
			}
		}
	}
	return targets
}

func (fl *fileLower) lowerOptSend(s *syntax.SendStmt) {
	s.Chan = fl.lowerOptExpr(s.Chan)
	var elem types2.Type
	if ch, ok := types2.Unalias(fl.optType(s.Chan)).(*types2.Chan); ok {
		elem = ch.Elem()
	}
	s.Value = fl.wrapKnown(fl.lowerOptExpr(s.Value), fl.optType(s.Value), elem)
}

func structFieldType(st *types2.Struct, key syntax.Expr) types2.Type {
	n, ok := syntax.Unparen(key).(*syntax.Name)
	if !ok {
		return nil
	}
	for i := range st.NumFields() {
		if st.Field(i).Name() == n.Value {
			return st.Field(i).Type()
		}
	}
	return nil
}

func (fl *fileLower) lowerOptComposite(e *syntax.CompositeLit) syntax.Expr {
	t := fl.optType(e)
	if t == nil {
		for i, x := range e.ElemList {
			e.ElemList[i] = fl.lowerOptExpr(x)
		}
		return e
	}
	u := types2.Unalias(t)
	if p, ok := u.(*types2.Pointer); ok {
		u = types2.Unalias(p.Elem())
	}
	if n, ok := u.(*types2.Named); ok {
		u = n.Underlying()
	}
	switch c := u.(type) {
	case *types2.Struct:
		for i, x := range e.ElemList {
			if kv, ok := syntax.Unparen(x).(*syntax.KeyValueExpr); ok {
				if _, isName := syntax.Unparen(kv.Key).(*syntax.Name); isName {
					nv := fl.lowerOptExpr(kv.Value)
					kv.Value = fl.wrapKnown(nv, fl.optType(kv.Value), structFieldType(c, kv.Key))
					continue
				}
			}
			nv := fl.lowerOptExpr(x)
			var ft types2.Type
			if i < c.NumFields() {
				ft = c.Field(i).Type()
			}
			e.ElemList[i] = fl.wrapKnown(nv, fl.optType(x), ft)
		}
	case *types2.Map:
		for i, x := range e.ElemList {
			if kv, ok := syntax.Unparen(x).(*syntax.KeyValueExpr); ok {
				nk := fl.lowerOptExpr(kv.Key)
				nv := fl.lowerOptExpr(kv.Value)
				kv.Key = fl.wrapKnown(nk, fl.optType(kv.Key), c.Key())
				kv.Value = fl.wrapKnown(nv, fl.optType(kv.Value), c.Elem())
				continue
			}
			e.ElemList[i] = fl.lowerOptExpr(x)
		}
	case *types2.Slice, *types2.Array:
		var elem types2.Type
		switch c := c.(type) {
		case *types2.Slice:
			elem = c.Elem()
		case *types2.Array:
			elem = c.Elem()
		}
		for i, x := range e.ElemList {
			nv := fl.lowerOptExpr(x)
			e.ElemList[i] = fl.wrapKnown(nv, fl.optType(x), elem)
		}
	default:
		for i, x := range e.ElemList {
			e.ElemList[i] = fl.lowerOptExpr(x)
		}
	}
	return e
}

func (fl *fileLower) lowerOptRange(s *syntax.RangeClause) {
	s.X = fl.lowerOptExpr(s.X)
	if s.Lhs != nil {
		fl.lowerOptLHS(s.Lhs)
	}
}

// lowerOptForRange wraps the range element when a `for lhs = range`
// clause assigns into an option-typed variable. The clause is rewritten
// to declare temporaries, and the body opens with the wrapped assignment,
// because the per-iteration wrap cannot sit on the range clause itself.
func (fl *fileLower) lowerOptForRange(f *syntax.ForStmt) {
	rc, ok := f.Init.(*syntax.RangeClause)
	if !ok || rc.Def || rc.Lhs == nil || f.Body == nil {
		return
	}
	lhs, _ := unlist(rc.Lhs)
	recs := fl.rangeRecs(rc.X)
	if len(lhs) == 0 || len(lhs) > len(recs) {
		return
	}
	recs = recs[:len(lhs)]
	need := false
	for i := range lhs {
		if wrap, _ := fl.needsWrap(lhs[i], recs[i], fl.targetOfLHS(lhs[i])); wrap {
			need = true
			break
		}
	}
	if !need {
		return
	}
	pos := rc.Pos()
	vs := fl.wrapSpanOf(rc.Lhs)
	xOff := fl.off(rc.X.Pos())
	opStart, opEnd := -1, -1
	scanSegment(fl.src, vs.end, xOff, func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		if t.kind == token.ASSIGN || t.kind == token.DEFINE {
			opStart, opEnd = t.start, t.end
			return false
		}
		return true
	})
	if opStart < 0 {
		return
	}
	texts := make([]otext, 0, 2*len(lhs)+3)
	texts = append(texts, gen(" "))
	tmps := make([]syntax.Expr, len(lhs))
	vals := make([]syntax.Expr, len(lhs))
	vtexts := make([]string, len(lhs))
	for i := range lhs {
		// _logos_rangeN avoids the _logos_rN result names ensureNamed
		// emits: a scope defer lowers a return to `_logos_r0 = ...`, and
		// a same-named loop temp would shadow it.
		tmp := fmt.Sprintf("_logos_range%d", i)
		tn := nameOf(pos, tmp)
		tmps[i] = tn
		val, head, tail := fl.wrapParts(tn, recs[i], fl.targetOfLHS(lhs[i]), pos)
		vals[i] = val
		vtexts[i] = head + tmp + tail
		texts = append(texts, fl.lazyText(fl.wrapSpanOf(lhs[i])))
		if i+1 < len(lhs) {
			texts = append(texts, gen(", "))
		}
	}
	// One multi-assign keeps the two-phase assignment order: every
	// destination operand (e.g. the i in dst[i]) is evaluated before any
	// store, so a destination that reads an earlier loop variable still
	// sees its previous value.
	assigns := []syntax.Stmt{assignOf(pos, lhs, vals)}
	texts = append(texts, gen(" = "+strings.Join(vtexts, ", ")+"; "))
	rc.Lhs = listOf(pos, tmps)
	rc.Def = true
	f.Body.List = append(assigns, f.Body.List...)
	fl.add(vs.start, vs.end, strings.Join(tmpNames(tmps), ", "))
	fl.add(opStart, opEnd, ":=")
	braceOff := fl.off(f.Body.Pos()) + 1
	fl.addT(braceOff, braceOff, cat(texts...))
}

// rangeRecs returns the types a range clause assigns, index or key first.
// A defined collection (`type Parts []Text`) ranges by its underlying
// type, so the switch reads the underlying, not the named surface.
func (fl *fileLower) rangeRecs(x syntax.Expr) []types2.Type {
	intT := types2.Typ[types2.Int]
	xt := fl.optType(x)
	if xt == nil {
		return nil
	}
	switch t := types2.Unalias(xt).Underlying().(type) {
	case *types2.Slice:
		return []types2.Type{intT, t.Elem()}
	case *types2.Array:
		return []types2.Type{intT, t.Elem()}
	case *types2.Map:
		return []types2.Type{t.Key(), t.Elem()}
	case *types2.Chan:
		return []types2.Type{t.Elem()}
	case *types2.Basic:
		if t.Kind() == types2.String {
			return []types2.Type{intT, types2.Typ[types2.Rune]}
		}
		if t.Info()&types2.IsInteger != 0 {
			return []types2.Type{intT}
		}
	}
	return nil
}

func tmpNames(list []syntax.Expr) []string {
	out := make([]string, len(list))
	for i, e := range list {
		if n, ok := e.(*syntax.Name); ok {
			out[i] = n.Value
		}
	}
	return out
}

func (fl *fileLower) findCallParens(selEnd int) (lparen, rparen int) {
	lparen = -1
	scanSegment(fl.src, selEnd, len(fl.src), func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		if t.kind != token.LPAREN {
			base.Fatalf("logos: optional call at offset %d has no (", selEnd)
		}
		lparen = t.start
		return false
	})
	if lparen < 0 {
		base.Fatalf("logos: optional call at offset %d has no (", selEnd)
	}
	depth := 0
	scanSegment(fl.src, lparen, len(fl.src), func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		switch t.kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
			if depth == 0 {
				rparen = t.start
				return false
			}
		case token.SEMICOLON:
			if depth == 0 {
				base.Fatalf("logos: optional call at offset %d has no )", lparen)
			}
		}
		return true
	})
	if rparen <= lparen {
		base.Fatalf("logos: optional call at offset %d has no )", lparen)
	}
	return lparen, rparen
}

func (fl *fileLower) findLbrack(from, kStart int) int {
	lbrack := -1
	scanSegment(fl.src, from, kStart, func(t tok) bool {
		if !t.comment && t.kind == token.LBRACK {
			lbrack = t.start
		}
		return true
	})
	if lbrack < 0 {
		base.Fatalf("logos: index expression at offset %d has no [", from)
	}
	return lbrack
}

func (fl *fileLower) findRbrack(kStart int) int {
	rbrack := -1
	depth := 0
	scanSegment(fl.src, kStart, len(fl.src), func(t tok) bool {
		if t.comment || t.auto {
			return true
		}
		switch t.kind {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			if depth == 0 {
				if t.kind != token.RBRACK {
					base.Fatalf("logos: index expression at offset %d ends in %s", kStart, fl.src[t.start:t.end])
				}
				rbrack = t.start
				return false
			}
			depth--
		case token.SEMICOLON, token.EOF:
			base.Fatalf("logos: index expression at offset %d has no ]", kStart)
		}
		return true
	})
	if rbrack < 0 {
		base.Fatalf("logos: index expression at offset %d has no ]", kStart)
	}
	return rbrack
}

func (fl *fileLower) exprEnd(from int) int {
	return fl.exprEndThrough(from, from)
}

func (fl *fileLower) exprEndNested(from int) int {
	return fl.exprEndThrough(from, len(fl.src))
}

func (fl *fileLower) exprEndThrough(from, through int) int {
	last := from
	depth := 0
	scanSegment(fl.src, from, len(fl.src), func(t tok) bool {
		if t.comment {
			return true
		}
		if t.auto {
			return depth > 0
		}
		switch t.kind {
		case token.LPAREN, token.LBRACK:
			depth++
		case token.LBRACE:
			if depth == 0 && t.start >= through {
				return false
			}
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			if depth == 0 {
				return false
			}
			depth--
		case token.COMMA, token.SEMICOLON, token.COLON, token.ASSIGN, token.DEFINE:
			if depth == 0 && !fl.fatArrowAt(t) {
				return false
			}
		}
		last = t.end
		return true
	})
	return last
}

func (fl *fileLower) fatArrowAt(t tok) bool {
	return t.kind == token.ASSIGN && t.end < len(fl.src) && fl.src[t.end] == '>'
}

func (w ctWalker) ctOptionTarget(parent syntax.Node, child syntax.Expr, index int) types2.Type {
	info := w.fl.pkg.info
	switch p := parent.(type) {
	case *syntax.AssignStmt:
		if p.Op != 0 && p.Op != syntax.Def {
			return nil
		}
		lhs := syntax.UnpackListExpr(p.Lhs)
		if index >= len(lhs) {
			return nil
		}
		root := lhs[index]
		l := syntax.Unparen(root)
		if name, ok := l.(*syntax.Name); ok {
			if v, ok := info.ObjectOf(name).(*types2.Var); ok {
				return v.Type()
			}
		}
		if t := info.TypeOf(root); t != nil {
			return t
		}
		if ix, ok := l.(*syntax.IndexExpr); ok {
			if m := w.ctOptionMap(ix.X); m != nil {
				return m.Elem()
			}
		}
	case *syntax.ReturnStmt:
		if t := w.fl.armResult[p]; t != nil {
			return t
		}
		if index < len(w.specs) {
			return w.specs[index].typ
		}
	case *syntax.VarDecl:
		if index < len(p.NameList) {
			if obj := info.Defs[p.NameList[index]]; obj != nil {
				return obj.Type()
			}
		}
	case *syntax.ConstDecl:
		if p.Type != nil {
			return info.TypeOf(p.Type)
		}
	case *syntax.CallExpr:
		sig := w.fl.instCallSig(p.Fun)
		if sig == nil {
			if t := info.TypeOf(p.Fun); t != nil {
				sig, _ = t.Underlying().(*types2.Signature)
			}
		}
		if sig == nil {
			return nil
		}
		if w.fl.methodExprCallee(p.Fun) != nil && sig.Recv() != nil {
			if index == 0 {
				return sig.Recv().Type()
			}
			index--
		}
		return w.fl.paramTarget(sig.Params(), sig.Variadic(), p.HasDots, len(p.ArgList), index)
	case *syntax.CompositeLit:
		t := info.TypeOf(p)
		if t == nil {
			return nil
		}
		if ptr, ok := t.Underlying().(*types2.Pointer); ok {
			t = ptr.Elem()
		}
		switch u := optElemBase(t).Underlying().(type) {
		case *types2.Struct:
			if kv, ok := syntax.Unparen(child).(*syntax.KeyValueExpr); ok {
				return structFieldType(u, kv.Key)
			}
			if index < u.NumFields() {
				return u.Field(index).Type()
			}
		case *types2.Map:
			return u.Elem()
		case *types2.Slice:
			return u.Elem()
		case *types2.Array:
			return u.Elem()
		}
	case *syntax.IndexExpr:
		return w.ctOptionIndexKey(p)
	case *syntax.SendStmt:
		if t := info.TypeOf(p.Chan); t != nil {
			if ch, ok := types2.CoreType(t).(*types2.Chan); ok {
				return ch.Elem()
			}
		}
	}
	return nil
}

func (w ctWalker) ctOptionLiteralKey(p *syntax.CompositeLit) types2.Type {
	if m := w.ctOptionMap(p); m != nil {
		return m.Key()
	}
	return nil
}

func (w ctWalker) ctOptionIndexKey(e *syntax.IndexExpr) types2.Type {
	if m := w.ctOptionMap(e.X); m != nil {
		return m.Key()
	}
	return nil
}

func (w ctWalker) ctOptionMap(e syntax.Expr) *types2.Map {
	if t := w.fl.optType(e); t != nil {
		if p, ok := t.Underlying().(*types2.Pointer); ok {
			t = p.Elem()
		}
		m, _ := types2.CoreType(optElemBase(t)).(*types2.Map)
		return m
	}
	return nil
}

func (fl *fileLower) isOptionNil(e syntax.Expr) bool {
	if fl.pkg.info.Types[e].IsNil() {
		return true
	}
	if n, ok := syntax.Unparen(e).(*syntax.Name); ok {
		return fl.pkg.info.Uses[n] == types2.Universe.Lookup("nil")
	}
	return false
}

func (w ctWalker) ctOptionValue(original, lowered syntax.Expr, target types2.Type) syntax.Expr {
	if original == nil || lowered == nil || w.fl.asOption(target) == nil {
		return lowered
	}
	if w.fl.isOptionNil(original) {
		return w.ctOptionNone(original.Pos(), target)
	}
	return w.ctOptionValueFrom(lowered, w.fl.optType(original), target)
}

func (w ctWalker) ctOptionValueFrom(value syntax.Expr, source, target types2.Type) syntax.Expr {
	opt := w.fl.asOption(target)
	if opt == nil || source == nil || types2.Identical(source, target) {
		return value
	}
	if _, multi := source.(*types2.Tuple); multi {
		return value
	}
	pos := value.Pos()
	if !types2.Identical(source, opt.Elem) {
		if src := w.fl.asOption(source); src != nil && types2.Identical(src, opt) {
			return callOf(pos, w.fl.ctTypeExpr(pos, target), []syntax.Expr{value})
		}
		value = w.ctOptionValueFrom(value, source, opt.Elem)
	}
	if types2.LogosOptionIsBare(opt.Elem) {
		if _, named := types2.Unalias(target).(*types2.Named); named {
			return callOf(pos, w.fl.ctTypeExpr(pos, target), []syntax.Expr{value})
		}
		return value
	}
	return at(&syntax.CompositeLit{
		Type: w.fl.ctTypeExpr(pos, target),
		ElemList: []syntax.Expr{
			at(&syntax.KeyValueExpr{Key: nameOf(pos, "Value"), Value: value}, pos),
			at(&syntax.KeyValueExpr{Key: nameOf(pos, "Present"), Value: w.fl.ctBuiltin(pos, "true")}, pos),
		},
		NKeys:  2,
		Rbrace: pos,
	}, pos)
}

func (w ctWalker) ctOptionMulti(parent syntax.Node, original, lowered syntax.Expr) ([]syntax.Expr, []syntax.Stmt, bool) {
	return w.ctOptionMultiTargets(original, lowered, func(i int) types2.Type {
		return w.ctOptionTarget(parent, original, i)
	})
}

func (w ctWalker) ctOptionMultiTargets(original, lowered syntax.Expr, target func(int) types2.Type) ([]syntax.Expr, []syntax.Stmt, bool) {
	tuple, ok := w.fl.optType(original).(*types2.Tuple)
	if !ok || tuple.Len() < 2 {
		return nil, nil, false
	}
	for i := range tuple.Len() {
		source := tuple.At(i).Type()
		t := target(i)
		opt := w.fl.asOption(t)
		if opt == nil || types2.Identical(source, t) {
			continue
		}
		if types2.LogosOptionIsBare(opt.Elem) {
			_, named := types2.Unalias(t).(*types2.Named)
			_, sourceOption := types2.Unalias(source).(*types2.Named)
			if !named && !(sourceOption && w.fl.asOption(source) != nil) {
				continue
			}
		}
		pos := original.Pos()
		names := make([]syntax.Expr, tuple.Len())
		values := make([]syntax.Expr, tuple.Len())
		for j := range names {
			name := w.nextArg()
			names[j] = nameOf(pos, name)
			values[j] = w.ctOptionValueFrom(nameOf(pos, name), tuple.At(j).Type(), target(j))
		}
		return values, []syntax.Stmt{defineOf(pos, names, []syntax.Expr{lowered})}, true
	}
	return nil, nil, false
}

func (w ctWalker) ctOptionNone(pos syntax.Pos, t types2.Type) syntax.Expr {
	if w.fl.bareResult(t) {
		return w.fl.ctBuiltin(pos, "nil")
	}
	return at(&syntax.CompositeLit{Type: w.fl.ctTypeExpr(pos, t), Rbrace: pos}, pos)
}

func (w ctWalker) ctOptionPresent(pos syntax.Pos, e syntax.Expr, opt *types2.Option) syntax.Expr {
	if types2.LogosOptionIsBare(opt.Elem) {
		return at(&syntax.Operation{Op: syntax.Neq, X: e, Y: w.fl.ctBuiltin(pos, "nil")}, pos)
	}
	return selOf(pos, e, "Present")
}

func (w ctWalker) ctOptionPayload(pos syntax.Pos, e syntax.Expr, t types2.Type, opt *types2.Option) syntax.Expr {
	if !types2.LogosOptionIsBare(opt.Elem) {
		return selOf(pos, e, "Value")
	}
	if _, named := types2.Unalias(t).(*types2.Named); named {
		return callOf(pos, w.fl.ctTypeExpr(pos, opt.Elem), []syntax.Expr{e})
	}
	return e
}

func (w ctWalker) ctOptionReceiver(e syntax.Expr) (syntax.Expr, []syntax.Stmt, types2.Type, *types2.Option) {
	if n, ok := syntax.Unparen(e).(*syntax.Name); ok {
		if obj, ok := w.fl.pkg.info.Uses[n].(*types2.Var); ok {
			if opt := w.fl.asOption(obj.Type()); opt != nil {
				return e, nil, obj.Type(), opt
			}
		}
	}
	t := w.fl.optType(e)
	opt := w.fl.asOptionThroughConst(t)
	out, pre := w.expr(e)
	return out, pre, t, opt
}

func (w ctWalker) ctOptionExpr(e syntax.Expr) (syntax.Expr, []syntax.Stmt, bool) {
	info := w.fl.pkg.info
	switch e := e.(type) {
	case *syntax.OptionType:
		if t := info.TypeOf(e); t != nil {
			return w.fl.ctTypeExpr(e.Pos(), t), nil, true
		}
		w.reject(e, "missing checked option type")
		return e, nil, true
	case *syntax.Name:
		if opt := w.fl.narrowTarget(e); opt != nil {
			t := info.Uses[e].Type()
			return w.ctOptionPayload(e.Pos(), e, t, opt), nil, true
		}
	case *syntax.Operation:
		return w.ctOptionNilComparison(e)
	case *syntax.IndexExpr:
		if w.fl.isMapRead(e) {
			if m := w.ctOptionMap(e.X); m != nil {
				next, pre := w.ctOptionIndex(e, m)
				return next, pre, true
			}
		}
	case *syntax.CoalesceExpr:
		out, pre := w.ctOptionCoalesce(e)
		return out, pre, true
	case *syntax.OptSelectExpr:
		out, pre := w.ctOptionSelect(e)
		return out, pre, true
	case *syntax.CallExpr:
		if sel, ok := syntax.Unparen(e.Fun).(*syntax.OptSelectExpr); ok {
			out, pre := w.ctOptionCall(e, sel)
			return out, pre, true
		}
		if len(e.ArgList) == 1 && w.fl.isTypeExpr(e.Fun) && w.fl.isOptionNil(e.ArgList[0]) {
			if t := info.TypeOf(e); w.fl.asOption(t) != nil {
				return w.ctOptionNone(e.Pos(), t), nil, true
			}
		}
	}
	return nil, nil, false
}

func (w ctWalker) ctOptionNilComparison(e *syntax.Operation) (syntax.Expr, []syntax.Stmt, bool) {
	if e.Y == nil || e.Op != syntax.Eql && e.Op != syntax.Neq {
		return nil, nil, false
	}
	operand := e.X
	if !w.fl.isOptionNil(e.Y) {
		if !w.fl.isOptionNil(e.X) {
			return nil, nil, false
		}
		operand = e.Y
	}
	opt := w.fl.asOption(optElemBase(w.fl.optType(operand)))
	if opt == nil {
		return nil, nil, false
	}
	next, pre := w.expr(operand)
	pos := e.Pos()
	cond := w.ctOptionPresent(pos, next, opt)
	if e.Op == syntax.Eql {
		cond = at(&syntax.Operation{Op: syntax.Not, X: cond}, pos)
	}
	return cond, pre, true
}

func (w ctWalker) ctOptionIndexArgs(e *syntax.IndexExpr, m *types2.Map) []syntax.Stmt {
	return w.walkExprs(2, func(i int) *syntax.Expr {
		if i == 0 {
			return &e.X
		}
		return &e.Index
	}, func(i int, _ syntax.Expr) types2.Type {
		if i == 1 {
			return m.Key()
		}
		return nil
	})
}

func (w ctWalker) ctOptionIndex(e *syntax.IndexExpr, m *types2.Map) (syntax.Expr, []syntax.Stmt) {
	t := w.fl.optType(e)
	mapType := w.fl.optType(e.X)
	pre := w.ctOptionIndexArgs(e, m)
	opt := w.fl.asOption(t)
	// The checker records the map element itself for a comma-ok read.
	if opt == nil || types2.Identical(t, m.Elem()) || types2.LogosOptionIsBare(opt.Elem) {
		return e, pre
	}
	pos := e.Pos()
	args := at(&syntax.ListExpr{ElemList: []syntax.Expr{
		w.fl.ctTypeExpr(pos, mapType),
		w.fl.ctTypeExpr(pos, m.Key()),
		w.fl.ctTypeExpr(pos, m.Elem()),
	}}, pos)
	lookup := at(&syntax.IndexExpr{X: w.fl.ctPkgSelector(pos, rtImportPath, "Lookup"), Index: args}, pos)
	return callOf(pos, lookup, []syntax.Expr{e.X, e.Index}), pre
}

func (w ctWalker) ctOptionCoalesce(e *syntax.CoalesceExpr) (syntax.Expr, []syntax.Stmt) {
	pos := e.Pos()
	var value, cond syntax.Expr
	var pre []syntax.Stmt
	ix, _ := syntax.Unparen(e.X).(*syntax.IndexExpr)
	var m *types2.Map
	if ix != nil && w.fl.isMapRead(ix) {
		m = w.ctOptionMap(ix.X)
	}
	if m != nil {
		var read syntax.Expr = ix
		if g, ok := w.ctMapUse(ix.X); ok && g.valid() {
			var key syntax.Expr
			key, pre = w.optionOperand(ix.Index, m.Key())
			read = callOf(ix.Pos(), w.ctMapHelper(ix.Pos(), g, types2.LogosCtMapLookup), []syntax.Expr{key})
		} else {
			pre = w.ctOptionIndexArgs(ix, m)
		}
		v, ok := w.nextArg(), w.nextArg()
		pre = append(pre, defineOf(pos, []syntax.Expr{nameOf(pos, v), nameOf(pos, ok)}, []syntax.Expr{read}))
		value, cond = nameOf(pos, v), nameOf(pos, ok)
	} else {
		x, head, t, opt := w.ctOptionReceiver(e.X)
		if opt == nil {
			w.reject(e, "missing checked coalesce receiver type")
			return e, head
		}
		tmp := w.nextArg()
		pre = append(head, defineOf(pos, []syntax.Expr{nameOf(pos, tmp)}, []syntax.Expr{x}))
		value = w.ctOptionPayload(pos, nameOf(pos, tmp), t, opt)
		cond = w.ctOptionPresent(pos, nameOf(pos, tmp), opt)
	}
	t := w.fl.optType(e)
	if t == nil {
		w.reject(e, "missing checked coalesce result type")
		return e, pre
	}
	result := w.nextArg()
	pre = append(pre, typedVarDecl(pos, nameOf(pos, result), w.fl.ctTypeExpr(pos, t)))
	branch := ifOf(pos, nil, cond, []syntax.Stmt{
		assignOf(pos, []syntax.Expr{nameOf(pos, result)}, []syntax.Expr{value}),
	})
	rhs, fallback := w.expr(e.Y)
	rhs = w.ctOptionValue(e.Y, rhs, t)
	fallback = append(fallback, assignOf(pos, []syntax.Expr{nameOf(pos, result)}, []syntax.Expr{rhs}))
	branch.Else = blockOf(e.Y.Pos(), fallback)
	return nameOf(pos, result), append(pre, branch)
}

func (w ctWalker) ctOptionAccess(e *syntax.OptSelectExpr) (syntax.Expr, syntax.Expr, []syntax.Stmt, types2.Object) {
	pos := e.Pos()
	recv, pre, t, opt := w.ctOptionReceiver(e.X)
	if opt == nil {
		w.reject(e, "missing checked optional selector receiver type")
		return nil, nil, pre, nil
	}
	base := optElemBase(opt.Elem)
	name := tupleSelectionName(base, e.Sel.Value)
	obj, _, _ := types2.LookupFieldOrMethod(base, true, w.fl.pkg.tpkg, e.Sel.Value)
	if obj == nil {
		obj, _, _ = types2.LookupFieldOrMethod(base.Underlying(), true, w.fl.pkg.tpkg, name)
	}
	tmp := w.nextArg()
	pre = append(pre, defineOf(pos, []syntax.Expr{nameOf(pos, tmp)}, []syntax.Expr{recv}))
	value := w.ctOptionPayload(pos, nameOf(pos, tmp), t, opt)
	cond := w.ctOptionPresent(pos, nameOf(pos, tmp), opt)
	return selOf(pos, value, name), cond, pre, obj
}

func (w ctWalker) ctOptionSelect(e *syntax.OptSelectExpr) (syntax.Expr, []syntax.Stmt) {
	access, cond, pre, obj := w.ctOptionAccess(e)
	t := w.fl.optType(e)
	opt := w.fl.asOption(t)
	if access == nil || opt == nil {
		w.reject(e, "missing checked optional selector result type")
		return e, pre
	}
	if obj == nil || w.fl.asOption(obj.Type()) == nil {
		access = w.ctOptionValueFrom(access, opt.Elem, t)
	}
	pos := e.Pos()
	result := w.nextArg()
	pre = append(pre, typedVarDecl(pos, nameOf(pos, result), w.fl.ctTypeExpr(pos, t)))
	set := assignOf(pos, []syntax.Expr{nameOf(pos, result)}, []syntax.Expr{access})
	return nameOf(pos, result), append(pre, ifOf(pos, nil, cond, []syntax.Stmt{set}))
}

func (w ctWalker) ctOptionGuardCall(e *syntax.CallExpr, sel *syntax.OptSelectExpr, body func(*syntax.CallExpr, *types2.Signature) []syntax.Stmt) []syntax.Stmt {
	fun, cond, pre, obj := w.ctOptionAccess(sel)
	opt := w.fl.asOption(w.fl.optType(sel))
	if fun == nil || opt == nil {
		w.reject(sel, "missing checked optional callee type")
		return pre
	}
	sig, _ := opt.Elem.Underlying().(*types2.Signature)
	if sig == nil {
		w.reject(sel, "missing checked optional callee signature")
		return pre
	}
	pos := sel.Pos()
	if obj != nil {
		t := obj.Type()
		if field := w.fl.asOption(t); field != nil {
			tmp := w.nextArg()
			init := defineOf(pos, []syntax.Expr{nameOf(pos, tmp)}, []syntax.Expr{fun})
			fun = w.ctOptionPayload(pos, nameOf(pos, tmp), t, field)
			test := w.ctOptionPresent(pos, nameOf(pos, tmp), field)
			guarded := w.ctOptionInvoke(e, sel, fun, sig, body)
			return append(pre, ifOf(pos, nil, cond, []syntax.Stmt{ifOf(pos, init, test, guarded)}))
		}
	}
	return append(pre, ifOf(pos, nil, cond, w.ctOptionInvoke(e, sel, fun, sig, body)))
}

func (w ctWalker) ctOptionInvoke(e *syntax.CallExpr, sel *syntax.OptSelectExpr, fun syntax.Expr, sig *types2.Signature, body func(*syntax.CallExpr, *types2.Signature) []syntax.Stmt) []syntax.Stmt {
	callee := w.fl.pkg.info.Types[sel]
	callee.Type = sig
	w.fl.pkg.info.Types[fun] = callee
	call := at(&syntax.CallExpr{Fun: fun, ArgList: e.ArgList, HasDots: e.HasDots}, e.Pos())
	w.fl.pkg.info.Types[call] = w.fl.pkg.info.Types[e]
	return body(call, sig)
}

func (w ctWalker) ctOptionCallArgs(call *syntax.CallExpr) []syntax.Stmt {
	var multi []syntax.Expr
	pre := w.watchExprs(1+len(call.ArgList), func(i int) *syntax.Expr {
		if i == 0 {
			return &call.Fun
		}
		return &call.ArgList[i-1]
	}, func(i int, old syntax.Expr) (syntax.Expr, []syntax.Stmt) {
		if i == 0 {
			return w.expr(old)
		}
		value, head := w.checkedOperand(old, w.ctOptionTarget(call, old, i-1))
		if len(call.ArgList) == 1 {
			if values, split, ok := w.ctOptionMulti(call, old, value); ok {
				multi = values
				head = append(head, split...)
			}
		}
		return value, head
	})
	if multi != nil {
		call.ArgList = multi
	}
	return pre
}

func (w ctWalker) ctOptionCall(e *syntax.CallExpr, sel *syntax.OptSelectExpr) (syntax.Expr, []syntax.Stmt) {
	pos := e.Pos()
	t := w.fl.optType(e)
	var targets []types2.Type
	if tuple, ok := t.(*types2.Tuple); ok {
		targets = tupleTypes(tuple)
	} else if w.fl.asOption(t) != nil {
		targets = []types2.Type{t}
	}
	if len(targets) == 0 {
		w.reject(e, "optional call without a result requires a call statement")
		return e, nil
	}
	results := make([]string, len(targets))
	pre := make([]syntax.Stmt, 0, len(targets)+1)
	for i, target := range targets {
		results[i] = w.nextArg()
		pre = append(pre, typedVarDecl(pos, nameOf(pos, results[i]), w.fl.ctTypeExpr(pos, target)))
	}
	pre = append(pre, w.ctOptionGuardCall(e, sel, func(call *syntax.CallExpr, sig *types2.Signature) []syntax.Stmt {
		list := w.ctOptionCallArgs(call)
		if len(results) == 1 {
			some := w.ctOptionValueFrom(call, sig.Results().At(0).Type(), targets[0])
			return append(list, assignOf(pos, []syntax.Expr{nameOf(pos, results[0])}, []syntax.Expr{some}))
		}
		names := make([]syntax.Expr, len(results))
		lhs := make([]syntax.Expr, len(results))
		rhs := make([]syntax.Expr, len(results))
		for i := range results {
			tmp := w.nextArg()
			names[i] = nameOf(pos, tmp)
			lhs[i] = nameOf(pos, results[i])
			rhs[i] = w.ctOptionValueFrom(nameOf(pos, tmp), sig.Results().At(i).Type(), targets[i])
		}
		return append(list, defineOf(pos, names, []syntax.Expr{call}), assignOf(pos, lhs, rhs))
	})...)
	if len(results) == 1 {
		return nameOf(pos, results[0]), pre
	}
	fields := make([]*syntax.Field, len(results))
	values := make([]syntax.Expr, len(results))
	for i, target := range targets {
		fields[i] = at(&syntax.Field{Type: w.fl.ctTypeExpr(pos, target)}, pos)
		values[i] = nameOf(pos, results[i])
	}
	sig := at(&syntax.FuncType{ResultList: fields}, pos)
	lit := at(&syntax.FuncLit{Type: sig, Body: blockOf(pos, []syntax.Stmt{returnOf(pos, values)})}, pos)
	return callOf(pos, lit, nil), pre
}

func (w ctWalker) ctOptionSwitch(s *syntax.SwitchStmt) ([]syntax.Stmt, bool) {
	opt := w.fl.asOption(optElemBase(w.fl.optType(s.Tag)))
	if opt == nil || types2.LogosOptionIsBare(opt.Elem) {
		return nil, false
	}
	n := 0
	for _, clause := range s.Body {
		if clause.Cases == nil {
			continue
		}
		for _, value := range syntax.UnpackListExpr(clause.Cases) {
			if !w.fl.isOptionNil(value) {
				w.reject(value, "boxed option switch case is not nil")
				return nil, true
			}
		}
		n++
	}
	var pre []syntax.Stmt
	var absent string
	if n > 1 {
		// Unlike a constant, a nil case may appear in more than one clause.
		absent = w.nextArg()
		pos := s.Pos()
		pre = append(pre, typedVarDecl(pos, nameOf(pos, absent), w.fl.ctBuiltin(pos, "bool")))
	}
	for _, clause := range s.Body {
		if clause.Cases != nil {
			pos := clause.Cases.Pos()
			if absent == "" {
				clause.Cases = w.fl.ctBuiltin(pos, "false")
			} else {
				clause.Cases = nameOf(pos, absent)
			}
		}
	}
	s.Tag = selOf(s.Tag.Pos(), s.Tag, "Present")
	return append(pre, w.switchStmt(s)...), true
}

func (w ctWalker) ctOptionCleanup(e *syntax.CallExpr, lowerCall func(*syntax.CallExpr) []syntax.Stmt) ([]syntax.Stmt, bool) {
	sel, ok := syntax.Unparen(e.Fun).(*syntax.OptSelectExpr)
	if !ok {
		return nil, false
	}
	return w.ctOptionGuardCall(e, sel, func(call *syntax.CallExpr, _ *types2.Signature) []syntax.Stmt {
		return lowerCall(call)
	}), true
}

func (w ctWalker) ctOptionStmt(s syntax.Stmt) ([]syntax.Stmt, syntax.Stmt, bool) {
	var x syntax.Expr
	switch s := s.(type) {
	case *syntax.ExprStmt:
		x = s.X
	case *syntax.CallStmt:
		x = s.Call
	default:
		return nil, nil, false
	}
	e, ok := syntax.Unparen(x).(*syntax.CallExpr)
	if !ok {
		return nil, nil, false
	}
	sel, ok := syntax.Unparen(e.Fun).(*syntax.OptSelectExpr)
	if !ok {
		return nil, nil, false
	}
	list := w.ctOptionGuardCall(e, sel, func(call *syntax.CallExpr, _ *types2.Signature) []syntax.Stmt {
		pre := w.ctOptionCallArgs(call)
		switch s := s.(type) {
		case *syntax.CallStmt:
			s.Call = call
			pre = append(pre, w.expressions(&s.DeferAt)...)
		case *syntax.ExprStmt:
			s.X = call
		}
		return append(pre, s)
	})
	return list, nil, true
}

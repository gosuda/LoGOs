// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"go/constant"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

const (
	interpConvPkg   = "_logos_strconv"
	interpUnsafePkg = "_logos_unsafe"
	interpFmtPkg    = "_logos_fmt"
	interpBuf       = "_logos_b"
	interpVal       = "_logos_v%d"
	interpStr       = "_logos_s%d"
)

const (
	interpIntBound   = 20
	interpFloatBound = 24
	interpBoolBound  = 5
)

func InterpFixedBytes(parts []string, kinds []types2.LogosInterpKind) int {
	n := 0
	for _, part := range parts {
		n += len(part)
	}
	for _, k := range kinds {
		n += interpFixedWidth(k)
	}
	return n
}

func interpFixedWidth(k types2.LogosInterpKind) int {
	switch k {
	case types2.LogosInterpInt, types2.LogosInterpUint:
		return interpIntBound
	case types2.LogosInterpFloat32, types2.LogosInterpFloat64:
		return interpFloatBound
	case types2.LogosInterpBool:
		return interpBoolBound
	}
	return 0
}

var interpKinds = make(map[*syntax.InterpExpr][]types2.LogosInterpKind)

func (fl *fileLower) collectInterpKinds() {
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		if fl.interpDeferred(n) {
			return false
		}
		e, ok := n.(*syntax.InterpExpr)
		if !ok || len(e.Exprs) == 0 {
			return true
		}
		if _, done := interpKinds[e]; done {
			return true
		}
		kinds := make([]types2.LogosInterpKind, len(e.Exprs))
		for i, op := range e.Exprs {
			t := fl.pkg.info.TypeOf(op)
			k, ok := types2.LogosInterpKindOf(t)
			if !ok {
				if base.Errors() > 0 {
					return true
				}
				base.Fatalf("logos: interpolation operand at %s has unexpected type %s", op.Pos().String(), t)
			}
			kinds[i] = k
		}
		interpKinds[e] = kinds
		return true
	})
}

func (fl *fileLower) interpDeferred(n syntax.Node) bool {
	switch n := n.(type) {
	case *syntax.ConstForStmt:
		return true
	case *syntax.FuncDecl:
		if fl.pkg.ctExp == nil {
			return false
		}
		_, isDef := fl.pkg.ctExp.byDecl[n]
		return isDef
	case syntax.Stmt:
		return fl.pkg.info.LogosConstIfDead[n]
	}
	return false
}

func interpLitsUnder(root syntax.Node) []*syntax.InterpExpr {
	var lits []*syntax.InterpExpr
	syntax.Inspect(root, func(n syntax.Node) bool {
		if e, ok := n.(*syntax.InterpExpr); ok {
			lits = append(lits, e)
		}
		return true
	})
	slices.SortFunc(lits, func(a, b *syntax.InterpExpr) int {
		return len(a.Raw) - len(b.Raw)
	})
	return lits
}

func (fl *fileLower) lowerInterp() {
	needConv := fl.interpNeedConv
	for _, e := range interpLitsUnder(fl.file) {
		kinds := interpKinds[e]
		delete(interpKinds, e)
		if kinds == nil && len(e.Exprs) > 0 && base.Errors() > 0 {
			continue
		}
		need, _ := fl.lowerInterpExpr(e, kinds)
		needConv = needConv || need
	}
	fl.flushInnerFirst()
	fl.flushInterpImports(needConv, fl.interpNeedUnsafe)
}

func replaceFieldExprs(n syntax.Node, interfaceField func(reflect.Type) bool, repl func(syntax.Expr) syntax.Expr) {
	v := reflect.ValueOf(n)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return
	}
	v = v.Elem()
	if v.Kind() != reflect.Struct {
		return
	}
	typ := v.Type()
	for i := range v.NumField() {
		if typ.Field(i).IsExported() {
			replaceFieldExpr(v.Field(i), interfaceField, repl)
		}
	}
}

func replaceFieldExpr(f reflect.Value, interfaceField func(reflect.Type) bool, repl func(syntax.Expr) syntax.Expr) {
	switch f.Kind() {
	case reflect.Interface:
		if f.IsNil() || !f.CanSet() || !interfaceField(f.Type()) {
			return
		}
		e, ok := f.Elem().Interface().(syntax.Expr)
		if !ok {
			return
		}
		f.Set(reflect.ValueOf(repl(e)))
	case reflect.Slice, reflect.Array:
		for i := range f.Len() {
			replaceFieldExpr(f.Index(i), interfaceField, repl)
		}
	}
}

func anyInterfaceFields(t reflect.Type) bool {
	return true
}

type interpOperand struct {
	kind   types2.LogosInterpKind
	helper string
}

func (fl *fileLower) checkedInterpOperands(e *syntax.InterpExpr) []interpOperand {
	operands := make([]interpOperand, len(e.Exprs))
	for i, op := range e.Exprs {
		t := fl.pkg.info.TypeOf(op)
		kind, ok := types2.LogosInterpKindOf(t)
		if !ok {
			base.Fatalf("logos: interpolation operand at %s has unexpected type %s", op.Pos().String(), t)
		}
		operands[i] = interpOperandOf(t, kind)
	}
	return operands
}

func interpOperandOf(t types2.Type, kind types2.LogosInterpKind) interpOperand {
	operand := interpOperand{kind: kind}
	switch kind {
	case types2.LogosInterpError, types2.LogosInterpStringer:
		operand.helper = interpHelper(t, kind)
	}
	return operand
}

func interpHelper(t types2.Type, kind types2.LogosInterpKind) string {
	helper := "InterpError"
	if kind == types2.LogosInterpStringer {
		helper = "InterpStringer"
	}
	if t == nil {
		return helper
	}
	if c := constAsView(t); c != nil {
		t = c.Elem
	}
	switch types2.Unalias(t).Underlying().(type) {
	case *types2.Interface:
		return helper
	case *types2.Pointer:
		return helper + "Ptr"
	}
	return helper + "Value"
}

type interpNames struct {
	builtin  func(syntax.Pos, string) *syntax.Name
	selector func(syntax.Pos, string, string) *syntax.SelectorExpr
}

func (fl *fileLower) interpSyntax(e *syntax.InterpExpr, operands []interpOperand) syntax.Expr {
	pos := e.Pos()
	names := interpNames{builtin: fl.ctBuiltin, selector: fl.ctPkgSelector}
	stmts := interpBody(e, e.Exprs, operands, names)
	return interpCall(pos, stmts, names.builtin(pos, "string"))
}

func interpConstString(e *syntax.InterpExpr, info *types2.Info) (string, bool) {
	var b strings.Builder
	for i, op := range e.Exprs {
		typ, val := ctDefaultValue(info, op)
		if val == nil || val.Kind() != constant.String {
			return "", false
		}
		if kind, ok := types2.LogosInterpKindOf(typ); !ok || kind != types2.LogosInterpString {
			return "", false
		}
		b.WriteString(e.Parts[i])
		b.WriteString(constant.StringVal(val))
	}
	b.WriteString(e.Parts[len(e.Parts)-1])
	return b.String(), true
}

func (fl *fileLower) lowerInterpExpr(e *syntax.InterpExpr, kinds []types2.LogosInterpKind) (bool, otext) {
	pos := e.Pos()
	start := fl.off(pos)
	end := start + len(e.Raw)
	if s, ok := interpConstString(e, fl.pkg.info); ok {
		text := strconv.Quote(s)
		fl.add(start, end, text)
		fl.stageReplacement(e, litOf(pos, text, syntax.StringLit))
		return false, gen(text)
	}
	if len(kinds) != len(e.Exprs) || len(e.Opens) != len(e.Exprs) || len(e.Closes) != len(e.Exprs) {
		base.Fatalf("logos: interpolation at %s has %d operands but %d kinds, %d opens, %d closes",
			pos.String(), len(e.Exprs), len(kinds), len(e.Opens), len(e.Closes))
	}
	exprs := make([]syntax.Expr, len(e.Exprs))
	operands := make([]interpOperand, len(e.Exprs))
	needRT := false
	fl.interpNeedUnsafe = true
	for i, op := range e.Exprs {
		if native, ok := fl.stagedReplacements[op]; ok {
			op = native
		}
		exprs[i] = op
		operand := interpOperandOf(fl.pkg.info.TypeOf(op), kinds[i])
		operands[i] = operand
		needRT = needRT || interpFixedWidth(operand.kind) != 0 || operand.helper != ""
		fl.interpNeedFmt = fl.interpNeedFmt || operand.helper != ""
	}
	names := interpNames{builtin: nameOf, selector: interpTextSelector}
	stmts := interpBody(e, exprs, operands, names)
	text := fl.interpEjectText(e, stmts, operands, exprs)
	native := interpCall(pos, stmts, nameOf(pos, "string"))
	fl.stageReplacement(e, native)
	if fl.optBodyType == nil {
		fl.optBodyType = make(map[syntax.Expr]types2.Type)
	}
	fl.optBodyType[native] = types2.Typ[types2.String]
	fl.addT(start, end, text)
	return needRT, text
}

func interpTextSelector(pos syntax.Pos, path, name string) *syntax.SelectorExpr {
	var alias string
	switch path {
	case rtImportPath:
		alias = rtAlias
	case "fmt":
		alias = interpFmtPkg
	case "unsafe":
		alias = interpUnsafePkg
	default:
		base.Fatalf("logos: unexpected interpolation import %q", path)
	}
	return selOf(pos, nameOf(pos, alias), name)
}

func (fl *fileLower) interpEjectText(e *syntax.InterpExpr, stmts []syntax.Stmt, operands []interpOperand, exprs []syntax.Expr) otext {
	subs := make([]textSub, 0, len(operands))
	seq, stmt := 0, 0
	for i, operand := range operands {
		bind := stmts[stmt].(*syntax.AssignStmt)
		bind.Rhs = markerText(&subs, &seq, e.Pos(), fl.interpOperandText(e.Opens[i], e.Closes[i]))
		stmt++
		if operand.helper != "" {
			stmt++
		}
	}
	text := cat(gen("func() string {\n"), renderText(stmts, subs), gen("}()"))
	stmt = 0
	for i, operand := range operands {
		stmts[stmt].(*syntax.AssignStmt).Rhs = exprs[i]
		stmt++
		if operand.helper != "" {
			stmt++
		}
	}
	return text
}

func interpValueName(i int) string { return fmt.Sprintf(interpVal, i) }

func interpStringName(i int) string { return fmt.Sprintf(interpStr, i) }

func interpBody(e *syntax.InterpExpr, exprs []syntax.Expr, operands []interpOperand, names interpNames) []syntax.Stmt {
	pos := e.Pos()
	stmts := make([]syntax.Stmt, 0, len(e.Parts)+2*len(operands)+2)
	for i, operand := range operands {
		v := interpValueName(i)
		stmts = append(stmts, defineOf(pos, []syntax.Expr{nameOf(pos, v)}, []syntax.Expr{exprs[i]}))
		if operand.helper == "" {
			continue
		}
		format := names.selector(pos, "fmt", "Sprint")
		helper := names.selector(pos, rtImportPath, operand.helper)
		stmts = append(stmts, defineOf(pos,
			[]syntax.Expr{nameOf(pos, interpStringName(i))},
			[]syntax.Expr{callOf(pos, helper, []syntax.Expr{nameOf(pos, v), format})}))
	}
	bound := interpBound(e, operands, names)
	stmts = append(stmts, defineOf(pos, []syntax.Expr{nameOf(pos, interpBuf)}, []syntax.Expr{
		callOf(pos, names.builtin(pos, "make"), []syntax.Expr{
			at(&syntax.SliceType{Elem: names.builtin(pos, "byte")}, pos),
			litOf(pos, "0", syntax.IntLit),
			bound,
		}),
	}))
	appendBytes := func(arg syntax.Expr) syntax.Stmt {
		call := callOf(pos, names.builtin(pos, "append"), []syntax.Expr{nameOf(pos, interpBuf), arg})
		call.HasDots = true
		return assignOf(pos, []syntax.Expr{nameOf(pos, interpBuf)}, []syntax.Expr{call})
	}
	for i, part := range e.Parts {
		if part != "" {
			stmts = append(stmts, appendBytes(litOf(pos, strconv.Quote(part), syntax.StringLit)))
		}
		if i == len(operands) {
			break
		}
		operand := operands[i]
		switch operand.kind {
		case types2.LogosInterpString, types2.LogosInterpBytes:
			stmts = append(stmts, appendBytes(nameOf(pos, interpValueName(i))))
		case types2.LogosInterpError, types2.LogosInterpStringer:
			stmts = append(stmts, appendBytes(nameOf(pos, interpStringName(i))))
		default:
			stmts = append(stmts, interpAppendScalar(pos, names, operand.kind, nameOf(pos, interpValueName(i))))
		}
	}
	return append(stmts, returnOf(pos, []syntax.Expr{
		callOf(pos, names.selector(pos, "unsafe", "String"), []syntax.Expr{
			callOf(pos, names.selector(pos, "unsafe", "SliceData"), []syntax.Expr{nameOf(pos, interpBuf)}),
			callOf(pos, names.builtin(pos, "len"), []syntax.Expr{nameOf(pos, interpBuf)}),
		}),
	}))
}

func interpBound(e *syntax.InterpExpr, operands []interpOperand, names interpNames) syntax.Expr {
	pos := e.Pos()
	static := 0
	for _, part := range e.Parts {
		static += len(part)
	}
	var bound syntax.Expr = litOf(pos, strconv.Itoa(static), syntax.IntLit)
	for i, operand := range operands {
		width := interpFixedWidth(operand.kind)
		var term syntax.Expr
		if width != 0 {
			term = litOf(pos, strconv.Itoa(width), syntax.IntLit)
		} else {
			ref := interpValueName(i)
			if operand.helper != "" {
				ref = interpStringName(i)
			}
			term = callOf(pos, names.builtin(pos, "len"), []syntax.Expr{nameOf(pos, ref)})
		}
		bound = at(&syntax.Operation{Op: syntax.Add, X: bound, Y: term}, pos)
	}
	return bound
}

func interpAppendScalar(pos syntax.Pos, names interpNames, kind types2.LogosInterpKind, value syntax.Expr) syntax.Stmt {
	buf := nameOf(pos, interpBuf)
	var helper string
	var args []syntax.Expr
	switch kind {
	case types2.LogosInterpInt:
		helper = "AppendInt"
		args = []syntax.Expr{buf, callOf(pos, names.builtin(pos, "int64"), []syntax.Expr{value}), litOf(pos, "10", syntax.IntLit)}
	case types2.LogosInterpUint:
		helper = "AppendUint"
		args = []syntax.Expr{buf, callOf(pos, names.builtin(pos, "uint64"), []syntax.Expr{value}), litOf(pos, "10", syntax.IntLit)}
	case types2.LogosInterpFloat32, types2.LogosInterpFloat64:
		helper = "AppendFloat"
		bits := "64"
		if kind == types2.LogosInterpFloat32 {
			bits = "32"
		}
		args = []syntax.Expr{
			buf, callOf(pos, names.builtin(pos, "float64"), []syntax.Expr{value}),
			litOf(pos, "'g'", syntax.RuneLit),
			at(&syntax.Operation{Op: syntax.Sub, X: litOf(pos, "1", syntax.IntLit)}, pos),
			litOf(pos, bits, syntax.IntLit),
		}
	case types2.LogosInterpBool:
		helper = "AppendBool"
		args = []syntax.Expr{buf, value}
	default:
		base.Fatalf("logos: unexpected scalar interpolation kind %d", kind)
	}
	call := callOf(pos, names.selector(pos, rtImportPath, helper), args)
	return assignOf(pos, []syntax.Expr{nameOf(pos, interpBuf)}, []syntax.Expr{call})
}

func interpCall(pos syntax.Pos, stmts []syntax.Stmt, result syntax.Expr) syntax.Expr {
	signature := at(&syntax.FuncType{
		ResultList: []*syntax.Field{at(&syntax.Field{Type: result}, pos)},
	}, pos)
	lit := at(&syntax.FuncLit{Type: signature, Body: blockOf(pos, stmts)}, pos)
	return callOf(pos, lit, nil)
}

func (fl *fileLower) interpOperandText(open, close syntax.Pos) otext {
	text := fl.textOf(span{fl.off(open), fl.off(close) + 1})
	return text.slice(1, len(text.s)-1)
}

func (fl *fileLower) flushInterpImports(needConv, needUnsafe bool) {
	if !needConv && !needUnsafe {
		return
	}
	pos := fl.file.Pos()
	var decls []syntax.Decl
	var text string
	if needConv && !fl.rtDeclEmitted {
		decls = append(decls, rtImport(pos))
		if !fl.needRTText {
			text += "\n\nimport " + rtAlias + " " + fmt.Sprintf("%q", rtImportPath)
		}
		fl.needRT = true
	}
	if needUnsafe {
		decls = append(decls, at(&syntax.ImportDecl{
			LocalPkgName: nameOf(pos, interpUnsafePkg),
			Path:         litOf(pos, `"unsafe"`, syntax.StringLit),
		}, pos))
		text += "\nimport " + interpUnsafePkg + ` "unsafe"`
	}
	if fl.interpNeedFmt {
		decls = append(decls, at(&syntax.ImportDecl{
			LocalPkgName: nameOf(pos, interpFmtPkg),
			Path:         litOf(pos, `"fmt"`, syntax.StringLit),
		}, pos))
		text += "\nimport " + interpFmtPkg + ` "fmt"`
	}
	fl.file.DeclList = append(decls, fl.file.DeclList...)
	name := fl.file.PkgName.Value
	nameOff := fl.off(fl.file.PkgName.Pos())
	fl.add(nameOff, nameOff+len(name), name+text)
}

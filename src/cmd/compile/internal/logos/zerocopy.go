// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"cmd/compile/internal/base"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
	"fmt"
	"reflect"
)

type zeroCopy struct {
	fl      *fileLower
	info    *types2.Info
	ok      map[*types2.Var]bool
	seen    map[*types2.Var]bool
	pkgVars map[*types2.Var]bool
}

func (fl *fileLower) lowerZeroCopy() {
	z := &zeroCopy{
		fl:      fl,
		info:    fl.pkg.info,
		ok:      make(map[*types2.Var]bool),
		seen:    make(map[*types2.Var]bool),
		pkgVars: make(map[*types2.Var]bool),
	}
	for _, decl := range fl.file.DeclList {
		if vd, ok := decl.(*syntax.VarDecl); ok {
			for _, name := range vd.NameList {
				if v, ok := z.info.Defs[name].(*types2.Var); ok {
					z.pkgVars[v] = true
				}
			}
		}
	}
	syntax.Inspect(fl.file, func(n syntax.Node) bool { z.collect(n); return true })
	syntax.Inspect(fl.file, func(n syntax.Node) bool { return z.visit(n) })
	z.eraseSurvivingConstFuns()
}

func (z *zeroCopy) eraseSurvivingConstFuns() {
	syntax.Inspect(z.fl.file, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok {
			if _, ok := syntax.Unparen(call.Fun).(*syntax.ConstType); ok {
				call.Fun = z.eraseConstFun(call.Fun)
			}
		}
		return true
	})
}

func (z *zeroCopy) eraseConstFun(e syntax.Expr) syntax.Expr {
	for {
		c, ok := e.(*syntax.ConstType)
		if !ok {
			return e
		}
		start := z.fl.off(c.Pos())
		end := z.fl.off(c.Elem.Pos())
		if end > start {
			z.fl.add(start, end, "")
		}
		e = c.Elem
	}
}

func (z *zeroCopy) visit(n syntax.Node) bool {
	z.rewriteNode(n)
	return true
}

func (z *zeroCopy) collect(n syntax.Node) {
	switch n := n.(type) {
	case *syntax.VarDecl:
		if len(n.NameList) != 1 {
			return
		}
		v, ok := z.info.Defs[n.NameList[0]].(*types2.Var)
		if !ok || !z.isViewBytes(v.Type()) {
			return
		}
		z.setVar(v, z.provOf(n.Values))
	case *syntax.AssignStmt:
		if n.Op != 0 && n.Op != syntax.Def {
			return
		}
		lhs := unlistExpr(n.Lhs)
		vals := unlistExpr(n.Rhs)
		if len(lhs) != 1 {
			return
		}
		name, ok := syntax.Unparen(lhs[0]).(*syntax.Name)
		if !ok {
			return
		}
		var v *types2.Var
		if n.Op == syntax.Def {
			v, _ = z.info.Defs[name].(*types2.Var)
		} else {
			v, _ = z.info.ObjectOf(name).(*types2.Var)
		}
		if v == nil || !z.isViewBytes(v.Type()) {
			return
		}
		var rhs syntax.Expr
		if len(vals) == 1 {
			rhs = vals[0]
		}
		z.setVar(v, z.provOf(rhs))
	}
}

func (z *zeroCopy) setVar(v *types2.Var, p bool) {
	if z.pkgVars[v] {
		return
	}
	if !z.seen[v] {
		z.seen[v] = true
		z.ok[v] = p
		return
	}
	z.ok[v] = z.ok[v] && p
}

func (z *zeroCopy) provOf(e syntax.Expr) bool {
	if e == nil {
		return false
	}
	switch n := syntax.Unparen(e).(type) {
	case *syntax.CallExpr:
		return z.convProv(n)
	case *syntax.CompositeLit:
		return z.isByteSlice(z.info.TypeOf(n))
	case *syntax.Name:
		return z.varProv(n)
	}
	return false
}

func (z *zeroCopy) convProv(call *syntax.CallExpr) bool {
	switch syntax.Unparen(call.Fun).(type) {
	case *syntax.ConstType:
		if !z.isViewBytesCall(call) || len(call.ArgList) != 1 {
			return false
		}
		return z.stringOrVarProv(call.ArgList[0])
	case *syntax.SliceType:
		if !z.isByteSlice(z.info.TypeOf(call)) || len(call.ArgList) != 1 {
			return false
		}
		return z.stringOrVarProv(call.ArgList[0])
	}
	return false
}

func (z *zeroCopy) stringOrVarProv(e syntax.Expr) bool {
	a := syntax.Unparen(e)
	if z.isStringType(z.info.TypeOf(a)) {
		return true
	}
	if name, ok := a.(*syntax.Name); ok {
		return z.varProv(name)
	}
	return false
}

func (z *zeroCopy) varProv(name *syntax.Name) bool {
	v, ok := z.info.ObjectOf(name).(*types2.Var)
	if !ok || z.pkgVars[v] || !z.isViewBytes(v.Type()) {
		return false
	}
	return z.ok[v]
}

func (z *zeroCopy) rewriteNode(n syntax.Node) {
	switch n := n.(type) {
	case *syntax.VarDecl:
		if len(n.NameList) == 1 {
			if v, ok := z.info.Defs[n.NameList[0]].(*types2.Var); ok && z.isViewBytes(v.Type()) {
				n.Values = z.rewritePlainConv(n.Values)
			}
		}
	case *syntax.AssignStmt:
		if n.Op != 0 {
			break
		}
		lhs := unlistExpr(n.Lhs)
		vals := unlistExpr(n.Rhs)
		if len(lhs) != 1 || len(vals) != 1 {
			break
		}
		name, ok := syntax.Unparen(lhs[0]).(*syntax.Name)
		if !ok {
			break
		}
		if v, ok := z.info.ObjectOf(name).(*types2.Var); ok && z.isViewBytes(v.Type()) {
			n.Rhs = z.rewritePlainConv(n.Rhs)
		}
	}
	z.rewriteFields(n)
}

func (z *zeroCopy) rewritePlainConv(e syntax.Expr) syntax.Expr {
	call, ok := syntax.Unparen(e).(*syntax.CallExpr)
	if !ok {
		return e
	}
	if _, ok := syntax.Unparen(call.Fun).(*syntax.SliceType); !ok {
		return e
	}
	if !z.isByteSlice(z.info.TypeOf(call)) || len(call.ArgList) != 1 {
		return e
	}
	arg := syntax.Unparen(call.ArgList[0])
	if !z.isStringType(z.info.TypeOf(arg)) {
		return e
	}
	return z.sliceOfString(call, arg)
}

func (z *zeroCopy) rewriteFields(n syntax.Node) {
	rv := reflect.ValueOf(n)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return
	}
	rt := rv.Type()
	for i := range rt.NumField() {
		fv := rv.Field(i)
		if !fv.CanSet() {
			continue
		}
		ft := rt.Field(i).Type
		switch {
		case ft == reflect.TypeFor[syntax.Expr]():
			if e, _ := fv.Interface().(syntax.Expr); e != nil {
				if ne, ok := z.rewriteCall(e); ok {
					fv.Set(reflect.ValueOf(ne))
				}
			}
		case (ft.Kind() == reflect.Slice || ft.Kind() == reflect.Array) && ft.Elem() == reflect.TypeFor[syntax.Expr]():
			for j := range fv.Len() {
				ev := fv.Index(j)
				if e, _ := ev.Interface().(syntax.Expr); e != nil {
					if ne, ok := z.rewriteCall(e); ok {
						ev.Set(reflect.ValueOf(ne))
					}
				}
			}
		case ft == reflect.TypeFor[*syntax.CallExpr]():
			if e, _ := fv.Interface().(*syntax.CallExpr); e != nil {
				if ne, ok := z.rewriteCall(e); ok {
					fv.Set(reflect.ValueOf(ne))
				}
			}
		}
	}
}

func (z *zeroCopy) rewriteCall(e syntax.Expr) (syntax.Expr, bool) {
	call, ok := syntax.Unparen(e).(*syntax.CallExpr)
	if !ok {
		return nil, false
	}
	switch fun := syntax.Unparen(call.Fun).(type) {
	case *syntax.ConstType:
		if !z.isViewBytesCall(call) || len(call.ArgList) != 1 {
			return nil, false
		}
		arg := syntax.Unparen(call.ArgList[0])
		if !z.isStringType(z.info.TypeOf(arg)) {
			return nil, false
		}
		return z.sliceOfString(call, arg), true
	case *syntax.Name:
		if fun.Value != "string" || !z.bareStringName(fun) {
			return nil, false
		}
		if !z.isStringType(z.info.TypeOf(call)) || len(call.ArgList) != 1 {
			return nil, false
		}
		arg, ok := syntax.Unparen(call.ArgList[0]).(*syntax.Name)
		if !ok || !z.varProv(arg) {
			return nil, false
		}
		return z.stringOfSlice(call, arg), true
	}
	return nil, false
}

func (z *zeroCopy) bareStringName(name *syntax.Name) bool {
	if obj := z.info.ObjectOf(name); obj != nil {
		_, ok := obj.(*types2.TypeName)
		return ok
	}
	return true
}

func (z *zeroCopy) sliceOfString(call *syntax.CallExpr, arg syntax.Expr) syntax.Expr {
	return z.runtimeConvert(call, arg, types2.Uint8)
}

func (z *zeroCopy) stringOfSlice(call *syntax.CallExpr, arg syntax.Expr) syntax.Expr {
	return z.runtimeConvert(call, arg, types2.String)
}

func (z *zeroCopy) runtimeConvert(call *syntax.CallExpr, arg syntax.Expr, target types2.BasicKind) syntax.Expr {
	pos := call.Pos()
	split, ok := splitCall(z.fl.src, z.fl.off(pos))
	if !ok || len(split.args) != 1 {
		base.Fatalf("logos: conversion at %s has no single operand", pos.String())
	}
	start := z.fl.off(syntax.Unparen(call.Fun).Pos())
	text := z.fl.lazyText(split.args[0])
	qual := z.fl.synthQualifier(types2.Unsafe)
	name := fmt.Sprintf("_logos_zeroCopy_%x", pos.String())
	header := "struct { data " + qual + ".Pointer; length Length }"
	decl := "func " + name + "[Source, Result, Length interface{}](source Source, result Result, _ Length) Result {\n" +
		"header := *(*" + header + ")(" + qual + ".Pointer(&source))\n"
	zero := qual + ".String(" + qual + ".StringData(\"\"), 0)"
	if target == types2.Uint8 {
		sliceHeader := "struct { data " + qual + ".Pointer; length, capacity Length }"
		decl += "*(*" + sliceHeader + ")(" + qual + ".Pointer(&result)) = " + sliceHeader + "{header.data, header.length, header.length}\n"
		zero = qual + ".Slice(" + qual + ".StringData(\"\"), 0)"
	} else {
		decl += "*(*" + header + ")(" + qual + ".Pointer(&result)) = header\n"
	}
	decl += "return result\n}\n"
	z.fl.constDecls = append(z.fl.constDecls, parseGeneratedDecls(decl, pos)...)
	z.fl.constText += "\n" + decl
	z.fl.addT(start, split.end, cat(gen(name+"("), text, gen(", "+zero+", 0)")))
	generated, ok := parseConstMapEntryExpr(name+"(operand, "+zero+", 0)", pos)
	if !ok {
		base.Fatalf("logos: cannot generate zero-copy conversion at %s", pos.String())
	}
	converted := generated.(*syntax.CallExpr)
	converted.ArgList[0] = arg
	return converted
}

func (z *zeroCopy) isViewBytesCall(call *syntax.CallExpr) bool {
	return z.isViewBytes(z.info.TypeOf(call))
}

func (z *zeroCopy) isViewBytes(t types2.Type) bool {
	c := constAsView(t)
	return c != nil && z.isByteSlice(c.Elem)
}

func (z *zeroCopy) isByteSlice(t types2.Type) bool {
	s, ok := types2.Unalias(t).(*types2.Slice)
	if !ok {
		return false
	}
	b, ok := types2.Unalias(s.Elem()).(*types2.Basic)
	return ok && b.Kind() == types2.Uint8
}

func (z *zeroCopy) isStringType(t types2.Type) bool {
	if t == nil {
		return false
	}
	b, ok := t.Underlying().(*types2.Basic)
	return ok && (b.Kind() == types2.String || b.Kind() == types2.UntypedString)
}

type ctByteOrigin struct {
	local   bool
	blocked bool
	inputs  []syntax.Expr
}

func ctByteConversions(root syntax.Node, info *types2.Info) map[*syntax.CallExpr]types2.BasicKind {
	z := zeroCopy{info: info}
	origins := make(map[*types2.Var]ctByteOrigin)
	readonlyCalls := make(map[*syntax.CallExpr]types2.BasicKind)
	var strings []*syntax.CallExpr
	write := func(lhs *syntax.Name, rhs syntax.Expr, unknown bool) {
		v, ok := info.ObjectOf(lhs).(*types2.Var)
		if !ok || !z.isViewBytes(v.Type()) {
			return
		}
		origin := origins[v]
		origin.local = origin.local || info.Defs[lhs] == v
		origin.blocked = origin.blocked || unknown
		if rhs != nil && !info.Types[rhs].IsNil() {
			origin.inputs = append(origin.inputs, rhs)
		}
		origins[v] = origin
		if call, ok := syntax.Unparen(rhs).(*syntax.CallExpr); ok && z.ctStringBytes(call, true) {
			readonlyCalls[call] = types2.Uint8
		}
	}
	writeList := func(lhs, rhs []syntax.Expr) {
		paired := len(lhs) == len(rhs)
		for i, dst := range lhs {
			id, ok := syntax.Unparen(dst).(*syntax.Name)
			if !ok {
				continue
			}
			var src syntax.Expr
			if paired {
				src = rhs[i]
			}
			write(id, src, !paired)
		}
	}
	syntax.Inspect(root, func(n syntax.Node) bool {
		switch n := n.(type) {
		case *syntax.VarDecl:
			rhs := unlistExpr(n.Values)
			for i, id := range n.NameList {
				var src syntax.Expr
				if len(rhs) == len(n.NameList) {
					src = rhs[i]
				}
				write(id, src, len(rhs) != 0 && src == nil)
			}
		case *syntax.AssignStmt:
			writeList(unlistExpr(n.Lhs), unlistExpr(n.Rhs))
		case *syntax.RangeClause:
			writeList(unlistExpr(n.Lhs), nil)
		case *syntax.Operation:
			if id, ok := syntax.Unparen(n.X).(*syntax.Name); ok && n.Op == syntax.And && n.Y == nil {
				write(id, nil, true)
			}
		case *syntax.CallExpr:
			if z.ctStringBytes(n, false) {
				readonlyCalls[n] = types2.Uint8
			} else if tv := info.Types[n.Fun]; tv.IsType() && z.isStringType(tv.Type) && len(n.ArgList) == 1 && !n.HasDots {
				strings = append(strings, n)
			}
		}
		return true
	})
	proven := make(map[*types2.Var]bool)
	var stable func(syntax.Expr) bool
	stable = func(e syntax.Expr) bool {
		switch x := syntax.Unparen(e).(type) {
		case *syntax.Name:
			switch obj := info.ObjectOf(x).(type) {
			case *types2.Const:
				return z.isViewBytes(obj.Type()) || z.isByteSlice(obj.Type())
			case *types2.Var:
				if ok, known := proven[obj]; known {
					return ok
				}
				proven[obj] = false
				o := origins[obj]
				if !o.local || o.blocked {
					return false
				}
				for _, input := range o.inputs {
					if !stable(input) {
						return false
					}
				}
				proven[obj] = true
				return true
			}
		case *syntax.CompositeLit:
			return z.isByteSlice(info.TypeOf(x)) || z.isViewBytes(info.TypeOf(x))
		case *syntax.CallExpr:
			if readonlyCalls[x] == types2.Uint8 {
				return true
			}
			fun := info.Types[x.Fun]
			return fun.IsType() && z.isViewBytes(fun.Type) && len(x.ArgList) == 1 &&
				z.isViewBytes(info.TypeOf(x.ArgList[0])) && stable(x.ArgList[0])
		case *syntax.SliceExpr:
			return stable(x.X)
		}
		return false
	}
	for _, call := range strings {
		if stable(call.ArgList[0]) {
			readonlyCalls[call] = types2.String
		}
	}
	return readonlyCalls
}

func (z *zeroCopy) ctStringBytes(call *syntax.CallExpr, readonlySink bool) bool {
	if len(call.ArgList) != 1 || call.HasDots {
		return false
	}
	fun := z.info.Types[call.Fun]
	if !fun.IsType() || !(z.isViewBytes(fun.Type) || readonlySink && z.isByteSlice(fun.Type)) {
		return false
	}
	return z.isStringType(z.info.TypeOf(call.ArgList[0]))
}

func (w ctWalker) ctZeroCopy(call *syntax.CallExpr) (syntax.Expr, []syntax.Stmt, bool) {
	direction := w.fl.ct.byteConversions[call]
	if direction != types2.Uint8 && direction != types2.String {
		return nil, nil, false
	}
	pos := call.Pos()
	target := w.fl.pkg.info.TypeOf(call.Fun)
	var source types2.Type = types2.Typ[types2.String]
	data, result := "StringData", "Slice"
	operand := call.ArgList[0]
	arg, pre := w.expr(operand)
	if direction == types2.String {
		source = types2.NewSlice(types2.Typ[types2.Uint8])
		data, result = "SliceData", "String"
	} else if t := w.fl.pkg.info.TypeOf(operand); !types2.Identical(types2.Default(t), source) {
		arg = callOf(pos, w.fl.ctBuiltin(pos, "string"), []syntax.Expr{arg})
	}
	id := nameOf(pos, "_logos_bytes_source")
	length := callOf(pos, w.fl.ctBuiltin(pos, "len"), []syntax.Expr{nameOf(pos, id.Value)})
	ptr := callOf(pos, w.fl.ctPkgSelector(pos, "unsafe", data), []syntax.Expr{nameOf(pos, id.Value)})
	var view syntax.Expr = callOf(pos, w.fl.ctPkgSelector(pos, "unsafe", result), []syntax.Expr{ptr, length})
	if direction == types2.String && !types2.Identical(target, types2.Typ[types2.String]) {
		view = callOf(pos, w.fl.ctTypeExpr(pos, target), []syntax.Expr{view})
	}
	ft := at(&syntax.FuncType{
		ParamList:  []*syntax.Field{at(&syntax.Field{Name: id, Type: w.fl.ctTypeExpr(pos, source)}, pos)},
		ResultList: []*syntax.Field{at(&syntax.Field{Type: w.fl.ctTypeExpr(pos, target)}, pos)},
	}, pos)
	lit := at(&syntax.FuncLit{Type: ft, Body: blockOf(pos, []syntax.Stmt{returnOf(pos, []syntax.Expr{view})})}, pos)
	return callOf(pos, lit, []syntax.Expr{arg}), pre, true
}

func unlistExpr(e syntax.Expr) []syntax.Expr {
	if e == nil {
		return nil
	}
	if l, ok := syntax.Unparen(e).(*syntax.ListExpr); ok {
		return l.ElemList
	}
	return []syntax.Expr{e}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"bytes"
	"reflect"
	"slices"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

func (fl *fileLower) hoistTryInits() {
	for _, decl := range fl.file.DeclList {
		switch d := decl.(type) {
		case *syntax.FuncDecl:
			if d.Body != nil {
				newFuncLower(fl, d.Type, fl.cfFuncSig(d), d.Body).hoistSwitchExprInits()
			}
		case *syntax.VarDecl:
			syntax.Inspect(d, func(n syntax.Node) bool {
				if lit, ok := n.(*syntax.FuncLit); ok {
					newFuncLower(fl, lit.Type, fl.cfLitSig(lit), lit.Body).hoistSwitchExprInits()
					return false
				}
				return true
			})
		}
	}
}

func (fc *funcLower) hoistSwitchExprInits() {
	fc.body.List = fc.processSwitchList(fc.body.List)
}

func (fc *funcLower) processSwitchList(list []syntax.Stmt) []syntax.Stmt {
	var out []syntax.Stmt
	for _, s := range list {
		out = append(out, fc.processSwitchStmt(s)...)
	}
	return out
}

func (fc *funcLower) processSwitchStmt(s syntax.Stmt) []syntax.Stmt {
	fc.processLitBodies(s)
	direct := fc.directStmtSwitches(s)
	if len(direct) == 0 {
		fc.recurseSwitchChildren(s)
		return []syntax.Stmt{s}
	}
	if fc.stmtNeedsTemp(s, direct) {
		return fc.tempSwitchStmt(s, direct)
	}
	return fc.wrapSwitchStmt(s, direct)
}

func (fc *funcLower) processLitBodies(s syntax.Stmt) {
	syntax.Inspect(s, func(n syntax.Node) bool {
		switch t := n.(type) {
		case *syntax.FuncLit:
			if t.Body != nil {
				newFuncLower(fc.fl, t.Type, fc.fl.cfLitSig(t), t.Body).hoistSwitchExprInits()
			}
			return false
		case *syntax.GroupChild:
			if t.Body != nil {
				ft := at(&syntax.FuncType{
					ResultList: []*syntax.Field{at(&syntax.Field{Type: nameOf(t.Body.Pos(), "error")}, t.Body.Pos())},
				}, t.Body.Pos())
				newFuncLower(fc.fl, ft, nil, t.Body).hoistSwitchExprInits()
			}
			return false
		case *syntax.GroupStmt:
			return true
		case *syntax.LambdaExpr:
			return false
		}
		return true
	})
}

func (fc *funcLower) recurseSwitchChildren(s syntax.Stmt) {
	switch t := s.(type) {
	case *syntax.LabeledStmt:
		t.Stmt = fc.processSwitchSingle(t.Stmt)
	case *syntax.BlockStmt:
		t.List = fc.processSwitchList(t.List)
	case *syntax.IfStmt:
		t.Then.List = fc.processSwitchList(t.Then.List)
		fc.recurseSwitchElse(t)
	case *syntax.ForStmt:
		t.Body.List = fc.processSwitchList(t.Body.List)
	case *syntax.SwitchStmt:
		for _, c := range t.Body {
			c.Body = fc.processSwitchList(c.Body)
		}
	case *syntax.SelectStmt:
		for _, c := range t.Body {
			c.Body = fc.processSwitchList(c.Body)
		}
	}
}

func (fc *funcLower) recurseSwitchElse(s *syntax.IfStmt) {
	switch els := s.Else.(type) {
	case *syntax.BlockStmt:
		els.List = fc.processSwitchList(els.List)
	case *syntax.IfStmt:
		els.Then.List = fc.processSwitchList(els.Then.List)
		fc.recurseSwitchElse(els)
	}
}

func (fc *funcLower) processSwitchSingle(s syntax.Stmt) syntax.Stmt {
	res := fc.processSwitchStmt(s)
	if len(res) == 1 {
		return res[0]
	}
	return blockOf(s.Pos(), res)
}

func hasDirectTryInit(sx *syntax.SwitchExpr) bool {
	if sx.Init == nil {
		return false
	}
	switch s := sx.Init.(type) {
	case *syntax.AssignStmt:
		_, _, ok := tryAssignOf(s)
		return ok
	case *syntax.ExprStmt:
		_, _, ok := tryExprOf(s)
		return ok
	}
	return false
}

func (fc *funcLower) collectExprSwitches(e syntax.Node, out *[]*syntax.SwitchExpr) {
	if e == nil {
		return
	}
	syntax.Inspect(e, func(n syntax.Node) bool {
		switch t := n.(type) {
		case *syntax.FuncLit, *syntax.LambdaExpr, *syntax.GroupStmt, *syntax.GroupChild:
			return false
		case *syntax.SwitchExpr:
			if hasDirectTryInit(t) {
				*out = append(*out, t)
			}
			return true
		}
		return true
	})
}

func (fc *funcLower) directStmtSwitches(s syntax.Stmt) []*syntax.SwitchExpr {
	var out []*syntax.SwitchExpr
	switch t := s.(type) {
	case *syntax.AssignStmt:
		fc.collectExprSwitches(t.Rhs, &out)
	case *syntax.ExprStmt:
		fc.collectExprSwitches(t.X, &out)
	case *syntax.ReturnStmt:
		fc.collectExprSwitches(t.Results, &out)
	case *syntax.IfStmt:
		if t.Init != nil {
			fc.collectExprSwitches(t.Init, &out)
		}
		fc.collectExprSwitches(t.Cond, &out)
	case *syntax.ForStmt:
		if t.Init != nil {
			fc.collectExprSwitches(t.Init, &out)
		}
		fc.collectExprSwitches(t.Cond, &out)
		if t.Post != nil {
			fc.collectExprSwitches(t.Post, &out)
		}
	case *syntax.SwitchStmt:
		if t.Init != nil {
			fc.collectExprSwitches(t.Init, &out)
		}
		fc.collectExprSwitches(t.Tag, &out)
	case *syntax.LabeledStmt:
		return fc.directStmtSwitches(t.Stmt)
	}
	return out
}

func (fc *funcLower) stmtNeedsTemp(s syntax.Stmt, switches []*syntax.SwitchExpr) bool {
	switch s.(type) {
	case *syntax.ForStmt:
		return false
	case *syntax.AssignStmt, *syntax.ExprStmt:
		for _, sx := range switches {
			if fc.switchUnderShort(s, sx) {
				return false
			}
		}
		for _, sx := range switches {
			if !fc.isSingleExpr(s, sx) {
				return false
			}
		}
		return true
	case *syntax.ReturnStmt, *syntax.IfStmt, *syntax.SwitchStmt:
		for _, sx := range switches {
			if !fc.isSingleExpr(s, sx) {
				return false
			}
		}
		return true
	}
	return false
}

func (fc *funcLower) isSingleExpr(s syntax.Stmt, sx *syntax.SwitchExpr) bool {
	switch t := s.(type) {
	case *syntax.AssignStmt:
		return syntax.Unparen(t.Rhs) == syntax.Expr(sx)
	case *syntax.ExprStmt:
		return syntax.Unparen(t.X) == syntax.Expr(sx)
	case *syntax.ReturnStmt:
		for _, r := range syntax.UnpackListExpr(t.Results) {
			if syntax.Unparen(r) == syntax.Expr(sx) {
				return true
			}
		}
		return false
	case *syntax.IfStmt:
		if t.Init != nil {
			if a, ok := t.Init.(*syntax.AssignStmt); ok {
				if syntax.Unparen(a.Rhs) == syntax.Expr(sx) {
					return true
				}
			}
		}
		return syntax.Unparen(t.Cond) == syntax.Expr(sx)
	case *syntax.ForStmt:
		return false
	case *syntax.SwitchStmt:
		if t.Init != nil {
			if a, ok := t.Init.(*syntax.AssignStmt); ok {
				if syntax.Unparen(a.Rhs) == syntax.Expr(sx) {
					return true
				}
			}
		}
		if t.Tag != nil {
			return syntax.Unparen(t.Tag) == syntax.Expr(sx)
		}
		return false
	case *syntax.LabeledStmt:
		return fc.isSingleExpr(t.Stmt, sx)
	}
	return false
}

func (fc *funcLower) switchUnderShort(s syntax.Stmt, sx *syntax.SwitchExpr) bool {
	fields := fc.stmtExprFields(s)
	for _, f := range fields {
		if exprContainsShortSwitch(f, sx) {
			return true
		}
	}
	return false
}

func (fc *funcLower) stmtExprFields(s syntax.Stmt) []syntax.Node {
	switch t := s.(type) {
	case *syntax.AssignStmt:
		return []syntax.Node{t.Rhs}
	case *syntax.ExprStmt:
		return []syntax.Node{t.X}
	case *syntax.ReturnStmt:
		return []syntax.Node{t.Results}
	case *syntax.IfStmt:
		var out []syntax.Node
		if t.Init != nil {
			out = append(out, t.Init)
		}
		out = append(out, t.Cond)
		return out
	case *syntax.ForStmt:
		var out []syntax.Node
		if t.Init != nil {
			out = append(out, t.Init)
		}
		if t.Cond != nil {
			out = append(out, t.Cond)
		}
		if t.Post != nil {
			out = append(out, t.Post)
		}
		return out
	case *syntax.SwitchStmt:
		var out []syntax.Node
		if t.Init != nil {
			out = append(out, t.Init)
		}
		if t.Tag != nil {
			out = append(out, t.Tag)
		}
		return out
	case *syntax.LabeledStmt:
		return fc.stmtExprFields(t.Stmt)
	}
	return nil
}

func exprContainsShortSwitch(root syntax.Node, sx *syntax.SwitchExpr) bool {
	found := false
	syntax.Inspect(root, func(n syntax.Node) bool {
		if found {
			return false
		}
		op, ok := n.(*syntax.Operation)
		if !ok || (op.Op != syntax.AndAnd && op.Op != syntax.OrOr) {
			return true
		}
		inside := false
		syntax.Inspect(op, func(m syntax.Node) bool {
			if m == syntax.Node(sx) {
				inside = true
				return false
			}
			return true
		})
		if inside {
			found = true
			return false
		}
		return true
	})
	return found
}

func (fc *funcLower) tempSwitchStmt(s syntax.Stmt, switches []*syntax.SwitchExpr) []syntax.Stmt {
	ordered := orderByOff(fc.fl, switches)
	var out []syntax.Stmt
	fl := fc.fl
	hpos := stmtStartPos(s)
	for _, sx := range ordered {
		typ := fl.pkg.info.TypeOf(sx)
		nativeT, _, ok := fl.spellTypeBoth(typ, sx.Pos())
		if !ok {
			continue
		}
		_, call := fc.errorSwitchIIFE(sx, nativeT, typ)
		tmpName := fc.nextArg()
		errName := fc.nextErr()
		tmp := nameOf(sx.Pos(), tmpName)
		decl := defineOf(hpos, []syntax.Expr{nameOf(hpos, tmpName), nameOf(hpos, errName)}, []syntax.Expr{call})
		declText := renderText([]syntax.Stmt{decl}, nil)
		fl.addT(fl.off(sx.Pos()), fl.off(sx.Rbrace)+1, cat(gen(tmpName)))
		fl.addT(fl.off(hpos), fl.off(hpos), cat(declText.trimRight("\n"), gen("; ")))
		checkNative, checkText := fc.plainReturnCheck(hpos, errName)
		fl.addT(fl.off(hpos), fl.off(hpos), cat(checkText.trimRight("\n"), gen("; ")))
		out = append(out, decl)
		out = append(out, checkNative...)
		fc.replaceSwitchWith(s, sx, tmp)
	}
	fc.recurseSwitchChildren(s)
	out = append(out, s)
	return out
}

func (fc *funcLower) wrapSwitchStmt(s syntax.Stmt, switches []*syntax.SwitchExpr) []syntax.Stmt {
	stmtErr := fc.nextErr()
	pos := stmtStartPos(s)
	fl := fc.fl
	declNative := errorVarDecl(pos, nameOf(pos, stmtErr))
	declText := renderText([]syntax.Stmt{declNative}, nil)
	fl.addT(fl.off(pos), fl.off(pos), cat(declText.trimRight("\n"), gen("; ")))
	outer := outermostSwitches(fl, switches)
	inner := orderInnerFirst(fl, switches)
	for _, sx := range inner {
		typ := fl.pkg.info.TypeOf(sx)
		nativeT, _, ok := fl.spellTypeBoth(typ, sx.Pos())
		if !ok {
			continue
		}
		lit, _ := fc.errorSwitchIIFE(sx, nativeT, typ)
		wcall := fc.wrapCallWithStmtErr(sx, lit, nativeT, typ, stmtErr)
		if isOutermost(fl, sx, switches, outer) {
			txt := renderExprFull(wcall)
			fl.addT(fl.off(sx.Pos()), fl.off(sx.Rbrace)+1, cat(gen(txt)))
		}
		fc.replaceSwitchWith(s, sx, wcall)
	}
	epos := syntax.EndPos(s)
	tailOff := stmtEnd(fl.src, fl.off(pos))
	checkNative, checkText := fc.plainReturnCheck(epos, stmtErr)
	if fs, ok := s.(*syntax.ForStmt); ok {
		topNative, topText := fc.plainReturnCheck(fs.Body.Pos(), stmtErr)
		fs.Body.List = append(topNative, fs.Body.List...)
		fl.addT(fl.off(fs.Body.Pos())+1, fl.off(fs.Body.Pos())+1, cat(gen(" "), topText))
	}
	out := []syntax.Stmt{declNative, s}
	out = append(out, checkNative...)
	if _, isCtl := s.(*syntax.ForStmt); isCtl {
		tailOff = blockEndOff(fl, s)
	}
	fl.addT(tailOff, tailOff, cat(gen("; "), checkText))
	fc.recurseSwitchChildren(s)
	return out
}
func orderByOff(fl *fileLower, switches []*syntax.SwitchExpr) []*syntax.SwitchExpr {
	out := slices.Clone(switches)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && fl.off(out[j].Pos()) < fl.off(out[j-1].Pos()); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func orderInnerFirst(fl *fileLower, switches []*syntax.SwitchExpr) []*syntax.SwitchExpr {
	out := slices.Clone(switches)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && fl.off(out[j].Pos()) > fl.off(out[j-1].Pos()); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func outermostSwitches(fl *fileLower, switches []*syntax.SwitchExpr) map[*syntax.SwitchExpr]bool {
	out := make(map[*syntax.SwitchExpr]bool)
	for _, sx := range switches {
		contained := false
		ss, se := fl.off(sx.Pos()), fl.off(sx.Rbrace)
		for _, other := range switches {
			if other == sx {
				continue
			}
			os, oe := fl.off(other.Pos()), fl.off(other.Rbrace)
			if os <= ss && se <= oe {
				contained = true
				break
			}
		}
		if !contained {
			out[sx] = true
		}
	}
	return out
}

func isOutermost(fl *fileLower, sx *syntax.SwitchExpr, all []*syntax.SwitchExpr, outer map[*syntax.SwitchExpr]bool) bool {
	return outer[sx]
}

func (fc *funcLower) replaceSwitchWith(s syntax.Stmt, sx *syntax.SwitchExpr, with syntax.Expr) {
	v := reflect.ValueOf(s)
	replaceInValue(v, sx, with)
}

func replaceInValue(v reflect.Value, sx *syntax.SwitchExpr, with syntax.Expr) bool {
	if !v.IsValid() {
		return false
	}
	if v.Kind() == reflect.Interface && !v.IsNil() && v.CanSet() {
		if cur, ok := v.Elem().Interface().(*syntax.SwitchExpr); ok && cur == sx {
			v.Set(reflect.ValueOf(with))
			return true
		}
	}
	switch v.Kind() {
	case reflect.Ptr:
		if v.IsNil() {
			return false
		}
		if sxNode, ok := v.Interface().(*syntax.SwitchExpr); ok && sxNode == sx {
			return false
		}
		return replaceInValue(v.Elem(), sx, with)
	case reflect.Struct:
		t := v.Type()
		for i := range v.NumField() {
			if !t.Field(i).IsExported() {
				continue
			}
			if replaceInValue(v.Field(i), sx, with) {
				return true
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if replaceInValue(v.Index(i), sx, with) {
				return true
			}
		}
	case reflect.Interface:
		if v.IsNil() {
			return false
		}
		if cur, ok := v.Elem().Interface().(*syntax.SwitchExpr); ok && cur == sx {
			if v.CanSet() {
				v.Set(reflect.ValueOf(with))
				return true
			}
			return false
		}
		return replaceInValue(v.Elem(), sx, with)
	}
	return false
}

func (fc *funcLower) errorSwitchIIFE(sx *syntax.SwitchExpr, nativeT syntax.Expr, typ types2.Type) (*syntax.FuncLit, *syntax.CallExpr) {
	pos := sx.Pos()
	ft := at(&syntax.FuncType{
		ResultList: []*syntax.Field{
			at(&syntax.Field{Type: nativeT}, pos),
			at(&syntax.Field{Type: nameOf(pos, "error")}, pos),
		},
	}, pos)
	body := fc.errorSwitchBody(sx, nativeT, typ)
	lit := at(&syntax.FuncLit{Type: ft, Body: body}, pos)
	return lit, callOf(pos, lit, nil)
}

func (fc *funcLower) errorSwitchBody(sx *syntax.SwitchExpr, nativeT syntax.Expr, typ types2.Type) *syntax.BlockStmt {
	pos := sx.Pos()
	var list []syntax.Stmt
	if sx.Init != nil {
		switch init := sx.Init.(type) {
		case *syntax.AssignStmt:
			if t, call, ok := tryAssignOf(init); ok {
				list = append(list, fc.innerTryAssign(init, t, call, nativeT, typ)...)
			} else {
				list = append(list, init)
			}
		case *syntax.ExprStmt:
			if t, call, ok := tryExprOf(init); ok {
				list = append(list, fc.innerTryExpr(init, t, call, nativeT, typ)...)
			} else {
				list = append(list, init)
			}
		}
	}
	tag := sx.Tag
	clauses := make([]*syntax.CaseClause, 0, len(sx.Arms))
	for _, arm := range sx.Arms {
		result := returnOf(arm.Arrow, []syntax.Expr{arm.Body, nilOf(arm.Arrow)})
		clauses = append(clauses, at(&syntax.CaseClause{
			Cases: arm.Cases,
			Body:  []syntax.Stmt{result},
			Colon: arm.Arrow,
		}, arm.Pos()))
	}
	list = append(list, at(&syntax.SwitchStmt{Tag: tag, Body: clauses, Rbrace: sx.Rbrace}, pos))
	return at(&syntax.BlockStmt{List: list, Rbrace: sx.Rbrace}, pos)
}

func (fc *funcLower) innerTryAssign(s *syntax.AssignStmt, t *syntax.TryExpr, call *syntax.CallExpr, nativeT syntax.Expr, typ types2.Type) []syntax.Stmt {
	pos := s.Pos()
	errSpell := fc.nextErr()
	err := nameOf(pos, errSpell)
	lhs := syntax.UnpackListExpr(s.Lhs)
	zeroT := fc.zeroOfSwitchResult(nativeT, typ, pos)
	site := nameOf(pos, fc.fl.newSite(t.Pos(), t.Pos().Line()))
	trace := exprStmtOf(pos, callOf(pos, rtSel(pos, "TraceErr"), []syntax.Expr{err, at(&syntax.Operation{Op: syntax.And, X: site}, pos)}))
	exit := []syntax.Stmt{trace, returnOf(pos, []syntax.Expr{zeroT, err})}
	if s.Op == syntax.Def {
		return []syntax.Stmt{
			errDefineOf(pos, lhs, call, err),
			errCheckOf(pos, err, exit),
		}
	}
	return []syntax.Stmt{
		errorVarDecl(pos, err),
		assignOf(pos, append(slices.Clone(lhs), err), []syntax.Expr{call}),
		errCheckOf(pos, err, exit),
	}
}

func (fc *funcLower) innerTryExpr(s *syntax.ExprStmt, t *syntax.TryExpr, call *syntax.CallExpr, nativeT syntax.Expr, typ types2.Type) []syntax.Stmt {
	pos := s.Pos()
	errSpell := fc.nextErr()
	err := nameOf(pos, errSpell)
	zeroT := fc.zeroOfSwitchResult(nativeT, typ, pos)
	site := nameOf(pos, fc.fl.newSite(t.Pos(), t.Pos().Line()))
	trace := exprStmtOf(pos, callOf(pos, rtSel(pos, "TraceErr"), []syntax.Expr{err, at(&syntax.Operation{Op: syntax.And, X: site}, pos)}))
	exit := []syntax.Stmt{trace, returnOf(pos, []syntax.Expr{zeroT, err})}
	n := fc.callResults(call)
	if n == 0 {
		return []syntax.Stmt{errCheckInitOf(pos, nil, call, err, exit)}
	}
	return []syntax.Stmt{
		errDefineOf(pos, blankNames(pos, n), call, err),
		errCheckOf(pos, err, exit),
	}
}

func (fc *funcLower) zeroOfSwitchResult(nativeT syntax.Expr, typ types2.Type, pos syntax.Pos) syntax.Expr {
	z := zeroForType(types2.Unalias(typ), nativeT, pos)
	if z == nil {
		return nilOf(pos)
	}
	return z
}

func (fc *funcLower) plainReturnCheck(pos syntax.Pos, errName string) ([]syntax.Stmt, otext) {
	err := nameOf(pos, errName)
	zeros := fc.zerosAt(pos)
	results := append(append([]syntax.Expr(nil), zeros...), err)
	native := []syntax.Stmt{ifOf(pos, nil, notNil(pos, err), []syntax.Stmt{returnOf(pos, results)})}
	return native, renderText(native, nil)
}

func (fc *funcLower) wrapCallWithStmtErr(sx *syntax.SwitchExpr, lit *syntax.FuncLit, nativeT syntax.Expr, typ types2.Type, stmtErr string) syntax.Expr {
	pos := sx.Pos()
	zeroT := fc.zeroOfSwitchResult(nativeT, typ, pos)
	innerCall := callOf(pos, lit, nil)
	tmpT := fc.nextArg()
	tmpE := fc.nextErr()
	serr := nameOf(pos, stmtErr)
	body := []syntax.Stmt{
		ifOf(pos, nil, notNil(pos, serr), []syntax.Stmt{returnOf(pos, []syntax.Expr{zeroT})}),
		defineOf(pos, []syntax.Expr{nameOf(pos, tmpT), nameOf(pos, tmpE)}, []syntax.Expr{innerCall}),
		ifOf(pos, nil, notNil(pos, nameOf(pos, tmpE)), []syntax.Stmt{
			assignOf(pos, []syntax.Expr{serr}, []syntax.Expr{nameOf(pos, tmpE)}),
			returnOf(pos, []syntax.Expr{zeroT}),
		}),
		returnOf(pos, []syntax.Expr{nameOf(pos, tmpT)}),
	}
	wlit := at(&syntax.FuncLit{
		Type: at(&syntax.FuncType{ResultList: []*syntax.Field{at(&syntax.Field{Type: nativeT}, pos)}}, pos),
		Body: at(&syntax.BlockStmt{List: body, Rbrace: pos}, pos),
	}, pos)
	return callOf(pos, wlit, nil)
}

func renderExprFull(e syntax.Expr) string {
	var buf bytes.Buffer
	_, _ = syntax.Fprint(&buf, e, 0)
	return buf.String()
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lint

import (
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

var withlockLeak = Rule{
	ID:      "correctness::withlock_leak",
	Group:   Correctness,
	Default: Deny,
	Go:      true,
	Check:   checkWithlockLeak,
}

type leakClosure struct {
	node     syntax.Node
	body     syntax.Node
	captures bool
	escapes  bool
}

type leakReturn struct {
	stmt    *syntax.ReturnStmt
	closure *leakClosure
}

type leakCheck struct {
	pass      *Pass
	paramName string
	paramObjs []types2.Object
	locals    map[types2.Object]bool
	closures  []*leakClosure
	returns   []leakReturn
}

func checkWithlockLeak(pass *Pass) {
	for _, f := range pass.Files {
		syntax.Inspect(f, func(n syntax.Node) bool {
			call, ok := n.(*syntax.CallExpr)
			if !ok || len(call.ArgList) != 1 {
				return true
			}
			fn := calleeFunc(pass, call)
			if fn == nil || fn.Name() != "WithLock" || fn.Pkg() == nil || fn.Pkg().Path() != logosSyncPath {
				return true
			}
			arg := syntax.Unparen(call.ArgList[0])
			var body syntax.Node
			switch a := arg.(type) {
			case *syntax.FuncLit:
				body = a.Body
			case *syntax.LambdaExpr:
				body = a.Body
			default:
				return true
			}
			if body == nil {
				return true
			}
			name, objs := leakParam(pass, arg)
			if name == "" || len(objs) == 0 {
				return true
			}
			c := &leakCheck{pass: pass, paramName: name, paramObjs: objs, locals: make(map[types2.Object]bool)}
			c.indexLocals(body)
			c.scanUnit(body, nil)
			c.markEscapes(body)
			for _, r := range c.returns {
				if r.closure == nil || r.closure.escapes {
					pass.Reportf(r.stmt, "WithLock argument must not return from the callback")
				}
			}
			return true
		})
	}
}

func leakParam(pass *Pass, arg syntax.Expr) (string, []types2.Object) {
	var name string
	var decls []*syntax.Name
	switch a := arg.(type) {
	case *syntax.FuncLit:
		if a.Type != nil {
			for _, field := range a.Type.ParamList {
				if field.Name != nil {
					decls = append(decls, field.Name)
				}
			}
		}
	case *syntax.LambdaExpr:
		decls = append(decls, a.Params...)
	}
	if len(decls) == 0 {
		return "", nil
	}
	name = decls[0].Value
	var objs []types2.Object
	add := func(o types2.Object) {
		if o == nil {
			return
		}
		for _, have := range objs {
			if have == o {
				return
			}
		}
		objs = append(objs, o)
	}
	if sig, ok := TypeOf(arg).(*types2.Signature); ok && sig.Params().Len() == 1 {
		add(sig.Params().At(0))
	}
	for _, d := range decls {
		add(pass.Info.Defs[d])
	}
	return name, objs
}

func (c *leakCheck) isParamObj(obj types2.Object) bool {
	if obj == nil {
		return false
	}
	for _, o := range c.paramObjs {
		if o == obj {
			return true
		}
	}
	return false
}

func (c *leakCheck) isParamUse(name *syntax.Name) bool {
	obj, ok := c.pass.Info.Uses[name]
	if !ok {
		return false
	}
	return c.isParamObj(obj)
}

func (c *leakCheck) indexLocals(body syntax.Node) {
	syntax.Inspect(body, func(n syntax.Node) bool {
		name, ok := n.(*syntax.Name)
		if !ok {
			return true
		}
		if obj, ok := c.pass.Info.Defs[name]; ok && !c.isParamObj(obj) {
			c.locals[obj] = true
		}
		return true
	})
}
func (c *leakCheck) scanUnit(body syntax.Node, cl *leakClosure) {
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.FuncLit:
			nc := &leakClosure{node: x, body: x.Body}
			c.closures = append(c.closures, nc)
			c.scanUnit(x.Body, nc)
			return false
		case *syntax.LambdaExpr:
			nc := &leakClosure{node: x, body: x.Body}
			c.closures = append(c.closures, nc)
			c.scanUnit(x.Body, nc)
			return false
		case *syntax.ReturnStmt:
			if x.Results != nil && c.carriesPtr(x.Results) {
				c.returns = append(c.returns, leakReturn{stmt: x, closure: cl})
			}
		case *syntax.AssignStmt:
			if x.Rhs != nil && c.carriesPtr(x.Rhs) && c.lhsEscapes(x.Lhs) {
				c.pass.Reportf(x, "WithLock argument must not be stored outside the callback")
			}
		case *syntax.SendStmt:
			if c.carriesPtr(x.Value) {
				c.pass.Reportf(x, "WithLock argument must not be sent on a channel")
			}
		case *syntax.CallStmt:
			if x.Tok == syntax.Go {
				call := callOf(x.Call)
				if call == nil {
					break
				}
				for _, a := range call.ArgList {
					if c.carriesPtr(a) {
						c.pass.Reportf(x, "WithLock argument must not be passed to a goroutine")
						break
					}
				}
				if fun, ok := syntax.Unparen(call.Fun).(*syntax.FuncLit); ok && c.captures(fun.Body) {
					c.pass.Reportf(x, "WithLock argument must not be passed to a goroutine")
				}
				if fun, ok := syntax.Unparen(call.Fun).(*syntax.LambdaExpr); ok && c.captures(fun.Body) {
					c.pass.Reportf(x, "WithLock argument must not be passed to a goroutine")
				}
			}
		}
		return true
	})
}

func (c *leakCheck) carriesPtr(e syntax.Expr) bool {
	found := false
	syntax.Inspect(e, func(n syntax.Node) bool {
		if found {
			return false
		}
		name, ok := n.(*syntax.Name)
		if !ok {
			return true
		}
		if !c.isParamUse(name) {
			return true
		}
		if p := leakParent(e, name); p != nil {
			if op, ok := p.(*syntax.Operation); ok && op.Op == syntax.Mul && op.Y == nil {
				if operand, ok := op.X.(*syntax.Name); ok && operand == name {
					return true
				}
			}
			if sel, ok := p.(*syntax.SelectorExpr); ok {
				if operand, ok := sel.X.(*syntax.Name); ok && operand == name {
					if !addrTaken(e, sel) {
						return true
					}
				}
			}
		}
		found = true
		return false
	})
	return found
}

func addrTaken(e syntax.Node, sel *syntax.SelectorExpr) bool {
	cur := syntax.Node(sel)
	for {
		p := leakParent(e, cur)
		if par, ok := p.(*syntax.ParenExpr); ok {
			cur = par
			continue
		}
		op, ok := p.(*syntax.Operation)
		return ok && op.Op == syntax.And && op.Y == nil && syntax.Unparen(op.X) == syntax.Node(sel)
	}
}

func (c *leakCheck) captures(body syntax.Node) bool {
	captured := false
	syntax.Inspect(body, func(n syntax.Node) bool {
		if captured {
			return false
		}
		name, ok := n.(*syntax.Name)
		if !ok {
			return true
		}
		if c.isParamUse(name) {
			captured = true
			return false
		}
		return true
	})
	return captured
}

func (c *leakCheck) lhsEscapes(e syntax.Expr) bool {
	for _, el := range exprList(e) {
		if c.operandEscapes(syntax.Unparen(el)) {
			return true
		}
	}
	return false
}

func (c *leakCheck) operandEscapes(e syntax.Expr) bool {
	switch x := e.(type) {
	case *syntax.Name:
		if isBlank(x) {
			return false
		}
		obj := c.pass.Info.Uses[x]
		if obj == nil || c.isParamObj(obj) {
			return false
		}
		return !c.locals[obj]
	case *syntax.SelectorExpr, *syntax.IndexExpr:
		return true
	case *syntax.Operation:
		return c.operandEscapes(syntax.Unparen(x.X))
	case *syntax.ListExpr:
		for _, el := range x.ElemList {
			if c.lhsEscapes(el) {
				return true
			}
		}
	}
	return false
}

func (c *leakCheck) markEscapes(body syntax.Node) {
	for _, cl := range c.closures {
		cl.captures = c.captures(cl.body)
	}
	var escaping []syntax.Node
	syntax.Inspect(body, func(n syntax.Node) bool {
		switch x := n.(type) {
		case *syntax.ReturnStmt:
			if x.Results != nil {
				escaping = append(escaping, x.Results)
			}
		case *syntax.SendStmt:
			escaping = append(escaping, x.Value)
		case *syntax.CallStmt:
			if x.Tok == syntax.Go {
				if call := callOf(x.Call); call != nil {
					escaping = append(escaping, call.Fun)
					for _, a := range call.ArgList {
						escaping = append(escaping, a)
					}
				}
			}
		case *syntax.AssignStmt:
			if x.Rhs != nil && c.lhsEscapes(x.Lhs) {
				escaping = append(escaping, x.Rhs)
			}
		}
		return true
	})
	aliases := make(map[types2.Object]bool)
	syntax.Inspect(body, func(n syntax.Node) bool {
		x, ok := n.(*syntax.AssignStmt)
		if !ok || x.Rhs == nil {
			return true
		}
		for _, el := range exprList(x.Lhs) {
			name, ok := syntax.Unparen(el).(*syntax.Name)
			if !ok || isBlank(name) {
				continue
			}
			obj := c.pass.Info.Uses[name]
			if obj == nil || c.isParamObj(obj) || !c.locals[obj] {
				continue
			}
			for _, cl := range c.closures {
				if leakContains(x.Rhs, cl.node) {
					aliases[obj] = true
				}
			}
		}
		return true
	})
	for _, cl := range c.closures {
		if !cl.captures {
			continue
		}
		if leakContainsAny(escaping, cl.node) {
			cl.escapes = true
			continue
		}
		for obj := range aliases {
			if leakUses(c.pass, escaping, obj) {
				cl.escapes = true
				break
			}
		}
	}
}

func leakUses(pass *Pass, escaping []syntax.Node, obj types2.Object) bool {
	for _, r := range escaping {
		used := false
		syntax.Inspect(r, func(n syntax.Node) bool {
			if used {
				return false
			}
			name, ok := n.(*syntax.Name)
			if !ok {
				return true
			}
			if pass.Info.Uses[name] == obj {
				used = true
				return false
			}
			return true
		})
		if used {
			return true
		}
	}
	return false
}

func leakContains(root, target syntax.Node) bool {
	found := false
	syntax.Inspect(root, func(n syntax.Node) bool {
		if n == target {
			found = true
			return false
		}
		return !found
	})
	return found
}

func leakContainsAny(roots []syntax.Node, target syntax.Node) bool {
	for _, r := range roots {
		if leakContains(r, target) {
			return true
		}
	}
	return false
}

type leakParentFinder struct {
	target syntax.Node
	parent syntax.Node
	stack  []syntax.Node
}

func (v *leakParentFinder) Visit(n syntax.Node) syntax.Visitor {
	if v.parent != nil {
		return nil
	}
	if n == nil {
		v.stack = v.stack[:len(v.stack)-1]
		return v
	}
	if n == v.target {
		if len(v.stack) > 0 {
			v.parent = v.stack[len(v.stack)-1]
		}
		return nil
	}
	v.stack = append(v.stack, n)
	return v
}

func leakParent(root, target syntax.Node) syntax.Node {
	v := &leakParentFinder{target: target}
	syntax.Walk(root, v)
	return v.parent
}

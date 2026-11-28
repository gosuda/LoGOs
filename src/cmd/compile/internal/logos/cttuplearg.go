// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// ctTupleCall is one direct comptime-method call whose argument list holds
// exactly one multi-valued expression. After rewriteMethodCall prepends the
// receiver, the tuple no longer stands alone and neither the unified-IR
// writer nor plain Go can express the call. The lowering below expands it
// into statement temporaries before the noder writes it.
type ctTupleCall struct {
	call       *syntax.CallExpr
	sel        *syntax.SelectorExpr
	methodExpr bool
	recv       syntax.Expr   // selector receiver, or args[0] for a method expression
	pre        []syntax.Expr // plain arguments before the tuple, excluding the receiver
	tup        syntax.Expr   // the multi-result argument
	arity      int           // tuple length
	recvAddr   bool          // temporary holds &recv so pointer methods mutate the caller
}

// ctTupleSiteOf reports whether call is a direct comptime-method call with
// exactly one multi-valued argument. A site key proves the call rewrites;
// when the reduced instance body has not been rechecked yet, the generic
// method origin proves it instead. Anything else (two spreads, a dot-dot-dot
// call, a non-method call) is out of scope and keeps today's behavior.
func ctTupleSiteOf(info *types2.Info, call *syntax.CallExpr) (*ctTupleCall, bool) {
	if call.HasDots {
		return nil, false
	}
	fun := syntax.Unparen(call.Fun)
	if idx, ok := fun.(*syntax.IndexExpr); ok {
		fun = syntax.Unparen(idx.X)
	}
	sel, ok := fun.(*syntax.SelectorExpr)
	if !ok {
		return nil, false
	}
	method := false
	var origin *types2.Func
	if id := info.LogosCtSiteKeys[sel.Sel]; id != "" {
		for _, rec := range info.LogosCtInst {
			if rec.ID == id && rec.Origin.Signature().Recv() != nil {
				method = true
				origin = rec.Origin
				break
			}
		}
	}
	if !method {
		if fn, ok := info.Uses[sel.Sel].(*types2.Func); ok && fn.Signature().Recv() != nil {
			if _, ok := info.LogosCtGenerics[fn.Origin()]; ok {
				method = true
				origin = fn.Origin()
			}
		}
	}
	if !method || origin == nil {
		return nil, false
	}
	selection, ok := info.Selections[sel]
	if !ok {
		return nil, false
	}
	methodExpr := selection.Kind() == types2.MethodExpr
	if !methodExpr && selection.Kind() != types2.MethodVal {
		return nil, false
	}
	site := &ctTupleCall{call: call, sel: sel, methodExpr: methodExpr}
	rest := call.ArgList
	if methodExpr {
		if len(rest) == 0 {
			return nil, false
		}
		site.recv = rest[0]
		rest = rest[1:]
	} else {
		site.recv = sel.X
	}
	tupIdx := -1
	for i, arg := range rest {
		tup, ok := info.TypeOf(arg).(*types2.Tuple)
		if !ok || tup.Len() < 2 {
			continue
		}
		// A multi-value argument in a call is a call result or a map
		// receive. A tuple-typed variable is one value and keeps the
		// direct call, so only those two shapes desugar.
		switch syntax.Unparen(arg).(type) {
		case *syntax.CallExpr, *syntax.IndexExpr:
		default:
			continue
		}
		if tupIdx >= 0 {
			return nil, false
		}
		tupIdx = i
		site.tup = arg
		site.arity = tup.Len()
	}
	if tupIdx < 0 {
		return nil, false
	}
	site.pre = rest[:tupIdx:tupIdx]
	if recv := origin.Signature().Recv(); recv != nil {
		if _, wantPtr := types2.Unalias(recv.Type()).(*types2.Pointer); wantPtr {
			if _, isPtr := types2.Unalias(info.TypeOf(site.recv)).(*types2.Pointer); !isPtr {
				// A pointer method must reach the caller's variable, so
				// the temporary holds its address. Anything else keeps
				// the checker's pointer-method error.
				if !ctTupleAddressable(info, site.recv) {
					return nil, false
				}
				site.recvAddr = true
			}
		}
	}
	return site, true
}

// ctTupleAddressable reports whether e can take & in Go: a variable, a
// selection or index into addressable storage (never a map index), or a
// pointer dereference. Anything else keeps the checker's pointer-method
// error instead of a temporary.
func ctTupleAddressable(info *types2.Info, e syntax.Expr) bool {
	switch e := syntax.Unparen(e).(type) {
	case *syntax.Name:
		if e.Value == "_" {
			return false
		}
		_, ok := info.Uses[e].(*types2.Var)
		return ok
	case *syntax.SelectorExpr:
		return ctTupleAddressable(info, e.X)
	case *syntax.IndexExpr:
		if t := info.TypeOf(e.X); t != nil {
			if _, ok := types2.Unalias(t).(*types2.Map); ok {
				return false
			}
		}
		return ctTupleAddressable(info, e.X)
	case *syntax.Operation:
		return e.Op == syntax.Mul && e.Y == nil
	default:
		return false
	}
}

// ctTupleNamer mints temporary names for one instance body, where no file
// counter is available. Main files use fileLower.cswName instead so the
// names stay unique across every lowering pass of the file.
type ctTupleNamer struct {
	seq int
}

func (m *ctTupleNamer) next(prefix string) string {
	m.seq++
	return fmt.Sprintf("_logos_%s%d", prefix, m.seq)
}

// desugarCtTupleCalls expands every comptime-method call of the file that
// holds a multi-result argument beside other arguments into statement
// temporaries in evaluation order: the receiver, every argument before the
// multi-result call, then the multi-result call itself, then the outer call
// with the temporaries. The tree keeps the selector form so the finish pass
// still recognizes the site; the recheck after lowering types the fresh
// names. It runs after comptime retirement (so ejected text already holds
// the original shape) and after const-for unrolling (so each copy is
// desugared with names unique to the file).
func (fl *fileLower) desugarCtTupleCalls() {
	info := fl.pkg.info
	var sites []*ctTupleCall
	syntax.Inspect(fl.file, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok {
			if site, ok := ctTupleSiteOf(info, call); ok {
				sites = append(sites, site)
			}
		}
		return true
	})
	for _, site := range sites {
		// Outermost first: an outer site moves a whole inner call into a
		// temporary, and rebuilding the parents below anchors the inner
		// site at that temporary statement, preserving source order.
		ctTupleRewrite(site, cfParents(fl.file), fl.cswName, func() { fl.changed = true })
	}
}

// desugarCtTupleBody expands tuple-argument comptime-method calls in one
// reduced instance declaration and its helpers before the fresh check
// types them. Detection runs on the reducer's facts; the fresh check then
// assigns site keys to the plain calls as usual.
func desugarCtTupleBody(info *types2.Info, mint *ctTupleNamer, roots ...syntax.Node) {
	var sites []*ctTupleCall
	for _, root := range roots {
		syntax.Inspect(root, func(n syntax.Node) bool {
			if call, ok := n.(*syntax.CallExpr); ok {
				if site, ok := ctTupleSiteOf(info, call); ok {
					sites = append(sites, site)
				}
			}
			return true
		})
	}
	for _, site := range sites {
		merged := make(map[syntax.Node]syntax.Node)
		for _, root := range roots {
			for k, v := range cfParents(root) {
				merged[k] = v
			}
		}
		ctTupleRewrite(site, merged, mint.next, nil)
	}
}

// ctTupleRewrite rewrites one site into statement temporaries: the receiver,
// every argument before the tuple, then the tuple itself, then the outer
// call with the temporaries. fresh mints temporary names; changed, when
// non-nil, records that the tree changed.
func ctTupleRewrite(site *ctTupleCall, parents map[syntax.Node]syntax.Node, fresh func(string) string, changed func()) bool {
	list, idx, ok := ctTupleSlot(site.call, parents)
	if !ok {
		return false
	}
	pos := site.call.Pos()
	recvName := nameOf(pos, fresh("mtuprecv"))
	recvRHS := site.recv
	if site.recvAddr {
		recvRHS = addrOf(pos, site.recv)
	}
	temps := []syntax.Stmt{defineOf(pos, []syntax.Expr{recvName}, []syntax.Expr{recvRHS})}
	newArgs := []syntax.Expr{recvName}
	for _, arg := range site.pre {
		name := nameOf(pos, fresh("mtup"))
		temps = append(temps, defineOf(pos, []syntax.Expr{name}, []syntax.Expr{arg}))
		newArgs = append(newArgs, name)
	}
	var lhs []syntax.Expr
	for k := 0; k < site.arity; k++ {
		lhs = append(lhs, nameOf(pos, fresh("mtup")))
	}
	temps = append(temps, defineOf(pos, lhs, []syntax.Expr{site.tup}))
	newArgs = append(newArgs, lhs...)
	if site.methodExpr {
		site.call.ArgList = newArgs
	} else {
		// The call is still a method call until the finish pass
		// prepends the receiver, so the receiver stays in the
		// selector and only the plain arguments carry over.
		site.sel.X = recvName
		site.call.ArgList = newArgs[1:]
	}
	// The temporaries evaluate immediately before the statement, which
	// is also when a go or defer statement captures its operands, so
	// every slot shares the one insertion.
	old := len(*list)
	*list = append(*list, make([]syntax.Stmt, len(temps))...)
	copy((*list)[idx+len(temps):], (*list)[idx:old])
	copy((*list)[idx:], temps)
	if changed != nil {
		changed()
	}
	return true
}

// ctTupleSlot finds the statement list holding the statement the
// temporaries for call precede. A go or defer statement counts as an
// ordinary statement: its operands evaluate when it executes, so the
// temporaries go right before it. Statements the temporaries must not
// leave (a for condition or post statement, package-level declarations)
// have no slot and keep today's behavior.
func ctTupleSlot(call *syntax.CallExpr, parents map[syntax.Node]syntax.Node) (list *[]syntax.Stmt, idx int, ok bool) {
	node := syntax.Node(call)
	for {
		p, found := parents[node]
		if !found {
			return nil, 0, false
		}
		switch p := p.(type) {
		case *syntax.BlockStmt:
			for i, s := range p.List {
				if syntax.Node(s) == node {
					return &p.List, i, true
				}
			}
			return nil, 0, false
		case *syntax.CaseClause:
			for i, s := range p.Body {
				if syntax.Node(s) == node {
					return &p.Body, i, true
				}
			}
			return nil, 0, false
		case *syntax.CommClause:
			for i, s := range p.Body {
				if syntax.Node(s) == node {
					return &p.Body, i, true
				}
			}
			return nil, 0, false
		case *syntax.ForStmt:
			if p.Init != nil && syntax.Node(p.Init) == node {
				node = p
				continue
			}
			return nil, 0, false
		default:
			node = p
		}
	}
}

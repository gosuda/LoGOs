// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"cmd/compile/internal/syntax"
	"maps"
	"slices"
)

// Deferred calls belong to the path which registered them. Exact nodes
// execute in stack order. A choice summarizes divergent or repeated
// registrations: any subset may run, in any order, before the shared
// earlier stack. Widening only that prefix keeps the analysis finite
// without letting an optional cleanup prove a replacement occurred.
type logosEFDeferredCall struct {
	lit     *syntax.FuncLit
	scope   syntax.Node // nil means function exit; otherwise block or clause
	onError bool
}

type logosEFCleanup struct {
	call   logosEFDeferredCall
	choice map[*syntax.FuncLit]logosEFDeferredCall
	next   *logosEFCleanup
}

func logosEFCleanupSame(a, b *logosEFCleanup) bool {
	for a != b {
		if a == nil || b == nil || a.call != b.call || !maps.Equal(a.choice, b.choice) {
			return false
		}
		a, b = a.next, b.next
	}
	return true
}

func logosEFCollectCleanups(dst map[*syntax.FuncLit]logosEFDeferredCall, from, until *logosEFCleanup) {
	for node := from; node != until; node = node.next {
		if node.call.lit != nil {
			dst[node.call.lit] = node.call
		} else {
			maps.Copy(dst, node.choice)
		}
	}
}

func logosEFCleanupJoin(a, b *logosEFCleanup) *logosEFCleanup {
	if logosEFCleanupSame(a, b) {
		return a
	}
	// Find the shared earlier stack. It remains definite even when
	// later registrations differ, such as a cleanup inside an if arm.
	var commonA, commonB *logosEFCleanup
find:
	for x := a; x != nil; x = x.next {
		for y := b; y != nil; y = y.next {
			if logosEFCleanupSame(x, y) {
				commonA, commonB = x, y
				break find
			}
		}
	}
	choice := make(map[*syntax.FuncLit]logosEFDeferredCall)
	logosEFCollectCleanups(choice, a, commonA)
	logosEFCollectCleanups(choice, b, commonB)
	return &logosEFCleanup{choice: choice, next: commonA}
}

func (s *logosEFState) withCleanups(cleanups *logosEFCleanup) *logosEFState {
	if s == nil || s.cleanups == cleanups {
		return s
	}
	out := *s
	out.cleanups = cleanups
	return &out
}

func (s *logosEFState) deferCall(call logosEFDeferredCall) *logosEFState {
	for node := s.cleanups; node != nil; node = node.next {
		_, repeated := node.choice[call.lit]
		if node.call.lit != call.lit && !repeated {
			continue
		}
		// A loop or backward edge can register the same source call
		// repeatedly. Widen the repeated prefix instead of growing an
		// unbounded stack at each data-flow iteration.
		choice := make(map[*syntax.FuncLit]logosEFDeferredCall)
		logosEFCollectCleanups(choice, s.cleanups, node.next)
		choice[call.lit] = call
		return s.withCleanups(&logosEFCleanup{choice: choice, next: node.next})
	}
	return s.withCleanups(&logosEFCleanup{call: call, next: s.cleanups})
}

func (p *logosEFPass) runCleanup(call logosEFDeferredCall, st *logosEFState) *logosEFState {
	// A cleanup runs against the state at its entry, before the
	// enclosing invocation projects its exit. Materialize the live
	// unchecked pairings so the cleanup reads need the same check a
	// flow read would; a cleanup that checks the guard first proves
	// its own read. Reads of values the cleanup body itself stores
	// fail on their may-reach marks while the body runs; ordinary
	// reads elsewhere stay silent.
	seeded := p.seedCleanupReads(call.lit, st)
	p.inCleanup++
	out := p.inlineFuncLit(call.lit, seeded)
	p.inCleanup--
	out = p.dropCleanupSeeds(st, seeded, out)
	if call.onError {
		// The error return is not generally known at this join. Keep
		// the success path, on which errdefer does not execute.
		out = logosEFJoin(st, out)
	}
	return out
}

// dropCleanupSeeds forgets the ordinary entries seedCleanupReads added
// that the cleanup body never replaced: they scoped the cleanup's own
// reads and must not leak into caller flow. Entries the body proved
// or rewrote, and all may-mark and pairing changes, survive.
func (p *logosEFPass) dropCleanupSeeds(before, seeded, out *logosEFState) *logosEFState {
	if out == nil || seeded == nil || len(seeded.m) == 0 {
		return out
	}
	var bm map[logosEFKey]logosEFEntry
	if before != nil {
		bm = before.m
	}
	var mout map[logosEFKey]logosEFEntry
	for k, e := range seeded.m {
		if _, ok := bm[k]; ok {
			continue
		}
		oe, ok := out.m[k]
		if !ok || oe != e {
			continue
		}
		if mout == nil {
			mout = maps.Clone(out.m)
		}
		delete(mout, k)
	}
	if mout == nil {
		return out
	}
	return &logosEFState{m: mout, may: out.may, mayErr: out.mayErr, mayAlt: out.mayAlt, cleanups: out.cleanups}
}

// finishCleanups consumes only calls whose boundary is being crossed.
// Retained registrations keep their original order and ownership. The
// transfer operates on the actual exit state, so nested cleanup and
// later-registered calls cannot leave stale error pairings behind. A
// cleanup that panics does not stop earlier cleanups: they run during
// unwinding, so they are still checked against the pre-panic state. An
// ordinary cleanup return preserves an active panic; only a callback
// that can recover ends unwinding. The invocation itself has no normal
// exit on the unrecovered path.
func (p *logosEFPass) finishCleanups(st *logosEFState, crosses func(logosEFDeferredCall) bool) *logosEFState {
	if st == nil || st.cleanups == nil {
		return st
	}
	stack := st.cleanups
	st = st.withCleanups(nil)
	var keep []logosEFCleanup
	panicked := false
	for node := stack; node != nil; node = node.next {
		if node.call.lit != nil {
			if crosses(node.call) {
				if next := p.runCleanup(node.call, st); next == nil {
					panicked = true
				} else if p.litCanRecover(node.call.lit) {
					st = next
					panicked = false
				} else {
					st = next
				}
			} else {
				keep = append(keep, logosEFCleanup{call: node.call})
			}
			continue
		}
		var calls []logosEFDeferredCall
		retained := make(map[*syntax.FuncLit]logosEFDeferredCall)
		for lit, call := range node.choice {
			if crosses(call) {
				calls = append(calls, call)
			} else {
				retained[lit] = call
			}
		}
		if len(retained) != 0 {
			keep = append(keep, logosEFCleanup{choice: retained})
		}
		slices.SortFunc(calls, func(a, b logosEFDeferredCall) int { return a.lit.Pos().Cmp(b.lit.Pos()) })
		for {
			next := st
			for _, call := range calls {
				next = logosEFJoin(next, p.runCleanup(call, st))
			}
			if logosEFSame(st, next) {
				st = next
				break
			}
			st = next
		}
	}
	var remaining *logosEFCleanup
	for i := len(keep) - 1; i >= 0; i-- {
		node := keep[i]
		node.next = remaining
		remaining = &node
	}
	if panicked {
		return nil
	}
	return st.withCleanups(remaining)
}

func (p *logosEFPass) scopeStmts(owner syntax.Node, list []syntax.Stmt, st *logosEFState, top bool) *logosEFState {
	p.ctx.scopes = append(p.ctx.scopes, owner)
	st = p.stmts(list, st, top)
	st = p.finishCleanups(st, func(call logosEFDeferredCall) bool { return call.scope == owner })
	p.ctx.scopes = p.ctx.scopes[:len(p.ctx.scopes)-1]
	return st
}

func (p *logosEFPass) leaveScopes(st *logosEFState, depth int) *logosEFState {
	leaving := p.ctx.scopes[depth:]
	return p.finishCleanups(st, func(call logosEFDeferredCall) bool {
		return call.scope != nil && slices.Contains(leaving, call.scope)
	})
}

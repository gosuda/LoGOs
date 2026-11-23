// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// platformSpanSource is the shape of test/logos/run/region_list.lgo that
// broke the eject text: a for-post statement assigning a field read to a
// pointer variable whose declared type forces a MustNonNil check. The
// post statement has no terminator, so a span scanner that eats the loop
// brace wraps the body into the check call.
const platformSpanSource = `package main

type node struct {
	next *node
	v    int
}

func mk[T any]() *T {
	return new(T)
}

func f() {
	head := mk[node]()
	tail := head
	for i := 0; i < 2; i++ {
		n := mk[node]()
		tail.next = n
		tail = n
	}
	tail.next = head
	for h := head.next; h != head; h = h.next {
		_ = h.v
	}
}
`

// TestPlatformSpanEnclosesOperand lowers the for-post shape and asserts
// the lowered text parses and each MustNonNil wrapper encloses exactly
// its recorded operand.
func TestPlatformSpanEnclosesOperand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform_span.lgo")
	if err := os.WriteFile(path, []byte(platformSpanSource), 0666); err != nil {
		t.Fatal(err)
	}
	var perrs []error
	f, err := syntax.ParseFile(path, func(err error) { perrs = append(perrs, err) }, nil, syntax.CheckBranches|syntax.LogosMode)
	if err != nil {
		t.Fatal(errors.Join(append(perrs, err)...))
	}
	info := &types2.Info{
		Defs:       make(map[*syntax.Name]types2.Object),
		Uses:       make(map[*syntax.Name]types2.Object),
		Types:      make(map[syntax.Expr]types2.TypeAndValue),
		Selections: make(map[*syntax.SelectorExpr]*types2.Selection),
		Scopes:     make(map[syntax.Node]*types2.Scope),
	}
	var cerrs []error
	conf := types2.Config{
		Error: func(err error) {
			if types2.LogosWarnOnly(err) {
				return
			}
			cerrs = append(cerrs, err)
		},
		Logos: true,
	}
	data := []byte(platformSpanSource)
	files := []*syntax.File{f}
	src := map[*syntax.File][]byte{f: data}
	pkg, _ := conf.Check("main", files, info)
	if err := errors.Join(cerrs...); err != nil {
		t.Fatalf("type check: %v", err)
	}
	_, edits := Lower(pkg, info, files, src)
	out, err := ApplyEdits(data, f.Pos().FileBase(), edits[f])
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	reparseLowered(t, "platform_span.lgo", out)
	text := string(out)
	for _, want := range []string{`MustNonNil(n, "*main.node", &`, `MustNonNil(h.next, "*main.node", &`} {
		if !strings.Contains(text, want) {
			t.Errorf("lowered text misses %q:\n%s", want, text)
		}
	}
	for _, bad := range []string{"MustNonNil(n {", "MustNonNil(h.next {"} {
		if strings.Contains(text, bad) {
			t.Errorf("lowered text wraps past its operand %q:\n%s", bad, text)
		}
	}
}

// platformCommaOkSource assigns one map lookup to two destinations. The
// lookup stays a two-result form: wrapping it in the single-result
// MustNonNil call would not compile. A single-destination store of a
// platform field still checks.
const platformCommaOkSource = `package main

type Holder struct {
	q *int
	m map[int]*int
}

type node struct {
	next *node
}

func mk[T any]() *T {
	return new(T)
}

func useN(p *node) {}

func f() {
	p := mk[Holder]()
	p.m = map[int]*int{}
	v := new(int)
	var ok bool
	v, ok = p.m[0]
	_ = v
	_ = ok
	w := new(int)
	w = p.q
	_ = w
	g := mk[node]()
	useN(g.next)
	useN(mk[node]().next)
}
`

// TestPlatformCommaOkKeepsTwoResults lowers a comma-ok assignment of a
// platform map element and asserts the lookup is left intact while the
// single-destination field store still checks.
func TestPlatformCommaOkKeepsTwoResults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "platform_commaok.lgo")
	if err := os.WriteFile(path, []byte(platformCommaOkSource), 0666); err != nil {
		t.Fatal(err)
	}
	var perrs []error
	f, err := syntax.ParseFile(path, func(err error) { perrs = append(perrs, err) }, nil, syntax.CheckBranches|syntax.LogosMode)
	if err != nil {
		t.Fatal(errors.Join(append(perrs, err)...))
	}
	info := &types2.Info{
		Defs:       make(map[*syntax.Name]types2.Object),
		Uses:       make(map[*syntax.Name]types2.Object),
		Types:      make(map[syntax.Expr]types2.TypeAndValue),
		Selections: make(map[*syntax.SelectorExpr]*types2.Selection),
		Scopes:     make(map[syntax.Node]*types2.Scope),
	}
	var cerrs []error
	conf := types2.Config{
		Error: func(err error) {
			if types2.LogosWarnOnly(err) {
				return
			}
			cerrs = append(cerrs, err)
		},
		Logos: true,
	}
	data := []byte(platformCommaOkSource)
	files := []*syntax.File{f}
	src := map[*syntax.File][]byte{f: data}
	pkg, _ := conf.Check("main", files, info)
	if err := errors.Join(cerrs...); err != nil {
		t.Fatalf("type check: %v", err)
	}
	_, edits := Lower(pkg, info, files, src)
	out, err := ApplyEdits(data, f.Pos().FileBase(), edits[f])
	if err != nil {
		t.Fatalf("ApplyEdits: %v", err)
	}
	reparseLowered(t, "platform_commaok.lgo", out)
	// A reparse alone cannot show the comma-ok assignment stayed
	// two-valued, so inspect the native tree as well: a multi-destination
	// assignment keeps its bare lookup, while the single-destination store
	// of the other field is a MustNonNil call.
	isMustNonNil := func(e syntax.Expr) bool {
		call, ok := syntax.Unparen(e).(*syntax.CallExpr)
		if !ok {
			return false
		}
		sel, ok := syntax.Unparen(call.Fun).(*syntax.SelectorExpr)
		return ok && sel.Sel.Value == "MustNonNil"
	}
	sawCommaOk := false
	sawWrappedSingle := false
	syntax.Inspect(f, func(n syntax.Node) bool {
		as, ok := n.(*syntax.AssignStmt)
		if !ok {
			return true
		}
		if lhs, ok := as.Lhs.(*syntax.ListExpr); ok && len(lhs.ElemList) == 2 {
			// Only the exact lookup shape counts: generated code
			// holds other two-destination assignments, and a later
			// two-destination assignment over plain names must not
			// satisfy this check if the lookup is dropped.
			a, aok := lhs.ElemList[0].(*syntax.Name)
			b, bok := lhs.ElemList[1].(*syntax.Name)
			if aok && bok && a.Value == "v" && b.Value == "ok" {
				if _, isIdx := syntax.Unparen(as.Rhs).(*syntax.IndexExpr); isIdx {
					sawCommaOk = true
				} else {
					t.Errorf("native tree lost the v, ok = p.m[0] shape")
				}
			}
			return true
		}
		if isMustNonNil(as.Rhs) {
			sawWrappedSingle = true
		}
		return true
	})
	if !sawCommaOk {
		t.Errorf("native tree lost the comma-ok assignment")
	}
	if !sawWrappedSingle {
		t.Errorf("native tree misses the wrapped single-destination store")
	}
	text := string(out)
	if strings.Contains(text, "ok = _logos_rt.MustNonNil") {
		t.Errorf("lowered text wraps the comma-ok lookup:\n%s", text)
	}
	if !strings.Contains(text, "_logos_rt.MustNonNil(p.q") {
		t.Errorf("lowered text misses the single-destination check:\n%s", text)
	}
	if !strings.Contains(text, "_logos_rt.MustNonNil(mk[node]().next") {
		t.Errorf("lowered text misses the call-rooted check:\n%s", text)
	}
}

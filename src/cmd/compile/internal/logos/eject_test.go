// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"bytes"
	"fmt"
	"internal/testenv"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"cmd/compile/internal/importer"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

func TestEjectMatchesLowering(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	dir := filepath.Join(testenv.GOROOT(t), "test", "logos", "run")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".lgo") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		t.Fatalf("no .lgo files in %s", dir)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			ejectCheckFile(t, dir, name)
		})
	}
}

func ejectCheckFile(t *testing.T, dir, name string) {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	base := syntax.NewFileBase(filepath.Join(dir, name))
	file := ejectParse(t, base, src, syntax.LogosMode|syntax.CheckBranches)
	pkg, info := ejectCheck(t, file)
	edits := ejectLower(t, pkg, info, file, src)
	ejected, err := ApplyEdits(src, base, edits)
	if err != nil {
		t.Fatalf("ApplyEdits %s: %v", name, err)
	}
	back := ejectParse(t, syntax.NewFileBase(name+".go"), ejected, syntax.CheckBranches)
	if ok, reason := ejectTreesEqual(name, file, back); !ok {
		t.Errorf("ejected text re-parses to a different tree for %s (%s):\nlowered:\n%s\nejected:\n%s", name, reason, syntax.String(file), syntax.String(back))
	}
	ejectExpectComments(t, name, src, ejected)
}

type ejectTryPair struct {
	err   string
	sel   string
	nargs int
}

func ejectTreesEqual(name string, file, back *syntax.File) (bool, string) {
	if syntax.String(file) == syntax.String(back) {
		return true, ""
	}
	markers := ejectCollectMarkers(file)
	if len(markers) == 0 {
		return false, "no markers"
	}
	twins := ejectCollectTwins(back)
	if len(twins) != len(markers) {
		return false, fmt.Sprintf("count markers=%d twins=%d", len(markers), len(twins))
	}
	for i := range markers {
		if markers[i] != twins[i] {
			return false, fmt.Sprintf("pair %d marker=%+v twin=%+v", i, markers[i], twins[i])
		}
	}
	ejectSpliceMarkers(file)
	ejectSpliceTwins(back)
	if syntax.String(file) == syntax.String(back) {
		return true, ""
	}
	return false, "post-splice compare"
}
func ejectCalleeKey(call syntax.Expr) (string, int) {
	ce, ok := call.(*syntax.CallExpr)
	if !ok {
		return "call", 0
	}
	switch fun := ce.Fun.(type) {
	case *syntax.SelectorExpr:
		if fun.Sel != nil {
			return fun.Sel.Value, len(ce.ArgList)
		}
	case *syntax.Name:
		name := fun.Value
		if i := strings.IndexByte(name, '['); i >= 0 {
			name = name[:i]
		}
		return name, len(ce.ArgList)
	}
	return "call", len(ce.ArgList)
}

func ejectIsDeferTryCall(call syntax.Expr) (string, string, int, bool) {
	ce, ok := call.(*syntax.CallExpr)
	if !ok {
		return "", "", 0, false
	}
	fun, ok := ce.Fun.(*syntax.SelectorExpr)
	if !ok || fun.Sel == nil || fun.Sel.Value != "DeferTry" {
		return "", "", 0, false
	}
	if len(ce.ArgList) != 2 {
		return "", "", 0, false
	}
	addr, ok := ce.ArgList[0].(*syntax.Operation)
	if !ok || addr.Op != syntax.And || addr.X == nil {
		return "", "", 0, false
	}
	name, ok := addr.X.(*syntax.Name)
	if !ok {
		return "", "", 0, false
	}
	sel, nargs := ejectCalleeKey(ce.ArgList[1])
	return name.Value, sel, nargs, true
}

func ejectUnlabel(s syntax.Stmt) syntax.Stmt {
	for {
		lab, ok := s.(*syntax.LabeledStmt)
		if !ok || lab.Stmt == nil {
			return s
		}
		s = lab.Stmt
	}
}

func ejectCollectMarkers(file *syntax.File) []ejectTryPair {
	var out []ejectTryPair
	ejectWalkLists(file, func(list *[]syntax.Stmt) bool {
		for _, s := range *list {
			cs, ok := ejectUnlabel(s).(*syntax.CallStmt)
			if !ok || cs.Tok != syntax.Defer {
				continue
			}
			if err, sel, nargs, ok := ejectIsDeferTryCall(cs.Call); ok {
				out = append(out, ejectTryPair{err: err, sel: sel, nargs: nargs})
			}
		}
		return true
	})
	return out
}
func ejectIsTryTwin(s syntax.Stmt) (string, string, int, bool) {
	cs, ok := ejectUnlabel(s).(*syntax.CallStmt)
	if !ok || cs.Tok != syntax.Defer {
		return "", "", 0, false
	}
	ce, ok := cs.Call.(*syntax.CallExpr)
	if !ok || len(ce.ArgList) != 0 {
		return "", "", 0, false
	}
	lit, ok := ce.Fun.(*syntax.FuncLit)
	if !ok || lit.Body == nil || len(lit.Body.List) != 1 {
		return "", "", 0, false
	}
	check, ok := lit.Body.List[0].(*syntax.IfStmt)
	if !ok || check.Init == nil {
		return "", "", 0, false
	}
	init, ok := check.Init.(*syntax.AssignStmt)
	rhs := ejectUnpack(init.Rhs)
	if !ok || len(rhs) != 1 {
		return "", "", 0, false
	}
	lhs := ejectUnpack(init.Lhs)
	if len(lhs) != 1 {
		return "", "", 0, false
	}
	inner, ok := lhs[0].(*syntax.Name)
	if !ok || !strings.HasPrefix(inner.Value, "_logos_") {
		return "", "", 0, false
	}
	then := check.Then
	if then == nil || len(then.List) != 1 {
		return "", "", 0, false
	}
	joinIf, ok := then.List[0].(*syntax.IfStmt)
	if !ok {
		return "", "", 0, false
	}
	err, ok := ejectJoinTarget(joinIf)
	if !ok {
		return "", "", 0, false
	}
	sel, nargs := ejectCalleeKey(rhs[0])
	return err, sel, nargs, true
}

func ejectJoinTarget(joinIf *syntax.IfStmt) (string, bool) {
	els, ok := joinIf.Else.(*syntax.BlockStmt)
	if !ok {
		return "", false
	}
	for _, s := range els.List {
		as, ok := s.(*syntax.AssignStmt)
		if !ok {
			continue
		}
		for _, rhs := range ejectUnpack(as.Rhs) {
			if !ejectIsLogosJoin(rhs) {
				continue
			}
			lhs := ejectUnpack(as.Lhs)
			if len(lhs) != 1 {
				return "", false
			}
			name, ok := lhs[0].(*syntax.Name)
			if !ok {
				return "", false
			}
			return name.Value, true
		}
	}
	return "", false
}
func ejectCollectTwins(back *syntax.File) []ejectTryPair {
	var out []ejectTryPair
	ejectWalkLists(back, func(list *[]syntax.Stmt) bool {
		for i, s := range *list {
			err, sel, nargs, ok := ejectIsTryTwin(s)
			if !ok {
				continue
			}
			if strings.HasPrefix(sel, "_logos_") {
				if resolved, ok := ejectResolveTemp(*list, i, sel); ok {
					sel = resolved
				}
			}
			out = append(out, ejectTryPair{err: err, sel: sel, nargs: nargs})
		}
		return true
	})
	return out
}

func ejectResolveTemp(list []syntax.Stmt, idx int, name string) (string, bool) {
	if !strings.HasPrefix(name, "_logos_") {
		return "", false
	}
	for j := idx - 1; j >= 0; j-- {
		stmt := ejectUnlabel(list[j])
		as, ok := stmt.(*syntax.AssignStmt)
		if !ok || as.Op != syntax.Def {
			if ejectIsTwinPrefix(stmt) {
				continue
			}
			return "", false
		}
		lhs := ejectUnpack(as.Lhs)
		rhs := ejectUnpack(as.Rhs)
		for k, e := range lhs {
			n, ok := e.(*syntax.Name)
			if !ok || n.Value != name {
				continue
			}
			if k >= len(rhs) {
				return "", false
			}
			switch r := rhs[k]; r.(type) {
			case *syntax.Name:
				return rhs[k].(*syntax.Name).Value, true
			case *syntax.SelectorExpr:
				if sel := rhs[k].(*syntax.SelectorExpr).Sel; sel != nil {
					return sel.Value, true
				}
			}
			return "", false
		}
	}
	return "", false
}

func ejectUnpack(e syntax.Expr) []syntax.Expr {
	if e == nil {
		return nil
	}
	return syntax.UnpackListExpr(e)
}

func ejectIsLogosJoin(e syntax.Expr) bool {
	ce, ok := e.(*syntax.CallExpr)
	if !ok {
		return false
	}
	fun, ok := ce.Fun.(*syntax.SelectorExpr)
	if !ok || fun.Sel == nil || fun.Sel.Value != "Join" {
		return false
	}
	x, ok := fun.X.(*syntax.Name)
	return ok && strings.HasPrefix(x.Value, "_logos_")
}

func ejectSpliceMarkers(file *syntax.File) {
	ejectWalkLists(file, func(list *[]syntax.Stmt) bool {
		for i, s := range *list {
			cs, ok := ejectUnlabel(s).(*syntax.CallStmt)
			if !ok || cs.Tok != syntax.Defer {
				continue
			}
			if _, _, _, ok := ejectIsDeferTryCall(cs.Call); ok {
				marker := ejectMarkerDefer()
				if lab, ok := s.(*syntax.LabeledStmt); ok {
					lab.Stmt = marker
					marker = lab
				}
				(*list)[i] = marker
			}
		}
		return true
	})
}

func ejectSpliceTwins(back *syntax.File) {
	ejectWalkLists(back, func(list *[]syntax.Stmt) bool {
		for i := 0; i < len(*list); i++ {
			if _, _, _, ok := ejectIsTryTwin((*list)[i]); !ok {
				continue
			}
			start := i
			for start > 0 && ejectIsTwinPrefix((*list)[start-1]) {
				start--
			}
			marker := ejectMarkerDefer()
			if lab, ok := (*list)[start].(*syntax.LabeledStmt); ok && start < i {
				lab.Stmt = marker
				marker = lab
			}
			rest := append([]syntax.Stmt{marker}, (*list)[i+1:]...)
			*list = append((*list)[:start], rest...)
			i = start
		}
		return true
	})
}

func ejectMarkerDefer() syntax.Stmt {
	return &syntax.CallStmt{Tok: syntax.Defer, Call: &syntax.CallExpr{Fun: &syntax.Name{Value: "marker"}}}
}

func ejectIsTwinPrefix(s syntax.Stmt) bool {
	if as, ok := ejectUnlabel(s).(*syntax.AssignStmt); ok && as.Op == syntax.Def {
		lhs := ejectUnpack(as.Lhs)
		if len(lhs) == 0 {
			return false
		}
		for _, e := range lhs {
			name, ok := e.(*syntax.Name)
			if !ok || !strings.HasPrefix(name.Value, "_logos_") {
				return false
			}
		}
		return true
	}
	if cond, ok := s.(*syntax.IfStmt); ok && cond.Init == nil {
		return ejectMentionsLogosTemp(cond.Cond)
	}
	return false
}

func ejectMentionsLogosTemp(n syntax.Expr) bool {
	op, ok := n.(*syntax.Operation)
	if !ok || op.Op != syntax.Eql {
		return false
	}
	for _, x := range []syntax.Expr{op.X, op.Y} {
		if name, ok := x.(*syntax.Name); ok && strings.HasPrefix(name.Value, "_logos_") {
			return true
		}
	}
	return false
}

func ejectWalkLists(f *syntax.File, fn func(*[]syntax.Stmt) bool) bool {
	for _, d := range f.DeclList {
		fd, ok := d.(*syntax.FuncDecl)
		if !ok || fd.Body == nil {
			continue
		}
		if !ejectWalkBlock(&fd.Body.List, fn) {
			return false
		}
	}
	return true
}

func ejectWalkBlock(list *[]syntax.Stmt, fn func(*[]syntax.Stmt) bool) bool {
	if !fn(list) {
		return false
	}
	for _, s := range *list {
		for _, child := range ejectChildLists(s) {
			if !ejectWalkBlock(child, fn) {
				return false
			}
		}
		for _, lit := range ejectStmtLits(s) {
			if lit.Body != nil && !ejectWalkBlock(&lit.Body.List, fn) {
				return false
			}
		}
	}
	return true
}

type ejectLitVisitor struct {
	lits []*syntax.FuncLit
}

func (v *ejectLitVisitor) Visit(n syntax.Node) syntax.Visitor {
	if lit, ok := n.(*syntax.FuncLit); ok && lit.Body != nil {
		v.lits = append(v.lits, lit)
	}
	return v
}

func ejectStmtLits(s syntax.Stmt) []*syntax.FuncLit {
	v := &ejectLitVisitor{}
	syntax.Walk(s, v)
	return v.lits
}

func ejectChildLists(s syntax.Stmt) []*[]syntax.Stmt {
	switch s := s.(type) {
	case *syntax.BlockStmt:
		return []*[]syntax.Stmt{&s.List}
	case *syntax.IfStmt:
		out := []*[]syntax.Stmt{}
		if s.Then != nil {
			out = append(out, &s.Then.List)
		}
		switch e := s.Else.(type) {
		case *syntax.BlockStmt:
			out = append(out, &e.List)
		case *syntax.IfStmt:
			out = append(out, ejectChildLists(e)...)
		}
		return out
	case *syntax.ForStmt:
		if s.Body != nil {
			return []*[]syntax.Stmt{&s.Body.List}
		}
	case *syntax.SwitchStmt:
		var out []*[]syntax.Stmt
		for _, cc := range s.Body {
			out = append(out, &cc.Body)
		}
		return out
	case *syntax.SelectStmt:
		var out []*[]syntax.Stmt
		for _, cc := range s.Body {
			out = append(out, &cc.Body)
		}
		return out
	case *syntax.LabeledStmt:
		if s.Stmt != nil {
			return ejectChildLists(s.Stmt)
		}
	}
	return nil
}
func ejectParse(t *testing.T, base *syntax.PosBase, src []byte, mode syntax.Mode) *syntax.File {
	t.Helper()
	var errs []error
	file, _ := syntax.Parse(base, bytes.NewReader(src), func(err error) {
		errs = append(errs, err)
	}, nil, mode)
	for _, err := range errs {
		t.Errorf("parse %s: %v", base.Filename(), err)
	}
	if len(errs) > 0 {
		t.FailNow()
	}
	if file == nil {
		t.Fatalf("parse %s: nil file without errors", base.Filename())
	}
	return file
}

func ejectCheck(t *testing.T, file *syntax.File) (*types2.Package, *types2.Info) {
	t.Helper()
	info := &types2.Info{
		Types:      make(map[syntax.Expr]types2.TypeAndValue),
		Instances:  make(map[*syntax.Name]types2.Instance),
		Defs:       make(map[*syntax.Name]types2.Object),
		Uses:       make(map[*syntax.Name]types2.Object),
		Implicits:  make(map[syntax.Node]types2.Object),
		Selections: make(map[*syntax.SelectorExpr]*types2.Selection),
		Scopes:     make(map[syntax.Node]*types2.Scope),
	}
	var errs []error
	conf := &types2.Config{
		Importer: &ejectImporter{pkgs: make(map[string]*types2.Package)},
		Error: func(err error) {
			if types2.LogosWarnOnly(err) {
				return
			}
			errs = append(errs, err)
		},
		Logos: true,
	}
	pkg, _ := conf.Check("main", []*syntax.File{file}, info)
	for _, err := range errs {
		t.Errorf("check: %v", err)
	}
	if len(errs) > 0 {
		t.FailNow()
	}
	return pkg, info
}

func ejectLower(t *testing.T, pkg *types2.Package, info *types2.Info, file *syntax.File, src []byte) []Edit {
	t.Helper()
	_, edits := Lower(pkg, info, []*syntax.File{file}, map[*syntax.File][]byte{file: src})
	return edits[file]
}

func ejectExpectComments(t *testing.T, name string, src, ejected []byte) {
	t.Helper()
	for _, c := range ejectComments(src) {
		if !bytes.Contains(ejected, c) {
			t.Errorf("%s: ejected text drops comment %q", name, c)
		}
	}
}

func ejectComments(src []byte) [][]byte {
	var out [][]byte
	i := 0
	for i < len(src) {
		switch src[i] {
		case '"', '\'', '`':
			i = ejectQuoted(src, i)
		case '/':
			i = ejectSlash(src, i, &out)
		default:
			i++
		}
	}
	return out
}

func ejectQuoted(src []byte, i int) int {
	quote := src[i]
	i++
	for i < len(src) {
		c := src[i]
		if c == '\\' && quote != '`' {
			i += 2
			continue
		}
		if c == quote {
			return i + 1
		}
		i++
	}
	return i
}

func ejectSlash(src []byte, i int, out *[][]byte) int {
	if i+1 >= len(src) {
		return i + 1
	}
	switch src[i+1] {
	case '/':
		end := bytes.IndexByte(src[i:], '\n')
		if end < 0 {
			end = len(src) - i
		}
		*out = append(*out, bytes.TrimSuffix(src[i:i+end], []byte{'\r'}))
		return i + end
	case '*':
		end := bytes.Index(src[i+2:], []byte("*/"))
		if end < 0 {
			*out = append(*out, src[i:])
			return len(src)
		}
		*out = append(*out, src[i:i+2+end+2])
		return i + 2 + end + 2
	default:
		return i + 1
	}
}

type ejectImporter struct {
	pkgs map[string]*types2.Package
}

func (m *ejectImporter) Import(path string) (*types2.Package, error) {
	return m.ImportFrom(path, "", 0)
}

func (m *ejectImporter) ImportFrom(path, srcDir string, mode types2.ImportMode) (*types2.Package, error) {
	return importer.Import(m.pkgs, path, srcDir, nil)
}
func TestEjectDeferTryJoinBehavior(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	dir := filepath.Join(testenv.GOROOT(t), "test", "logos", "run")
	const name = "defer_try.lgo"
	src, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	base := syntax.NewFileBase(filepath.Join(dir, name))
	file := ejectParse(t, base, src, syntax.LogosMode|syntax.CheckBranches)
	pkg, info := ejectCheck(t, file)
	edits := ejectLower(t, pkg, info, file, src)
	ejected, err := ApplyEdits(src, base, edits)
	if err != nil {
		t.Fatalf("ApplyEdits %s: %v", name, err)
	}
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module ejectjoin\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "main.go"), ejected, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(testenv.GoToolPath(t), "run", ".")
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("ejected run: %v\n%s", err, out)
	}
	want, err := os.ReadFile(filepath.Join(dir, "defer_try.out"))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != string(want) {
		t.Fatalf("ejected verdicts differ:\ngot:\n%swant:\n%s", got, want)
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lint

import (
	"slices"
	"strings"
	"testing"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"

	"internal/testenv"
)

func checkSyncPath(t testing.TB, name, src string) *Input {
	t.Helper()
	var errs []error
	var reported []Reported
	file, _ := syntax.Parse(syntax.NewFileBase(name), strings.NewReader(src), func(err error) {
		errs = append(errs, err)
	}, nil, syntax.CheckBranches)
	if len(errs) > 0 || file == nil {
		t.Fatalf("parse %s: %v", name, errs)
	}
	info := &types2.Info{
		StoreTypesInSyntax: true,
		Defs:               make(map[*syntax.Name]types2.Object),
		Uses:               make(map[*syntax.Name]types2.Object),
		Selections:         make(map[*syntax.SelectorExpr]*types2.Selection),
		Implicits:          make(map[syntax.Node]types2.Object),
		Scopes:             make(map[syntax.Node]*types2.Scope),
		Instances:          make(map[*syntax.Name]types2.Instance),
	}
	conf := &types2.Config{
		Importer: &lintImporter{pkgs: make(map[string]*types2.Package)},
		Error: func(err error) {
			if r, ok := ReportedOf(err); ok {
				reported = append(reported, r)
				return
			}
			errs = append(errs, err)
		},
	}
	pkg, _ := conf.Check("logos/sync", []*syntax.File{file}, info)
	if len(errs) > 0 {
		t.Fatalf("check %s: %v", name, errs)
	}
	return &Input{
		Pkg:      pkg,
		Info:     info,
		Files:    []*syntax.File{file},
		Sources:  map[*syntax.File][]byte{file: []byte(src)},
		Kind:     KindGo,
		Reported: reported,
	}
}

const advLeakHead = `package sync

import stdsync "sync"

type Locked[T any] struct {
	mu stdsync.Mutex
	v  T
}

func (l *Locked[T]) WithLock(f func(*T)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f(&l.v)
}

type Box struct {
	n int
	p *int
}

var leaked *Box
var leakedInt *int
var pslot **Box

func useBox(b *Box) {}
`

func TestAdvWithlockLeak(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			"shadowedParamNameStaysClean",
			"func p3(l *Locked[Box]) { l.WithLock(func(b *Box) { func() { b := &Box{}; leaked = b }() }) }\n",
			nil,
		},
		{
			"derefStoreEscapes",
			"func p5(l *Locked[Box]) { l.WithLock(func(b *Box) { *pslot = b }) }\n",
			[]string{"26 correctness::withlock_leak deny"},
		},
		{
			"localDerefStoreStaysClean",
			"func p11(l *Locked[Box]) { l.WithLock(func(b *Box) { var tmp *Box; ptmp := &tmp; *ptmp = b }) }\n",
			nil,
		},
		{
			"deferredCallArgStaysClean",
			"func p4(l *Locked[Box]) { l.WithLock(func(b *Box) { defer useBox(b) }) }\n",
			nil,
		},
		{
			"fieldReadStaysClean",
			"func p9(l *Locked[Box]) int { n := 0; l.WithLock(func(b *Box) { n = b.n }) ; return n }\n",
			nil,
		},
		{
			"valueCopyStaysClean",
			"func p10(l *Locked[Box]) int { n := 0; l.WithLock(func(b *Box) { v := *b; n = v.n }) ; return n }\n",
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarize(Run(checkSyncPath(t, "x.go", advLeakHead+tt.body)))
			if !slices.Equal(got, tt.want) {
				t.Errorf("results = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAdvWithlockLeakInteriorPointer(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	body := "func p6(l *Locked[Box]) *int { var out *int; l.WithLock(func(b *Box) { v := *b; out = v.p }); return out }\n"
	got := summarize(Run(checkSyncPath(t, "x.go", advLeakHead+body)))
	if len(got) != 0 {
		t.Errorf("results = %q, want no findings", got)
	}
}

const advDeadlockHead = `package sync

import stdsync "sync"

type Locked[T any] struct {
	mu stdsync.Mutex
	v  T
}

func (l *Locked[T]) WithLock(f func(*T)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f(&l.v)
}

type Emb struct {
	Locked[int]
}

type Ctr struct {
	mu Locked[int]
}

type Wrap2 struct {
	inner Ctr
}

func (w *Wrap2) lockInner() {
	w.inner.mu.WithLock(func(m *int) { *m++ })
}

`

func TestAdvReentrantDeadlock(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			"genericHelperDeadlocks",
			"func relock[T any](l *Locked[T]) { l.WithLock(func(n *T) {}) }\nfunc d5(l *Locked[int]) { l.WithLock(func(n *int) { relock(l) }) }\n",
			[]string{"33 concurrency::reentrant_withlock_deadlock deny"},
		},
		{
			"fourDeepChainDeadlocks",
			"func c1(l *Locked[int]) { l.WithLock(func(n *int) { *n++ }) }\nfunc c2(l *Locked[int]) { c1(l) }\nfunc c3(l *Locked[int]) { c2(l) }\nfunc c4(l *Locked[int]) { c3(l) }\nfunc d6(l *Locked[int]) { l.WithLock(func(n *int) { c4(l) }) }\n",
			[]string{"36 concurrency::reentrant_withlock_deadlock deny"},
		},
		{
			"directNestedDeadlocks",
			"func d12(l *Locked[int]) { l.WithLock(func(n *int) { l.WithLock(func(m *int) { *m++ }) }) }\n",
			[]string{"32 concurrency::reentrant_withlock_deadlock deny"},
		},
		{
			"wrapperPathDeadlocks",
			"func d13(w *Wrap2) { w.inner.mu.WithLock(func(n *int) { w.lockInner() }) }\n",
			[]string{"32 concurrency::reentrant_withlock_deadlock deny"},
		},
		{
			"differentLockStaysClean",
			"func e1(a, b *Locked[int]) { a.WithLock(func(n *int) { b.WithLock(func(m *int) { *m++ }) }) }\n",
			nil,
		},
		{
			"sequentialCallsStayClean",
			"func e2(l *Locked[int]) { l.WithLock(func(n *int) { *n++ }); l.WithLock(func(n *int) { *n++ }) }\n",
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarize(Run(checkSyncPath(t, "x.go", advDeadlockHead+tt.body)))
			if !slices.Equal(got, tt.want) {
				t.Errorf("results = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestAdvReentrantDeadlockLimitations(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			"indexReceiverUnreported",
			"func d4(ls []*Locked[int]) { ls[0].WithLock(func(n *int) { ls[0].WithLock(func(m *int) { *m++ }) }) }\n",
			nil,
		},
		{
			"unreachableBranchReported",
			"func d11(l *Locked[int]) { l.WithLock(func(n *int) { if false { l.WithLock(func(m *int) { *m++ }) } }) }\n",
			[]string{"32 concurrency::reentrant_withlock_deadlock deny"},
		},
		{
			"goSpawnedRelockUnreported",
			"func d14(l *Locked[int]) { l.WithLock(func(n *int) { go func() { l.WithLock(func(m *int) { *m++ }) }() }) }\n",
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarize(Run(checkSyncPath(t, "x.go", advDeadlockHead+tt.body)))
			if !slices.Equal(got, tt.want) {
				t.Errorf("results = %q, want %q", got, tt.want)
			}
		})
	}
}

const advLocksHead = `package p

import "sync"

type stats struct {
	mu sync.RWMutex

	//logos:guardedby mu
	total int
}

type emb struct {
	sync.Mutex

	//logos:guardedby Mutex
	n int
}

type pair struct {
	mu sync.Mutex

	//logos:guardedby mu
	n int
}

var arr [4]pair

func apply(f func() int) int { return f() }
`

func TestAdvGuardedAccess(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			"lockUnlockSequenceStaysClean",
			"func h0(s *stats) int {\n s.mu.Lock()\n defer s.mu.Unlock()\n return s.total\n}\n",
			nil,
		},
		{
			"readUnderReadLockStaysClean",
			"func h1(s *stats) int {\n s.mu.RLock()\n defer s.mu.RUnlock()\n return s.total\n}\n",
			nil,
		},
		{
			"earlyReturnReadsHeldLock",
			"func g8(s *stats, c bool) int {\n s.mu.Lock()\n if c {\n return s.total\n }\n s.mu.Unlock()\n return 0\n}\n",
			nil,
		},
		{
			"loopBodyHoldsLock",
			"func g9(s *stats) {\n for i := 0; i < 3; i++ {\n s.mu.Lock()\n s.total++\n s.mu.Unlock()\n }\n}\n",
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name+"/lgo", func(t *testing.T) {
			got := summarize(Run(checkSrc(t, KindLgo, "x.lgo", advLocksHead+tt.body)))
			if !slices.Equal(got, tt.want) {
				t.Errorf("results = %q, want %q", got, tt.want)
			}
		})
		t.Run(tt.name+"/go", func(t *testing.T) {
			want := make([]string, 0, len(tt.want))
			for _, w := range tt.want {
				goW, _ := strings.CutSuffix(w, " forbid")
				want = append(want, goW+" deny")
			}
			got := summarize(Run(checkSrc(t, KindGo, "x.go", advLocksHead+tt.body)))
			if !slices.Equal(got, want) {
				t.Errorf("results = %q, want %q", got, want)
			}
		})
	}
}

func TestAdvGuardedAccessLimitations(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			"gotoSkipsLockUnreported",
			"func g1(s *stats) int {\n goto End\n s.mu.Lock()\nEnd:\n return s.total\n}\n",
			nil,
		},
		{
			"methodValueUnlockUnreported",
			"func g3(s *stats) int {\n s.mu.Lock()\n unlock := s.mu.Unlock\n _ = s.total\n unlock()\n return s.total\n}\n",
			nil,
		},
		{
			"indexLockReported",
			"func g4() {\n arr[0].mu.Lock()\n arr[0].n = 1\n arr[0].mu.Unlock()\n}\n",
			[]string{"31 concurrency::unguarded_access deny"},
		},
		{
			"tryLockReported",
			"func g6(s *stats) {\n if s.mu.TryLock() {\n _ = s.total\n s.mu.Unlock()\n }\n}\n",
			[]string{"31 concurrency::unguarded_access deny"},
		},
		{
			"storedClosureReported",
			"func h2(s *stats) int {\n s.mu.Lock()\n defer s.mu.Unlock()\n f := func() int { return s.total }\n return f()\n}\n",
			[]string{"32 concurrency::unguarded_access deny"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarize(Run(checkSrc(t, KindGo, "x.go", advLocksHead+tt.body)))
			if !slices.Equal(got, tt.want) {
				t.Errorf("results = %q, want %q", got, tt.want)
			}
		})
	}
}

const advRequireHead = `package p

import "sync"

type gate struct {
	mu sync.Mutex

	//logos:guardedby mu
	open bool
}

//logos:requires g.mu
func (g *gate) toggle() {
	g.open = !g.open
}

//logos:releases g.mu
func (g *gate) release() {}
`

func TestAdvLockRequirement(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			"directRequireHeld",
			"func r4(g *gate) {\n g.mu.Lock()\n g.toggle()\n g.mu.Unlock()\n}\n",
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarize(Run(checkSrc(t, KindLgo, "x.lgo", advRequireHead+tt.body)))
			if !slices.Equal(got, tt.want) {
				t.Errorf("results = %q, want %q", got, tt.want)
			}
		})
	}
}

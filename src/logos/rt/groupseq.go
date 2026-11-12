// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rt

import (
	"cmp"
	"context"
	"io"
	"iter"
	"slices"
	"sync"
	"sync/atomic"
)

func GroupSeq[S, R any](ctx context.Context, limit int, src iter.Seq[S], f func(context.Context, S) (R, bool, error)) iter.Seq2[R, error] {
	s := newGroupStage(limit, f)
	return s.ranged(ctx, func() { s.fromSeq(src) })
}

func GroupSeqUnordered[S, R any](ctx context.Context, limit int, src iter.Seq[S], f func(context.Context, S) (R, bool, error)) iter.Seq2[R, error] {
	s := newGroupStage(limit, f)
	s.completion = true
	return s.ranged(ctx, func() { s.fromSeq(src) })
}

func GroupChan[S, R any](ctx context.Context, limit int, src <-chan S, f func(context.Context, S) (R, bool, error)) iter.Seq2[R, error] {
	s := newGroupStage(limit, f)
	return s.ranged(ctx, func() { s.fromChan(src) })
}

func GroupChanUnordered[S, R any](ctx context.Context, limit int, src <-chan S, f func(context.Context, S) (R, bool, error)) iter.Seq2[R, error] {
	s := newGroupStage(limit, f)
	s.completion = true
	return s.ranged(ctx, func() { s.fromChan(src) })
}

type groupItem[R any] struct {
	idx  int
	keep bool
	r    R
}

type groupStage[S, R any] struct {
	limit      int
	f          func(context.Context, S) (R, bool, error)
	completion bool
	used       atomic.Bool

	yield   func(R, error) bool
	gdone   <-chan struct{}
	g       *Group
	cancel  context.CancelFunc
	wake    chan struct{}
	spawned int
	held    int
	stopped bool
	joined  bool
	err     error

	mu   sync.Mutex
	buf  map[int]groupItem[R]
	done int
	next int
}

func newGroupStage[S, R any](limit int, f func(context.Context, S) (R, bool, error)) *groupStage[S, R] {
	return &groupStage[S, R]{limit: limit, f: f}
}

func (s *groupStage[S, R]) ranged(ctx context.Context, drive func()) iter.Seq2[R, error] {
	return func(yield func(R, error) bool) {
		s.start(ctx, yield)
		defer s.join()
		if s.live() {
			drive()
			s.drain()
		}
		s.conclude(ctx)
	}
}

func (s *groupStage[S, R]) start(ctx context.Context, yield func(R, error) bool) {
	if s.limit <= 0 {
		panic("logos: group stage limit must be positive")
	}
	if s.used.Swap(true) {
		panic("logos: group stage sequence ranged twice")
	}
	s.yield = yield
	segCtx, cancel := context.WithCancel(ctx)
	g, gctx := NewGroup(segCtx, 0)
	s.g, s.cancel, s.gdone = g, cancel, gctx.Done()
	s.wake = make(chan struct{}, 1)
	s.buf = make(map[int]groupItem[R])
}

func (s *groupStage[S, R]) live() bool {
	if s.stopped {
		return false
	}
	select {
	case <-s.gdone:
		return false
	default:
		return true
	}
}

func (s *groupStage[S, R]) fromSeq(src iter.Seq[S]) {
	src(func(v S) bool {
		if !s.slot() {
			return false
		}
		s.spawn(v)
		return true
	})
}

func (s *groupStage[S, R]) fromChan(src <-chan S) {
	for {
		s.flush()
		if !s.live() {
			return
		}
		var in <-chan S
		if s.held < s.limit {
			in = src
		}
		select {
		case v, ok := <-in:
			if !ok {
				return
			}
			s.spawn(v)
		case <-s.wake:
		case <-s.gdone:
			return
		}
	}
}

func (s *groupStage[S, R]) slot() bool {
	for {
		s.flush()
		if !s.live() {
			return false
		}
		if s.held < s.limit {
			return true
		}
		s.await()
	}
}

func (s *groupStage[S, R]) drain() {
	for {
		s.flush()
		if s.held == 0 || !s.live() {
			return
		}
		s.await()
	}
}

func (s *groupStage[S, R]) await() {
	select {
	case <-s.wake:
	case <-s.gdone:
	}
}

func (s *groupStage[S, R]) spawn(v S) {
	idx := s.spawned
	s.spawned++
	s.held++
	s.g.Go(func(c context.Context) error {
		r, keep, err := s.f(c, v)
		if err != nil {
			return err
		}
		s.finish(groupItem[R]{idx: idx, keep: keep, r: r})
		return nil
	})
}

func (s *groupStage[S, R]) finish(it groupItem[R]) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := it.idx
	if s.completion {
		key = s.done
	}
	s.done++
	s.buf[key] = it
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *groupStage[S, R]) pop() (groupItem[R], bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.buf[s.next]
	if ok {
		delete(s.buf, s.next)
		s.next++
	}
	return it, ok
}

func (s *groupStage[S, R]) flush() {
	for s.live() {
		it, ok := s.pop()
		if !ok {
			return
		}
		s.process(it)
	}
}

func (s *groupStage[S, R]) process(it groupItem[R]) {
	s.held--
	if !it.keep {
		closeValue(it.r)
		return
	}
	if !s.yield(it.r, nil) {
		s.stopped = true
	}
}

func (s *groupStage[S, R]) conclude(ctx context.Context) {
	err := s.join()
	if s.stopped {
		return
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		var zero R
		s.yield(zero, err)
	}
}

func (s *groupStage[S, R]) join() error {
	if !s.joined {
		s.joined = true
		s.cancel()
		s.err = s.g.Wait()
		s.abandon()
	}
	return s.err
}

func (s *groupStage[S, R]) abandon() {
	lost := make([]groupItem[R], 0, len(s.buf))
	for _, it := range s.buf {
		lost = append(lost, it)
	}
	slices.SortFunc(lost, func(a, b groupItem[R]) int { return cmp.Compare(b.idx, a.idx) })
	closeAll(lost)
}

func closeAll[R any](items []groupItem[R]) {
	if len(items) == 0 {
		return
	}
	defer closeAll(items[1:])
	closeValue(items[0].r)
}

func closeValue[R any](r R) {
	if c, ok := any(r).(io.Closer); ok {
		_ = c.Close()
	}
}

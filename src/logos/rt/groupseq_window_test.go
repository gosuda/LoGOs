// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rt

import (
	"context"
	"errors"
	"iter"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestGroupStageWindowBoundsOutstandingResultsOnEveryEntry(t *testing.T) {
	eachEntry(t, func(t *testing.T, run entryFunc[int]) {
		const limit, total = 3, 40
		var outstanding, high atomic.Int32
		seq := run(t.Context(), limit, total, func(_ context.Context, i int) (int, bool, error) {
			raise(&high, outstanding.Add(1))
			return i, true, nil
		})
		d := finished(t, startStage(seq, func(int) bool {
			time.Sleep(time.Millisecond)
			outstanding.Add(-1)
			return true
		}))
		if d.err != nil || len(d.rs) != total {
			t.Fatalf("len(rs) = %d, err = %v, want %d, nil", len(d.rs), d.err, total)
		}
		if high.Load() > limit {
			t.Fatalf("children in flight plus finished results not yet consumed reached %d, want at most %d", high.Load(), limit)
		}
	})
}

func TestGroupChanEndsWhenTheParentIsCancelledWhileTheChannelIsQuiet(t *testing.T) {
	for name, entry := range map[string]func(context.Context, <-chan int, func(context.Context, int) (int, bool, error)) iter.Seq2[int, error]{
		"chan": func(ctx context.Context, src <-chan int, f func(context.Context, int) (int, bool, error)) iter.Seq2[int, error] {
			return GroupChan(ctx, 2, src, f)
		},
		"chan-unordered": func(ctx context.Context, src <-chan int, f func(context.Context, int) (int, bool, error)) iter.Seq2[int, error] {
			return GroupChanUnordered(ctx, 2, src, f)
		},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				ch := make(chan int)
				seq := entry(ctx, ch, func(_ context.Context, v int) (int, bool, error) { return v, true, nil })
				c := startStage(seq, nil)
				synctest.Wait()
				select {
				case d := <-c:
					t.Fatalf("the range ended before the cancel: rs = %v, err = %v", d.rs, d.err)
				default:
				}
				cancel()
				d := finished(t, c)
				if !errors.Is(d.err, context.Canceled) || len(d.rs) != 0 {
					t.Fatalf("rs = %v, err = %v, want none, context.Canceled", d.rs, d.err)
				}
			})
		})
	}
}

func TestGroupSeqBlockedSourceDelaysDeliveryAndCancellationUntilItYields(t *testing.T) {
	for name, entry := range map[string]func(context.Context, iter.Seq[int], func(context.Context, int) (int, bool, error)) iter.Seq2[int, error]{
		"seq": func(ctx context.Context, src iter.Seq[int], f func(context.Context, int) (int, bool, error)) iter.Seq2[int, error] {
			return GroupSeq(ctx, 2, src, f)
		},
		"seq-unordered": func(ctx context.Context, src iter.Seq[int], f func(context.Context, int) (int, bool, error)) iter.Seq2[int, error] {
			return GroupSeqUnordered(ctx, 2, src, f)
		},
	} {
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				release := make(chan struct{})
				src := func(yield func(int) bool) {
					if yield(1) {
						<-release
					}
				}
				var delivered atomic.Int32
				seq := entry(ctx, src, func(_ context.Context, v int) (int, bool, error) { return v, true, nil })
				c := startStage(seq, func(int) bool {
					delivered.Add(1)
					return true
				})
				synctest.Wait()
				cancel()
				synctest.Wait()
				select {
				case d := <-c:
					t.Fatalf("the range ended while the source was blocked: rs = %v, err = %v", d.rs, d.err)
				default:
				}
				if delivered.Load() != 0 {
					t.Fatalf("delivered %d results while the source was blocked, want none", delivered.Load())
				}
				close(release)
				d := finished(t, c)
				if !errors.Is(d.err, context.Canceled) || !slices.Equal(d.rs, nil) {
					t.Fatalf("rs = %v, err = %v, want none, context.Canceled", d.rs, d.err)
				}
			})
		})
	}
}

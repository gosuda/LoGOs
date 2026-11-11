// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package json

import (
	stdjson "encoding/json"
	"reflect"
	"testing"
)

func TestWalkEnterFrameBalances(t *testing.T) {
	var s Walk
	for range 100 {
		if err := s.EnterFrame(0); err != nil {
			t.Fatalf("EnterFrame = %v", err)
		}
	}
	for range 100 {
		s.LeaveFrame()
	}
	if err := s.EnterFrame(0); err != nil {
		t.Fatalf("EnterFrame after Leave = %v", err)
	}
	s.LeaveFrame()
}

func TestWalkEnterFrameDepthBound(t *testing.T) {
	var s Walk
	ok := 0
	for range 10001 {
		if err := s.EnterFrame(int64(ok)); err != nil {
			if ok < 10000 {
				t.Fatalf("EnterFrame failed at depth %d: %v", ok, err)
			}
			s.LeaveFrame()
			if err := s.EnterFrame(0); err != nil {
				t.Fatalf("EnterFrame after depth failure = %v", err)
			}
			s.LeaveFrame()
			for range ok {
				s.LeaveFrame()
			}
			return
		}
		ok++
	}
	for range ok {
		s.LeaveFrame()
	}
}

func TestWalkEnterIgnoresScalars(t *testing.T) {
	var s Walk
	if err := s.Enter(nil); err != nil {
		t.Fatalf("Enter nil = %v", err)
	}
	s.Leave(nil)
	if err := s.Enter(7); err != nil {
		t.Fatalf("Enter int = %v", err)
	}
	s.Leave(7)
	if err := s.Enter("x"); err != nil {
		t.Fatalf("Enter string = %v", err)
	}
	s.Leave("x")
}

func TestWalkEnterCycleNeedsRepeat(t *testing.T) {
	var s Walk
	p := &struct{ X int }{X: 1}
	m := map[string]int{"a": 1}
	sl := []int{1, 2}
	for _, v := range []any{p, m, sl} {
		if err := s.Enter(v); err != nil {
			t.Fatalf("Enter %T = %v", v, err)
		}
		s.Leave(v)
	}
	q := &struct{ X int }{X: 2}
	for range 1001 {
		if err := s.Enter(q); err != nil {
			t.Fatalf("Enter repeat setup = %v", err)
		}
	}
	err := s.Enter(q)
	if err == nil {
		for range 1001 {
			s.Leave(q)
		}
		t.Fatal("Enter repeated pointer past threshold did not fail")
	}
	var unsupported *stdjson.UnsupportedValueError
	if reflect.TypeOf(err) != reflect.TypeOf(unsupported) {
		t.Fatalf("Enter cycle type = %T", err)
	}
	s.Leave(q)
	for range 1000 {
		s.Leave(q)
	}
	if err := s.Enter(q); err != nil {
		t.Fatalf("Enter after Leave = %v", err)
	}
	s.Leave(q)
}

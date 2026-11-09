// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package syntax

import (
	"strings"
	"testing"
)

const logosScannerMode = uint(LogosMode)

func TestLogosQuestionAfterName(t *testing.T) {
	var errs []string
	var s scanner
	s.init(strings.NewReader("x?"), func(_, _ uint, msg string) {
		errs = append(errs, msg)
	}, logosScannerMode)

	s.next()
	if s.tok != _Name || s.lit != "x" {
		t.Fatalf("got %s %q; want _Name %q", s.tok, s.lit, "x")
	}
	s.next()
	if s.tok != _Question {
		t.Fatalf("got %s; want _Question", s.tok)
	}
	s.next()
	if s.tok != _Semi || s.lit != "EOF" {
		t.Fatalf("got %s %q; want _Semi %q", s.tok, s.lit, "EOF")
	}
	s.next()
	if s.tok != _EOF {
		t.Fatalf("got %s; want _EOF", s.tok)
	}
	if len(errs) != 0 {
		t.Fatalf("got errors %q; want none", errs)
	}
}

func TestLogosQuestionNlsemi(t *testing.T) {
	for _, test := range []struct {
		src    string
		tokens []token
	}{
		{"x?\n", []token{_Name, _Question, _Semi, _EOF}},
		{"f()?\n", []token{_Name, _Lparen, _Rparen, _Question, _Semi, _EOF}},
	} {
		var errs []string
		var s scanner
		s.init(strings.NewReader(test.src), func(_, _ uint, msg string) {
			errs = append(errs, msg)
		}, logosScannerMode)

		for i, want := range test.tokens {
			s.next()
			if s.tok != want {
				t.Errorf("%q token %d: got %s; want %s", test.src, i, s.tok, want)
			}
			if want == _Semi && s.lit != "newline" {
				t.Errorf("%q token %d: got semi %q; want %q", test.src, i, s.lit, "newline")
			}
		}
		if len(errs) != 0 {
			t.Errorf("%q: got errors %q; want none", test.src, errs)
		}
	}
}

func TestPlainQuestionRejected(t *testing.T) {
	const want = "invalid character U+003F '?'"
	for _, test := range []struct {
		src       string
		line, col uint
	}{
		{"?", 0, 0},
		{"x?", 0, 1},
		{"f()?", 0, 3},
	} {
		var s scanner
		var line, col uint
		var err string
		s.init(strings.NewReader(test.src), func(l, c uint, msg string) {
			if err == "" {
				line, col = l-linebase, c-colbase
				err = msg
			}
		}, 0)

		for {
			s.next()
			if s.tok == _EOF {
				break
			}
		}

		if err != want {
			t.Errorf("%q: got err = %q; want %q", test.src, err, want)
		}
		if line != test.line {
			t.Errorf("%q: got line = %d; want %d", test.src, line, test.line)
		}
		if col != test.col {
			t.Errorf("%q: got col = %d; want %d", test.src, col, test.col)
		}
	}
}

func TestQuestionString(t *testing.T) {
	if got := _Question.String(); got != "?" {
		t.Errorf("got _Question.String() = %q; want %q", got, "?")
	}
}

func TestKeywordRangeUnchanged(t *testing.T) {
	for _, mode := range []uint{0, logosScannerMode} {
		for _, test := range []struct {
			src  string
			want token
		}{
			{"break", _Break},
			{"var", _Var},
		} {
			var errs []string
			var s scanner
			s.init(strings.NewReader(test.src), func(_, _ uint, msg string) {
				errs = append(errs, msg)
			}, mode)

			s.next()
			if s.tok != test.want {
				t.Errorf("mode %d: %q: got %s; want %s", mode, test.src, s.tok, test.want)
			}
			if len(errs) != 0 {
				t.Errorf("mode %d: %q: got errors %q; want none", mode, test.src, errs)
			}
		}
	}
	if !(_Break < _Var && _Var < _Question && _Question < _QuestionDot && _QuestionDot < _Coalesce && _Coalesce < tokenCount) {
		t.Errorf("want _Break < _Var < _Question < _QuestionDot < _Coalesce < tokenCount, got %d < %d < %d < %d < %d < %d",
			_Break, _Var, _Question, _QuestionDot, _Coalesce, tokenCount)
	}
}

func TestTokenCountUnder64(t *testing.T) {
	if tokenCount > 64 {
		t.Errorf("got tokenCount = %d; want <= 64", tokenCount)
	}
}

func TestLogosCoalesceScan(t *testing.T) {
	for _, test := range []struct {
		src    string
		tokens []token
	}{
		{"a??b", []token{_Name, _Coalesce, _Name, _Semi, _EOF}},
		{"a ?? b", []token{_Name, _Coalesce, _Name, _Semi, _EOF}},
		{"a? ?b", []token{_Name, _Question, _Question, _Name, _Semi, _EOF}},
		{"a ? ? b", []token{_Name, _Question, _Question, _Name, _Semi, _EOF}},
		{"a?\n?b", []token{_Name, _Question, _Semi, _Question, _Name, _Semi, _EOF}},
	} {
		var errs []string
		var s scanner
		s.init(strings.NewReader(test.src), func(_, _ uint, msg string) {
			errs = append(errs, msg)
		}, logosScannerMode)

		for i, want := range test.tokens {
			s.next()
			if s.tok != want {
				t.Errorf("%q token %d: got %s; want %s", test.src, i, s.tok, want)
			}
			if want == _Semi && i == len(test.tokens)-2 && s.lit != "EOF" {
				t.Errorf("%q token %d: got semi %q; want %q", test.src, i, s.lit, "EOF")
			}
		}
		if len(errs) != 0 {
			t.Errorf("%q: got errors %q; want none", test.src, errs)
		}
	}
}

func TestLogosQuestionDotScan(t *testing.T) {
	for _, test := range []struct {
		src    string
		tokens []token
	}{
		{"a?.b", []token{_Name, _QuestionDot, _Name, _Semi, _EOF}},
		{"a ?. b", []token{_Name, _QuestionDot, _Name, _Semi, _EOF}},
		{"a ? . b", []token{_Name, _Question, _Dot, _Name, _Semi, _EOF}},
		{"a?\t.b", []token{_Name, _Question, _Dot, _Name, _Semi, _EOF}},
		{"a?\n.b", []token{_Name, _Question, _Semi, _Dot, _Name, _Semi, _EOF}},
		{"a?.5", []token{_Name, _Question, _Literal, _Semi, _EOF}},
	} {
		var errs []string
		var s scanner
		s.init(strings.NewReader(test.src), func(_, _ uint, msg string) {
			errs = append(errs, msg)
		}, logosScannerMode)

		for i, want := range test.tokens {
			s.next()
			if s.tok != want {
				t.Errorf("%q token %d: got %s; want %s", test.src, i, s.tok, want)
			}
			if want == _Semi && i == len(test.tokens)-2 && s.lit != "EOF" {
				t.Errorf("%q token %d: got semi %q; want %q", test.src, i, s.lit, "EOF")
			}
		}
		if len(errs) != 0 {
			t.Errorf("%q: got errors %q; want none", test.src, errs)
		}
	}
}

func TestLogosOptionNlsemi(t *testing.T) {
	for _, test := range []struct {
		src    string
		tokens []token
	}{
		{"a?.\n b", []token{_Name, _QuestionDot, _Name, _Semi, _EOF}},
		{"a ??\n b", []token{_Name, _Coalesce, _Name, _Semi, _EOF}},
		{"x?\n y", []token{_Name, _Question, _Semi, _Name, _Semi, _EOF}},
	} {
		var errs []string
		var s scanner
		s.init(strings.NewReader(test.src), func(_, _ uint, msg string) {
			errs = append(errs, msg)
		}, logosScannerMode)

		for i, want := range test.tokens {
			s.next()
			if s.tok != want {
				t.Errorf("%q token %d: got %s; want %s", test.src, i, s.tok, want)
			}
			if want == _Semi {
				wantLit := "newline"
				if i == len(test.tokens)-2 {
					wantLit = "EOF"
				}
				if s.lit != wantLit {
					t.Errorf("%q token %d: got semi %q; want %q", test.src, i, s.lit, wantLit)
				}
			}
		}
		if len(errs) != 0 {
			t.Errorf("%q: got errors %q; want none", test.src, errs)
		}
	}
}

func TestOptionTokenStrings(t *testing.T) {
	if got := _QuestionDot.String(); got != "?." {
		t.Errorf("got _QuestionDot.String() = %q; want %q", got, "?.")
	}
	if got := _Coalesce.String(); got != "??" {
		t.Errorf("got _Coalesce.String() = %q; want %q", got, "??")
	}
}

func TestPlainOptionTailsRejected(t *testing.T) {
	const want = "invalid character U+003F '?'"
	for _, test := range []struct {
		src       string
		line, col uint
	}{
		{"a?.b", 0, 1},
		{"a??b", 0, 1},
	} {
		var s scanner
		var line, col uint
		var err string
		s.init(strings.NewReader(test.src), func(l, c uint, msg string) {
			if err == "" {
				line, col = l-linebase, c-colbase
				err = msg
			}
		}, 0)

		for {
			s.next()
			if s.tok == _EOF {
				break
			}
		}

		if err != want {
			t.Errorf("%q: got err = %q; want %q", test.src, err, want)
		}
		if line != test.line {
			t.Errorf("%q: got line = %d; want %d", test.src, line, test.line)
		}
		if col != test.col {
			t.Errorf("%q: got col = %d; want %d", test.src, col, test.col)
		}
	}
}

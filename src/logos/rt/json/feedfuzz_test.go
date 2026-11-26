// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package json_test

import (
	stdjson "encoding/json"
	"testing"

	rtjson "logos/rt/json"
)

// A lone surrogate code unit (ED A0 80 through ED BF BF) must fail
// encoding: the decoder maps lone \uD800..\uDFFF escapes to U+FFFD, so
// a surrogate that encoded silently could never round-trip. Other
// invalid UTF-8 bytes keep the jsonv2 replacement rule.
func TestWalkRejectsLoneSurrogate(t *testing.T) {
	for _, s := range []string{
		"\xed\xa0\x80",
		"\xed\xbf\xbf",
		"a\xed\xa0\x80b",
		"\xed\xa0\x80\xed\xbf\xbf",
	} {
		var w rtjson.Walk
		if _, err := w.AppendAny(nil, s); !isLoneSurrogateError(err) {
			t.Errorf("AppendAny(%q) error = %v, want lone surrogate rejection", s, err)
		}
		var m rtjson.Walk
		if _, err := m.AppendAny(nil, map[string]string{s: "v"}); !isLoneSurrogateError(err) {
			t.Errorf("AppendAny(map key %q) error = %v, want lone surrogate rejection", s, err)
		}
		type quoted struct {
			S string `json:",string"`
		}
		var q rtjson.Walk
		if _, err := q.AppendAny(nil, quoted{S: s}); !isLoneSurrogateError(err) {
			t.Errorf("AppendAny(quoted %q) error = %v, want lone surrogate rejection", s, err)
		}
	}
	// Invalid bytes that are not surrogates still encode by replacement.
	for _, s := range []string{"a\xffb", "\xe2\x80", "\xff", "plain"} {
		var w rtjson.Walk
		got, err := w.AppendAny(nil, s)
		if err != nil {
			t.Errorf("AppendAny(%q) error = %v, want replacement without error", s, err)
			continue
		}
		want, werr := stdjson.Marshal(s)
		if werr != nil || string(got) != string(want) {
			t.Errorf("AppendAny(%q) = %q, %v; encoding/json %q, %v", s, got, err, want, werr)
		}
	}
}

func isLoneSurrogateError(err error) bool {
	uverr, ok := err.(*stdjson.UnsupportedValueError)
	return ok && uverr.Str == "lone surrogate code unit in string"
}

func hasLoneSurrogate(s string) bool {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == 0xED && s[i+1] >= 0xA0 && s[i+1] <= 0xBF && s[i+2] >= 0x80 && s[i+2] <= 0xBF {
			return true
		}
	}
	return false
}

func FuzzWalkLoneSurrogate(f *testing.F) {
	for _, s := range []string{"", "x", "a\xffb", "\xed\xa0\x80", "a\xed\xbf\xbfb", "\xff<\u2028"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 512 {
			return
		}
		var w rtjson.Walk
		_, verr := w.AppendAny(nil, s)
		var m rtjson.Walk
		_, merr := m.AppendAny(nil, map[string]string{"k": s})
		wantErr := hasLoneSurrogate(s)
		if (verr != nil) != wantErr {
			t.Fatalf("value %q: error = %v, want error = %v", s, verr, wantErr)
		}
		if (merr != nil) != wantErr {
			t.Fatalf("map value %q: error = %v, want error = %v", s, merr, wantErr)
		}
		if verr != nil && !isLoneSurrogateError(verr) {
			t.Fatalf("value %q: wrong error type %T %q", s, verr, verr)
		}
	})
}

func FuzzChunked(f *testing.F) {
	seeds := []string{
		`null`,
		`{"a":1}`,
		`[1,2]`,
		`"a\"b"`,
		`123.456e-7`,
		`[1,{"b":[null]}]`,
		`{"a":}`,
		`01`,
		`"bad \x"`,
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512 {
			return
		}
		whole, werr := collect([][]byte{data})
		wvalid := werr == nil && hasEnd(whole)
		if wvalid != stdjson.Valid(data) {
			t.Fatalf("validity: scanner %v oracle %v for %q", wvalid, stdjson.Valid(data), data)
		}
		if len(data) == 0 {
			return
		}
		mid := len(data) / 2
		part, perr := collect([][]byte{data[:mid], data[mid:]})
		pvalid := perr == nil && hasEnd(part)
		if pvalid != wvalid {
			t.Fatalf("split validity: %v vs %v for %q", pvalid, wvalid, data)
		}
		if !wvalid {
			return
		}
		wlog, err := logical(whole)
		if err != nil {
			t.Fatalf("whole logical: %v", err)
		}
		plog, err := logical(part)
		if err != nil {
			t.Fatalf("split logical: %v", err)
		}
		if !equalStr(plog, wlog) {
			t.Fatalf("split tokens differ for %q", data)
		}
	})
}

func hasEnd(toks []rtjson.Token) bool {
	for _, t := range toks {
		if t.Kind == rtjson.TokenEndOfDocument {
			return true
		}
	}
	return false
}

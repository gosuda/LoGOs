// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package json

import (
	stdjson "encoding/json"
	"testing"
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
		var w Walk
		if _, err := w.AppendAny(nil, s); !isLoneSurrogateError(err) {
			t.Errorf("AppendAny(%q) error = %v, want lone surrogate rejection", s, err)
		}
		var m Walk
		if _, err := m.AppendAny(nil, map[string]string{s: "v"}); !isLoneSurrogateError(err) {
			t.Errorf("AppendAny(map key %q) error = %v, want lone surrogate rejection", s, err)
		}
		type quoted struct {
			S string `json:",string"`
		}
		var q Walk
		if _, err := q.AppendAny(nil, quoted{S: s}); !isLoneSurrogateError(err) {
			t.Errorf("AppendAny(quoted %q) error = %v, want lone surrogate rejection", s, err)
		}
	}
	// Invalid bytes that are not surrogates still encode by replacement.
	for _, s := range []string{"a\xffb", "\xe2\x80", "\xff", "plain"} {
		var w Walk
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

func FuzzWalkLoneSurrogate(f *testing.F) {
	for _, s := range []string{"", "x", "a\xffb", "\xed\xa0\x80", "a\xed\xbf\xbfb", "\xff<\u2028"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 512 {
			return
		}
		var w Walk
		_, verr := w.AppendAny(nil, s)
		var m Walk
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

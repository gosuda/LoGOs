// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package json_test

import (
	stdjson "encoding/json"
	"testing"

	rtjson "logos/rt/json"
)

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

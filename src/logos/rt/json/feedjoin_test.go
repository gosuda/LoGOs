// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package json_test

import (
	"testing"

	rtjson "logos/rt/json"
)

// A one-value join closes exactly once: partial input, two closing
// tokens, and a digit-free joined number are all syntax errors. An empty
// final Number only closes digits that arrived in earlier chunks, which is
// how the scanner encodes a number split across chunk boundaries.
// Planted fault: Join checks token families but not where the value
// ends, so partial-only input succeeds and two finals concatenate.
func TestJoinValueShape(t *testing.T) {
	num := func(b string) rtjson.Token { return rtjson.Token{Kind: rtjson.TokenNumber, Bytes: []byte(b)} }
	pnum := func(b string) rtjson.Token {
		return rtjson.Token{Kind: rtjson.TokenPartialNumber, Bytes: []byte(b)}
	}
	str := func(b string) rtjson.Token { return rtjson.Token{Kind: rtjson.TokenString, Bytes: []byte(b)} }
	pstr := func(b string) rtjson.Token {
		return rtjson.Token{Kind: rtjson.TokenPartialString, Bytes: []byte(b)}
	}
	rows := []struct {
		name string
		toks []rtjson.Token
		want string
		fail bool
	}{
		{"number halves", []rtjson.Token{pnum("12"), num("34")}, "1234", false},
		{"single string", []rtjson.Token{str("ab")}, "ab", false},
		{"empty string", []rtjson.Token{str("")}, "", false},
		{"string pieces", []rtjson.Token{pstr("a"), pstr("b"), str("c")}, "abc", false},
		{"partial only", []rtjson.Token{pnum("12")}, "", true},
		{"two finals", []rtjson.Token{num("12"), num("34")}, "", true},
		{"final before partial", []rtjson.Token{num("12"), pnum("34")}, "", true},
		{"empty final number", []rtjson.Token{pnum("12"), num("")}, "12", false},
		{"lone empty number", []rtjson.Token{num("")}, "", true},
		{"split number closes empty", []rtjson.Token{pnum("12"), num("")}, "12", false},
		{"single empty number", []rtjson.Token{num("")}, "", true},
		{"empty pieces", []rtjson.Token{pnum(""), num("")}, "", true},
		{"mixed families", []rtjson.Token{pnum("1"), str("x")}, "", true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			got, err := rtjson.Join(row.toks, 1<<20)
			if row.fail {
				if err != rtjson.ErrSyntax {
					t.Fatalf("Join = (%q, %v), want syntax error", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Join = %v, want nil", err)
			}
			if string(got) != row.want {
				t.Fatalf("Join = %q, want %q", got, row.want)
			}
		})
	}
}

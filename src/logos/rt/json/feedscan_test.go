// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package json_test

import (
	stdjson "encoding/json"
	"testing"

	rtjson "logos/rt/json"
)

func collect(chunks [][]byte) ([]rtjson.Token, error) {
	var s rtjson.FeedScanner
	var out []rtjson.Token
	for _, c := range chunks {
		if len(c) == 0 {
			continue
		}
		s.Feed(c)
		if s.Err() != nil {
			return out, s.Err()
		}
		for {
			t, ok := s.Next()
			if ok {
				cp := append([]byte(nil), t.Bytes...)
				t.Bytes = cp
				out = append(out, t)
				if t.Kind == rtjson.TokenEndOfDocument {
					return out, nil
				}
				continue
			}
			if s.Err() != nil {
				return out, s.Err()
			}
			break
		}
	}
	s.End()
	for {
		t, ok := s.Next()
		if ok {
			cp := append([]byte(nil), t.Bytes...)
			t.Bytes = cp
			out = append(out, t)
			if t.Kind == rtjson.TokenEndOfDocument {
				return out, nil
			}
			continue
		}
		if s.Err() != nil {
			return out, s.Err()
		}
		break
	}
	return out, nil
}

func logical(raw []rtjson.Token) ([]string, error) {
	var out []string
	var parts []rtjson.Token
	for _, t := range raw {
		switch t.Kind {
		case rtjson.TokenPartialNumber, rtjson.TokenPartialString:
			parts = append(parts, t)
		case rtjson.TokenNumber, rtjson.TokenString:
			parts = append(parts, t)
			b, err := rtjson.Join(parts, 1<<20)
			if err != nil {
				return nil, err
			}
			out = append(out, t.Kind.String()+":"+string(b))
			parts = nil
		case rtjson.TokenEndOfDocument:
			if len(parts) != 0 {
				return nil, rtjson.ErrSyntax
			}
			out = append(out, "End")
		default:
			if len(parts) != 0 {
				return nil, rtjson.ErrSyntax
			}
			out = append(out, t.Kind.String())
		}
	}
	if len(parts) != 0 {
		return nil, rtjson.ErrSyntax
	}
	return out, nil
}

func equalStr(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func scanValid(doc string) bool {
	raw, err := collect([][]byte{[]byte(doc)})
	if err != nil {
		return false
	}
	for _, t := range raw {
		if t.Kind == rtjson.TokenEndOfDocument {
			return true
		}
	}
	return false
}

func chunked(data []byte, seed uint64) [][]byte {
	var out [][]byte
	i := 0
	s := seed
	for i < len(data) {
		s = s*1664525 + 1013904223
		n := int(s%5) + 1
		j := i + n
		if j > len(data) {
			j = len(data)
		}
		out = append(out, data[i:j])
		i = j
	}
	return out
}

func deepDoc(n int) string {
	b := make([]byte, 0, 2*n)
	for i := 0; i < n; i++ {
		b = append(b, '[')
	}
	for i := 0; i < n; i++ {
		b = append(b, ']')
	}
	return string(b)
}

func TestWholeKinds(t *testing.T) {
	docs := []string{
		`{"a":1}`,
		`[1,2]`,
		`"hi"`,
		`true`,
		`null`,
		`{"a":[1,{"b":null}]}`,
	}
	wants := [][]string{
		{"ObjectBegin", "String:a", "Number:1", "ObjectEnd", "End"},
		{"ArrayBegin", "Number:1", "Number:2", "ArrayEnd", "End"},
		{"String:hi", "End"},
		{"True", "End"},
		{"Null", "End"},
		{"ObjectBegin", "String:a", "ArrayBegin", "Number:1", "ObjectBegin", "String:b", "Null", "ObjectEnd", "ArrayEnd", "ObjectEnd", "End"},
	}
	for i := range docs {
		raw, err := collect([][]byte{[]byte(docs[i])})
		if err != nil {
			t.Fatalf("doc %d: %v", i, err)
		}
		got, err := logical(raw)
		if err != nil {
			t.Fatalf("doc %d: %v", i, err)
		}
		if !equalStr(got, wants[i]) {
			t.Fatalf("doc %d: got %q want %q", i, got, wants[i])
		}
	}
}

func TestValidityAgrees(t *testing.T) {
	valid := []string{
		`null`,
		`true`,
		`false`,
		`0`,
		`-1`,
		`123.456e-7`,
		`"hello"`,
		`"a\"b"`,
		`[]`,
		`{}`,
		`{"a":1}`,
		`[1,{"b":[null]}]`,
		"  \n\t null  ",
	}
	invalid := []string{
		``,
		` `,
		`{`,
		`[`,
		`{"a":}`,
		`{"a":1,}`,
		`[1,]`,
		`01`,
		`+1`,
		`1.`,
		`1e`,
		`"unterminated`,
		`"bad \x"`,
		`tru`,
		`nul`,
		`{"a":1} trailing`,
		`[1} `,
		`{"a":1]`,
	}
	for _, d := range valid {
		if !scanValid(d) {
			t.Errorf("valid rejected: %q", d)
		}
		if !stdjson.Valid([]byte(d)) {
			t.Errorf("oracle rejects valid: %q", d)
		}
	}
	for _, d := range invalid {
		if scanValid(d) {
			t.Errorf("invalid accepted: %q", d)
		}
		if stdjson.Valid([]byte(d)) {
			t.Errorf("oracle accepts invalid: %q", d)
		}
	}
}

func TestSplitPointsEqual(t *testing.T) {
	docs := []string{
		`{"a":1}`,
		`[1,2]`,
		`"a\"b"`,
		`123.456e-7`,
		`[1,{"b":[null]}]`,
		`true`,
		`null`,
	}
	for _, d := range docs {
		whole, err := collect([][]byte{[]byte(d)})
		if err != nil {
			t.Fatalf("whole %q: %v", d, err)
		}
		wlog, err := logical(whole)
		if err != nil {
			t.Fatalf("whole %q: %v", d, err)
		}
		data := []byte(d)
		for k := 0; k <= len(data); k++ {
			raw, err := collect([][]byte{data[:k], data[k:]})
			if err != nil {
				t.Fatalf("split %q at %d: %v", d, k, err)
			}
			got, err := logical(raw)
			if err != nil {
				t.Fatalf("split %q at %d: %v", d, k, err)
			}
			if !equalStr(got, wlog) {
				t.Fatalf("split %q at %d: got %q want %q", d, k, got, wlog)
			}
		}
	}
}

func TestChunkedSplits(t *testing.T) {
	docs := []string{
		`{"a":"a\"b\\c\/d\be\ff\ng\rh\ti"}`,
		`{"u":"a\u0062"}`,
		`[1.5,-2e+10,0,123456789]`,
		`{"a":[1,{"b":[null,true,false]}]}`,
		"  {\n \"a\" : [1, 2] }  ",
	}
	for _, d := range docs {
		whole, err := collect([][]byte{[]byte(d)})
		if err != nil {
			t.Fatalf("whole %q: %v", d, err)
		}
		wlog, err := logical(whole)
		if err != nil {
			t.Fatalf("whole %q: %v", d, err)
		}
		data := []byte(d)
		raw, err := collect(chunked(data, uint64(len(data))+1))
		if err != nil {
			t.Fatalf("chunked %q: %v", d, err)
		}
		got, err := logical(raw)
		if err != nil {
			t.Fatalf("chunked %q: %v", d, err)
		}
		if !equalStr(got, wlog) {
			t.Fatalf("chunked %q: got %q want %q", d, got, wlog)
		}
	}
}

func TestLoneSurrogateCoerced(t *testing.T) {
	docs := []string{
		`"a\uDC00b"`,
		`"a\uD800b"`,
		`"a\uD800\u0041b"`,
		`"a\uD800\uD123b"`,
		`"a\uD800\uD800\uDC00b"`,
		`"\uDC00"`,
		`"\uD800"`,
		`"\uD800\uD800"`,
		`"\ud800\udc00"`,
		`"\uD83D\uDE00"`,
		`"\\\uD800\\"`,
	}
	for _, d := range docs {
		var want string
		if err := stdjson.Unmarshal([]byte(d), &want); err != nil {
			t.Fatalf("oracle rejects %q: %v", d, err)
		}
		data := []byte(d)
		for k := 0; k <= len(data); k++ {
			raw, err := collect([][]byte{data[:k], data[k:]})
			if err != nil {
				t.Fatalf("split %q at %d: %v", d, k, err)
			}
			got, err := logical(raw)
			if err != nil {
				t.Fatalf("split %q at %d: %v", d, k, err)
			}
			wantLog := []string{"String:" + want, "End"}
			if !equalStr(got, wantLog) {
				t.Fatalf("split %q at %d: got %q want %q", d, k, got, wantLog)
			}
		}
	}
	truncated := []struct {
		doc  string
		want error
	}{
		{`"a\uD800\u004"`, rtjson.ErrSyntax},
		{`"a\uD800\u"`, rtjson.ErrSyntax},
		{`"a\uD800\xb"`, rtjson.ErrSyntax},
		{`"a\uD800\"`, rtjson.ErrUnexpectedEnd},
		{`"a\uD8"`, rtjson.ErrSyntax},
		{`"a\uD800`, rtjson.ErrUnexpectedEnd},
		{`"a\uD800\uD8`, rtjson.ErrUnexpectedEnd},
	}
	for _, tc := range truncated {
		if scanValid(tc.doc) {
			t.Errorf("invalid accepted: %q", tc.doc)
		}
		if stdjson.Valid([]byte(tc.doc)) {
			t.Errorf("oracle accepts invalid: %q", tc.doc)
		}
		data := []byte(tc.doc)
		for k := 0; k <= len(data); k++ {
			_, err := collect([][]byte{data[:k], data[k:]})
			if err != tc.want {
				t.Errorf("split %q at %d: error %v, want %v", tc.doc, k, err, tc.want)
			}
		}
	}
}

func TestMaxValueLen(t *testing.T) {
	parts := []rtjson.Token{
		{Kind: rtjson.TokenPartialString, Bytes: []byte("123")},
		{Kind: rtjson.TokenString, Bytes: []byte("45")},
	}
	b, err := rtjson.Join(parts, 5)
	if err != nil || string(b) != "12345" {
		t.Fatalf("join max 5: %q %v", b, err)
	}
	if _, err := rtjson.Join(parts, 4); err != rtjson.ErrValueTooLong {
		t.Fatalf("join max 4: %v", err)
	}
	mixed := []rtjson.Token{
		{Kind: rtjson.TokenNumber, Bytes: []byte("1")},
		{Kind: rtjson.TokenString, Bytes: []byte("x")},
	}
	if _, err := rtjson.Join(mixed, 10); err != rtjson.ErrSyntax {
		t.Fatalf("join mixed: %v", err)
	}
}

func TestDepthBound(t *testing.T) {
	doc := deepDoc(512)
	raw, err := collect([][]byte{[]byte(doc)})
	if err != nil {
		t.Fatalf("deep 512: %v", err)
	}
	if _, err := logical(raw); err != nil {
		t.Fatalf("deep 512: %v", err)
	}
	var s rtjson.FeedScanner
	prefix := make([]byte, 512)
	for i := range prefix {
		prefix[i] = '['
	}
	s.Feed(prefix)
	for {
		_, ok := s.Next()
		if ok {
			continue
		}
		break
	}
	if s.Err() != nil {
		t.Fatalf("prefix: %v", s.Err())
	}
	if s.Depth() != 512 {
		t.Fatalf("depth = %d want 512", s.Depth())
	}
	big := deepDoc(10001)
	if scanValid(big) {
		t.Fatalf("depth 10001 accepted")
	}
	if stdjson.Valid([]byte(big)) {
		t.Fatalf("oracle accepts depth 10001")
	}
}

func TestAllocs(t *testing.T) {
	doc := []byte(`12345`)
	n := testing.AllocsPerRun(100, func() {
		var s rtjson.FeedScanner
		s.Feed(doc)
		ended := false
		for {
			tok, ok := s.Next()
			if ok {
				if tok.Kind == rtjson.TokenEndOfDocument {
					break
				}
				continue
			}
			if s.Err() != nil {
				break
			}
			if ended {
				break
			}
			s.End()
			ended = true
		}
	})
	if n != 0 {
		t.Fatalf("AllocsPerRun = %v want 0", n)
	}
}

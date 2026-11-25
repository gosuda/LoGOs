// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package json

// AppendTokens appends the payloads of one value's parts and enforces max.
// Parts are PartialNumber followed by Number, or PartialString pieces
// followed by String. Any other kind is a syntax error. A negative max
// selects DefaultMaxValueLen.
func AppendTokens(dst []byte, toks []Token, max int) ([]byte, error) {
	if max < 0 {
		max = DefaultMaxValueLen
	}
	num := false
	str := false
	for _, t := range toks {
		switch t.Kind {
		case TokenNumber, TokenPartialNumber:
			num = true
		case TokenString, TokenPartialString:
			str = true
		default:
			return dst, ErrSyntax
		}
	}
	if num && str {
		return dst, ErrSyntax
	}
	n := 0
	for _, t := range toks {
		if n+len(t.Bytes) > max {
			return dst, ErrValueTooLong
		}
		n += len(t.Bytes)
		dst = append(dst, t.Bytes...)
	}
	return dst, nil
}

// Join concatenates one value's parts and enforces max.
func Join(toks []Token, max int) ([]byte, error) {
	return AppendTokens(nil, toks, max)
}

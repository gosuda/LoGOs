// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package json

// AppendTokens appends the payloads of one value's parts and enforces max.
// Parts are PartialNumber followed by Number, or PartialString pieces
// followed by String. Any other kind is a syntax error. A negative max
// selects DefaultMaxValueLen. A value closes exactly once: only the last
// token may be a final kind, every earlier token must be the matching
// partial kind, and a final Number carries at least one digit. Partial
// input with no closing token, two closing tokens, or a digit-free
// closing number are all syntax errors.
func AppendTokens(dst []byte, toks []Token, max int) ([]byte, error) {
	if max < 0 {
		max = DefaultMaxValueLen
	}
	if len(toks) == 0 {
		return dst, nil
	}
	last := toks[len(toks)-1]
	var partial TokenKind
	switch last.Kind {
	case TokenNumber:
		partial = TokenPartialNumber
		if len(last.Bytes) == 0 {
			return dst, ErrSyntax
		}
	case TokenString:
		partial = TokenPartialString
	default:
		return dst, ErrSyntax
	}
	for _, t := range toks[:len(toks)-1] {
		if t.Kind != partial {
			return dst, ErrSyntax
		}
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

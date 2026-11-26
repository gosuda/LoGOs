// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package json

// Parts are PartialNumber followed by Number, or PartialString pieces
// followed by String. Any other kind is a syntax error. A negative max
// selects DefaultMaxValueLen. A value closes exactly once: only the last
// token may be a final kind and every earlier token must be the matching
// partial kind. A final Number may be empty only when partials contributed
// the payload: a chunk split at a value end emits PartialNumber pieces
// followed by an empty Number for the delimiter or EOF boundary. Partial
// input with no closing token, two closing tokens, a lone empty number,
// or a digit-free joined number are all syntax errors.
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
	if last.Kind == TokenNumber {
		total := len(last.Bytes)
		for _, t := range toks[:len(toks)-1] {
			total += len(t.Bytes)
		}
		if total == 0 {
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

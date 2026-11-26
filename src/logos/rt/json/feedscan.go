// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package json

import "errors"

// TokenKind names the kind of a Token.
type TokenKind int

// Token kinds emitted by FeedScanner.
const (
	TokenInvalid TokenKind = iota
	TokenObjectBegin
	TokenObjectEnd
	TokenArrayBegin
	TokenArrayEnd
	TokenTrue
	TokenFalse
	TokenNull
	TokenNumber
	TokenPartialNumber
	TokenString
	TokenPartialString
	TokenEndOfDocument
)

// String returns the kind name.
func (k TokenKind) String() string {
	switch k {
	case TokenObjectBegin:
		return "ObjectBegin"
	case TokenObjectEnd:
		return "ObjectEnd"
	case TokenArrayBegin:
		return "ArrayBegin"
	case TokenArrayEnd:
		return "ArrayEnd"
	case TokenTrue:
		return "True"
	case TokenFalse:
		return "False"
	case TokenNull:
		return "Null"
	case TokenNumber:
		return "Number"
	case TokenPartialNumber:
		return "PartialNumber"
	case TokenString:
		return "String"
	case TokenPartialString:
		return "PartialString"
	case TokenEndOfDocument:
		return "EndOfDocument"
	default:
		return "Invalid"
	}
}

// Token is one scanner event. Bytes borrows the fed chunk for Number,
// PartialNumber, String and PartialString plain pieces. Decoded escape
// bytes borrow scanner memory valid until the next Next or Feed call.
// Other kinds leave Bytes nil.
type Token struct {
	Kind  TokenKind
	Bytes []byte
}

// Error values reported through Err.
var (
	ErrSyntax        = errors.New("json: syntax error")
	ErrUnexpectedEnd = errors.New("json: unexpected end of input")
	ErrValueTooLong  = errors.New("json: value too long")
)

// DefaultMaxValueLen bounds one assembled string or number value.
const DefaultMaxValueLen = 4 * 1024 * 1024

// MaxDepth bounds object and array nesting.
const MaxDepth = 10000

const (
	stValue uint8 = iota
	stPostValue
	stObjectStart
	stObjectComma
	stArrayStart
	stNumMinus
	stNumZero
	stNumInt
	stNumDot
	stNumFrac
	stNumE
	stNumESign
	stNumExp
	stStr
	stStrEsc
	stStrU
	stStrU1
	stStrU2
	stStrU3
	stStrSur
	stStrSurBS
	stStrSurU
	stStrSurU1
	stStrSurU2
	stStrSurU3
	stStrUTF
	stLitT
	stLitTR
	stLitTRU
	stLitF
	stLitFA
	stLitFAL
	stLitFALS
	stLitN
	stLitNU
	stLitNUL
)

// FeedScanner reads one JSON document from fed chunks.
type FeedScanner struct {
	buf      []byte
	pos      int
	start    int
	state    uint8
	stack    []byte
	depth    int
	objKey   bool
	end      bool
	err      error
	cu0      uint16
	cu1      uint16
	utfFirst byte
	utfNeed  int
	utfGot   int
	esc      [6]byte
}

// Feed supplies the next input chunk. Call it only when the previous
// chunk is fully consumed, and never after End.
func (s *FeedScanner) Feed(p []byte) {
	if s.err != nil {
		return
	}
	if s.end {
		s.err = ErrSyntax
		return
	}
	if s.buf != nil && s.pos != len(s.buf) {
		s.err = ErrSyntax
		return
	}
	s.buf = p
	s.pos = 0
	s.start = 0
}

// End marks that no further chunk follows.
func (s *FeedScanner) End() {
	if s.err != nil {
		return
	}
	s.end = true
}

// Err reports the first syntax or truncation error.
func (s *FeedScanner) Err() error {
	return s.err
}

// Depth returns current object and array nesting.
func (s *FeedScanner) Depth() int {
	return s.depth
}

// Reset clears the scanner for another document. It keeps stack capacity.
func (s *FeedScanner) Reset() {
	*s = FeedScanner{stack: s.stack[:0]}
}

// Next returns the next token. False means need more input when Err is
// nil, or a fatal error otherwise.
func (s *FeedScanner) Next() (Token, bool) {
	if s.err != nil {
		return Token{}, false
	}
	for {
		t, ok, again := s.nextStep()
		if ok {
			return t, true
		}
		if !again {
			return Token{}, false
		}
	}
}

func (s *FeedScanner) nextStep() (Token, bool, bool) {
	switch {
	case s.state == stValue:
		return s.stepValue()
	case s.state == stPostValue:
		return s.stepPostValue()
	case s.state == stObjectStart:
		return s.stepObjectStart()
	case s.state == stObjectComma:
		return s.stepObjectComma()
	case s.state == stArrayStart:
		return s.stepArrayStart()
	case s.state >= stNumMinus && s.state <= stNumExp:
		return s.stepNumber()
	case s.state >= stStr && s.state <= stStrUTF:
		return s.stepString()
	default:
		return s.stepLiteral()
	}
}

func (s *FeedScanner) push(bit byte) {
	if s.depth >= MaxDepth {
		s.err = ErrSyntax
		return
	}
	idx := s.depth / 8
	if idx >= len(s.stack) {
		s.stack = append(s.stack, 0)
	}
	off := uint(s.depth % 8)
	if bit == 1 {
		s.stack[idx] |= 1 << off
	} else {
		s.stack[idx] &^= 1 << off
	}
	s.depth++
}

func (s *FeedScanner) pop() byte {
	s.depth--
	idx := s.depth / 8
	off := uint(s.depth % 8)
	return (s.stack[idx] >> off) & 1
}

func (s *FeedScanner) peek() byte {
	idx := (s.depth - 1) / 8
	off := uint((s.depth - 1) % 8)
	return (s.stack[idx] >> off) & 1
}

func (s *FeedScanner) skipSpace() {
	s.pos = skipSpace(s.buf, s.pos)
}

func (s *FeedScanner) stepValue() (Token, bool, bool) {
	s.skipSpace()
	if s.pos >= len(s.buf) {
		if s.end {
			s.err = ErrUnexpectedEnd
		}
		return Token{}, false, false
	}
	c := s.buf[s.pos]
	switch c {
	case '{':
		s.push(1)
		if s.err != nil {
			return Token{}, false, false
		}
		s.pos++
		s.start = s.pos
		s.state = stObjectStart
		return Token{Kind: TokenObjectBegin}, true, false
	case '[':
		s.push(0)
		if s.err != nil {
			return Token{}, false, false
		}
		s.pos++
		s.start = s.pos
		s.state = stArrayStart
		return Token{Kind: TokenArrayBegin}, true, false
	case '"':
		s.pos++
		s.start = s.pos
		s.objKey = false
		s.state = stStr
		return Token{}, false, true
	case 't', 'f', 'n':
		return s.valueLiteral(c)
	}
	return s.valueNumber(c)
}

func (s *FeedScanner) valueLiteral(c byte) (Token, bool, bool) {
	s.pos++
	switch c {
	case 't':
		s.state = stLitT
	case 'f':
		s.state = stLitF
	default:
		s.state = stLitN
	}
	return Token{}, false, true
}

func (s *FeedScanner) valueNumber(c byte) (Token, bool, bool) {
	switch {
	case c == '-':
		s.start = s.pos
		s.pos++
		s.state = stNumMinus
		return Token{}, false, true
	case c == '0':
		s.start = s.pos
		s.pos++
		s.state = stNumZero
		return Token{}, false, true
	case c >= '1' && c <= '9':
		s.start = s.pos
		s.pos++
		s.state = stNumInt
		return Token{}, false, true
	default:
		s.err = ErrSyntax
		return Token{}, false, false
	}
}
func (s *FeedScanner) stepPostValue() (Token, bool, bool) {
	s.skipSpace()
	if s.pos >= len(s.buf) {
		if s.end {
			if s.depth == 0 {
				return Token{Kind: TokenEndOfDocument}, true, false
			}
			s.err = ErrUnexpectedEnd
		}
		return Token{}, false, false
	}
	c := s.buf[s.pos]
	if s.objKey {
		if c != ':' {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		s.pos++
		s.start = s.pos
		s.objKey = false
		s.state = stValue
		return Token{}, false, true
	}
	if s.depth == 0 {
		s.err = ErrSyntax
		return Token{}, false, false
	}
	return s.postPunct(c)
}

func (s *FeedScanner) postPunct(c byte) (Token, bool, bool) {
	switch c {
	case '}':
		if s.peek() != 1 {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		s.pop()
		s.pos++
		s.start = s.pos
		s.state = stPostValue
		return Token{Kind: TokenObjectEnd}, true, false
	case ']':
		if s.peek() != 0 {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		s.pop()
		s.pos++
		s.start = s.pos
		s.state = stPostValue
		return Token{Kind: TokenArrayEnd}, true, false
	case ',':
		top := s.peek()
		s.pos++
		s.start = s.pos
		if top == 1 {
			s.state = stObjectComma
		} else {
			s.state = stValue
		}
		return Token{}, false, true
	default:
		s.err = ErrSyntax
		return Token{}, false, false
	}
}

func (s *FeedScanner) stepObjectStart() (Token, bool, bool) {
	s.skipSpace()
	if s.pos >= len(s.buf) {
		if s.end {
			s.err = ErrUnexpectedEnd
		}
		return Token{}, false, false
	}
	c := s.buf[s.pos]
	if c == '"' {
		s.pos++
		s.start = s.pos
		s.objKey = true
		s.state = stStr
		return Token{}, false, true
	}
	if c == '}' {
		if s.depth == 0 || s.peek() != 1 {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		s.pop()
		s.pos++
		s.start = s.pos
		s.state = stPostValue
		return Token{Kind: TokenObjectEnd}, true, false
	}
	s.err = ErrSyntax
	return Token{}, false, false
}

func (s *FeedScanner) stepObjectComma() (Token, bool, bool) {
	s.skipSpace()
	if s.pos >= len(s.buf) {
		if s.end {
			s.err = ErrUnexpectedEnd
		}
		return Token{}, false, false
	}
	if s.buf[s.pos] != '"' {
		s.err = ErrSyntax
		return Token{}, false, false
	}
	s.pos++
	s.start = s.pos
	s.objKey = true
	s.state = stStr
	return Token{}, false, true
}

func (s *FeedScanner) stepArrayStart() (Token, bool, bool) {
	s.skipSpace()
	if s.pos >= len(s.buf) {
		if s.end {
			s.err = ErrUnexpectedEnd
		}
		return Token{}, false, false
	}
	if s.buf[s.pos] == ']' {
		if s.depth == 0 || s.peek() != 0 {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		s.pop()
		s.pos++
		s.start = s.pos
		s.state = stPostValue
		return Token{Kind: TokenArrayEnd}, true, false
	}
	s.state = stValue
	return Token{}, false, true
}

func (s *FeedScanner) stepLiteral() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		if s.end {
			s.err = ErrUnexpectedEnd
		}
		return Token{}, false, false
	}
	switch {
	case s.state >= stLitT && s.state <= stLitTRU:
		return s.litTrue()
	case s.state >= stLitF && s.state <= stLitFALS:
		return s.litFalse()
	case s.state >= stLitN && s.state <= stLitNUL:
		return s.litNull()
	default:
		s.err = ErrSyntax
		return Token{}, false, false
	}
}

func (s *FeedScanner) litTrue() (Token, bool, bool) {
	c := s.buf[s.pos]
	switch s.state {
	case stLitT:
		if c != 'r' {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		s.pos++
		s.state = stLitTR
		return Token{}, false, true
	case stLitTR:
		if c != 'u' {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		s.pos++
		s.state = stLitTRU
		return Token{}, false, true
	case stLitTRU:
		if c != 'e' {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		s.pos++
		s.start = s.pos
		s.state = stPostValue
		return Token{Kind: TokenTrue}, true, false
	default:
		s.err = ErrSyntax
		return Token{}, false, false
	}
}

func (s *FeedScanner) litFalse() (Token, bool, bool) {
	c := s.buf[s.pos]
	switch s.state {
	case stLitF:
		if c != 'a' {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		s.pos++
		s.state = stLitFA
		return Token{}, false, true
	case stLitFA:
		if c != 'l' {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		s.pos++
		s.state = stLitFAL
		return Token{}, false, true
	case stLitFAL:
		if c != 's' {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		s.pos++
		s.state = stLitFALS
		return Token{}, false, true
	case stLitFALS:
		if c != 'e' {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		s.pos++
		s.start = s.pos
		s.state = stPostValue
		return Token{Kind: TokenFalse}, true, false
	default:
		s.err = ErrSyntax
		return Token{}, false, false
	}
}

func (s *FeedScanner) litNull() (Token, bool, bool) {
	c := s.buf[s.pos]
	switch s.state {
	case stLitN:
		if c != 'u' {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		s.pos++
		s.state = stLitNU
		return Token{}, false, true
	case stLitNU:
		if c != 'l' {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		s.pos++
		s.state = stLitNUL
		return Token{}, false, true
	case stLitNUL:
		if c != 'l' {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		s.pos++
		s.start = s.pos
		s.state = stPostValue
		return Token{Kind: TokenNull}, true, false
	default:
		s.err = ErrSyntax
		return Token{}, false, false
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package json

func (s *FeedScanner) stepNumber() (Token, bool, bool) {
	switch s.state {
	case stNumMinus:
		return s.numMinus()
	case stNumZero:
		return s.numZero()
	case stNumInt:
		return s.numInt()
	case stNumDot:
		return s.numDot()
	case stNumFrac:
		return s.numFrac()
	case stNumE:
		return s.numE()
	case stNumESign:
		return s.numESign()
	case stNumExp:
		return s.numExp()
	default:
		s.err = ErrSyntax
		return Token{}, false, false
	}
}

func (s *FeedScanner) numberAtEnd(allowEnd bool) (Token, bool, bool) {
	if s.end {
		if !allowEnd {
			s.err = ErrUnexpectedEnd
			return Token{}, false, false
		}
		b := s.buf[s.start:s.pos]
		s.start = s.pos
		s.state = stPostValue
		return Token{Kind: TokenNumber, Bytes: b}, true, false
	}
	b := s.buf[s.start:s.pos]
	if len(b) == 0 {
		return Token{}, false, false
	}
	s.start = s.pos
	return Token{Kind: TokenPartialNumber, Bytes: b}, true, false
}

func (s *FeedScanner) numMinus() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.numberAtEnd(false)
	}
	c := s.buf[s.pos]
	if c == '0' {
		s.pos++
		s.state = stNumZero
		return Token{}, false, true
	}
	if c >= '1' && c <= '9' {
		s.pos++
		s.state = stNumInt
		return Token{}, false, true
	}
	s.err = ErrSyntax
	return Token{}, false, false
}

func (s *FeedScanner) numZero() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.numberAtEnd(true)
	}
	c := s.buf[s.pos]
	if c == '.' {
		s.pos++
		s.state = stNumDot
		return Token{}, false, true
	}
	if c == 'e' || c == 'E' {
		s.pos++
		s.state = stNumE
		return Token{}, false, true
	}
	b := s.buf[s.start:s.pos]
	s.start = s.pos
	s.state = stPostValue
	return Token{Kind: TokenNumber, Bytes: b}, true, false
}

func (s *FeedScanner) numInt() (Token, bool, bool) {
	for s.pos < len(s.buf) {
		c := s.buf[s.pos]
		if isDigit(c) {
			s.pos++
			continue
		}
		if c == '.' {
			s.pos++
			s.state = stNumDot
			return Token{}, false, true
		}
		if c == 'e' || c == 'E' {
			s.pos++
			s.state = stNumE
			return Token{}, false, true
		}
		b := s.buf[s.start:s.pos]
		s.start = s.pos
		s.state = stPostValue
		return Token{Kind: TokenNumber, Bytes: b}, true, false
	}
	return s.numberAtEnd(true)
}

func (s *FeedScanner) numDot() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.numberAtEnd(false)
	}
	c := s.buf[s.pos]
	if !isDigit(c) {
		s.err = ErrSyntax
		return Token{}, false, false
	}
	s.pos++
	s.state = stNumFrac
	return Token{}, false, true
}

func (s *FeedScanner) numFrac() (Token, bool, bool) {
	for s.pos < len(s.buf) {
		c := s.buf[s.pos]
		if isDigit(c) {
			s.pos++
			continue
		}
		if c == 'e' || c == 'E' {
			s.pos++
			s.state = stNumE
			return Token{}, false, true
		}
		b := s.buf[s.start:s.pos]
		s.start = s.pos
		s.state = stPostValue
		return Token{Kind: TokenNumber, Bytes: b}, true, false
	}
	return s.numberAtEnd(true)
}

func (s *FeedScanner) numE() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.numberAtEnd(false)
	}
	c := s.buf[s.pos]
	if isDigit(c) {
		s.pos++
		s.state = stNumExp
		return Token{}, false, true
	}
	if c == '+' || c == '-' {
		s.pos++
		s.state = stNumESign
		return Token{}, false, true
	}
	s.err = ErrSyntax
	return Token{}, false, false
}

func (s *FeedScanner) numESign() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.numberAtEnd(false)
	}
	c := s.buf[s.pos]
	if !isDigit(c) {
		s.err = ErrSyntax
		return Token{}, false, false
	}
	s.pos++
	s.state = stNumExp
	return Token{}, false, true
}

func (s *FeedScanner) numExp() (Token, bool, bool) {
	for s.pos < len(s.buf) {
		c := s.buf[s.pos]
		if isDigit(c) {
			s.pos++
			continue
		}
		b := s.buf[s.start:s.pos]
		s.start = s.pos
		s.state = stPostValue
		return Token{Kind: TokenNumber, Bytes: b}, true, false
	}
	return s.numberAtEnd(true)
}

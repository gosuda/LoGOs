// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package json

import "unicode/utf8"

func (s *FeedScanner) stepString() (Token, bool, bool) {
	switch s.state {
	case stStr:
		return s.strPlain()
	case stStrEsc:
		return s.strEsc()
	case stStrU:
		return s.strU()
	case stStrU1:
		return s.strU1()
	case stStrU2:
		return s.strU2()
	case stStrU3:
		return s.strU3()
	case stStrSur:
		return s.strSur()
	case stStrSurBS:
		return s.strSurBS()
	case stStrSurU:
		return s.strSurU()
	case stStrSurU1:
		return s.strSurU1()
	case stStrSurU2:
		return s.strSurU2()
	case stStrSurU3:
		return s.strSurU3()
	case stStrUTF:
		return s.strUTF()
	default:
		s.err = ErrSyntax
		return Token{}, false, false
	}
}

func isHigh(u uint16) bool {
	return u >= 0xD800 && u <= 0xDBFF
}

func isLow(u uint16) bool {
	return u >= 0xDC00 && u <= 0xDFFF
}

func validCont(first byte, got int, c byte) bool {
	if c < 0x80 || c > 0xBF {
		return false
	}
	switch {
	case first == 0xE0:
		return got != 0 || c >= 0xA0
	case first == 0xED:
		return got != 0 || c <= 0x9F
	case first == 0xF0:
		return got != 0 || c >= 0x90
	case first == 0xF4:
		return got != 0 || c <= 0x8F
	default:
		return true
	}
}

func trailingFor(state uint8) int {
	switch state {
	case stStrEsc:
		return 1
	case stStrU:
		return 2
	case stStrU1:
		return 3
	case stStrU2:
		return 4
	case stStrU3:
		return 5
	case stStrSur:
		return 6
	case stStrSurBS:
		return 7
	case stStrSurU:
		return 8
	case stStrSurU1:
		return 9
	case stStrSurU2:
		return 10
	case stStrSurU3:
		return 11
	default:
		return 0
	}
}

func (s *FeedScanner) stringNeedMore(trailing int) (Token, bool, bool) {
	if s.end {
		s.err = ErrUnexpectedEnd
		return Token{}, false, false
	}
	end := s.pos - trailing
	if end < s.start {
		end = s.start
	}
	b := s.buf[s.start:end]
	s.start = s.pos
	if len(b) == 0 {
		return Token{}, false, false
	}
	return Token{Kind: TokenPartialString, Bytes: b}, true, false
}

func (s *FeedScanner) enterUTF(c byte) bool {
	switch {
	case c >= 0xC2 && c <= 0xDF:
		s.utfFirst = c
		s.utfNeed = 1
		s.utfGot = 0
	case c == 0xE0:
		s.utfFirst = c
		s.utfNeed = 2
		s.utfGot = 0
	case c >= 0xE1 && c <= 0xEC:
		s.utfFirst = c
		s.utfNeed = 2
		s.utfGot = 0
	case c >= 0xEE && c <= 0xEF:
		s.utfFirst = c
		s.utfNeed = 2
		s.utfGot = 0
	case c == 0xED:
		s.utfFirst = c
		s.utfNeed = 2
		s.utfGot = 0
	case c == 0xF0:
		s.utfFirst = c
		s.utfNeed = 3
		s.utfGot = 0
	case c >= 0xF1 && c <= 0xF3:
		s.utfFirst = c
		s.utfNeed = 3
		s.utfGot = 0
	case c == 0xF4:
		s.utfFirst = c
		s.utfNeed = 3
		s.utfGot = 0
	default:
		s.err = ErrSyntax
		return false
	}
	s.pos++
	s.state = stStrUTF
	return true
}

func (s *FeedScanner) strPlain() (Token, bool, bool) {
	for s.pos < len(s.buf) {
		c := s.buf[s.pos]
		if c == '"' {
			b := s.buf[s.start:s.pos]
			s.pos++
			s.start = s.pos
			s.state = stPostValue
			return Token{Kind: TokenString, Bytes: b}, true, false
		}
		if c == '\\' {
			b := s.buf[s.start:s.pos]
			old := s.pos
			s.start = old
			s.pos = old + 1
			s.state = stStrEsc
			if len(b) == 0 {
				return Token{}, false, true
			}
			return Token{Kind: TokenPartialString, Bytes: b}, true, false
		}
		if c < 0x20 {
			s.err = ErrSyntax
			return Token{}, false, false
		}
		if c < 0x80 {
			s.pos++
			continue
		}
		if !s.enterUTF(c) {
			return Token{}, false, false
		}
		return Token{}, false, true
	}
	if s.end {
		s.err = ErrUnexpectedEnd
		return Token{}, false, false
	}
	b := s.buf[s.start:s.pos]
	if len(b) == 0 {
		return Token{}, false, false
	}
	s.start = s.pos
	return Token{Kind: TokenPartialString, Bytes: b}, true, false
}

func (s *FeedScanner) strUTF() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.stringNeedMore(0)
	}
	c := s.buf[s.pos]
	if !validCont(s.utfFirst, s.utfGot, c) {
		s.err = ErrSyntax
		return Token{}, false, false
	}
	s.pos++
	s.utfGot++
	if s.utfGot >= s.utfNeed {
		s.state = stStr
	}
	return Token{}, false, true
}

func (s *FeedScanner) strEsc() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.stringNeedMore(1)
	}
	c := s.buf[s.pos]
	switch c {
	case '"', '\\', '/':
		s.start = s.pos
		s.pos++
		s.state = stStr
		return Token{}, false, true
	case 'b':
		s.esc[0] = 0x08
		s.pos++
		s.start = s.pos
		s.state = stStr
		return Token{Kind: TokenPartialString, Bytes: s.esc[:1]}, true, false
	case 'f':
		s.esc[0] = 0x0C
		s.pos++
		s.start = s.pos
		s.state = stStr
		return Token{Kind: TokenPartialString, Bytes: s.esc[:1]}, true, false
	case 'n':
		s.esc[0] = '\n'
		s.pos++
		s.start = s.pos
		s.state = stStr
		return Token{Kind: TokenPartialString, Bytes: s.esc[:1]}, true, false
	case 'r':
		s.esc[0] = '\r'
		s.pos++
		s.start = s.pos
		s.state = stStr
		return Token{Kind: TokenPartialString, Bytes: s.esc[:1]}, true, false
	case 't':
		s.esc[0] = '\t'
		s.pos++
		s.start = s.pos
		s.state = stStr
		return Token{Kind: TokenPartialString, Bytes: s.esc[:1]}, true, false
	case 'u':
		s.pos++
		s.cu0 = 0
		s.state = stStrU
		return Token{}, false, true
	default:
		s.err = ErrSyntax
		return Token{}, false, false
	}
}

func (s *FeedScanner) strU() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.stringNeedMore(2)
	}
	v := hexVal(s.buf[s.pos])
	if v < 0 {
		s.err = ErrSyntax
		return Token{}, false, false
	}
	s.cu0 = uint16(v) << 12
	s.pos++
	s.state = stStrU1
	return Token{}, false, true
}

func (s *FeedScanner) strU1() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.stringNeedMore(3)
	}
	v := hexVal(s.buf[s.pos])
	if v < 0 {
		s.err = ErrSyntax
		return Token{}, false, false
	}
	s.cu0 |= uint16(v) << 8
	s.pos++
	s.state = stStrU2
	return Token{}, false, true
}

func (s *FeedScanner) strU2() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.stringNeedMore(4)
	}
	v := hexVal(s.buf[s.pos])
	if v < 0 {
		s.err = ErrSyntax
		return Token{}, false, false
	}
	s.cu0 |= uint16(v) << 4
	s.pos++
	s.state = stStrU3
	return Token{}, false, true
}

func (s *FeedScanner) strU3() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.stringNeedMore(5)
	}
	v := hexVal(s.buf[s.pos])
	if v < 0 {
		s.err = ErrSyntax
		return Token{}, false, false
	}
	s.cu0 |= uint16(v)
	s.pos++
	if isHigh(s.cu0) {
		s.state = stStrSur
		return Token{}, false, true
	}
	if isLow(s.cu0) {
		n := utf8.EncodeRune(s.esc[:], utf8.RuneError)
		s.start = s.pos
		s.state = stStr
		return Token{Kind: TokenPartialString, Bytes: s.esc[:n]}, true, false
	}
	n := utf8.EncodeRune(s.esc[:], rune(s.cu0))
	s.start = s.pos
	s.state = stStr
	return Token{Kind: TokenPartialString, Bytes: s.esc[:n]}, true, false
}

func (s *FeedScanner) strSur() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		if s.end {
			n := utf8.EncodeRune(s.esc[:], utf8.RuneError)
			s.start = s.pos
			s.state = stStr
			return Token{Kind: TokenPartialString, Bytes: s.esc[:n]}, true, false
		}
		return s.stringNeedMore(6)
	}
	if s.buf[s.pos] != '\\' {
		n := utf8.EncodeRune(s.esc[:], utf8.RuneError)
		s.start = s.pos
		s.state = stStr
		return Token{Kind: TokenPartialString, Bytes: s.esc[:n]}, true, false
	}
	s.pos++
	s.state = stStrSurBS
	return Token{}, false, true
}
func (s *FeedScanner) strSurBS() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.stringNeedMore(7)
	}
	if s.buf[s.pos] != 'u' {
		n := utf8.EncodeRune(s.esc[:], utf8.RuneError)
		s.start = s.pos
		s.state = stStrEsc
		return Token{Kind: TokenPartialString, Bytes: s.esc[:n]}, true, false
	}
	s.pos++
	s.cu1 = 0
	s.state = stStrSurU
	return Token{}, false, true
}

func (s *FeedScanner) strSurU() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.stringNeedMore(8)
	}
	v := hexVal(s.buf[s.pos])
	if v < 0 {
		s.err = ErrSyntax
		return Token{}, false, false
	}
	s.cu1 = uint16(v) << 12
	s.pos++
	s.state = stStrSurU1
	return Token{}, false, true
}

func (s *FeedScanner) strSurU1() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.stringNeedMore(9)
	}
	v := hexVal(s.buf[s.pos])
	if v < 0 {
		s.err = ErrSyntax
		return Token{}, false, false
	}
	s.cu1 |= uint16(v) << 8
	s.pos++
	s.state = stStrSurU2
	return Token{}, false, true
}

func (s *FeedScanner) strSurU2() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.stringNeedMore(10)
	}
	v := hexVal(s.buf[s.pos])
	if v < 0 {
		s.err = ErrSyntax
		return Token{}, false, false
	}
	s.cu1 |= uint16(v) << 4
	s.pos++
	s.state = stStrSurU3
	return Token{}, false, true
}

func (s *FeedScanner) strSurU3() (Token, bool, bool) {
	if s.pos >= len(s.buf) {
		return s.stringNeedMore(11)
	}
	v := hexVal(s.buf[s.pos])
	if v < 0 {
		s.err = ErrSyntax
		return Token{}, false, false
	}
	s.cu1 |= uint16(v)
	s.pos++
	if isLow(s.cu1) {
		r := rune(0x10000) + rune(s.cu0-0xD800)<<10 + rune(s.cu1-0xDC00)
		n := utf8.EncodeRune(s.esc[:], r)
		s.start = s.pos
		s.state = stStr
		return Token{Kind: TokenPartialString, Bytes: s.esc[:n]}, true, false
	}
	n := utf8.EncodeRune(s.esc[:], utf8.RuneError)
	if isHigh(s.cu1) {
		s.cu0 = s.cu1
		s.start = s.pos
		s.state = stStrSur
		return Token{Kind: TokenPartialString, Bytes: s.esc[:n]}, true, false
	}
	n += utf8.EncodeRune(s.esc[n:], rune(s.cu1))
	s.start = s.pos
	s.state = stStr
	return Token{Kind: TokenPartialString, Bytes: s.esc[:n]}, true, false
}

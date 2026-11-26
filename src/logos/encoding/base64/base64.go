// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package base64 provides base64 codecs as values with exact sizing.
//
// An Encoding is a codec value holding an alphabet and a padding rule.
// Std uses the padded standard alphabet of RFC 4648 section 4. URL uses
// the padded URL-safe alphabet of section 5. RawStd uses the unpadded
// standard alphabet of section 3.2. RawURL uses the unpadded URL-safe
// alphabet of sections 5 and 3.2. The zero value is Std.
//
// Encode writes into a caller buffer sized by EncodedLen and Decode
// writes into a caller buffer sized by DecodedLen. Both success paths
// allocate nothing. Decode is always strict under RFC 4648 section 3.5:
// non-alphabet bytes, misplaced or missing padding, short quanta,
// non-zero trailing pad bits and bytes after padding are errors.
// Carriage return and line feed are ignored.
//
// The codec shape follows the Codecs type of Zig lib/std/base64.zig at
// revision c7826aa7a0 with padding as a codec property. Behavior matches
// RFC 4648 section 10 and encoding/base64 in strict mode.
package base64

import "strconv"

// CorruptInputError reports the raw input offset of the first illegal byte.
type CorruptInputError int64

// Error reports the failure offset with the package prefix.
func (e CorruptInputError) Error() string {
	return "base64: illegal base64 data at input byte " + strconv.FormatInt(int64(e), 10)
}

// Encoding is a base64 codec value: an alphabet plus a padding rule.
// The zero value is the padded standard codec Std.
// Encoding values hold no mutable state and are safe for concurrent use.
type Encoding struct {
	url bool
	raw bool
}

// Std is the padded standard codec of RFC 4648 section 4.
var Std = Encoding{}

// URL is the padded URL-safe codec of RFC 4648 section 5.
var URL = Encoding{url: true}

// RawStd is the unpadded standard codec of RFC 4648 section 3.2.
var RawStd = Encoding{raw: true}

// RawURL is the unpadded URL-safe codec of RFC 4648 sections 5 and 3.2.
var RawURL = Encoding{url: true, raw: true}

// EncodedLen reports the exact encoded length of n source bytes.
func (e Encoding) EncodedLen(n int) int {
	if e.raw {
		return n/3*4 + (n%3*8+5)/6
	}
	return (n + 2) / 3 * 4
}

// DecodedLen reports the maximum decoded length of n encoded bytes.
// The actual length is smaller when padded input carries padding.
func (e Encoding) DecodedLen(n int) int {
	if e.raw {
		return n/4*3 + n%4*6/8
	}
	return n / 4 * 3
}

// Encode writes the encoding of src into dst and returns the used prefix.
// Encode panics when dst is shorter than EncodedLen(len(src)).
func (e Encoding) Encode(dst, src []byte) []byte {
	out := e.EncodedLen(len(src))
	if len(dst) < out {
		panic("base64: short destination buffer")
	}
	i := 0
	j := 0
	for len(src)-i >= 3 {
		w := uint32(src[i])<<16 | uint32(src[i+1])<<8 | uint32(src[i+2])
		dst[j] = e.charAt(byte(w >> 18))
		dst[j+1] = e.charAt(byte(w>>12) & 0x3f)
		dst[j+2] = e.charAt(byte(w>>6) & 0x3f)
		dst[j+3] = e.charAt(byte(w) & 0x3f)
		i += 3
		j += 4
	}
	switch len(src) - i {
	case 1:
		dst[j] = e.charAt(src[i] >> 2)
		dst[j+1] = e.charAt((src[i] & 0x03) << 4)
		if !e.raw {
			dst[j+2] = '='
			dst[j+3] = '='
		}
	case 2:
		dst[j] = e.charAt(src[i] >> 2)
		dst[j+1] = e.charAt((src[i]&0x03)<<4 | src[i+1]>>4)
		dst[j+2] = e.charAt((src[i+1] & 0x0f) << 2)
		if !e.raw {
			dst[j+3] = '='
		}
	}
	return dst[:out]
}

// Decode decodes src into dst and reports the bytes written.
// Decode rejects non-alphabet bytes, misplaced or missing padding, short
// quanta, non-zero trailing pad bits and bytes after padding. Carriage
// return and line feed are ignored. Decode panics when dst is too short;
// size dst with DecodedLen.
func (e Encoding) Decode(dst, src []byte) (int, error) {
	n := 0
	i := 0
	for {
		v, sig, pads, padAt, next, err := e.gather(src, i)
		if err != nil {
			return n, err
		}
		if sig+pads == 0 {
			return n, nil
		}
		if sig+pads < 4 {
			return e.finishShort(dst, n, src, v, sig)
		}
		done, m, err := e.writeQuantum(dst, n, v, sig, pads, padAt)
		n = m
		if err != nil {
			return n, err
		}
		if done {
			return checkTail(n, src, next)
		}
		i = next
	}
}

// EncodeToString returns the encoding of src. It allocates for the output.
func (e Encoding) EncodeToString(src []byte) string {
	dst := make([]byte, e.EncodedLen(len(src)))
	return string(e.Encode(dst, src))
}

// DecodeString decodes s and returns the decoded bytes. It allocates for
// the output. It rejects the same inputs Decode rejects.
func (e Encoding) DecodeString(s string) ([]byte, error) {
	dst := make([]byte, e.DecodedLen(len(s)))
	n, err := e.Decode(dst, []byte(s))
	return dst[:n], err
}

func (e Encoding) charAt(v byte) byte {
	switch {
	case v < 26:
		return 'A' + v
	case v < 52:
		return 'a' + v - 26
	case v < 62:
		return '0' + v - 52
	case v == 62:
		if e.url {
			return '-'
		}
		return '+'
	default:
		if e.url {
			return '_'
		}
		return '/'
	}
}

func (e Encoding) value(c byte) (byte, bool) {
	switch {
	case c >= 'A' && c <= 'Z':
		return c - 'A', true
	case c >= 'a' && c <= 'z':
		return c - 'a' + 26, true
	case c >= '0' && c <= '9':
		return c - '0' + 52, true
	case c == '+' && !e.url:
		return 62, true
	case c == '/' && !e.url:
		return 63, true
	case c == '-' && e.url:
		return 62, true
	case c == '_' && e.url:
		return 63, true
	}
	return 0, false
}

func (e Encoding) gather(src []byte, i int) ([4]byte, int, int, int, int, error) {
	var v [4]byte
	sig := 0
	pads := 0
	padAt := -1
	for sig+pads < 4 && i < len(src) {
		c := src[i]
		if c == '\r' || c == '\n' {
			i++
			continue
		}
		if c == '=' {
			if e.raw {
				return v, 0, 0, 0, 0, CorruptInputError(i)
			}
			if padAt < 0 {
				padAt = i
			}
			pads++
			i++
			continue
		}
		d, ok := e.value(c)
		if !ok {
			return v, 0, 0, 0, 0, CorruptInputError(i)
		}
		if pads > 0 {
			return v, 0, 0, 0, 0, CorruptInputError(i)
		}
		v[sig] = d
		sig++
		i++
	}
	return v, sig, pads, padAt, i, nil
}

func (e Encoding) writeQuantum(dst []byte, n int, v [4]byte, sig, pads, padAt int) (bool, int, error) {
	switch pads {
	case 0:
		if len(dst)-n < 3 {
			panic("base64: short destination buffer")
		}
		dst[n] = v[0]<<2 | v[1]>>4
		dst[n+1] = v[1]<<4 | v[2]>>2
		dst[n+2] = v[2]<<6 | v[3]
		return false, n + 3, nil
	case 1:
		if sig != 3 {
			return true, n, CorruptInputError(padAt)
		}
		if v[2]&0x03 != 0 {
			return true, n, CorruptInputError(padAt)
		}
		if len(dst)-n < 2 {
			panic("base64: short destination buffer")
		}
		dst[n] = v[0]<<2 | v[1]>>4
		dst[n+1] = v[1]<<4 | v[2]>>2
		return true, n + 2, nil
	case 2:
		if sig != 2 {
			return true, n, CorruptInputError(padAt)
		}
		if v[1]&0x0f != 0 {
			return true, n, CorruptInputError(padAt)
		}
		if len(dst)-n < 1 {
			panic("base64: short destination buffer")
		}
		dst[n] = v[0]<<2 | v[1]>>4
		return true, n + 1, nil
	default:
		return true, n, CorruptInputError(padAt)
	}
}

func (e Encoding) finishShort(dst []byte, n int, src []byte, v [4]byte, sig int) (int, error) {
	if !e.raw {
		return n, CorruptInputError(len(src))
	}
	switch sig {
	case 2:
		if v[1]&0x0f != 0 {
			return n, CorruptInputError(len(src))
		}
		if len(dst)-n < 1 {
			panic("base64: short destination buffer")
		}
		dst[n] = v[0]<<2 | v[1]>>4
		return n + 1, nil
	case 3:
		if v[2]&0x03 != 0 {
			return n, CorruptInputError(len(src))
		}
		if len(dst)-n < 2 {
			panic("base64: short destination buffer")
		}
		dst[n] = v[0]<<2 | v[1]>>4
		dst[n+1] = v[1]<<4 | v[2]>>2
		return n + 2, nil
	default:
		return n, CorruptInputError(len(src))
	}
}

func checkTail(n int, src []byte, i int) (int, error) {
	for i < len(src) {
		if src[i] != '\r' && src[i] != '\n' {
			return n, CorruptInputError(i)
		}
		i++
	}
	return n, nil
}

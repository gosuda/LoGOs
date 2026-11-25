// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"encoding/binary"
	"errors"
	"io"
)

func readH3Varint(r io.ByteReader) (uint64, error) {
	first, err := r.ReadByte()
	if err != nil {
		return 0, err
	}
	width := 1 << (first >> 6)
	value := uint64(first & 0x3f)
	for range width - 1 {
		octet, err := r.ReadByte()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, err
		}
		value = value<<8 | uint64(octet)
	}
	return value, nil
}

func parseH3Varint(b []byte) (uint64, int, error) {
	if len(b) == 0 {
		return 0, 0, io.EOF
	}
	first := b[0]
	width := 1 << (first >> 6)
	if len(b) < width {
		return 0, 0, io.ErrUnexpectedEOF
	}
	value := uint64(first & 0x3f)
	for _, octet := range b[1:width] {
		value = value<<8 | uint64(octet)
	}
	return value, width, nil
}

func appendH3Varint(dst []byte, value uint64) []byte {
	switch {
	case value < 1<<6:
		return append(dst, byte(value))
	case value < 1<<14:
		return binary.BigEndian.AppendUint16(dst, uint16(value)|1<<14)
	case value < 1<<30:
		return binary.BigEndian.AppendUint32(dst, uint32(value)|2<<30)
	case value < 1<<62:
		return binary.BigEndian.AppendUint64(dst, value|3<<62)
	default:
		panic("http3: QUIC varint exceeds 62 bits")
	}
}

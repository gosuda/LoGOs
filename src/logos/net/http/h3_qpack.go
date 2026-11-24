// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	"errors"
	"fmt"
	"io"
	"unsafe"
)

const (
	h3QPACKMaxInteger    = 1<<62 - 1
	h3QPACKFieldOverhead = 32
)

var (
	errH3QPACK                = errors.New("http3: QPACK decompression failed")
	errH3QPACKTruncated       = fmt.Errorf("%w: %w", errH3QPACK, io.ErrUnexpectedEOF)
	errH3FieldSectionTooLarge = errors.New("http3: field section too large")
)

type h3Field struct {
	name, value string
	sensitive   bool
}

func decodeH3Fields(dst []h3Field, block []byte, maxFieldSectionSize uint64) ([]h3Field, error) {
	required, block, err := consumeH3QPACKInt(block, 0xff)
	if err != nil {
		return dst, err
	}
	if required != 0 {
		return dst, errH3QPACK
	}
	if len(block) != 0 && block[0]&0x80 != 0 {
		return dst, errH3QPACK
	}
	_, block, err = consumeH3QPACKInt(block, 0x7f)
	if err != nil {
		return dst, err
	}

	start := len(dst)
	remaining := maxFieldSectionSize
	for len(block) != 0 {
		if remaining < h3QPACKFieldOverhead {
			clear(dst[start:])
			return dst[:start], errH3FieldSectionTooLarge
		}
		remaining -= h3QPACKFieldOverhead
		field, rest, err := consumeH3QPACKField(block, remaining)
		if err != nil {
			clear(dst[start:])
			return dst[:start], err
		}
		remaining -= uint64(len(field.name)) + uint64(len(field.value))
		dst = append(dst, field)
		block = rest
	}
	return dst, nil
}

func consumeH3QPACKField(block []byte, remaining uint64) (h3Field, []byte, error) {
	var field h3Field
	var name h3QPACKString
	var err error
	first := block[0]
	switch {
	case first&0xc0 == 0xc0:
		field, block, err = consumeH3QPACKStatic(block, 0x3f)
		if err == nil && uint64(len(field.name))+uint64(len(field.value)) > remaining {
			err = errH3FieldSectionTooLarge
		}
		return field, block, err
	case first&0xd0 == 0x50:
		field, block, err = consumeH3QPACKStatic(block, 0x0f)
		name.raw, field.sensitive = field.name, first&0x20 != 0
	case first&0xe0 == 0x20:
		name, block, err = consumeH3QPACKString(block, 0x07)
		field.sensitive = first&0x10 != 0
	default:
		return h3Field{}, nil, errH3QPACK
	}
	if err != nil {
		return h3Field{}, nil, err
	}
	value, block, err := consumeH3QPACKString(block, 0x7f)
	if err != nil {
		return h3Field{}, nil, err
	}
	nameSize, valueSize := uint64(len(name.raw)), uint64(len(value.raw))
	if nameSize > remaining || valueSize > remaining-nameSize {
		return h3Field{}, nil, errH3FieldSectionTooLarge
	}
	field.name, field.value = name.raw, value.raw
	if len(name.huffman) != 0 || len(value.huffman) != 0 {
		field, err = decodeH3QPACKHuffmanLiterals(field, name.huffman, value.huffman, remaining-nameSize-valueSize)
	}
	return field, block, err
}

func consumeH3QPACKStatic(block []byte, mask byte) (h3Field, []byte, error) {
	index, rest, err := consumeH3QPACKInt(block, mask)
	if err != nil {
		return h3Field{}, nil, err
	}
	if index >= uint64(len(h3QPACKStaticTable)) {
		return h3Field{}, nil, errH3QPACK
	}
	entry := h3QPACKStaticTable[index]
	return h3Field{name: entry[0], value: entry[1]}, rest, nil
}

type h3QPACKString struct {
	raw     string
	huffman []byte
}

func consumeH3QPACKString(block []byte, mask byte) (h3QPACKString, []byte, error) {
	size, rest, err := consumeH3QPACKInt(block, mask)
	if err != nil {
		return h3QPACKString{}, nil, err
	}
	if size > uint64(len(rest)) {
		return h3QPACKString{}, nil, errH3QPACKTruncated
	}
	encoded, rest := rest[:int(size)], rest[int(size):]
	if block[0]&(mask+1) != 0 {
		return h3QPACKString{huffman: encoded}, rest, nil
	}
	return h3QPACKString{raw: h3ImmutableString(encoded)}, rest, nil
}

func h3ImmutableString(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	// SAFETY: The caller owns b and must keep it immutable for the lifetime of any returned string.
	return unsafe.String(unsafe.SliceData(b), len(b))
}

func consumeH3QPACKInt(block []byte, mask byte) (uint64, []byte, error) {
	if len(block) == 0 {
		return 0, nil, errH3QPACKTruncated
	}
	value := uint64(block[0] & mask)
	block = block[1:]
	if value != uint64(mask) {
		return value, block, nil
	}
	for i, b := range block {
		shift := uint(i) * 7
		part := uint64(b & 0x7f)
		if part > (h3QPACKMaxInteger-value)>>shift || (i == 8 && b&0x80 != 0) {
			return 0, nil, errH3QPACK
		}
		value += part << shift
		if b&0x80 == 0 {
			return value, block[i+1:], nil
		}
	}
	return 0, nil, errH3QPACKTruncated
}

func appendH3Fields(dst []byte, fields []h3Field) ([]byte, error) {
	start := len(dst)
	dst = append(dst, 0, 0)
fieldsLoop:
	for _, field := range fields {
		if uint64(len(field.name)) > h3QPACKMaxInteger || uint64(len(field.value)) > h3QPACKMaxInteger {
			return dst[:start], errH3FieldSectionTooLarge
		}
		static := h3QPACKStaticNames[field.name]
		if !field.sensitive {
			for entry := static; entry != 0; entry = h3QPACKStaticNext[entry-1] {
				if h3QPACKStaticTable[entry-1][1] == field.value {
					dst = appendH3QPACKInt(dst, 0xc0, 0x3f, uint64(entry-1))
					continue fieldsLoop
				}
			}
		}
		dst = appendH3QPACKLiteral(dst, field, static)
	}
	return dst, nil
}

func appendH3QPACKLiteral(dst []byte, field h3Field, staticPlusOne byte) []byte {
	if staticPlusOne != 0 {
		prefix := byte(0x50)
		if field.sensitive {
			prefix |= 0x20
		}
		dst = appendH3QPACKInt(dst, prefix, 0x0f, uint64(staticPlusOne-1))
	} else {
		prefix := byte(0x20)
		if field.sensitive {
			prefix |= 0x10
		}
		dst = appendH3QPACKInt(dst, prefix, 0x07, uint64(len(field.name)))
		dst = append(dst, field.name...)
	}
	dst = appendH3QPACKInt(dst, 0, 0x7f, uint64(len(field.value)))
	return append(dst, field.value...)
}

func appendH3QPACKInt(dst []byte, flags, mask byte, value uint64) []byte {
	if value < uint64(mask) {
		return append(dst, flags|byte(value))
	}
	dst = append(dst, flags|mask)
	for value -= uint64(mask); value >= 0x80; value >>= 7 {
		dst = append(dst, byte(value&0x7f)|0x80)
	}
	return append(dst, byte(value))
}

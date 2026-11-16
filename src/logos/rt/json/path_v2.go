// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build goexperiment.jsonv2

package json

import (
	"bytes"
	"encoding"
	stdjson "encoding/json"
	"encoding/json/jsontext"
	"errors"
	"io"
	"reflect"
	"strconv"
	"strings"
)

const indexedFieldPaths = true

var stringContentReplacer = strings.NewReplacer(
	"object name", "object key",
	"at start of value", "looking for beginning of value",
	"at start of string", "looking for beginning of object key string",
	"after object value", "after object key:value pair",
	"in number", "in numeric literal",
)

func stringContentError(item string) error {
	_, err := jsontext.AppendUnquote(nil, []byte(item))
	if err == nil {
		return nil
	}
	msg := err.Error()
	if syn, ok := err.(*jsontext.SyntacticError); ok && syn.Err != nil {
		msg = syn.Err.Error()
	}
	if msg == io.ErrUnexpectedEOF.Error() {
		return errors.New("unexpected end of JSON input")
	}
	if before, _, found := strings.Cut(msg, " (expecting"); found && !strings.Contains(msg, " in literal") {
		msg = before
	}
	return errors.New(stringContentReplacer.Replace(msg))
}

func (s *Scanner) rejectQuotedBare(typ reflect.Type) {
	s.rejectKind(typ)
}

func (s *Scanner) rejectQuotedContent(res quotedResult, item string, typ reflect.Type) {
	switch typ.Kind() {
	case reflect.String:
		if contentErr := stringContentError(item); contentErr != nil {
			ute := s.typeError("string", typ)
			ute.Err = contentErr
			s.save(ute)
		} else {
			s.rejectKind(typ)
		}
	case reflect.Bool:
		ute := s.typeError("string "+s.literal(), typ)
		ute.Err = strconv.ErrSyntax
		s.save(ute)
	default:
		s.rejectNumber(item, typ)
	}
}

func quotedIntFallback(item string, bits int) (int64, bool) {
	v, err := strconv.ParseInt(item, 10, bits)
	if err != nil {
		return 0, false
	}
	return v, true
}

func quotedUintFallback(item string, bits int) (uint64, bool) {
	v, err := strconv.ParseUint(item, 10, bits)
	if err != nil {
		return 0, false
	}
	return v, true
}

func quotedFloatFallback(item string, bits int) (float64, bool) {
	v, err := strconv.ParseFloat(item, bits)
	if err != nil {
		return 0, false
	}
	return v, true
}

func unquoteStringContent(item string) (string, bool) {
	v, err := jsontext.AppendUnquote(nil, item)
	if err != nil {
		return "", false
	}
	return unsafeString(v), true
}

var quotedNullLiteral = []byte(`"null"`)

func QuotedJSON[T stdjson.Unmarshaler](s *Scanner, dst T) {
	if s.scalar() == 0 {
		return
	}
	tok := s.data[s.start:s.end]
	s.saveMethodError(dst.UnmarshalJSON(tok))
}

func QuotedJSONPtr[E any, T interface {
	*E
	stdjson.Unmarshaler
}](s *Scanner, dst *T) {
	if s.scalar() == 0 {
		return
	}
	tok := s.data[s.start:s.end]
	if s.kind == 'n' {
		*dst = nil
		return
	}
	if *dst == nil {
		*dst = new(E)
	}
	err := (*dst).UnmarshalJSON(tok)
	s.saveMethodError(err)
	if err == nil && bytes.Equal(tok, quotedNullLiteral) {
		*dst = nil
	}
}

// errNonStringValue mirrors the message encoding/json reports through
// UnmarshalTypeError.Err when a `,string` field's method sees a non-string
// value.
var errNonStringValue = errors.New("JSON value must be string type")

// rejectNonString saves the type error a `,string` field reports for a
// non-string value. The token may not reach a further unmarshal method:
// a fresh Unmarshal call would dispatch the type's UnmarshalJSONFrom a
// second time even though it already declined the value.
func (s *Scanner) rejectNonString(tok []byte, typ reflect.Type) {
	var value string
	switch tok[0] {
	case '{':
		value = "object"
	case '[':
		value = "array"
	case 't', 'f':
		value = "bool"
	default:
		value = "number"
	}
	off := int64(len(tok))
	if value == "object" || value == "array" {
		off = 1
	}
	s.saveFallback(&stdjson.UnmarshalTypeError{
		Value: value, Type: typ, Offset: off, Err: errNonStringValue})
}

func QuotedText[T encoding.TextUnmarshaler](s *Scanner, dst T) {
	if s.scalar() == 0 {
		return
	}
	tok := s.data[s.start:s.end]
	if s.kind == 'n' {
		return
	}
	if len(tok) > 0 && tok[0] == '"' {
		if unq, err := jsontext.AppendUnquote(nil, tok); err == nil {
			s.saveMethodError(dst.UnmarshalText(unq))
			return
		}
	}
	typ := reflect.TypeFor[T]()
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	s.rejectNonString(tok, typ)
}

func QuotedTextPtr[E any, T interface {
	*E
	encoding.TextUnmarshaler
}](s *Scanner, dst *T) {
	if s.scalar() == 0 {
		return
	}
	tok := s.data[s.start:s.end]
	if s.kind == 'n' {
		*dst = nil
		return
	}
	if len(tok) > 0 && tok[0] == '"' {
		if *dst == nil {
			*dst = new(E)
		}
		if unq, err := jsontext.AppendUnquote(nil, tok); err == nil {
			err = (*dst).UnmarshalText(unq)
			s.saveMethodError(err)
			if err == nil && bytes.Equal(tok, quotedNullLiteral) {
				*dst = nil
			}
			return
		}
	}
	if *dst == nil {
		*dst = new(E)
	}
	s.rejectNonString(tok, reflect.TypeFor[E]())
}

func QuotedFrom[T any, PT interface {
	*T
	UnmarshalJSONFrom(*jsontext.Decoder) error
}](s *Scanner, dst PT) bool {
	if s.scalar() == 0 {
		return true
	}
	tok := s.data[s.start:s.end]
	dec := jsontext.NewDecoder(bytes.NewReader(tok), stdjson.DefaultOptionsV1())
	err := dst.UnmarshalJSONFrom(dec)
	if errors.Is(err, errors.ErrUnsupported) {
		if dec.InputOffset() != 0 {
			s.save(errors.New("unsupported calls must not read or write any tokens"))
			return true
		}
		s.pos = s.start
		return false
	}
	if err == nil && dec.InputOffset() != int64(len(tok)) {
		s.save(errors.New("must read or write exactly one value"))
		return true
	}
	s.saveMethodError(err)
	return true
}

func QuotedFromPtr[E any, T interface {
	*E
	UnmarshalJSONFrom(*jsontext.Decoder) error
}](s *Scanner, dst *T) bool {
	if s.scalar() == 0 {
		return true
	}
	tok := s.data[s.start:s.end]
	if s.kind == 'n' {
		*dst = nil
		return true
	}
	if *dst == nil {
		*dst = new(E)
	}
	dec := jsontext.NewDecoder(bytes.NewReader(tok), stdjson.DefaultOptionsV1())
	err := (*dst).UnmarshalJSONFrom(dec)
	if errors.Is(err, errors.ErrUnsupported) {
		if dec.InputOffset() != 0 {
			s.save(errors.New("unsupported calls must not read or write any tokens"))
			return true
		}
		s.pos = s.start
		return false
	}
	if err == nil && dec.InputOffset() != int64(len(tok)) {
		s.save(errors.New("must read or write exactly one value"))
		return true
	}
	s.saveMethodError(err)
	if err == nil && bytes.Equal(tok, quotedNullLiteral) {
		*dst = nil
	}
	return true
}

func (s *Scanner) saveMethodError(err error) {
	if ute, ok := err.(*stdjson.UnmarshalTypeError); ok {
		if s.saved == nil {
			s.saved = ute
		}
		return
	}
	s.fatal(err)
}

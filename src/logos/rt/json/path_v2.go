// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build goexperiment.jsonv2

package json

import (
	"bytes"
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

func fallbackQuoted(s *Scanner, dst any) {
	if s.scalar() == 0 {
		return
	}
	tok := s.data[s.start:s.end]
	if s.decodeQuotedRaw(tok, dst, s.kind == 'n') && bytes.Equal(tok, quotedNullLiteral) {
		if rv := reflect.ValueOf(dst); rv.Kind() == reflect.Pointer && !rv.IsNil() && rv.Elem().Kind() == reflect.Pointer {
			rv.Elem().SetZero()
		}
	}
}

func (s *Scanner) decodeQuotedRaw(tok []byte, dst any, decodingNull bool) bool {
	u, ut, pv := quotedIndirect(reflect.ValueOf(dst), decodingNull)
	if u != nil {
		err := u.UnmarshalJSON(tok)
		s.saveMethodError(err)
		return err == nil
	} else if ut != nil {
		if len(tok) > 0 && tok[0] == '"' {
			if unq, err := jsontext.AppendUnquote(nil, tok); err == nil {
				err = ut.UnmarshalText(unq)
				s.saveMethodError(err)
				return err == nil
			}
		}
		return s.decodeRaw(tok, dst)
	}
	if decodingNull {
		switch pv.Kind() {
		case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice:
			pv.SetZero()
		}
		return true
	}
	return s.decodeRaw(tok, dst)
}

// saveMethodError records a `,string` method failure under the JSONv2
// profile. A *UnmarshalTypeError from the method is saved verbatim: the
// first one wins, decoding continues, and neither Offset nor struct/field
// context is added (ReportErrorsWithLegacySemantics returns method errors
// unwrapped). Any other non-nil error is fatal; nil is a no-op.
func (s *Scanner) saveMethodError(err error) {
	if ute, ok := err.(*stdjson.UnmarshalTypeError); ok {
		if s.saved == nil {
			s.saved = ute
		}
		return
	}
	s.fatal(err)
}

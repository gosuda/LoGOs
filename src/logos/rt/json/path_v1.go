// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !goexperiment.jsonv2

package json

import (
	"encoding"
	stdjson "encoding/json"
	"reflect"
)

const indexedFieldPaths = false

func (s *Scanner) rejectQuotedBare(typ reflect.Type) {
	s.save(tagUnquoted(typ))
}

func (s *Scanner) rejectQuotedContent(res quotedResult, item string, typ reflect.Type) {
	switch res {
	case qSavedTag:
		s.save(tagMismatch(item, typ))
	case qFatalTag:
		s.fatal(tagMismatch(item, typ))
	case qString:
		s.rejectKind(typ)
	}
}

func quotedIntFallback(item string, bits int) (int64, bool) {
	return 0, false
}

func quotedUintFallback(item string, bits int) (uint64, bool) {
	return 0, false
}

func quotedFloatFallback(item string, bits int) (float64, bool) {
	return 0, false
}

func unquoteStringContent(item string) (string, bool) {
	return unquoteLiteral(item)
}

func fallbackQuoted(s *Scanner, dst any) {
	item, st := s.readQuoted()
	switch st {
	case quotedBare:
		s.save(tagUnquoted(reflect.TypeOf(dst).Elem()))
	case quotedContent:
		if item == "" {
			s.save(tagMismatch(item, reflect.TypeOf(dst).Elem()))
			return
		}
		s.dispatchQuotedContent(dst, item)
	default:
		if s.kind == 'n' {
			s.dispatchBareNull(dst)
			return
		}
		if s.err == nil {
			s.decodeRaw(s.data[s.start:s.end], dst)
		}
	}
}

func (s *Scanner) dispatchBareNull(dst any) {
	u, _, pv := quotedIndirect(reflect.ValueOf(dst), true)
	if u != nil {
		s.fatalMethodError(u.UnmarshalJSON(s.data[s.start:s.end]))
		return
	}
	switch pv.Kind() {
	case reflect.Interface, reflect.Pointer, reflect.Map, reflect.Slice:
		pv.SetZero()
	}
}

func (s *Scanner) dispatchQuotedContent(dst any, item string) {
	u, ut, _ := quotedIndirect(reflect.ValueOf(dst), item[0] == 'n')
	if u != nil {
		s.fatalMethodError(u.UnmarshalJSON([]byte(item)))
		return
	}
	if ut != nil {
		s.dispatchQuotedText(ut, item, reflect.TypeOf(dst).Elem())
		return
	}
	if item[0] == 'n' && item != "null" {
		s.save(tagMismatch(item, reflect.TypeOf(dst).Elem()))
		return
	}
	s.decodeRaw([]byte(item), dst)
}

func (s *Scanner) dispatchQuotedText(ut encoding.TextUnmarshaler, item string, typ reflect.Type) {
	if item == "" || item[0] != '"' {
		s.save(tagMismatch(item, typ))
		return
	}
	unq, ok := unquoteStringContent(item)
	if !ok {
		s.fatal(tagMismatch(item, typ))
		return
	}
	s.fatalMethodError(ut.UnmarshalText([]byte(unq)))
}

func (s *Scanner) fatalMethodError(err error) {
	if ute, ok := err.(*stdjson.UnmarshalTypeError); ok {
		ute.Struct = s.structName()
		if ute.Field == "" {
			ute.Field = detach(s.key)
		} else {
			ute.Field = s.key + "." + ute.Field
		}
	}
	s.fatal(err)
}

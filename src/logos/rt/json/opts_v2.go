// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build goexperiment.jsonv2

package json

import (
	stdjson "encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
)

// v1CallOptionMask covers the jsonflags bits the encoding/json runtime
// sets around a MarshalJSONTo or UnmarshalJSONFrom call itself:
// WithinArshalCall (bit 3) on both paths and OmitTopLevelNewline
// (bit 4) on the encode path. No caller-facing option can set either
// bit; they are internal to json.Marshal, json.Unmarshal, and the
// streaming wrappers.
const v1CallOptionMask = 0x18

var v1DefaultOptions = stdjson.DefaultOptionsV1()

// optionsLayout mirrors the leaf fields of the options struct behind
// jsontext.Options, in declaration order. optionsLayoutHolds proves at
// start-up that the mirror matches the toolchain's struct; when it does
// not, CallerOptionsAreV1Default compares through reflection instead.
type optionsLayout struct {
	presence, values     uint64
	indent, indentPrefix string
	byteLimit            int64
	depthLimit           int
	marshalers           any
	unmarshalers         any
	format               string
}

var (
	v1OptionsType      = reflect.TypeOf(v1DefaultOptions)
	v1OptionsLayout    = (*optionsLayout)(reflect.ValueOf(v1DefaultOptions).UnsafePointer())
	optionsLayoutHolds = v1OptionsType.Kind() == reflect.Pointer &&
		sameLeafFields(v1OptionsType.Elem(), reflect.TypeFor[optionsLayout]()) &&
		v1OptionsLayout.marshalers == nil && v1OptionsLayout.unmarshalers == nil
)

type leafField struct {
	offset uintptr
	typ    reflect.Type
}

func leafFields(t reflect.Type, base uintptr, into []leafField) []leafField {
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Type.Kind() == reflect.Struct {
			into = leafFields(f.Type, base+f.Offset, into)
			continue
		}
		into = append(into, leafField{base + f.Offset, f.Type})
	}
	return into
}

func sameLeafFields(a, b reflect.Type) bool {
	if a.Kind() != reflect.Struct || a.Size() != b.Size() {
		return false
	}
	fa, fb := leafFields(a, 0, nil), leafFields(b, 0, nil)
	if len(fa) != len(fb) {
		return false
	}
	for i := range fa {
		if fa[i] != fb[i] {
			return false
		}
	}
	return true
}

// CallerOptionsAreV1Default reports whether opts are exactly the v1
// default options apart from the internal WithinArshalCall and
// OmitTopLevelNewline bits the runtime sets around the call. The
// generated MarshalJSONTo and UnmarshalJSONFrom bodies implement the
// v1 default semantics, so they may run only under these options; any
// other option, present or future, makes the generated method delegate
// to encoding/json's default marshaler or unmarshaler, which applies
// the caller's options in full.
//
// The options value is valid only within the MarshalJSONTo or
// UnmarshalJSONFrom call that produced it.
func CallerOptionsAreV1Default(opts jsontext.Options) bool {
	rv := reflect.ValueOf(opts)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Type() != v1OptionsType {
		return false
	}
	if !optionsLayoutHolds {
		return optionsEqualV1DefaultByReflection(rv)
	}
	got, want := (*optionsLayout)(rv.UnsafePointer()), v1OptionsLayout
	return got.presence&^v1CallOptionMask == want.presence&^v1CallOptionMask &&
		got.values&^v1CallOptionMask == want.values&^v1CallOptionMask &&
		got.indent == want.indent && got.indentPrefix == want.indentPrefix &&
		got.byteLimit == want.byteLimit && got.depthLimit == want.depthLimit &&
		got.marshalers == nil && got.unmarshalers == nil &&
		got.format == want.format
}

func optionsEqualV1DefaultByReflection(rv reflect.Value) bool {
	s := reflect.New(v1OptionsType.Elem()).Elem()
	s.Set(rv.Elem())
	flags := s.FieldByName("Flags")
	for _, name := range [...]string{"Presence", "Values"} {
		f := flags.FieldByName(name)
		f.SetUint(f.Uint() &^ v1CallOptionMask)
	}
	return reflect.DeepEqual(s.Interface(), reflect.ValueOf(v1DefaultOptions).Elem().Interface())
}

// DecodeDefault decodes the next JSON value from dec into v, whose type
// S carries no JSON methods, so encoding/json's default decoder applies
// the caller's options in full. A semantic error naming S is renamed to
// T, so an option-driven decode reports the original struct type.
func DecodeDefault[S, T any](dec *jsontext.Decoder, v *S) error {
	err := jsonv2.UnmarshalDecode(dec, v)
	return retypeShadowError[S, T](err)
}

// EncodeDefault marshals v, whose type S carries no JSON methods, so
// encoding/json's default encoder applies the caller's options in full,
// and renames S to T in a semantic error.
func EncodeDefault[S, T any](enc *jsontext.Encoder, v S) error {
	err := jsonv2.MarshalEncode(enc, v)
	return retypeShadowError[S, T](err)
}

func retypeShadowError[S, T any](err error) error {
	if err == nil {
		return nil
	}
	var se *jsonv2.SemanticError
	if errors.As(err, &se) && se.GoType != nil && se.GoType == reflect.TypeFor[S]() {
		se.GoType = reflect.TypeFor[T]()
	}
	return err
}

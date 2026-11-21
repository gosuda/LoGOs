// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build goexperiment.jsonv2

package json

import (
	"bytes"
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

// tagOptionsMask covers the struct-tag flags the reflective struct codec
// sets while encoding or decoding a `,string` or `,format` field: StringTag
// (bit 27) and FormatTag (bit 28) of the Marshal and Unmarshal flags word in
// encoding/json/internal/jsonflags. No caller-facing option can set either
// bit (encoding/json/v2 exposes no setter for them; only field tags set
// them, transiently, for that field), and v1 ignores both flags on struct,
// array and slice values. A v1 call that differs from the defaults only
// here still carries v1 semantics for the struct under the method.
const tagOptionsMask = 0x18000000

var v1DefaultOptions = stdjson.DefaultOptionsV1()

var v2DefaultOptions = jsonv2.DefaultOptionsV2()

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
	v2OptionsLayout    = (*optionsLayout)(reflect.ValueOf(v2DefaultOptions).UnsafePointer())
	optionsLayoutHolds = v1OptionsType.Kind() == reflect.Pointer &&
		v1OptionsType == reflect.TypeOf(v2DefaultOptions) &&
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
	return callerOptionsAreDefault(opts, v1OptionsLayout, v1DefaultOptions)
}

// CallerOptionsAreV1DefaultIgnoringTags reports whether opts carry v1
// default semantics apart from the internal call bits and the struct-tag
// flags a reflective parent sets while encoding or decoding a `,string`
// or `,format` field. The generated refusal methods run the v1 bytes or
// method under these options: v1 ignores those flags on struct values,
// so the tag changes nothing for the struct under the method, while any
// other option still refuses.
//
// The options value is valid only within the MarshalJSONTo or
// UnmarshalJSONFrom call that produced it.
func CallerOptionsAreV1DefaultIgnoringTags(opts jsontext.Options) bool {
	return callerOptionsAreDefaultMasked(opts, v1OptionsLayout, v1DefaultOptions, v1CallOptionMask|tagOptionsMask)
}

// CallerOptionsAreV2Default reports whether opts are exactly the v2
// default options apart from the internal call bits: either the explicit
// DefaultOptionsV2 set or no options at all. A plain Marshal, Unmarshal,
// MarshalEncode or UnmarshalDecode call passes a zero options struct,
// which behaves as the v2 defaults; the generated exempt methods take the
// v1 bytes under either default, while any other option needs the
// reflective path or a diagnostic.
//
// The options value is valid only within the MarshalJSONTo or
// UnmarshalJSONFrom call that produced it.
func CallerOptionsAreV2Default(opts jsontext.Options) bool {
	return callerOptionsAreDefault(opts, v2OptionsLayout, v2DefaultOptions) || callerOptionsAreUnset(opts)
}

func callerOptionsAreDefault(opts jsontext.Options, want *optionsLayout, wantAny any) bool {
	return callerOptionsAreDefaultMasked(opts, want, wantAny, v1CallOptionMask)
}

func callerOptionsAreDefaultMasked(opts jsontext.Options, want *optionsLayout, wantAny any, mask uint64) bool {
	rv := reflect.ValueOf(opts)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Type() != v1OptionsType {
		return false
	}
	if !optionsLayoutHolds {
		return optionsEqualDefaultByReflectionMasked(rv, wantAny, mask)
	}
	got := (*optionsLayout)(rv.UnsafePointer())
	return got.presence&^mask == want.presence&^mask &&
		got.values&^mask == want.values&^mask &&
		got.indent == want.indent && got.indentPrefix == want.indentPrefix &&
		got.byteLimit == want.byteLimit && got.depthLimit == want.depthLimit &&
		got.marshalers == nil && got.unmarshalers == nil &&
		got.format == want.format
}

func optionsEqualV1DefaultByReflection(rv reflect.Value) bool {
	return optionsEqualDefaultByReflection(rv, v1DefaultOptions)
}

func optionsEqualV2DefaultByReflection(rv reflect.Value) bool {
	return optionsEqualDefaultByReflection(rv, v2DefaultOptions)
}

func optionsEqualDefaultByReflection(rv reflect.Value, wantAny any) bool {
	return optionsEqualDefaultByReflectionMasked(rv, wantAny, v1CallOptionMask)
}

func optionsEqualDefaultByReflectionMasked(rv reflect.Value, wantAny any, mask uint64) bool {
	s, ok := maskedOptionsCopyWithMask(rv, mask)
	if !ok {
		return false
	}
	return reflect.DeepEqual(s.Interface(), reflect.ValueOf(wantAny).Elem().Interface())
}

// callerOptionsAreUnset reports whether opts carry no caller-facing option:
// a plain call passes a zero options struct, which behaves as the v2
// defaults. Only the internal call bits are masked out.
func callerOptionsAreUnset(opts jsontext.Options) bool {
	rv := reflect.ValueOf(opts)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Type() != v1OptionsType {
		return false
	}
	if !optionsLayoutHolds {
		return optionsEqualUnsetByReflection(rv)
	}
	got := (*optionsLayout)(rv.UnsafePointer())
	return got.presence&^v1CallOptionMask == 0 &&
		got.values&^v1CallOptionMask == 0 &&
		got.indent == "" && got.indentPrefix == "" &&
		got.byteLimit == 0 && got.depthLimit == 0 &&
		got.marshalers == nil && got.unmarshalers == nil &&
		got.format == ""
}

func optionsEqualUnsetByReflection(rv reflect.Value) bool {
	s, ok := maskedOptionsCopy(rv)
	if !ok {
		return false
	}
	return reflect.DeepEqual(s.Interface(), reflect.Zero(v1OptionsType.Elem()).Interface())
}

// maskedOptionsCopy copies the options behind rv and clears the internal
// call bits, reporting whether rv has the options struct shape at all.
func maskedOptionsCopy(rv reflect.Value) (reflect.Value, bool) {
	return maskedOptionsCopyWithMask(rv, v1CallOptionMask)
}

// maskedOptionsCopyWithMask is maskedOptionsCopy with a caller-supplied mask.
func maskedOptionsCopyWithMask(rv reflect.Value, mask uint64) (reflect.Value, bool) {
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Type() != v1OptionsType {
		return reflect.Value{}, false
	}
	s := reflect.New(v1OptionsType.Elem()).Elem()
	s.Set(rv.Elem())
	flags := s.FieldByName("Flags")
	if !flags.IsValid() || flags.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	for _, name := range [...]string{"Presence", "Values"} {
		f := flags.FieldByName(name)
		if !f.IsValid() || !f.CanSet() {
			return reflect.Value{}, false
		}
		switch f.Kind() {
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		default:
			return reflect.Value{}, false
		}
		f.SetUint(f.Uint() &^ mask)
	}
	return s, true
}

// matchCaseInsensitiveNamesBit is the jsonflags bit position of
// MatchCaseInsensitiveNames inside the options Flags word: the first
// flag after bit 22, per encoding/json/internal/jsonflags.
const matchCaseInsensitiveNamesBit = 1 << 23

// CallerMatchCaseInsensitive reports whether the caller's options demand
// case-insensitive member-name matching: v1 semantics always carry the
// MatchCaseInsensitiveNames value bit, and a v2 caller carries it only
// when it passed MatchCaseInsensitiveNames(true). The generated
// required-field presence scan folds member names only under this rule,
// so presence matches what the reflective decode actually saw.
//
// The options value is valid only within the MarshalJSONTo or
// UnmarshalJSONFrom call that produced it.
func CallerMatchCaseInsensitive(opts jsontext.Options) bool {
	rv := reflect.ValueOf(opts)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Type() != v1OptionsType {
		return false
	}
	if !optionsLayoutHolds {
		return optionValueBitByReflection(rv, matchCaseInsensitiveNamesBit)
	}
	got := (*optionsLayout)(rv.UnsafePointer())
	return got.values&matchCaseInsensitiveNamesBit != 0
}

// matchCaseSensitiveDelimiterBit is the jsonflags bit position of
// MatchCaseSensitiveDelimiter inside the options Flags word: the fifth
// marshal/unmarshal flag of the v1 block, per
// encoding/json/internal/jsonflags.
const matchCaseSensitiveDelimiterBit = 1 << 34

// CallerMatchCaseSensitiveDelimiter reports whether the caller's options
// demand delimiter-sensitive member-name matching: under v1 semantics the
// value bit is set and case-insensitive matching keeps '-' and '_'
// significant, while a v2 caller that asked for
// MatchCaseInsensitiveNames folds delimiters away unless it also passed
// MatchCaseSensitiveDelimiter(true). The generated presence scan picks
// its folded key dispatch by this rule so presence matches what the
// reflective decode actually accepts.
//
// The options value is valid only within the MarshalJSONTo or
// UnmarshalJSONFrom call that produced it.
func CallerMatchCaseSensitiveDelimiter(opts jsontext.Options) bool {
	rv := reflect.ValueOf(opts)
	if rv.Kind() != reflect.Pointer || rv.IsNil() || rv.Type() != v1OptionsType {
		return false
	}
	if !optionsLayoutHolds {
		return optionValueBitByReflection(rv, matchCaseSensitiveDelimiterBit)
	}
	got := (*optionsLayout)(rv.UnsafePointer())
	return got.values&matchCaseSensitiveDelimiterBit != 0
}

func optionValueBitByReflection(rv reflect.Value, bit uint64) bool {
	s := reflect.New(v1OptionsType.Elem()).Elem()
	s.Set(rv.Elem())
	flags := s.FieldByName("Flags")
	if !flags.IsValid() || flags.Kind() != reflect.Struct {
		return false
	}
	f := flags.FieldByName("Values")
	if !f.IsValid() || f.Kind() != reflect.Uint64 {
		return false
	}
	return f.Uint()&bit != 0
}

// UnsupportedOptions returns the diagnostic for a struct whose static
// JSON methods cannot honor the caller's options: a non-bare option
// field or a union member has no reflective representation, so the
// generated method reports this error instead of silently writing v1
// bytes.
func UnsupportedOptions(structName string) error {
	return errors.New("logos: json: caller options unsupported for struct with option fields: " + structName)
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
	var ute *stdjson.UnmarshalTypeError
	if errors.As(err, &ute) {
		if ute.Type != nil && ute.Type == reflect.TypeFor[S]() {
			ute.Type = reflect.TypeFor[T]()
		}
		if ute.Struct == reflect.TypeFor[S]().Name() {
			ute.Struct = reflect.TypeFor[T]().Name()
		}
	}
	return err
}

// DecodeDefaultRaw decodes raw, one complete JSON value buffered from the
// caller decoder, into v, whose type S carries no JSON methods, so the
// reflective decoder applies opts in full. opts is the materialized options
// of the caller decoder, joined onto a fresh decoder over raw. base is the
// absolute offset of raw within the original stream; error offsets are
// rebased by it, and error JSON pointers are prefixed with ptr, the
// caller decoder's stack pointer at the buffered value, so a re-decoded
// value reports the same positions as a streaming decode. A semantic
// error naming S is renamed to T.
func DecodeDefaultRaw[S, T any](raw jsontext.Value, opts jsontext.Options, base int64, ptr jsontext.Pointer, v *S) error {
	dec := jsontext.NewDecoder(bytes.NewReader(raw))
	var err error
	if opts == nil {
		err = jsonv2.UnmarshalDecode(dec, v)
	} else {
		err = jsonv2.UnmarshalDecode(dec, v, opts)
	}
	if err == nil {
		return nil
	}
	err = shiftErrorOffset(err, base, ptr)
	return retypeShadowError[S, T](err)
}

// shiftErrorOffset re-bases the byte offsets of a value decoded from a
// buffered copy by the absolute start of that copy, and prefixes error
// JSON pointers with ptr, the stack pointer of the buffered value in the
// original stream. Opaque caller errors carry no offsets and pass
// through unchanged.
func shiftErrorOffset(err error, base int64, ptr jsontext.Pointer) error {
	if err == nil {
		return err
	}
	for e := err; e != nil; e = errors.Unwrap(e) {
		switch t := e.(type) {
		case *jsonv2.SemanticError:
			t.ByteOffset += base
			if ptr != "" && t.JSONPointer != "" {
				t.JSONPointer = ptr + t.JSONPointer
			}
		case *jsontext.SyntacticError:
			t.ByteOffset += base
		case *stdjson.UnmarshalTypeError:
			t.Offset += base
		}
	}
	return err
}

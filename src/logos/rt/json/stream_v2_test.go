// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build goexperiment.jsonv2

package json

import (
	stdjson "encoding/json"
	"encoding/json/jsontext"
	"errors"
	"reflect"
	"testing"
)

type streamProbe string

func (p *streamProbe) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	tok, err := dec.ReadValue()
	if err != nil {
		return err
	}
	*p += streamProbe("|from:" + string(tok))
	return nil
}

func (p *streamProbe) UnmarshalJSON(b []byte) error {
	*p += streamProbe("|json:" + string(b))
	return nil
}

func (p *streamProbe) UnmarshalText(b []byte) error {
	*p += streamProbe("|text:" + string(b))
	return nil
}

type streamFailProbe string

func (p *streamFailProbe) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	tok, err := dec.ReadValue()
	if err != nil {
		return err
	}
	*p += streamFailProbe("|from:" + string(tok))
	return &stdjson.UnmarshalTypeError{Value: string(tok), Type: reflect.TypeFor[streamFailProbe]()}
}

func (p *streamFailProbe) UnmarshalJSON(b []byte) error {
	*p += streamFailProbe("|json:" + string(b))
	return nil
}

func (p *streamFailProbe) UnmarshalText(b []byte) error {
	*p += streamFailProbe("|text:" + string(b))
	return nil
}

type streamTarget[T any] struct {
	V     T   `json:"v,string"`
	After int `json:"after"`
}

func checkStream[T any](t *testing.T, in string, newDst func(*streamTarget[T]) any) {
	t.Helper()
	data := `{"v":` + in + `,"after":17}`
	var got, want streamTarget[T]
	s := NewScanner([]byte(data), (*streamTarget[T])(nil))
	if !s.Object() {
		t.Fatalf("%s: Object failed: %v", data, s.Err())
	}
	for s.Next() {
		switch s.Key() {
		case "v":
			switch dst := newDst(&got).(type) {
			case *streamProbe:
				QuotedFrom[streamProbe](&s, dst)
			case **streamProbe:
				QuotedFromPtr[streamProbe](&s, dst)
			case *streamFailProbe:
				QuotedFrom[streamFailProbe](&s, dst)
			case **streamFailProbe:
				QuotedFromPtr[streamFailProbe](&s, dst)
			default:
				t.Fatalf("%s: unsupported stream target %T", data, dst)
			}
		case "after":
			Int(&s, &got.After)
		}
	}
	wantErr := stdjson.Unmarshal([]byte(data), &want)
	if !sameError(s.End(), wantErr) {
		t.Errorf("%s: End() = %v, encoding/json = %v", in, s.End(), wantErr)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: value = %#v, encoding/json = %#v", in, got, want)
	}
}

func TestStreamPrecedenceMatchesEncodingJSON(t *testing.T) {
	inputs := []string{`"\"hi\""`, `"3"`, `null`, `"null"`, `7`, `true`, `{}`, `""`}
	for _, in := range inputs {
		checkStream[streamProbe](t, in, func(g *streamTarget[streamProbe]) any { return &g.V })
		checkStream[*streamProbe](t, in, func(g *streamTarget[*streamProbe]) any { return &g.V })
	}
}

func TestStreamErrorContinuationMatchesEncodingJSON(t *testing.T) {
	inputs := []string{`"\"hi\""`, `"3"`, `null`, `"null"`, `7`, `""`}
	for _, in := range inputs {
		checkStream[streamFailProbe](t, in, func(g *streamTarget[streamFailProbe]) any { return &g.V })
		checkStream[*streamFailProbe](t, in, func(g *streamTarget[*streamFailProbe]) any { return &g.V })
	}
}

type streamUnsupportedJSONProbe string

func (p *streamUnsupportedJSONProbe) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	return errors.ErrUnsupported
}

func (p *streamUnsupportedJSONProbe) UnmarshalJSON(b []byte) error {
	*p += streamUnsupportedJSONProbe("|json:" + string(b))
	return nil
}

type streamUnsupportedTextProbe string

func (p *streamUnsupportedTextProbe) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	return errors.ErrUnsupported
}

func (p *streamUnsupportedTextProbe) UnmarshalText(b []byte) error {
	*p += streamUnsupportedTextProbe("|text:" + string(b))
	return nil
}

type streamUnsupportedMutatedProbe string

func (p *streamUnsupportedMutatedProbe) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	if _, err := dec.ReadValue(); err != nil {
		return err
	}
	return errors.ErrUnsupported
}

func (p *streamUnsupportedMutatedProbe) UnmarshalJSON(b []byte) error {
	*p += streamUnsupportedMutatedProbe("|json:" + string(b))
	return nil
}

type streamNoConsumeProbe string

func (p *streamNoConsumeProbe) UnmarshalJSONFrom(dec *jsontext.Decoder) error {
	return nil
}

func checkStreamFallback[T any](t *testing.T, in string, newDst func(*streamTarget[T]) any) {
	t.Helper()
	data := `{"v":` + in + `,"after":17}`
	var got, want streamTarget[T]
	s := NewScanner([]byte(data), (*streamTarget[T])(nil))
	if !s.Object() {
		t.Fatalf("%s: Object failed: %v", data, s.Err())
	}
	for s.Next() {
		switch s.Key() {
		case "v":
			switch dst := newDst(&got).(type) {
			case *streamUnsupportedJSONProbe:
				if !QuotedFrom[streamUnsupportedJSONProbe](&s, dst) {
					QuotedJSON(&s, dst)
				}
			case **streamUnsupportedJSONProbe:
				if !QuotedFromPtr[streamUnsupportedJSONProbe](&s, dst) {
					QuotedJSONPtr[streamUnsupportedJSONProbe](&s, dst)
				}
			case *streamUnsupportedTextProbe:
				if !QuotedFrom[streamUnsupportedTextProbe](&s, dst) {
					QuotedText(&s, dst)
				}
			case **streamUnsupportedTextProbe:
				if !QuotedFromPtr[streamUnsupportedTextProbe](&s, dst) {
					QuotedTextPtr[streamUnsupportedTextProbe](&s, dst)
				}
			case *streamUnsupportedMutatedProbe:
				QuotedFrom[streamUnsupportedMutatedProbe](&s, dst)
			case **streamUnsupportedMutatedProbe:
				QuotedFromPtr[streamUnsupportedMutatedProbe](&s, dst)
			case *streamNoConsumeProbe:
				QuotedFrom[streamNoConsumeProbe](&s, dst)
			case **streamNoConsumeProbe:
				QuotedFromPtr[streamNoConsumeProbe](&s, dst)
			default:
				t.Fatalf("%s: unsupported fallback target %T", data, dst)
			}
		case "after":
			Int(&s, &got.After)
		}
	}
	wantErr := stdjson.Unmarshal([]byte(data), &want)
	if !sameError(s.End(), wantErr) {
		t.Errorf("%s: End() = %v, encoding/json = %v", in, s.End(), wantErr)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: value = %#v, encoding/json = %#v", in, got, want)
	}
}

func TestStreamUnsupportedFallbackMatchesEncodingJSON(t *testing.T) {
	inputs := []string{`"\"hi\""`, `"3"`, `7`, `""`}
	for _, in := range inputs {
		checkStreamFallback[streamUnsupportedJSONProbe](t, in, func(g *streamTarget[streamUnsupportedJSONProbe]) any { return &g.V })
		checkStreamFallback[*streamUnsupportedJSONProbe](t, in, func(g *streamTarget[*streamUnsupportedJSONProbe]) any { return &g.V })
		checkStreamFallback[streamUnsupportedTextProbe](t, in, func(g *streamTarget[streamUnsupportedTextProbe]) any { return &g.V })
		checkStreamFallback[*streamUnsupportedTextProbe](t, in, func(g *streamTarget[*streamUnsupportedTextProbe]) any { return &g.V })
	}
}

func TestStreamProtocolErrorMatchesEncodingJSON(t *testing.T) {
	inputs := []string{`"\"hi\""`, `"3"`, `7`, `{}`, `[]`, `""`}
	for _, in := range inputs {
		checkStreamFallback[streamUnsupportedMutatedProbe](t, in, func(g *streamTarget[streamUnsupportedMutatedProbe]) any { return &g.V })
		checkStreamFallback[*streamUnsupportedMutatedProbe](t, in, func(g *streamTarget[*streamUnsupportedMutatedProbe]) any { return &g.V })
		checkStreamFallback[streamNoConsumeProbe](t, in, func(g *streamTarget[streamNoConsumeProbe]) any { return &g.V })
		checkStreamFallback[*streamNoConsumeProbe](t, in, func(g *streamTarget[*streamNoConsumeProbe]) any { return &g.V })
	}
}

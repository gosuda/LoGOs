// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build goexperiment.jsonv2

package json

import (
	stdjson "encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"reflect"
	"testing"
)

func TestOptionsLayoutHolds(t *testing.T) {
	if !optionsLayoutHolds {
		t.Fatal("optionsLayout does not match the options struct of this toolchain; every generated JSON method pays the reflection compare")
	}
}

func TestCallerOptionsAreV1Default(t *testing.T) {
	marshalers := jsonv2.JoinMarshalers()
	unmarshalers := jsonv2.JoinUnmarshalers()
	tests := []struct {
		name string
		opts jsontext.Options
		want bool
	}{
		{"v1 default", stdjson.DefaultOptionsV1(), true},
		{"v1 default joined with itself", jsonv2.JoinOptions(stdjson.DefaultOptionsV1(), stdjson.DefaultOptionsV1()), true},
		{"v2 default", jsonv2.DefaultOptionsV2(), false},
		{"reject unknown members", jsonv2.JoinOptions(stdjson.DefaultOptionsV1(), jsonv2.RejectUnknownMembers(true)), false},
		{"omit zero struct fields", jsonv2.JoinOptions(stdjson.DefaultOptionsV1(), jsonv2.OmitZeroStructFields(true)), false},
		{"deterministic stays on", jsonv2.JoinOptions(stdjson.DefaultOptionsV1(), jsonv2.Deterministic(true)), true},
		{"deterministic off", jsonv2.JoinOptions(stdjson.DefaultOptionsV1(), jsonv2.Deterministic(false)), false},
		{"indent", jsonv2.JoinOptions(stdjson.DefaultOptionsV1(), jsontext.WithIndent("  ")), false},
		{"indent prefix", jsonv2.JoinOptions(stdjson.DefaultOptionsV1(), jsontext.WithIndentPrefix("\t")), false},
		{"escape for HTML off", jsonv2.JoinOptions(stdjson.DefaultOptionsV1(), jsontext.EscapeForHTML(false)), false},
		{"allow duplicate names off", jsonv2.JoinOptions(stdjson.DefaultOptionsV1(), jsontext.AllowDuplicateNames(false)), false},
		{"marshalers", jsonv2.JoinOptions(stdjson.DefaultOptionsV1(), jsonv2.WithMarshalers(marshalers)), false},
		{"unmarshalers", jsonv2.JoinOptions(stdjson.DefaultOptionsV1(), jsonv2.WithUnmarshalers(unmarshalers)), false},
		{"one v1 flag back to v2", jsonv2.JoinOptions(stdjson.DefaultOptionsV1(), stdjson.MatchCaseSensitiveDelimiter(false)), false},
		{"nil", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CallerOptionsAreV1Default(tt.opts); got != tt.want {
				t.Errorf("CallerOptionsAreV1Default = %v, want %v", got, tt.want)
			}
			if tt.opts == nil {
				return
			}
			if got := optionsEqualV1DefaultByReflection(reflect.ValueOf(tt.opts)); got != tt.want {
				t.Errorf("optionsEqualV1DefaultByReflection = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCallerOptionsAreV1DefaultDoesNotAllocate(t *testing.T) {
	opts := stdjson.DefaultOptionsV1()
	if n := testing.AllocsPerRun(100, func() { CallerOptionsAreV1Default(opts) }); n != 0 {
		t.Errorf("CallerOptionsAreV1Default allocates %v times per call, want 0", n)
	}
}

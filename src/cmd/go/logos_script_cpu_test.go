// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main_test

import (
	"cmd/internal/script"
	"fmt"
	"internal/cpu"
	"io"
	"runtime"
	"strings"
	"testing"
)

func TestLogosScriptHostAMD64(t *testing.T) {
	cond, ok := scriptConditions(t)["host-amd64"]
	if !ok {
		t.Fatal("missing host-amd64 script condition: target builds must not determine host execution support")
	}

	x := &cpu.X86
	v1 := runtime.GOARCH == "amd64"
	v2 := v1 && x.HasSSE3 && x.HasSSSE3 && x.HasCX16 && x.HasSSE41 && x.HasSSE42 && x.HasPOPCNT && x.HasLAHF
	v3 := v2 && x.HasFMA && x.HasMOVBE && x.HasOSXSAVE && x.HasAVX && x.HasF16C && x.HasLZCNT && x.HasBMI1 && x.HasAVX2 && x.HasBMI2
	v4 := v3 && x.HasAVX512F && x.HasAVX512DQ && x.HasAVX512CD && x.HasAVX512BW && x.HasAVX512VL
	levels := []struct {
		name string
		want bool
	}{
		{"v1", v1},
		{"v2", v2},
		{"v3", v3},
		{"v4", v4},
	}
	for _, target := range []string{"v1", "v2", "v3", "v4"} {
		t.Run("target-"+target, func(t *testing.T) {
			s, err := script.NewState(t.Context(), t.TempDir(), []string{
				"GOAMD64=" + target,
				"GOARCH=arm64",
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.CloseAndWait(io.Discard); err != nil {
					t.Fatal(err)
				}
			})
			for _, level := range levels {
				t.Run(level.name, func(t *testing.T) {
					got, err := cond.Eval(s, level.name)
					if err != nil {
						t.Fatal(err)
					}
					if got != level.want {
						t.Errorf("host-amd64:%s = %t, want %t from host CPU; script GOAMD64=%s must not change execution support", level.name, got, level.want, target)
					}
				})
			}
		})
	}
	for _, level := range []string{"", "v0", "v5", "v40", "native"} {
		t.Run("invalid-"+level, func(t *testing.T) {
			s, err := script.NewState(t.Context(), t.TempDir(), []string{"GOAMD64=v1"})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := s.CloseAndWait(io.Discard); err != nil {
					t.Fatal(err)
				}
			})
			got, err := cond.Eval(s, level)
			if err == nil {
				t.Fatalf("host-amd64:%s = %t without an error for an invalid GOAMD64 level", level, got)
			}
			if got {
				t.Errorf("host-amd64:%s reports execution support for an invalid GOAMD64 level", level)
			}
			if !strings.Contains(err.Error(), "GOAMD64") || !strings.Contains(err.Error(), fmt.Sprintf("%q", level)) {
				t.Errorf("invalid GOAMD64 level %q error does not identify the rejected level: %v", level, err)
			}
		})
	}
}

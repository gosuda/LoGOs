// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ctfe

import (
	"strings"
	"testing"
)

func TestInferredFloatArrayChargesTypeFloor(t *testing.T) {
	inferredFill := strings.Repeat("x, ", 32)
	inferredSrc := "package p\nfunc FloatArray() int { x := 0.0\nvar a [32]float64 = [32]float64{" + inferredFill + "}\nfor i := 0; i < 4; i++ { a = a }\nif a[0] == 0.0 { return 1 }\nreturn 0 }"
	inferred := constSpec{
		src:    inferredSrc,
		rtypes: []*Type{BasicType("int")},
		name:   "FloatArray",
		call:   "FloatArray()",
	}
	explicitFill := strings.Repeat("float64(0.0), ", 32)
	explicitSrc := "package p\nfunc FloatArrayExplicit() int { var a [32]float64 = [32]float64{" + explicitFill + "}\nfor i := 0; i < 4; i++ { a = a }\nif a[0] == 0.0 { return 1 }\nreturn 0 }"
	explicit := constSpec{
		src:    explicitSrc,
		rtypes: []*Type{BasicType("int")},
		name:   "FloatArrayExplicit",
		call:   "FloatArrayExplicit()",
	}
	wide := Config{MaxSteps: 100000, MaxValueBytes: 1 << 20, WordSize: 8}
	v, _, err := evalConstFunc(t, wide, inferred)
	if err != nil {
		t.Fatalf("FloatArray: %v", err)
	}
	mustInt(t, v, 1, "FloatArray")
	v, _, err = evalConstFunc(t, wide, explicit)
	if err != nil {
		t.Fatalf("FloatArrayExplicit: %v", err)
	}
	mustInt(t, v, 1, "FloatArrayExplicit")
	tight := Config{MaxSteps: 100000, MaxValueBytes: 128, WordSize: 8}
	for _, spec := range []struct {
		name string
		spec constSpec
	}{
		{"FloatArray", inferred},
		{"FloatArrayExplicit", explicit},
	} {
		_, _, err := evalConstFunc(t, tight, spec.spec)
		if err == nil {
			t.Fatalf("%s with 128-byte cap succeeded, want CodeTooLarge", spec.name)
		}
		cerr, ok := err.(*Error)
		if !ok {
			t.Fatalf("%s with 128-byte cap: %v, want CodeTooLarge", spec.name, err)
		}
		if cerr.Code != CodeTooLarge {
			t.Fatalf("%s code = %d, want %d", spec.name, cerr.Code, CodeTooLarge)
		}
	}
}

func TestStepChargeScalesWithLiveBytes(t *testing.T) {
	cfg := Config{MaxSteps: 100, MaxValueBytes: 1 << 20, WordSize: 8}
	src := "package p\nfunc CopyLoop() int { var a [4096]int64\nfor i := 0; i < 10; i++ { a = a }\nreturn len(a) }"
	_, _, err := evalConstFunc(t, cfg, constSpec{
		src:    src,
		rtypes: []*Type{BasicType("int")},
		name:   "CopyLoop",
		call:   "CopyLoop()",
	})
	if err == nil {
		t.Fatalf("CopyLoop succeeded, want step limit")
	}
	cerr, ok := err.(*Error)
	if !ok {
		t.Fatalf("want *Error, got %T (%v)", err, err)
	}
	if cerr.Code != CodeStepLimit {
		t.Fatalf("code = %d, want %d", cerr.Code, CodeStepLimit)
	}
}

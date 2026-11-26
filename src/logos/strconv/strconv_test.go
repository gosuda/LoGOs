// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package strconv

import (
	"math"
	"math/rand/v2"
	stdstrconv "strconv"
	"testing"
)

func edgeValues() []float64 {
	return []float64{
		0, math.Copysign(0, -1), 1, -1, 0.5, 0.1, -0.1,
		1e-6, -1e-6, 9.999999e-7, 1e-7, -1e-7, 1e-5,
		1e20, 1e21, -1e21, 1e22, 123456789, 12345.678,
		5e-324, -5e-324, 2.2250738585072014e-308, -2.2250738585072014e-308,
		math.MaxFloat64, -math.MaxFloat64,
		float64(math.SmallestNonzeroFloat32), float64(math.MaxFloat32),
		math.Pi, math.E, 16777216, 16777217, 9007199254740993,
		1e100, 1e-100, 1e300, 1e-300,
		math.Ldexp(1, -1074), math.Ldexp(1, -1022), math.Ldexp(1, 1023),
		math.Inf(1), math.Inf(-1), math.NaN(),
	}
}

func zigCarried() []float64 {
	return []float64{
		float64(float32(1.1754944e-38)), float64(float32(3.4028235e38)),
		float64(float32(1e-45)), float64(float32(1.4e-45)),
		float64(float32(8.999999e9)), float64(float32(4103.9003)),
		float64(float32(200)), float64(float32(0.0010310042)),
		2.2250738585072014e-308, 2.98023223876953125e-8,
		-2.109808898695963e16, 1.18575755e-316,
		4.708356024711512e18, 1.2345678901234567,
		4.294967294, 4.294967296,
		math.Float64frombits(0x7fefffffffffffff), math.Float64frombits(1),
	}
}

func TestAppendFloatParity(t *testing.T) {
	verbs := []byte{'e', 'g', 'f'}
	vals := append(edgeValues(), zigCarried()...)
	for _, v := range vals {
		for _, verb := range verbs {
			got := string(AppendFloat(nil, v, verb))
			want := stdstrconv.FormatFloat(v, verb, -1, 64)
			if got != want {
				t.Fatalf("AppendFloat(%v, %q) = %q, want %q", v, verb, got, want)
			}
		}
	}
	r := rand.New(rand.NewPCG(11, 22))
	for range 10000 {
		v := math.Float64frombits(r.Uint64())
		for _, verb := range verbs {
			got := string(AppendFloat(nil, v, verb))
			want := stdstrconv.FormatFloat(v, verb, -1, 64)
			if got != want {
				t.Fatalf("AppendFloat(%v, %q) = %q, want %q", v, verb, got, want)
			}
		}
	}
}

func TestAppendFloatZigVectors(t *testing.T) {
	for _, v := range zigCarried() {
		got := string(AppendFloat(nil, v, 'g'))
		want := stdstrconv.FormatFloat(v, 'g', -1, 64)
		if got != want {
			t.Fatalf("AppendFloat(%v) = %q, want %q", v, got, want)
		}
		back, err := stdstrconv.ParseFloat(got, 64)
		if err != nil {
			t.Fatalf("ParseFloat(%q) error %v", got, err)
		}
		checkRoundTrip(t, v, back, 'g')
	}
}

func TestAppendFloatRoundTrip(t *testing.T) {
	verbs := []byte{'e', 'g', 'f'}
	vals := append(edgeValues(), zigCarried()...)
	for _, v := range vals {
		for _, verb := range verbs {
			back, err := stdstrconv.ParseFloat(string(AppendFloat(nil, v, verb)), 64)
			if err != nil {
				t.Fatalf("ParseFloat(AppendFloat(%v, %q)) error %v", v, verb, err)
			}
			checkRoundTrip(t, v, back, verb)
		}
	}
	r := rand.New(rand.NewPCG(33, 44))
	for range 5000 {
		v := math.Float64frombits(r.Uint64())
		back, err := stdstrconv.ParseFloat(string(AppendFloat(nil, v, 'g')), 64)
		if err != nil {
			t.Fatalf("ParseFloat(AppendFloat(%v)) error %v", v, err)
		}
		checkRoundTrip(t, v, back, 'g')
	}
}

func checkRoundTrip(t *testing.T, v, back float64, verb byte) {
	t.Helper()
	if math.IsNaN(v) {
		if !math.IsNaN(back) {
			t.Fatalf("round trip NaN with %q gave %v", verb, back)
		}
		return
	}
	if math.Float64bits(v) != math.Float64bits(back) {
		t.Fatalf("round trip %v with %q gave %v", v, verb, back)
	}
}

func TestAppendFloatZeroAlloc(t *testing.T) {
	buf := make([]byte, 0, MaxFloat64Len)
	vals := []float64{-math.MaxFloat64, math.MaxFloat64, 1.2345678901234567e300, 5e-324, 0.1}
	for _, verb := range []byte{'e', 'g'} {
		for _, v := range vals {
			if n := len(AppendFloat(buf[:0], v, verb)); n > MaxFloat64Len {
				t.Fatalf("AppendFloat(%v, %q) wrote %d bytes, bound %d", v, verb, n, MaxFloat64Len)
			}
		}
	}
	for _, verb := range []byte{'e', 'g'} {
		for _, v := range vals {
			if a := testing.AllocsPerRun(100, func() {
				if got := AppendFloat(buf[:0], v, verb); len(got) == 0 {
					t.Error("AppendFloat wrote nothing")
				}
			}); a != 0 {
				t.Errorf("AppendFloat(%v, %q) allocs = %v, want 0", v, verb, a)
			}
		}
	}
}

func TestMaxFloat64Len(t *testing.T) {
	if got := len(AppendFloat(nil, -math.MaxFloat64, 'e')); got != MaxFloat64Len {
		t.Fatalf("AppendFloat(-MaxFloat64, 'e') len = %d, want %d", got, MaxFloat64Len)
	}
	if got := len(AppendFloat(nil, -math.MaxFloat64, 'g')); got != MaxFloat64Len {
		t.Fatalf("AppendFloat(-MaxFloat64, 'g') len = %d, want %d", got, MaxFloat64Len)
	}
	widest := 0
	for _, v := range append(edgeValues(), zigCarried()...) {
		for _, verb := range []byte{'e', 'E', 'g', 'G'} {
			widest = max(widest, len(AppendFloat(nil, v, verb)))
		}
	}
	r := rand.New(rand.NewPCG(55, 66))
	for range 20000 {
		v := math.Float64frombits(r.Uint64())
		for _, verb := range []byte{'e', 'E', 'g', 'G'} {
			widest = max(widest, len(AppendFloat(nil, v, verb)))
		}
	}
	if widest > MaxFloat64Len {
		t.Fatalf("observed %d bytes, bound %d", widest, MaxFloat64Len)
	}
	if widest != MaxFloat64Len {
		t.Errorf("bound %d is not tight, observed maximum %d", MaxFloat64Len, widest)
	}
}

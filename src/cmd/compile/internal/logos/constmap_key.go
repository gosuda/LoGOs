// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"cmd/compile/internal/logos/ctfe"
	"math"
	"math/big"
	"strconv"
	"strings"
)

var constKeyMask = new(big.Int).SetUint64(math.MaxUint64)

func constKeyIsInt(k ctfe.Kind) bool {
	switch k {
	case ctfe.KInt, ctfe.KInt8, ctfe.KInt16, ctfe.KInt32, ctfe.KInt64,
		ctfe.KUint, ctfe.KUint8, ctfe.KUint16, ctfe.KUint32, ctfe.KUint64, ctfe.KUintptr:
		return true
	}
	return false
}

func constKeyIsFloat(k ctfe.Kind) bool {
	return k == ctfe.KFloat32 || k == ctfe.KFloat64
}

func constKeyBits(v ctfe.Value) uint64 {
	return new(big.Int).And(v.BigInt(), constKeyMask).Uint64()
}

func constKeyFloat(v ctfe.Value) float64 {
	if v.Type().Kind == ctfe.KFloat32 {
		return float64(float32(v.Float64()))
	}
	return v.Float64()
}

func constKeyHash(v ctfe.Value) uint64 {
	t := v.Type()
	switch {
	case t.Kind == ctfe.KString:
		return constHashString(v.Str())
	case t.Kind == ctfe.KBool:
		return constHashBool(v.Bool())
	case constKeyIsFloat(t.Kind):
		return constHashFloat64(constKeyFloat(v))
	case constKeyIsInt(t.Kind):
		return constHashUint64(constKeyBits(v))
	case t.Kind == ctfe.KStruct:
		h := uint64(0)
		for i, f := range t.Fields {
			if f.Name != "_" {
				h = constHashCombine(h, constKeyHash(v.Fields()[i]))
			}
		}
		return h
	case t.Kind == ctfe.KArray:
		h := uint64(0)
		for _, e := range v.Elems() {
			h = constHashCombine(h, constKeyHash(e))
		}
		return h
	}
	panic("logos: const map key kind is not hashable: " + strconv.Itoa(int(t.Kind)))
}

func constKeyCanon(v ctfe.Value) string {
	t := v.Type()
	switch {
	case t.Kind == ctfe.KString:
		return strconv.Itoa(len(v.Str())) + ":" + v.Str()
	case t.Kind == ctfe.KBool:
		if v.Bool() {
			return "t"
		}
		return "f"
	case constKeyIsFloat(t.Kind):
		f := constKeyFloat(v)
		if f == 0 {
			f = 0
		}
		return strconv.FormatUint(math.Float64bits(f), 16)
	case constKeyIsInt(t.Kind):
		return strconv.FormatUint(constKeyBits(v), 16)
	case t.Kind == ctfe.KStruct:
		b := strings.Builder{}
		b.WriteByte('{')
		for i, f := range t.Fields {
			if f.Name != "_" {
				b.WriteString(constKeyCanon(v.Fields()[i]))
				b.WriteByte(',')
			}
		}
		b.WriteByte('}')
		return b.String()
	case t.Kind == ctfe.KArray:
		b := strings.Builder{}
		b.WriteByte('[')
		for _, e := range v.Elems() {
			b.WriteString(constKeyCanon(e))
			b.WriteByte(',')
		}
		b.WriteByte(']')
		return b.String()
	}
	return ""
}

func constKeyCompare(a, b ctfe.Value) int {
	t := a.Type()
	switch {
	case t.Kind == ctfe.KString:
		return strings.Compare(a.Str(), b.Str())
	case t.Kind == ctfe.KBool:
		ab, bb := a.Bool(), b.Bool()
		switch {
		case ab == bb:
			return 0
		case !ab:
			return -1
		}
		return 1
	case constKeyIsFloat(t.Kind):
		fa, fb := constKeyFloat(a), constKeyFloat(b)
		switch {
		case fa < fb:
			return -1
		case fa > fb:
			return 1
		}
		return 0
	case constKeyIsInt(t.Kind):
		return a.BigInt().Cmp(b.BigInt())
	case t.Kind == ctfe.KStruct:
		for i, f := range t.Fields {
			if f.Name == "_" {
				continue
			}
			if c := constKeyCompare(a.Fields()[i], b.Fields()[i]); c != 0 {
				return c
			}
		}
		return 0
	case t.Kind == ctfe.KArray:
		ea, eb := a.Elems(), b.Elems()
		for i := range ea {
			if c := constKeyCompare(ea[i], eb[i]); c != 0 {
				return c
			}
		}
		return 0
	}
	return 0
}

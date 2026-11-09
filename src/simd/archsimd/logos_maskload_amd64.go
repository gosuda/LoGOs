// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build goexperiment.simd && amd64

package archsimd

//go:noescape
func LoadFloat32x8SliceMasked(s []float32, mask Mask32x8) Float32x8

//go:noescape
func LoadInt32x8SliceMasked(s []int32, mask Mask32x8) Int32x8

//go:noescape
func LoadUint32x8SliceMasked(s []uint32, mask Mask32x8) Uint32x8

//go:noescape
func LoadFloat64x4SliceMasked(s []float64, mask Mask64x4) Float64x4

//go:noescape
func LoadInt64x4SliceMasked(s []int64, mask Mask64x4) Int64x4

//go:noescape
func LoadUint64x4SliceMasked(s []uint64, mask Mask64x4) Uint64x4

//go:noescape
func LoadInt16x32SliceMasked(s []int16, mask Mask16x32) Int16x32

//go:noescape
func LoadUint16x32SliceMasked(s []uint16, mask Mask16x32) Uint16x32

//go:noescape
func LoadInt8x64SliceMasked(s []int8, mask Mask8x64) Int8x64

//go:noescape
func LoadUint8x64SliceMasked(s []uint8, mask Mask8x64) Uint8x64

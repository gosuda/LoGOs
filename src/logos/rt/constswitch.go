// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rt

import (
	"reflect"
	"unsafe"
)

// Reinterpret returns the value at p viewed as an S. A `const switch` exact
// case rebinds each variable whose type names the switched type to the case
// type through it. The checker admits the rebinding only when the two types
// are identical after substitution, so the layouts agree (SPEC Sec 2.5).
// Every legitimate compiler call proves S == T, so the safe pointer assertion
// below preserves those calls and panics deterministically otherwise,
// without an unsafe cast or checkptr fatal.
func Reinterpret[S, T any](p *T) S {
	return *any(p).(*S)
}

// ReinterpretUnder returns the value at p viewed as an S. A `const switch`
// tilde case rebinds each variable whose type names the switched type to the
// case's basic type through it. The checker admits the rebinding only inside
// a matched ~B case, where T's underlying type is B and therefore S's: the
// layouts agree even though the types differ, so a layout-preserving pointer
// cast is sound where Reinterpret's identity assertion would panic
// (SPEC Sec 2.5). Sizes and alignment match exactly, so checkptr admits it.
func ReinterpretUnder[S, T any](p *T) S {
	return *(*S)(unsafe.Pointer(p))
}

// StoreUnder writes src's storage back through dst, viewed as a *D. A
// `const switch` case rebinds a variable of the switched type as a case-local
// copy; after the case body, the copy is stored back through the address
// captured before the rebinding, so writes made inside the case are visible
// afterwards. The checker admits the pair only where the layouts agree, so
// reading src's bytes as D is sound (SPEC Sec 2.5).
func StoreUnder[D, S any](dst *D, src S) {
	*(*D)(unsafe.Pointer(dst)) = *(*D)(unsafe.Pointer(&src))
}

// Box returns v as an I. Inside a `const switch` interface case, a value of
// the switched type keeps its concrete layout and gains I's method set where
// the checker admitted it; the comma-ok assert returns the zero I rather than
// panicking when v carries a nil interface value (SPEC Sec 2.5).
func Box[I, T any](v T) I {
	r, _ := any(v).(I)
	return r
}

// TildeIs reports whether the underlying type of T has the same kind as the
// basic type B. It is the run-time test of a `~B` case of a `const switch`
// (SPEC Sec 2.5).
func TildeIs[T, B any]() bool {
	return reflect.TypeFor[T]().Kind() == reflect.TypeFor[B]().Kind()
}

// Implements reports whether T implements the interface I. It is the
// run-time test of an interface case of a `const switch` (SPEC Sec 2.5).
func Implements[T, I any]() bool {
	return reflect.TypeFor[T]().Implements(reflect.TypeFor[I]())
}

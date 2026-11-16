// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package rt

// Logos_DefaultBool keeps generated field-presence masks bound to the built-in
// boolean type even when a source package declares its own bool identifier.
type Logos_DefaultBool = bool

// Logos_DefaultAddr returns a fresh variable containing value. Generated
// default constructors use it where the source may shadow the new builtin.
func Logos_DefaultAddr[T any](value T) *T {
	return &value
}

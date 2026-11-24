// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import "bytes"

// LogosLocalTypeString is TypeString, except that a type declared in a
// function body is spelled by its bare name. logos eject prints a comptime
// instance body, with the types it declares, into packages other than the
// defining package, where the defining package's qualifier does not reach
// such a type.
func LogosLocalTypeString(typ Type, qf Qualifier) string {
	var buf bytes.Buffer
	w := newTypeWriter(&buf, qf)
	w.logosLocal = true
	w.typ(typ)
	return buf.String()
}

// logosTypeName writes the LoGOs spelling of obj, when it has one, and
// reports whether it did.
func (w *typeWriter) logosTypeName(obj *TypeName) bool {
	if w.logosLocal && obj.pkg != nil && obj.parent != nil && obj.parent != obj.pkg.scope {
		w.string(obj.name)
		return true
	}
	return w.logosCgoTypeName(obj)
}

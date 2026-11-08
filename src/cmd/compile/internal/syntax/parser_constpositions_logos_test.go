// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package syntax

import (
	"strings"
	"testing"
)

func TestPlainGoRejectsConstPositions(t *testing.T) {
	for _, src := range []string{
		"package p\n\nconst for i := range x {}",
		"package p\n\nconst if true {}",
		"package p\n\nconst switch x {\n}",
		"package p\n\nconst func F() {}",
		"package p\n\ntype I interface {\n\tconst M() int\n}",
	} {
		var errs []string
		_, _ = Parse(nil, strings.NewReader(src), func(err error) {
			errs = append(errs, err.Error())
		}, nil, 0)
		if len(errs) == 0 {
			t.Errorf("%q: parsed without error; want a syntax error", src)
		}
	}
}

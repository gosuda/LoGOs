// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"strings"
	"testing"
)

func TestTuplePackParenCall(t *testing.T) {
	cases := []struct {
		name string
		body string
		op   string
	}{
		{"define bare", "t := pair()", ":="},
		{"define one pair", "t := (pair())", ":="},
		{"define two pairs", "t := ((pair()))", ":="},
		{"define spaced pairs", "t := ( ( pair() ) )", ":="},
		{"assign bare", "var t (int, error)\n\tt = pair()", "="},
		{"assign one pair", "var t (int, error)\n\tt = (pair())", "="},
		{"assign two pairs", "var t (int, error)\n\tt = ((pair()))", "="},
		{"assign spaced pairs", "var t (int, error)\n\tt = ( ( pair() ) )", "="},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := "package main\n\nfunc pair() (int, error) { return 1, nil }\n\nfunc f() {\n\t" + c.body + "\n\t_ = t\n}\n"
			_, ejected := lowerSpreadSrc(t, src)
			first := tupleTempName.FindString(ejected)
			second := strings.TrimSuffix(first, "_0") + "_1"
			want := "\t" + first + ", " + second + " := pair(); t " + c.op + " struct{ T_0 int; T_1 error }{" + first + ", " + second + "}\n"
			if first == "" || !strings.Contains(ejected, want) {
				t.Errorf("ejected text lacks %q:\n%s", want, ejected)
			}
			requirePlainGo(t, ejected)
		})
	}
}

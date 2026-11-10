//go:build ignore

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"fmt"
	"os"
	"strings"
)

var words = []string{"yield", "group", "scope", "errdefer", "property", "comptime", "arena", "region"}

func capWord(s string) string {
	return strings.ToUpper(s[:1]) + s[1:]
}

func genWord(w string, base int) string {
	c := capWord(w)
	var b strings.Builder
	fmt.Fprintf(&b, "package main\n\n")
	fmt.Fprintf(&b, "type T%s struct {\n\tV int\n}\n\n", c)
	fmt.Fprintf(&b, "func (t T%s) %s() int {\n\treturn t.V + %d\n}\n\n", c, w, base)
	fmt.Fprintf(&b, "func G%s[%s any](v %s) %s {\n\treturn v\n}\n\n", c, w, w, w)
	fmt.Fprintf(&b, "func F%sConst() int {\n\tconst %s = %d\n\treturn %s\n}\n\n", c, w, base, w)
	fmt.Fprintf(&b, "func F%sVar() int {\n\tvar %s = 3\n\treturn %s\n}\n\n", c, w, w)
	fmt.Fprintf(&b, "func F%sParam() int {\n\tf := func(%s int) int {\n\t\treturn %s * 2\n\t}\n\treturn f(21)\n}\n\n", c, w, w)
	fmt.Fprintf(&b, "func F%sResult() int {\n\tf := func() (%s int) {\n\t\t%s = 5\n\t\treturn\n\t}\n\treturn f()\n}\n\n", c, w, w)
	fmt.Fprintf(&b, "func F%sField() int {\n\ttype S struct {\n\t\t%s int\n\t}\n\ts := S{%s: 4}\n\treturn s.%s\n}\n\n", c, w, w, w)
	fmt.Fprintf(&b, "func F%sEmbed() int {\n\ttype %s struct {\n\t\tN int\n\t}\n\ttype S struct {\n\t\t%s\n\t}\n\ts := S{%s: %s{N: 6}}\n\treturn s.%s.N\n}\n\n", c, w, w, w, w, w)
	fmt.Fprintf(&b, "func F%sLocalType() int {\n\ttype %s int\n\tvar v %s = 7\n\treturn int(v)\n}\n\n", c, w, w)
	fmt.Fprintf(&b, "func F%sLabel() int {\n\tn := 0\n%s:\n\tn++\n\tif n < 3 {\n\t\tgoto %s\n\t}\n\treturn n\n}\n\n", c, w, w)
	fmt.Fprintf(&b, "func F%sRange() int {\n\ts := 0\n\tfor _, %s := range []int{1, 2, 3} {\n\t\ts += %s\n\t}\n\treturn s\n}\n\n", c, w, w)
	fmt.Fprintf(&b, "func F%sTParam() int {\n\treturn G%s[int](8)\n}\n\n", c, c)
	fmt.Fprintf(&b, "func F%sLoop() int {\n\ts := 0\n%s:\n\tfor i := 0; i < 3; i++ {\n\t\tfor j := 0; j < 3; j++ {\n\t\t\tif j == 1 {\n\t\t\t\tcontinue %s\n\t\t\t}\n\t\t\tif i == 2 {\n\t\t\t\tbreak %s\n\t\t\t}\n\t\t\ts++\n\t\t}\n\t}\n\treturn s\n}\n\n", c, w, w, w)
	fmt.Fprintf(&b, "func D%s() int {\n", c)
	fmt.Fprintf(&b, "\tt := T%s{V: 1}\n", c)
	fmt.Fprintf(&b, "\treturn t.%s() + F%sConst() + F%sVar() + F%sParam() + F%sResult() + F%sField() + F%sEmbed() + F%sLocalType() + F%sLabel() + F%sRange() + F%sTParam() + F%sLoop()\n}\n", w, c, c, c, c, c, c, c, c, c, c, c)
	return b.String()
}

func genAlias(w string, next string) string {
	c := capWord(w)
	return fmt.Sprintf("package main\n\nimport %s \"example.com/parity/at13_contextual_words/pkg_%s\"\n\nfunc Alias%s() string {\n\treturn %s.Who()\n}\n", w, next, c, w)
}

func main() {
	rot := map[string]string{}
	for i, w := range words {
		rot[w] = words[(i+1)%len(words)]
	}
	for i, w := range words {
		base := 40 + i
		if err := os.WriteFile(fmt.Sprintf("gen_%s.go", w), []byte(genWord(w, base)), 0644); err != nil {
			panic(err)
		}
		if err := os.WriteFile(fmt.Sprintf("alias_%s.go", w), []byte(genAlias(w, rot[w])), 0644); err != nil {
			panic(err)
		}
	}
}

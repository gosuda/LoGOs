// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"strings"
	"testing"
)

func TestBinaryMethodsGenerated(t *testing.T) {
	text := lowerStaticText(t, "package p\ntype Node struct {\n N int64 `json:\"n\"`\n Next *Node\n Name string\n Data []byte\n Values []uint16\n Lookup map[string]float32\n Maybe int?\n}\n")
	for _, want := range []string{
		"func (_logos_v Node) MarshalBinary() ([]byte, error)",
		"func (_logos_v *Node) UnmarshalBinary(src []byte) error",
		"func (_logos_v Node) AppendBinary(dst []byte) ([]byte, error)",
		"(1<<20)/size",
		"binary field %s at offset %d: truncated input",
		"bad presence byte",
		"wrong version %d",
		"$trailing at offset %d: trailing bytes",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in generated text", want)
		}
	}
}

func TestBinaryOptionRepresentations(t *testing.T) {
	text := lowerStaticText(t, "package p\ntype S struct { P *string? `json:\"p\"`; N int? `json:\"n\"` }\n")
	start := strings.Index(text, "func _logos_bin_S_")
	if start < 0 {
		t.Fatal("missing binary helpers")
	}
	text = text[start:]
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, "v *string)") && (strings.Contains(line, "v.Present") || strings.Contains(line, "v.Value")) {
			t.Errorf("pointer option uses wrapper accessor: %s", line)
		}
	}
	if !strings.Contains(text, "v.Present") || !strings.Contains(text, "v.Value") {
		t.Error("scalar option no longer uses its presence wrapper")
	}
	if !strings.Contains(text, "{ return _logos_bin_S_") {
		t.Error("bare pointer option does not delegate to its payload codec")
	}
}

func TestBinaryAllUserMethodsSuppressHelpers(t *testing.T) {
	src := `package p
type S struct {
	A int64  ` + "`json:\"a\"`" + `
	B string ` + "`json:\"b\"`" + `
}
func (s S) MarshalBinary() ([]byte, error) { return nil, nil }
func (s S) AppendBinary(b []byte) ([]byte, error) { return b, nil }
func (s *S) UnmarshalBinary(data []byte) error { return nil }
`
	text := lowerStaticText(t, src)
	if strings.Contains(text, "_logos_bin_S") {
		t.Errorf("binary helpers generated when user declares all binary methods:\n%s", text)
	}
	if !strings.Contains(text, "func (_logos_v S) MarshalJSON() ([]byte, error)") {
		t.Errorf("expected MarshalJSON to be generated for S:\n%s", text)
	}
}

func TestBinaryPartialUserMethodsPreserveCodecs(t *testing.T) {
	src := `package p
type S struct {
	A int64 ` + "`json:\"a\"`" + `
}
func (s S) MarshalBinary() ([]byte, error) { return nil, nil }
`
	text := lowerStaticText(t, src)
	if !strings.Contains(text, "func (_logos_v S) AppendBinary") {
		t.Errorf("missing AppendBinary in partial binary declaration:\n%s", text)
	}
	if !strings.Contains(text, "func (_logos_v *S) UnmarshalBinary") {
		t.Errorf("missing UnmarshalBinary in partial binary declaration:\n%s", text)
	}
	if !strings.Contains(text, "_logos_bin_S_") {
		t.Errorf("missing binary helpers in partial binary declaration:\n%s", text)
	}
}

func TestBinaryZeroWireNoIteration(t *testing.T) {
	text := lowerStaticText(t, "package p\ntype Empty struct{}\ntype S struct {\n\tV []Empty `json:\"v\"`\n}\n")
	if strings.Contains(text, "for i := uint64") {
		t.Errorf("zero-wire decode must not iterate per element:\n%s", text)
	}
	if !strings.Contains(text, "*v = make([]Empty, int(n))") {
		t.Errorf("zero-wire decode must allocate directly from the count:\n%s", text)
	}
}

func TestBinaryUnsupportedShapes(t *testing.T) {
	for name, decl := range map[string]string{
		"chan":    "type S struct { C chan int `json:\"c\"` }\n",
		"func":    "type S struct { F func() `json:\"f\"` }\n",
		"complex": "type S struct { C complex128 `json:\"c\"` }\n",
		"union":   "type A struct { X int `json:\"x\"` }\ntype B struct { Y int `json:\"y\"` }\ntype U interface { A | B }\ntype S struct { M U `json:\"m\"` }\n",
	} {
		t.Run(name, func(t *testing.T) {
			text := lowerStaticText(t, "package p\n\n"+decl)
			if strings.Contains(text, "func (_logos_v S) MarshalBinary()") || strings.Contains(text, "func (_logos_v *S) UnmarshalBinary(") {
				t.Fatalf("unsupported shape %s got binary methods:\n%s", name, text)
			}
		})
	}
}

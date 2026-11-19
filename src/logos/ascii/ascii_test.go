// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ascii_test

import (
	"logos/ascii"
	"strings"
	"testing"
	"unicode"
)

var foldSink int

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 128 {
			return false
		}
	}
	return true
}

func asciiLowerOracle(b byte) byte {
	if b < 128 {
		return byte(unicode.ToLower(rune(b)))
	}
	return b
}

func TestEqualFoldTable(t *testing.T) {
	cases := []struct {
		a    string
		b    string
		want bool
	}{
		{"", "", true},
		{"a", "a", true},
		{"a", "A", true},
		{"A", "a", true},
		{"Hello", "hELLo", true},
		{"Hello", "Hello ", false},
		{"Hello", "Hellp", false},
		{"abc", "ABD", false},
		{"HTTP", "http", true},
		{"Bearer", "bearer", true},
		{"Example.COM", "example.com", true},
		{"a", "b", false},
	}
	for _, c := range cases {
		got := ascii.EqualFold(c.a, c.b)
		if got != c.want {
			t.Fatalf("EqualFold(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
		if isASCII(c.a) && isASCII(c.b) {
			if got != strings.EqualFold(c.a, c.b) {
				t.Fatalf("EqualFold(%q, %q) = %v, strings.EqualFold differs", c.a, c.b, got)
			}
		}
		if got != ascii.EqualFold(c.b, c.a) {
			t.Fatalf("EqualFold(%q, %q) not symmetric", c.a, c.b)
		}
	}
}

func TestEqualFoldSingleByteExhaustive(t *testing.T) {
	for a := 0; a < 256; a++ {
		for b := 0; b < 256; b++ {
			sa := string([]byte{byte(a)})
			sb := string([]byte{byte(b)})
			want := asciiLowerOracle(byte(a)) == asciiLowerOracle(byte(b))
			if got := ascii.EqualFold(sa, sb); got != want {
				t.Fatalf("EqualFold single %d %d = %v, want %v", a, b, got, want)
			}
			if a < 128 && b < 128 && ascii.EqualFold(sa, sb) != strings.EqualFold(sa, sb) {
				t.Fatalf("EqualFold single %d %d disagrees with strings", a, b)
			}
		}
	}
}

func TestEqualFoldGeneratedPairs(t *testing.T) {
	for i := 0; i < 256; i++ {
		a := string([]byte{byte('a' + byte(i%26)), byte('0' + byte(i%10)), byte('A' + byte((i/26)%26))})
		b := string([]byte{byte('A' + byte(i%26)), byte('0' + byte(i%10)), byte('a' + byte((i/26)%26))})
		if got := ascii.EqualFold(a, b); !got {
			t.Fatalf("EqualFold(%q, %q) = false, want true", a, b)
		}
		if got := ascii.EqualFold(a, b); got != strings.EqualFold(a, b) {
			t.Fatalf("EqualFold(%q, %q) disagrees with strings", a, b)
		}
		c := a + "x"
		if got := ascii.EqualFold(a, c); got {
			t.Fatalf("EqualFold(%q, %q) = true, want false", a, c)
		}
	}
}

func TestEqualFoldNonASCII(t *testing.T) {
	if ascii.EqualFold("k", "K") {
		t.Fatalf("EqualFold k KELVIN = true, want false")
	}
	if !strings.EqualFold("k", "K") {
		t.Fatalf("strings.EqualFold k KELVIN = false, want true")
	}
	if ascii.EqualFold("caf\u00e9", "CAF\u00c9") {
		t.Fatalf("EqualFold cafe acute = true, want false")
	}
	if !strings.EqualFold("caf\u00e9", "CAF\u00c9") {
		t.Fatalf("strings.EqualFold cafe acute = false, want true")
	}
	if !ascii.EqualFold("caf\u00e9", "caf\u00e9") {
		t.Fatalf("EqualFold identical non-ASCII = false, want true")
	}
	if !ascii.EqualFold("\x80", "\x80") {
		t.Fatalf("EqualFold high byte identical = false, want true")
	}
	if ascii.EqualFold("\x80", "\x81") {
		t.Fatalf("EqualFold high bytes differ = true, want false")
	}
}

func TestHasPrefixFoldTable(t *testing.T) {
	cases := []struct {
		s      string
		prefix string
		want   bool
	}{
		{"boB", "Bo", true},
		{"Needle in hAyStAcK", "haystack", false},
		{"", "", true},
		{"abc", "", true},
		{"", "a", false},
		{"AbCdef", "aBC", true},
		{"AbC", "aBcD", false},
		{"HTTP/1.1 200 OK", "http/1.1", true},
		{"example.com", "EXAMPLE", true},
		{"Ex", "EXAMPLE", false},
	}
	for _, c := range cases {
		got := ascii.HasPrefixFold(c.s, c.prefix)
		if got != c.want {
			t.Fatalf("HasPrefixFold(%q, %q) = %v, want %v", c.s, c.prefix, got, c.want)
		}
		if isASCII(c.s) && isASCII(c.prefix) {
			var oracle bool
			if len(c.prefix) <= len(c.s) {
				oracle = strings.EqualFold(c.s[:len(c.prefix)], c.prefix)
			}
			if got != oracle {
				t.Fatalf("HasPrefixFold(%q, %q) disagrees with strings", c.s, c.prefix)
			}
		}
	}
}

func TestCompareFoldTable(t *testing.T) {
	cases := []struct {
		a    string
		b    string
		want int
	}{
		{"aBcD", "Bee", -1},
		{"AbC", "aBc", 0},
		{"abC", "aBc0", -1},
		{"", "", 0},
		{"", "a", -1},
		{"a", "", 1},
		{"abc", "ABC", 0},
		{"abc", "ABD", -1},
		{"ABD", "abc", 1},
		{"ab", "ABC", -1},
		{"ABC", "ab", 1},
		{"a", "B", -1},
		{"B", "a", 1},
	}
	for _, c := range cases {
		got := ascii.CompareFold(c.a, c.b)
		if got != c.want {
			t.Fatalf("CompareFold(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if (got == 0) != strings.EqualFold(c.a, c.b) {
			t.Fatalf("CompareFold(%q, %q) zero disagrees with strings", c.a, c.b)
		}
		if got != strings.Compare(strings.ToLower(c.a), strings.ToLower(c.b)) {
			t.Fatalf("CompareFold(%q, %q) disagrees with lowered compare", c.a, c.b)
		}
	}
}

func TestCompareFoldSingleByteExhaustive(t *testing.T) {
	for a := 0; a < 256; a++ {
		for b := 0; b < 256; b++ {
			sa := string([]byte{byte(a)})
			sb := string([]byte{byte(b)})
			la := asciiLowerOracle(byte(a))
			lb := asciiLowerOracle(byte(b))
			want := 0
			if la < lb {
				want = -1
			}
			if la > lb {
				want = 1
			}
			if got := ascii.CompareFold(sa, sb); got != want {
				t.Fatalf("CompareFold single %d %d = %d, want %d", a, b, got, want)
			}
			if a < 128 && b < 128 && (ascii.CompareFold(sa, sb) == 0) != strings.EqualFold(sa, sb) {
				t.Fatalf("CompareFold single %d %d zero disagrees", a, b)
			}
		}
	}
}

func TestCompareFoldConsistency(t *testing.T) {
	corpus := []string{"", "a", "A", "abc", "ABC", "ab", "aBcD", "Bee"}
	for _, a := range corpus {
		for _, b := range corpus {
			ab := ascii.CompareFold(a, b)
			ba := ascii.CompareFold(b, a)
			if ab != -ba {
				t.Fatalf("CompareFold(%q, %q) = %d, reverse = %d", a, b, ab, ba)
			}
			if (ab == 0) != ascii.EqualFold(a, b) {
				t.Fatalf("CompareFold(%q, %q) zero disagrees with EqualFold", a, b)
			}
		}
	}
}

func TestAllocsEqualFold(t *testing.T) {
	a := "HelloWorld"
	b := "hELLOWORLD"
	if n := testing.AllocsPerRun(100, func() {
		if ascii.EqualFold(a, b) {
			foldSink = 1
		} else {
			foldSink = 0
		}
	}); n != 0 {
		t.Fatalf("EqualFold allocated %v times, want 0", n)
	}
	if foldSink != 1 {
		t.Fatalf("EqualFold sink = %d, want 1", foldSink)
	}
}

func TestAllocsHasPrefixFold(t *testing.T) {
	s := "HelloWorld"
	p := "hELLo"
	if n := testing.AllocsPerRun(100, func() {
		if ascii.HasPrefixFold(s, p) {
			foldSink = 1
		} else {
			foldSink = 0
		}
	}); n != 0 {
		t.Fatalf("HasPrefixFold allocated %v times, want 0", n)
	}
	if foldSink != 1 {
		t.Fatalf("HasPrefixFold sink = %d, want 1", foldSink)
	}
}

func TestAllocsCompareFold(t *testing.T) {
	a := "HelloWorld"
	b := "hELLOWORLD"
	if n := testing.AllocsPerRun(100, func() {
		foldSink = ascii.CompareFold(a, b)
	}); n != 0 {
		t.Fatalf("CompareFold allocated %v times, want 0", n)
	}
	if foldSink != 0 {
		t.Fatalf("CompareFold sink = %d, want 0", foldSink)
	}
}

func FuzzEqualFold(f *testing.F) {
	f.Add("Hello", "hELLo")
	f.Add("", "")
	f.Add("abc", "ABD")
	f.Add("k", "K")
	f.Fuzz(func(t *testing.T, a, b string) {
		got := ascii.EqualFold(a, b)
		if len(a) != len(b) && got {
			t.Fatalf("EqualFold length mismatch true for %q %q", a, b)
		}
		if got != ascii.EqualFold(b, a) {
			t.Fatalf("EqualFold not symmetric for %q %q", a, b)
		}
		if !ascii.EqualFold(a, a) {
			t.Fatalf("EqualFold not reflexive for %q", a)
		}
		if isASCII(a) && isASCII(b) && got != strings.EqualFold(a, b) {
			t.Fatalf("EqualFold disagrees with strings for %q %q", a, b)
		}
	})
}

func FuzzHasPrefixFold(f *testing.F) {
	f.Add("boB", "Bo")
	f.Add("abc", "")
	f.Add("", "a")
	f.Add("HTTP/1.1", "http/1.1")
	f.Fuzz(func(t *testing.T, s, prefix string) {
		got := ascii.HasPrefixFold(s, prefix)
		if len(prefix) > len(s) && got {
			t.Fatalf("HasPrefixFold true with long prefix %q %q", s, prefix)
		}
		if got && !ascii.EqualFold(s[:len(prefix)], prefix) {
			t.Fatalf("HasPrefixFold true but prefix mismatch %q %q", s, prefix)
		}
		if len(prefix) <= len(s) && ascii.EqualFold(s[:len(prefix)], prefix) != got {
			t.Fatalf("HasPrefixFold inconsistent %q %q", s, prefix)
		}
		if isASCII(s) && isASCII(prefix) {
			var want bool
			if len(prefix) <= len(s) {
				want = strings.EqualFold(s[:len(prefix)], prefix)
			}
			if got != want {
				t.Fatalf("HasPrefixFold disagrees with strings %q %q", s, prefix)
			}
		}
	})
}

func FuzzCompareFold(f *testing.F) {
	f.Add("aBcD", "Bee")
	f.Add("AbC", "aBc")
	f.Add("", "a")
	f.Add("abc", "ABD")
	f.Fuzz(func(t *testing.T, a, b string) {
		got := ascii.CompareFold(a, b)
		if got != -ascii.CompareFold(b, a) {
			t.Fatalf("CompareFold not antisymmetric %q %q", a, b)
		}
		if (got == 0) != ascii.EqualFold(a, b) {
			t.Fatalf("CompareFold zero disagrees %q %q", a, b)
		}
		if isASCII(a) && isASCII(b) {
			if (got == 0) != strings.EqualFold(a, b) {
				t.Fatalf("CompareFold zero strings disagree %q %q", a, b)
			}
			if got != strings.Compare(strings.ToLower(a), strings.ToLower(b)) {
				t.Fatalf("CompareFold order strings disagree %q %q", a, b)
			}
		}
	})
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package base64_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"

	b64 "logos/encoding/base64"
)

var sinkOut []byte
var sinkN int
var sinkErr error

func patternBytes(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte(i*31 + 7)
	}
	return p
}

type codecPair struct {
	name string
	mine b64.Encoding
	std  *base64.Encoding
}

func codecPairs() []codecPair {
	return []codecPair{
		{"std", b64.Std, base64.StdEncoding},
		{"url", b64.URL, base64.URLEncoding},
		{"rawstd", b64.RawStd, base64.RawStdEncoding},
		{"rawurl", b64.RawURL, base64.RawURLEncoding},
	}
}

func pairByName(name string) codecPair {
	for _, c := range codecPairs() {
		if c.name == name {
			return c
		}
	}
	return codecPair{}
}

func TestRFC4648Section10Vectors(t *testing.T) {
	rows := []struct{ raw, enc string }{
		{"", ""},
		{"f", "Zg=="},
		{"fo", "Zm8="},
		{"foo", "Zm9v"},
		{"foob", "Zm9vYg=="},
		{"fooba", "Zm9vYmE="},
		{"foobar", "Zm9vYmFy"},
	}
	for _, r := range rows {
		if got := b64.Std.EncodeToString([]byte(r.raw)); got != r.enc {
			t.Errorf("Std.EncodeToString(%q) = %q, want %q", r.raw, got, r.enc)
		}
		if got := base64.StdEncoding.EncodeToString([]byte(r.raw)); got != r.enc {
			t.Fatalf("oracle drift for %q: %q", r.raw, got)
		}
		got, err := b64.Std.DecodeString(r.enc)
		if err != nil || string(got) != r.raw {
			t.Errorf("Std.DecodeString(%q) = %q, %v; want %q", r.enc, got, err, r.raw)
		}
	}
}

func TestFullByteAlphabetPoints(t *testing.T) {
	full := []byte{0xff, 0xff, 0xff}
	if got := b64.Std.EncodeToString(full); got != "////" {
		t.Errorf("Std full = %q, want ////", got)
	}
	if got := b64.URL.EncodeToString(full); got != "____" {
		t.Errorf("URL full = %q, want ____", got)
	}
	if got := b64.RawStd.EncodeToString(full); got != "////" {
		t.Errorf("RawStd full = %q, want ////", got)
	}
	if got := b64.RawURL.EncodeToString(full); got != "____" {
		t.Errorf("RawURL full = %q, want ____", got)
	}
	two := []byte{0xff, 0xff}
	if want := base64.StdEncoding.EncodeToString(two); want != "//8=" {
		t.Fatalf("oracle drift for two 0xff bytes: %q", want)
	}
	if got := b64.Std.EncodeToString(two); got != "//8=" {
		t.Errorf("Std two = %q, want //8=", got)
	}
	if got := b64.RawStd.EncodeToString(two); got != "//8" {
		t.Errorf("RawStd two = %q, want //8", got)
	}
	for _, c := range codecPairs() {
		if got, want := c.mine.EncodeToString(full), c.std.EncodeToString(full); got != want {
			t.Errorf("%s full = %q, oracle %q", c.name, got, want)
		}
	}
}

func TestZigCarriedVectors(t *testing.T) {
	stdRows := []struct{ raw, enc string }{
		{"foob", "Zm9vYg=="},
		{"fooba", "Zm9vYmE="},
		{"foobar", "Zm9vYmFy"},
		{"foobarfoobarfoo", "Zm9vYmFyZm9vYmFyZm9v"},
		{"foobarfoobarfoob", "Zm9vYmFyZm9vYmFyZm9vYg=="},
		{"foobarfoobarfooba", "Zm9vYmFyZm9vYmFyZm9vYmE="},
		{"foobarfoobarfoobar", "Zm9vYmFyZm9vYmFyZm9vYmFy"},
	}
	for _, r := range stdRows {
		if got := b64.Std.EncodeToString([]byte(r.raw)); got != r.enc {
			t.Errorf("Std(%q) = %q, want %q", r.raw, got, r.enc)
		}
		back, err := b64.Std.DecodeString(r.enc)
		if err != nil || string(back) != r.raw {
			t.Errorf("Std decode %q = %q, %v", r.enc, back, err)
		}
	}
	rawRows := []struct{ raw, enc string }{
		{"f", "Zg"},
		{"fo", "Zm8"},
		{"foo", "Zm9v"},
		{"foob", "Zm9vYg"},
		{"fooba", "Zm9vYmE"},
		{"foobar", "Zm9vYmFy"},
		{"foobarfoobarfoobar", "Zm9vYmFyZm9vYmFyZm9vYmFy"},
	}
	for _, r := range rawRows {
		if got := b64.RawURL.EncodeToString([]byte(r.raw)); got != r.enc {
			t.Errorf("RawURL(%q) = %q, want %q", r.raw, got, r.enc)
		}
		back, err := b64.RawStd.DecodeString(r.enc)
		if err != nil || string(back) != r.raw {
			t.Errorf("RawStd decode %q = %q, %v", r.enc, back, err)
		}
	}
}

func TestEncodeMatchesStdlib(t *testing.T) {
	lens := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 15, 16, 17, 31, 32, 33, 57, 64, 100, 255, 1024}
	for _, c := range codecPairs() {
		for _, n := range lens {
			src := patternBytes(n)
			if got, want := c.mine.EncodeToString(src), c.std.EncodeToString(src); got != want {
				t.Errorf("%s len %d: %q vs oracle %q", c.name, n, got, want)
			}
		}
	}
}

func TestRoundTripMatchesStrict(t *testing.T) {
	lens := []int{0, 1, 2, 3, 4, 5, 16, 64, 255}
	for _, c := range codecPairs() {
		for _, n := range lens {
			src := patternBytes(n)
			enc := c.mine.EncodeToString(src)
			dst := make([]byte, c.mine.DecodedLen(len(enc)))
			m, err := c.mine.Decode(dst, []byte(enc))
			if err != nil || !bytes.Equal(dst[:m], src) {
				t.Errorf("%s len %d round trip = %d, %v", c.name, n, m, err)
			}
			want, werr := c.std.Strict().DecodeString(enc)
			if werr != nil || !bytes.Equal(dst[:m], want) {
				t.Errorf("%s len %d differs from strict oracle", c.name, n)
			}
		}
	}
}

func TestEncodedLenExact(t *testing.T) {
	for _, c := range codecPairs() {
		for n := 0; n < 136; n++ {
			src := patternBytes(n)
			dst := make([]byte, c.mine.EncodedLen(n))
			got := c.mine.Encode(dst, src)
			if len(got) != c.mine.EncodedLen(n) {
				t.Fatalf("%s len %d: returned %d, EncodedLen %d", c.name, n, len(got), c.mine.EncodedLen(n))
			}
			if want := c.std.EncodeToString(src); string(got) != want {
				t.Fatalf("%s len %d bytes differ from oracle", c.name, n)
			}
		}
	}
}

func TestDecodedLenBound(t *testing.T) {
	for _, c := range codecPairs() {
		for n := 0; n < 136; n++ {
			enc := c.mine.EncodeToString(patternBytes(n))
			bound := c.mine.DecodedLen(len(enc))
			if bound < n {
				t.Errorf("%s len %d: bound %d below %d", c.name, n, bound, n)
			}
			dst := make([]byte, bound)
			m, err := c.mine.Decode(dst, []byte(enc))
			if err != nil || m != n {
				t.Errorf("%s len %d: decoded %d, %v", c.name, n, m, err)
			}
		}
	}
}

func TestStrictRejectClasses(t *testing.T) {
	rows := []struct {
		codec string
		input string
	}{
		{"std", "A"},
		{"std", "AA"},
		{"std", "AAA"},
		{"std", "A..A"},
		{"std", "AA=A"},
		{"std", "AA/="},
		{"std", "A/=="},
		{"std", "A==="},
		{"std", "===="},
		{"std", "Zm9vYmFyZm9vYmFyA..A"},
		{"std", "Zm9vYmFyZm9vYmFyAA=A"},
		{"std", "Zg==Zg=="},
		{"std", "Zm8=AAA="},
		{"std", "--__"},
		{"url", "++//"},
		{"url", "A"},
		{"rawstd", "Zg=="},
		{"rawstd", "AAA="},
		{"rawstd", "A"},
		{"rawstd", "AAAAA"},
		{"rawstd", "A..A"},
		{"rawstd", "__"},
		{"rawurl", "////"},
		{"rawurl", "Zg=="},
		{"rawurl", "A"},
	}
	for _, r := range rows {
		c := pairByName(r.codec)
		if _, err := c.std.Strict().DecodeString(r.input); err == nil {
			t.Fatalf("oracle accepts %s %q, row is wrong", r.codec, r.input)
		}
		dst := make([]byte, c.mine.DecodedLen(len(r.input)))
		_, err := c.mine.Decode(dst, []byte(r.input))
		if err == nil {
			t.Errorf("%s accepts %q, want error", r.codec, r.input)
			continue
		}
		var cerr b64.CorruptInputError
		if !errors.As(err, &cerr) {
			t.Errorf("%s %q error type %T, want CorruptInputError", r.codec, r.input, err)
		}
	}
}

func TestStrictTrailingBitsRejected(t *testing.T) {
	rows := []struct {
		codec string
		input string
	}{
		{"std", "AB=="},
		{"std", "AAF="},
		{"url", "AB=="},
		{"rawstd", "AF"},
		{"rawstd", "AAF"},
		{"rawurl", "AF"},
	}
	for _, r := range rows {
		c := pairByName(r.codec)
		if _, err := c.std.DecodeString(r.input); err != nil {
			t.Fatalf("non-strict oracle rejects %s %q, row is wrong", r.codec, r.input)
		}
		if _, err := c.std.Strict().DecodeString(r.input); err == nil {
			t.Fatalf("strict oracle accepts %s %q, row is wrong", r.codec, r.input)
		}
		dst := make([]byte, c.mine.DecodedLen(len(r.input)))
		if _, err := c.mine.Decode(dst, []byte(r.input)); err == nil {
			t.Errorf("%s accepts %q, want strict error", r.codec, r.input)
		}
	}
}

func TestNewlinesIgnored(t *testing.T) {
	rows := []struct {
		codec string
		input string
		want  string
	}{
		{"std", "Zm9v\nYmFy", "foobar"},
		{"std", "Zg==\r\n", "f"},
		{"std", "\nZ\nm\n8\n=\n", "fo"},
		{"rawstd", "Z\ng", "f"},
		{"rawstd", "Zm9v\nYmFy", "foobar"},
		{"url", "__\n__", "\xff\xff\xff"},
	}
	for _, r := range rows {
		c := pairByName(r.codec)
		got, err := c.mine.DecodeString(r.input)
		if err != nil || string(got) != r.want {
			t.Errorf("%s %q = %q, %v; want %q", r.codec, r.input, got, err, r.want)
		}
		if want, werr := c.std.Strict().DecodeString(r.input); werr != nil || string(want) != r.want {
			t.Fatalf("oracle differs for %s %q", r.codec, r.input)
		}
	}
}

func TestEncodeDecodeZeroAlloc(t *testing.T) {
	src := patternBytes(64)
	for _, c := range codecPairs() {
		dst := make([]byte, c.mine.EncodedLen(len(src)))
		if a := testing.AllocsPerRun(100, func() {
			sinkOut = c.mine.Encode(dst, src)
		}); a != 0 {
			t.Errorf("%s Encode allocates %v times, want 0", c.name, a)
		}
		enc := []byte(c.mine.EncodeToString(src))
		dbuf := make([]byte, c.mine.DecodedLen(len(enc)))
		if a := testing.AllocsPerRun(100, func() {
			m, err := c.mine.Decode(dbuf, enc)
			sinkN, sinkErr = m, err
		}); a != 0 {
			t.Errorf("%s Decode allocates %v times, want 0", c.name, a)
		}
	}
}

func TestShortDestinationPanics(t *testing.T) {
	mustPanic := func(t *testing.T, name string, f func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic", name)
			}
		}()
		f()
	}
	mustPanic(t, "Encode", func() {
		b64.Std.Encode(make([]byte, 3), []byte("foo"))
	})
	mustPanic(t, "Decode", func() {
		b64.Std.Decode(make([]byte, 2), []byte("Zm9v"))
	})
}

func TestStringHelpersMatchStdlib(t *testing.T) {
	for _, c := range codecPairs() {
		for _, n := range []int{0, 1, 2, 3, 5, 64} {
			src := patternBytes(n)
			if got, want := c.mine.EncodeToString(src), c.std.EncodeToString(src); got != want {
				t.Errorf("%s EncodeToString len %d = %q, want %q", c.name, n, got, want)
			}
			enc := c.std.EncodeToString(src)
			got, err := c.mine.DecodeString(enc)
			want, werr := c.std.Strict().DecodeString(enc)
			if (err != nil) != (werr != nil) || !bytes.Equal(got, want) {
				t.Errorf("%s DecodeString len %d = %d, %v", c.name, n, len(got), err)
			}
		}
		if _, err := c.mine.DecodeString("A..A"); err == nil {
			t.Errorf("%s DecodeString accepts bad input", c.name)
		}
	}
}

func TestMisplacedPaddingOffset(t *testing.T) {
	inputs := []string{"A=", "A===", "====", "=ABC", "A=C=", "Zm9vA="}
	for _, in := range inputs {
		_, stdErr := base64.StdEncoding.Strict().DecodeString(in)
		var want base64.CorruptInputError
		if !errors.As(stdErr, &want) {
			t.Fatalf("oracle error for %q is %T, want CorruptInputError", in, stdErr)
		}
		dst := make([]byte, b64.Std.DecodedLen(len(in)))
		_, err := b64.Std.Decode(dst, []byte(in))
		var got b64.CorruptInputError
		if !errors.As(err, &got) {
			t.Fatalf("Decode(%q) error type %T, want CorruptInputError", in, err)
		}
		if int64(got) != int64(want) {
			t.Errorf("Decode(%q) offset = %d, want oracle offset %d", in, int64(got), int64(want))
		}
	}
}

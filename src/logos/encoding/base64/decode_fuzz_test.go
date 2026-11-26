// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package base64_test

import (
	"bytes"
	"testing"
)

func FuzzDecodeAgreesWithStrictStdlib(f *testing.F) {
	seeds := []string{
		"", "Zg==", "Zm8=", "Zm9v", "AB==", "AAF=", "A..A",
		"AA=A", "Zg", "____", "////", "Zg==\n", "--__", "AF",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, c := range codecPairs() {
			want, werr := c.std.Strict().DecodeString(s)
			dst := make([]byte, c.mine.DecodedLen(len(s)))
			gotN, err := c.mine.Decode(dst, []byte(s))
			if (werr != nil) != (err != nil) {
				t.Fatalf("%s(%q): strict err %v, mine %v", c.name, s, werr, err)
			}
			if werr == nil && !bytes.Equal(dst[:gotN], want) {
				t.Fatalf("%s(%q) bytes differ", c.name, s)
			}
		}
	})
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"strings"
	"testing"
)

func TestBinaryZeroWireHelpersHaveNoElementLoop(t *testing.T) {
	text := lowerStaticText(t, "package p\n\ntype ZeroEmpty struct{}\n\ntype ZeroHolder struct {\n\tItems []ZeroEmpty `json:\"items\"`\n}\n")
	for _, kind := range []string{"_logos_bin_ZeroHolder_e", "_logos_bin_ZeroHolder_d"} {
		rest := text
		found := 0
		for {
			idx := strings.Index(rest, "func "+kind)
			if idx < 0 {
				break
			}
			found++
			headEnd := strings.Index(rest[idx:], " {")
			if headEnd < 0 {
				t.Fatalf("helper %q has no head", kind)
			}
			head := rest[idx : idx+headEnd+2]
			body := methodBody(t, text, head)
			for line := range strings.SplitSeq(body, "\n") {
				trimmed := strings.TrimLeft(line, " \t")
				if strings.HasPrefix(trimmed, "for ") || strings.HasPrefix(trimmed, "for\t") || strings.HasPrefix(trimmed, "for{") {
					t.Errorf("zero-wire helper %q iterates: %q", head, line)
				}
			}
			rest = rest[idx+len(head):]
		}
		if found == 0 {
			t.Errorf("no %s helpers generated", kind)
		}
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cfg

import (
	"testing"

	"golang.org/x/text/unicode/norm"
)

func TestIsLogosFileExtensionNormalization(t *testing.T) {
	exts := []string{".lgo", ".📜", ".⚡", ".ʕ◔ϖ◔ʔ"}
	for _, ext := range exts {
		if got := norm.NFC.String(ext); got != ext {
			t.Errorf("NFC(%q) = %q, want identity", ext, got)
		}
		if !IsLogosFile("main" + ext) {
			t.Errorf("IsLogosFile(%q) = false, want true", "main"+ext)
		}
		for _, vs := range []string{"\uFE0E", "\uFE0F", "\uFE0E\uFE0F", "\uFE0F\uFE0E"} {
			if name := "main" + ext + vs; !IsLogosFile(name) {
				t.Errorf("IsLogosFile(%q) = false, want true", name)
			}
		}
	}
	for _, name := range []string{"main.go", "main.txt", "main.ʕ◔π◔ʔ", "main.⚡x", "x.lgo.bak"} {
		if IsLogosFile(name) {
			t.Errorf("IsLogosFile(%q) = true, want false", name)
		}
	}
}

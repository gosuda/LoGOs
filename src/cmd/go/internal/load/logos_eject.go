// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package load

import (
	"path/filepath"
	"strings"
)

var logosEjectDir string

func ArmLogosEject(dir string) {
	logosEjectDir = dir
}

func DisarmLogosEject() {
	logosEjectDir = ""
}

func logosEjectGcflags(p *Package) []string {
	if logosEjectDir == "" || !p.IsLogos() {
		return nil
	}
	if p.Standard && !isEjectableGorootLogos(p.ImportPath) {
		return nil
	}
	p.Internal.LogosEject = true
	return []string{"-logos.eject=" + filepath.Join(logosEjectDir, filepath.FromSlash(p.ImportPath))}
}

// isEjectableGorootLogos reports whether a standard import path is a
// GOROOT logos/* package whose lowered output the eject support module
// may need. Only the fork's own tree is ejectable; every other standard
// package is already plain Go.
func isEjectableGorootLogos(path string) bool {
	rest, ok := strings.CutPrefix(path, "logos/")
	return ok && rest != ""
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package types2

import (
	"path/filepath"
	"strings"

	"cmd/compile/internal/syntax"
)

func (check *Checker) isLogosFile(at poser) bool {
	return check.conf.Logos && !logosGoModePos(at.Pos())
}

func logosGoModePos(pos syntax.Pos) bool {
	name := filepath.Base(pos.FileBase().Filename())
	return strings.HasPrefix(name, "_cgo_") && strings.HasSuffix(name, ".go")
}

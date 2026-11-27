// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gc

import (
	"cmd/compile/internal/base"
	"cmd/internal/objabi"
)

// rememberSanitizerRequests snapshots the requested sanitizer mode before the
// NoInstrument exemption below clears the flags for runtime packages.
// Generated equality payloads are shared across every package, so they key
// their explicit operand checks off the requested mode.
func rememberSanitizerRequests() {
	base.Flag.Cfg.ASanRequested = base.Flag.ASan
	base.Flag.Cfg.MSanRequested = base.Flag.MSan
	if objabi.LookupPkgSpecial(base.Ctxt.Pkgpath).NoInstrument {
		base.Flag.Race = false
		base.Flag.MSan = false
		base.Flag.ASan = false
	}
}

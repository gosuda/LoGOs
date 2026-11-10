// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package gc

import (
	"cmd/compile/internal/base"
	"cmd/compile/internal/logos/facts"
	"cmd/compile/internal/logos/wasmyield"
	"cmd/internal/bio"
)

func hasLogosAssertFacts() bool {
	f := facts.Current()
	if f == nil {
		return false
	}
	for i := range f.Fns {
		if f.Fns[i].HasAssert {
			return true
		}
	}
	return false
}

func hasLogosLintFacts() bool {
	f := facts.Current()
	if f == nil {
		return false
	}
	if len(f.AllocFrees) != 0 || len(f.Locks) != 0 {
		return true
	}
	for i := range f.Fns {
		if f.Fns[i].AlwaysNilError {
			return true
		}
	}
	return false
}

func writeLogosFacts(bout *bio.Writer) {
	if !base.Logos && !wasmyield.Active() && !hasLogosAssertFacts() && !hasLogosLintFacts() {
		return
	}
	start := startArchiveEntry(bout)
	bout.Write(facts.Encode(base.Ctxt.Fingerprint, facts.Current()))
	finishArchiveEntry(bout, start, facts.MemberName)
}

func logosWasmFinish() {
	wasmyield.Finish()
}

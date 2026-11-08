// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ld

import (
	"internal/abi"
	"strings"
)

var logosCloneInfixes = []string{
	abi.LogosCtDisplayInfix,
	abi.LogosCtASCIIInfix,
}

// Naming rule (SPEC T-205, red-team #9, #74): pclntab
// (runtime.funcnametab) strips every terminal clone suffix to the base so
// traces print <f> and FuncForPC resolves to the base name, while the
// link symbol and the symbol table (go tool nm) keep the full canonical
// suffix, so every clone stays stable, unique, and resolvable at link
// time. One generateFuncnametab path in pcln.go serves shared, plugin,
// and internal links, so the base display survives every link mode;
// logosCloneBaseName performs the identical strip for capability sink
// identity (capSinkName), and the two must stay identical.
//
// logosCloneBaseName strips every terminal clone suffix to the base: the
// middle-dot infix ·logosct and the source-level comptime infix _logosct,
// each with a nonempty ASCII decimal sequence number, preserving package
// path, method receiver, and generic shape spelling. The infix values are
// the single source of truth in internal/abi (Logos*Infix); the ASCII
// spelling must stay in sync with cfCopyInfix in
// cmd/compile/internal/logos/comptime.go. The dotted ASCII form
// ".logosct" is ordinary text and is never a suffix.
func logosCloneBaseName(name string) string {
	orig := name
	for {
		i := len(name)
		for i > 0 && name[i-1] >= '0' && name[i-1] <= '9' {
			i--
		}
		if i == len(name) {
			return name
		}
		prefix := name[:i]
		var base string
		stripped := false
		for _, infix := range logosCloneInfixes {
			if b, ok := strings.CutSuffix(prefix, infix); ok {
				base, stripped = b, true
				break
			}
		}
		if !stripped {
			return name
		}
		if base == "" {
			return orig
		}
		name = base
	}
}

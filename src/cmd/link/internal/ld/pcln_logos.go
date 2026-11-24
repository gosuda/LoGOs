// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ld

import (
	"internal/abi"
	"strings"

	"cmd/internal/goobj"
	"cmd/link/internal/loader"
)

func logosCloneBaseName(name string) string {
	var base []byte
	rest := name
	for {
		i := strings.Index(rest, abi.LogosCtInstanceInfix)
		if i < 0 {
			break
		}
		id := rest[i+len(abi.LogosCtInstanceInfix):]
		if len(base)+i == 0 || !logosCtInstanceID(id) {
			base = append(base, rest[:i+len(abi.LogosCtInstanceInfix)]...)
			rest = id
			continue
		}
		base = append(base, rest[:i]...)
		rest = id[abi.LogosCtInstanceIDLen:]
	}
	if base == nil {
		return name
	}
	return string(append(base, rest...))
}

func logosCtInstanceID(s string) bool {
	if len(s) < abi.LogosCtInstanceIDLen {
		return false
	}
	for _, c := range []byte(s[:abi.LogosCtInstanceIDLen]) {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return len(s) == abi.LogosCtInstanceIDLen || s[abi.LogosCtInstanceIDLen] == '.'
}

func logosDisplayName(ldr *loader.Loader, s loader.Sym) string {
	name := ldr.SymName(s)
	if ldr.SymAttr(s)&goobj.SymFlagLogosCtClone != 0 {
		return logosCloneBaseName(name)
	}
	return name
}

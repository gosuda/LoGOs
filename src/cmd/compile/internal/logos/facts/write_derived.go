// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package facts

import (
	"cmd/compile/internal/types2"
)

// writeDeriveds emits one `derived` record per exported package-scope
// variable the checker stamped holding a Go-derived value, so importers
// keep the platform-pointer checks the producer applied.
func writeDeriveds(pkg *types2.Package, f *Facts) {
	scope := pkg.Scope()
	if scope == nil {
		return
	}
	for _, name := range scope.Names() {
		if types2.LogosPkgDerived(pkg, name) {
			f.Deriveds = append(f.Deriveds, name)
		}
	}
}

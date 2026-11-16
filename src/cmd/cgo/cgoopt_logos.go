// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import "internal/buildcfg"

// logosStableCgoFrame reports whether the result frame stays at its original
// address until the C wrapper returns. Without a callback, cgocall remains in
// syscall state throughout that interval, preventing stack copying or shrinking.
// This does not prove noescape or change the runtime entry and callback guards.
func logosStableCgoFrame(p *Package, n *Name) bool {
	if *gccgo {
		return false
	}
	return logosStableFastCall(p, n) ||
		buildcfg.Experiment.LogosCgoOpt && p.noCallbacks[n.C]
}

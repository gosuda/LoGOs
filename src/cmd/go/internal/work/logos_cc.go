// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package work

import (
	"cmp"

	"cmd/go/internal/cfg"
)

// logosCompilerFallback resolves the CC/CXX compiler for a cgo build
// through the cfg zig fallback ("zig cc -target <triple>" or
// "zig c++ -target <triple>"), which honors the startup snapshots. Plain
// envList callers keep their behavior for every other key.
func logosCompilerFallback(key, def string) string {
	if key == "CXX" {
		return cfg.ZigCXX(def)
	}
	return cfg.ZigCC(def)
}

// logosCgoToolEnv returns CC/CXX overrides for the cgo tool's environment
// carrying the resolved zig fallback commands, or nil when the fallback is
// inactive. The cgo tool resolves its own compiler from its $CC (see
// checkGCCBaseCmd in cmd/cgo), ignoring the Go-side envList resolution, so
// without these entries it rejects a hidden default compiler before the
// fallback can run. Callers append the result after the base environment so
// it wins over the MkEnv-injected CC default; an explicit user CC/CXX (or
// a present default compiler, or disabled cgo) resolves unchanged and
// yields no override, and "go env CC" output is untouched.
func logosCgoToolEnv() []string {
	var env []string
	cc := cmp.Or(cfg.Getenv("CC"), cfg.DefaultCC(cfg.Goos, cfg.Goarch))
	if fb := cfg.ZigCC(cc); fb != cc {
		env = append(env, "CC="+fb)
	}
	cxx := cmp.Or(cfg.Getenv("CXX"), cfg.DefaultCXX(cfg.Goos, cfg.Goarch))
	if fb := cfg.ZigCXX(cxx); fb != cxx {
		env = append(env, "CXX="+fb)
	}
	return env
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package base

import "flag"

var LogosEject string

// LogosEjectCheck runs only the eject portability verifier, without
// emitting ejected sources. Plain Go packages in the ejected module
// get this flag so a fork-only selector fails the export before any
// output is written, while their compilation is otherwise native.
var LogosEjectCheck bool

var LogosCgoEject bool

var Logos bool

var LogosMode string

var LogosHintsOut string

var LogosNextOut string

var LogosCfg string

var LogosJSON bool

var LogosLint bool

var LogosLintOnly bool

var LogosLintOwned bool

var LogosAllocFacts bool
var LogosGoFacts bool

var LogosComptimeNodes = 4096

func LogosLintEnabled() bool {
	return LogosLint || LogosLintOnly
}

func logosRegisterFlags() {
	flag.StringVar(&LogosCfg, "logos.cfg", "", "read the lint policy from `file`")
	flag.BoolVar(&LogosJSON, "logos.json", false, "print diagnostics as JSON lines")
	flag.BoolVar(&LogosLint, "logos.lint", false, "lint the Go package while compiling")
	flag.BoolVar(&LogosLintOnly, "logos.lintonly", false, "lint the package and stop after escape analysis")
	flag.BoolVar(&LogosLintOwned, "logos.lintowned", false, "leave lint reporting to a separate lint action")
	flag.BoolVar(&LogosAllocFacts, "logos.allocfacts", false, "record per-function heap allocations and write AllocFree facts")
	flag.BoolVar(&LogosGoFacts, "logos.gofacts", false, "record per-parameter mutation facts for Go packages")
	flag.StringVar(&LogosEject, "logos.eject", "", "write ejected Go sources to `directory`")
	flag.BoolVar(&LogosEjectCheck, "logos.eject_check", false, "verify ejected portability without emitting sources")
	flag.BoolVar(&LogosCgoEject, "logos.cgoeject", false, "check and eject original cgo sources without emitting an object")
	flag.StringVar(&LogosMode, "logos.mode", "", "run a logos helper mode (`next`, `context`, or `hints`)")
	flag.StringVar(&LogosHintsOut, "logos.hintsout", "", "write the logos editor hints of the package to `file`")
	flag.StringVar(&LogosNextOut, "logos.nextout", "", "append logos next scan results to `file`")
	flag.IntVar(&LogosComptimeNodes, "logos.comptime_nodes", 4096, "cap expanded nodes per comptime expansion")
}

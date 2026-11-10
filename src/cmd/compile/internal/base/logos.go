// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package base

import (
	"flag"
	"maps"
	"strings"
)

var LogosEject string

var Logos bool

var LogosMode string

var LogosHintsOut string

var LogosNextOut string

var LogosCfg string

var LogosJSON bool

var LogosLintOnly bool

var LogosAllocFacts bool
var LogosGoFacts bool

var LogosComptimeNodes = 4096

var logosCtClones = map[string]bool{}

func RegisterLogosCtClone(pkgPath, name string) {
	logosCtClones[pkgPath+"\x00"+name] = true
}

func IsLogosCtClone(pkgPath, name string) bool {
	return logosCtClones[pkgPath+"\x00"+name]
}

func ClearLogosCtClones(pkgPath string) {
	maps.DeleteFunc(logosCtClones, func(k string, _ bool) bool {
		return strings.HasPrefix(k, pkgPath+"\x00")
	})
}

func logosRegisterFlags() {
	flag.StringVar(&LogosCfg, "logos.cfg", "", "read the lint policy from `file`")
	flag.BoolVar(&LogosJSON, "logos.json", false, "print diagnostics as JSON lines")
	flag.BoolVar(&LogosLintOnly, "logos.lintonly", false, "lint the package and stop after escape analysis")
	flag.BoolVar(&LogosAllocFacts, "logos.allocfacts", false, "record per-function heap allocations and write AllocFree facts")
	flag.BoolVar(&LogosGoFacts, "logos.gofacts", false, "record per-parameter mutation facts for Go packages")
	flag.StringVar(&LogosEject, "logos.eject", "", "write ejected Go sources to `directory`")
	flag.StringVar(&LogosMode, "logos.mode", "", "run a logos helper mode (`next`, `context`, or `hints`)")
	flag.StringVar(&LogosHintsOut, "logos.hintsout", "", "write the logos editor hints of the package to `file`")
	flag.StringVar(&LogosNextOut, "logos.nextout", "", "append logos next scan results to `file`")
	flag.IntVar(&LogosComptimeNodes, "logos.comptime_nodes", 4096, "cap expanded nodes per comptime expansion")
}

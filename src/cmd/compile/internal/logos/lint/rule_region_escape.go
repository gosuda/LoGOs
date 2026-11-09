// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lint

// regionEscape is correctness::region_escape (SPEC Sec 3.1, T-301.2): a
// Region handle, or a struct or array containing one, leaving the block
// that owns it. The types2 checker reports it while the package checks
// (M18-S7, types2/region_logos.go), before Run is called. The entry has
// no Check so the finding is reported once, and it carries the group and
// scope so severity resolution and the directive checks treat it like
// every other rule. It never applies to Go packages, which hold no
// regions.
var regionEscape = Rule{
	ID:    "correctness::region_escape",
	Group: Correctness,
	Go:    false,
}

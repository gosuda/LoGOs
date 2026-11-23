// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package modload

import (
	"maps"

	"cmd/go/internal/logos/settings"
)

// logosScanTags returns the tag set scanDir judges a package's files
// with. Under the race-runtime cgo exception the package loader gives
// "net" and "os/user" a per-package build context (see
// load.logosBuildContextFor); the "all" pattern's dependency scan must
// see the same files, or a dependency only the pure-Go variant imports
// is missing from "all".
func logosScanTags(importPath string, tags map[string]bool) map[string]bool {
	if !settings.Current().Cgo.RaceRuntimeOnly {
		return tags
	}
	var adjusted map[string]bool
	ensure := func(tag string) {
		if adjusted == nil {
			adjusted = maps.Clone(tags)
		}
		adjusted[tag] = true
	}
	drop := func(tag string) {
		if adjusted == nil {
			adjusted = maps.Clone(tags)
		}
		delete(adjusted, tag)
	}
	switch importPath {
	case "net":
		switch {
		case tags["netgo"] && tags["netcgo"]:
		case tags["netcgo"]:
			drop("netcgo")
			ensure("netgo")
		case !tags["netgo"]:
			ensure("netgo")
		}
	case "os/user":
		if !tags["osusergo"] {
			ensure("osusergo")
		}
	}
	if adjusted == nil {
		return tags
	}
	return adjusted
}

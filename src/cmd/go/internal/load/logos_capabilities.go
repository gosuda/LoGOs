// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package load

import (
	"fmt"
	"os"
	"slices"
	"strings"

	"cmd/go/internal/cfg"
	"cmd/go/internal/logos/capabilities"
	"cmd/go/internal/modload"
)

func checkLogosLoaded(ld *modload.Loader, all []*Package) {
	checkLogosModules(all)
	checkLogosCapabilities(ld, all)
	noteLogosBuildModules(all)
}

func noteLogosBuildModules(all []*Package) {
	if !cfg.Logos {
		return
	}
	for _, p := range all {
		if p.Module != nil {
			capabilities.NoteBuildModule(p.Module.Path)
		}
	}
}

func checkLogosCapabilities(ld *modload.Loader, all []*Package) {
	if !cfg.Logos {
		return
	}
	cf, err := logosConfig(ld)
	sev := capabilities.Deny
	var allowlist []string
	if err == nil && cf != nil {
		sev = capabilities.Severity(cf.Capabilities.TransitiveUnsafe)
		allowlist = cf.Capabilities.TransitiveUnsafeAllowlist
	}
	if sev == capabilities.Allow {
		return
	}
	nodes := make([]capabilities.Node, 0, len(all))
	for _, p := range all {
		if p.Internal.TestmainGo != nil {
			continue
		}
		nodes = append(nodes, capabilities.Node{
			ImportPath: p.ImportPath,
			Imports:    p.Imports,
			Goroot:     p.Goroot || p.Standard,
			Main:       capIsMain(p),
		})
	}
	violations := capabilities.CheckUnsafe(sev, allowlist, nodes)
	if len(violations) == 0 {
		return
	}
	if sev == capabilities.Warn {
		for _, v := range violations {
			fmt.Fprintln(os.Stderr, capabilities.FormatWarning(v))
		}
		return
	}
	var msgs []string
	for _, v := range violations {
		msgs = append(msgs, capabilities.FormatViolation(v))
	}
	if carrier := firstMainPackage(all); carrier != nil {
		carrier.Error = &PackageError{Err: fmt.Errorf("%s", strings.Join(msgs, "\n"))}
		carrier.Incomplete = true
	}
}

func capIsMain(p *Package) bool {
	if p.ImportPath == "command-line-arguments" {
		return true
	}
	return p.Module != nil && p.Module.Main
}

func firstMainPackage(all []*Package) *Package {
	var mains []*Package
	for _, p := range all {
		if p.Internal.TestmainGo != nil || !capIsMain(p) || p.Error != nil {
			continue
		}
		mains = append(mains, p)
	}
	if len(mains) == 0 {
		return nil
	}
	slices.SortFunc(mains, func(a, b *Package) int { return strings.Compare(a.ImportPath, b.ImportPath) })
	return mains[0]
}

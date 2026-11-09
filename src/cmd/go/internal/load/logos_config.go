// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package load

import (
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"cmd/go/internal/base"
	"cmd/go/internal/cfg"
	"cmd/go/internal/logos/capabilities"
	"cmd/go/internal/logos/config"
	"cmd/go/internal/logos/settings"
	"cmd/go/internal/modload"
	"golang.org/x/mod/module"
)

// checkLogosConfig applies logos.toml to one loaded package in logos mode.
// Plain go never reaches it. Only packages in a main module are checked,
// using that module's own root and its own logos.toml; dependency modules
// keep defaults and are never checked. Parse and validation failures become
// a package error carrying the exact config message on the first package of
// that module only, and a loGo/s source file whose extension is not in that
// module's strict_files fails with [correctness::extension_not_enabled].
// The generated test main and the standard library stay out of the census,
// mirroring checkLogosModules.
func checkLogosConfig(ld *modload.Loader, p *Package) {
	if !cfg.Logos || p.Internal.TestmainGo != nil || p.Standard {
		return
	}
	if p.Module == nil {
		return
	}
	if ld != nil && ld.MainModules != nil {
		if !ld.MainModules.Contains(p.Module.Path) {
			return
		}
	} else if !p.Module.Main {
		return
	}
	root := logosConfigRootForPackage(ld, p)
	m := settings.ModuleAt(root)
	if m.Err != nil {
		isNoGo := false
		if p.Error != nil {
			_, isNoGo = errors.AsType[*NoGoError](p.Error.Err)
		}
		if m.ClaimError() && (p.Error == nil || isNoGo) {
			p.Error = &PackageError{Err: m.Err}
			p.Incomplete = true
		}
		return
	}
	for _, files := range [][]string{p.GoFiles, p.CgoFiles, p.TestGoFiles, p.XTestGoFiles} {
		for _, name := range files {
			if !cfg.IsLogosFile(name) {
				continue
			}
			ext := logosConfigExt(name)
			if !slices.Contains(m.File.Project.StrictFiles, ext) {
				if p.Error == nil {
					p.Error = &PackageError{Err: fmt.Errorf("[correctness::extension_not_enabled] %s: extension %s is not enabled by logos.toml strict_files", filepath.Base(name), ext)}
					p.Incomplete = true
				}
				return
			}
		}
	}
}

// logosConfig loads the logos.toml of the main module containing the current
// directory, or defaults when there is no such module.
func logosConfig(ld *modload.Loader) (*config.File, error) {
	m := settings.ModuleAt(logosConfigRoot(ld))
	return m.File, m.Err
}

// MainModuleLinkPolicy resolves the linker capabilities inputs for one
// build: the logos.toml of the main module containing the current
// directory (the same config the load-phase audit enforces, never the
// linked dependency's own file) and the sorted paths of every modload
// main module. A config load failure returns the error; the load phase
// already reports it before linking.
func MainModuleLinkPolicy(ld *modload.Loader) (*config.File, []string, error) {
	cf, err := logosConfig(ld)
	if err != nil {
		return nil, nil, err
	}
	var mains []string
	if ld != nil && ld.MainModules != nil {
		for _, m := range ld.MainModules.Versions() {
			mains = append(mains, m.Path)
		}
	}
	slices.Sort(mains)
	return cf, mains, nil
}

// logosConfigRootForPackage resolves the module root whose logos.toml
// governs p: the ModRoot of p's own main module. It falls back to the
// module Dir recorded on the package and then to the CWD-based root.
func logosConfigRootForPackage(ld *modload.Loader, p *Package) string {
	if p.Module != nil {
		if ld != nil && ld.MainModules != nil {
			if root := ld.MainModules.ModRoot(module.Version{Path: p.Module.Path, Version: p.Module.Version}); root != "" {
				return root
			}
		}
		if p.Module.Main && p.Module.Dir != "" {
			return p.Module.Dir
		}
	}
	return logosConfigRoot(ld)
}

// logosConfigRoot resolves the module root whose logos.toml governs this
// build: the root of the main module containing the current directory
// (which covers workspace mode with several main modules). With no main
// module, in GOPATH mode, or for command-line-arguments builds the root is
// empty and defaults apply. Before modload.Init the go.mod is located from
// the working directory instead.
func logosConfigRoot(ld *modload.Loader) string {
	if ld != nil && ld.MainModules != nil && ld.MainModules.Len() > 0 {
		return ld.MainModules.ModRoot(ld.MainModules.ModContainingCWD())
	}
	if cfg.ModulesEnabled {
		if f := modload.FindGoMod(base.Cwd()); f != "" {
			return filepath.Dir(f)
		}
	}
	return ""
}

// checkLogosCgo enforces the logos.toml [build] cgo policy for one loaded
// package in logos mode (M0-S5). It runs in (*Package).load after imports
// resolve, beside checkLogosConfig (p.Module must be set, so it cannot run
// at the earlier UsesCgo site). Plain go never reaches past the logos guard.
// The policy follows the package's module: a main-module package answers to
// its own logos.toml, while a dependency answers to the governing main
// module's, so a restricted allowlist covers every package that imports "C"
// in the build, Go-syntax packages included; the standard library stays
// exempt. Cgo use is the compiled file set (UsesCgo): a bare cgo build tag
// without import "C" never counts, while a cgo-only package hidden by
// disabled cgo is still caught through its ignored files importing "C".
// A denied package fails with [restriction::cgo_usage] instead of the
// upstream "build constraints" text: an enabled denial lands directly, and
// a disabled denial replaces NoGoError. Any other pre-existing error keeps
// priority and is never overwritten. Config load failures are left to
// checkLogosConfig.
func checkLogosCgo(ld *modload.Loader, p *Package) {
	if !cfg.Logos || p.Internal.TestmainGo != nil || p.Standard {
		return
	}
	if p.Module == nil {
		return
	}
	root := logosConfigRootForPackage(ld, p)
	if root == "" || !settings.ModuleAt(root).Logos() {
		return
	}
	if !p.UsesCgo() && !logosCgoHidden(p) {
		return
	}
	m := settings.ModuleAt(root)
	if m.Err != nil || m.File == nil {
		return
	}
	allow, msg := m.CgoAllow(cfg.StartupCgoEnabled(), p.ImportPath)
	if allow {
		capabilities.NoteCgoAllowed(p.ImportPath)
		return
	}
	logosDenyCgo(p, msg)
}

// nothing compiled, but an ignored file imports "C". It runs only when the
// package failed with NoGoError, so builds that selected a non-cgo variant
// never scan. A bare cgo build tag without import "C" never counts.
func logosCgoHidden(p *Package) bool {
	if p.UsesCgo() || p.Internal.Build == nil {
		return false
	}
	if p.Error == nil {
		return false
	}
	if _, ok := errors.AsType[*NoGoError](p.Error.Err); !ok {
		return false
	}
	if !slices.Contains(p.Internal.Build.AllTags, "cgo") {
		return false
	}
	for _, name := range p.IgnoredGoFiles {
		if logosFileImportsC(p.Dir, name) {
			return true
		}
	}
	return false
}

// logosFileImportsC reports whether the file imports "C", parsed
// imports-only. Files that fail to parse never count: a cgo file must use
// Go syntax, so an unparseable file cannot be one.
func logosFileImportsC(dir, name string) bool {
	parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.ImportsOnly)
	if err != nil || parsed == nil {
		return false
	}
	for _, imp := range parsed.Imports {
		if path, err := strconv.Unquote(imp.Path.Value); err == nil && path == "C" {
			return true
		}
	}
	return false
}

// logosDenyCgo fails p with the policy denial, replacing NoGoError so the
// scripts see [restriction::cgo_usage] instead of the upstream text. Any
// other pre-existing error keeps priority and is never overwritten.
func logosDenyCgo(p *Package, msg string) {
	if p.Error != nil {
		if _, ok := errors.AsType[*NoGoError](p.Error.Err); !ok {
			return
		}
	}
	p.Error = &PackageError{Err: errors.New(msg)}
	p.Incomplete = true
}

// logosConfigExt returns the loGo/s extension of a source file name,
// stripping trailing variation selectors U+FE0E and U+FE0F the same way
// cfg.IsLogosFile does.
func logosConfigExt(name string) string {
	trimmed := strings.TrimRight(name, "\uFE0E\uFE0F")
	if i := strings.LastIndexByte(trimmed, '.'); i >= 0 {
		return trimmed[i:]
	}
	return trimmed
}

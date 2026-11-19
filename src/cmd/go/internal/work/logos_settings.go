// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package work

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/mod/modfile"

	"cmd/go/internal/base"
	"cmd/go/internal/cfg"
	"cmd/go/internal/logos/settings"
	"cmd/go/internal/modload"
	"cmd/internal/pathcache"
	"cmd/internal/quoted"
)

var logosSettingsErr error

func refreshLogosPolicy(ld *modload.Loader) {
	f := settings.CommandLine
	f.Logos, f.Env, f.Roots = cfg.Logos, cfg.LogosStartupEnv(), logosMainRoots(ld)
	f.RaceRuntime = cfg.BuildRace && cfg.Goos != "darwin"
	s, err := settings.Load(settings.CWDModuleRoot(), f)
	settings.SetCurrent(s)
	logosSettingsErr = err
	cfg.ApplyLogosEnv(s)
	if s.Cgo.RaceRuntimeOnly {
		requireRaceRuntimeCompiler()
	}
}

// requireRaceRuntimeCompiler ends a raced build that would fail later
// inside runtime/cgo with a message that names neither the race runtime
// nor logos.toml. Listings decide after loading packages whether build
// actions will run, so runList calls CheckRaceRuntimeCompiler once it
// knows; every other command takes the preflight here.
func requireRaceRuntimeCompiler() {
	if cfg.BuildN || cfg.CmdName == "list" {
		return
	}
	checkRaceRuntimeCompiler()
}

// CheckRaceRuntimeCompiler runs the race compiler preflight for callers,
// like runList, that decide after loading packages whether the command
// will run build actions.
func CheckRaceRuntimeCompiler() {
	if cfg.BuildN {
		return
	}
	checkRaceRuntimeCompiler()
}

func checkRaceRuntimeCompiler() {
	cc, ok := raceCompilerCommand()
	if !ok {
		// The CC setting does not parse. BuildInit validates CC, CXX and FC
		// and reports the malformed value with its configuration
		// diagnostic; the preflight stays out of its way.
		return
	}
	if len(cc) > 0 {
		if _, err := pathcache.LookPath(cc[0]); err == nil {
			return
		}
	}
	fmt.Fprintf(os.Stderr, "logos: -race links the race runtime through cgo and needs a C compiler; found neither %q nor zig on PATH (the [build] cgo policy of logos.toml still refuses every cgo package)\n", strings.Join(cc, " "))
	base.SetExitStatus(2)
	base.Exit()
}

// raceCompilerCommand reports the CC command for the race preflight. It
// mirrors envList for CC but returns false instead of panicking when the
// setting does not parse, so the caller can defer to the configuration
// diagnostic in BuildInit.
func raceCompilerCommand() ([]string, bool) {
	v := cfg.Getenv("CC")
	if v == "" {
		v = cfg.DefaultCC(cfg.Goos, cfg.Goarch)
	}
	v = logosCompilerFallback("CC", v)
	args, err := quoted.Split(v)
	if err != nil {
		return nil, false
	}
	return args, true
}

// logosRaceNeedsCgoLinkage ends a raced logos build that an explicit
// CGO_ENABLED=0 keeps without the race runtime. The upstream advice,
// CGO_ENABLED=1, does not lift a logos.toml cgo policy.
func logosRaceNeedsCgoLinkage() {
	if !cfg.Logos || !cfg.BuildRace || cfg.StartupCgoEnabled() != "0" {
		return
	}
	fmt.Fprintln(os.Stderr, "logos: -race links the race runtime through cgo, and CGO_ENABLED=0 is set; unset CGO_ENABLED (the [build] cgo policy of logos.toml still refuses every cgo package)")
	base.SetExitStatus(2)
	base.Exit()
}

func logosMainRoots(ld *modload.Loader) []string {
	var roots []string
	if ld != nil {
		if ld.MainModules != nil {
			for _, m := range ld.MainModules.Versions() {
				if root := ld.MainModules.ModRoot(m); root != "" {
					roots = append(roots, root)
				}
			}
		}
		if len(roots) == 0 {
			roots = workspaceMemberRoots(modload.WorkFilePath(ld))
		}
	}
	return roots
}

func workspaceMemberRoots(workFile string) []string {
	data, err := os.ReadFile(workFile)
	if err != nil {
		return nil
	}
	wf, err := modfile.ParseWork(workFile, data, nil)
	if err != nil {
		return nil
	}
	dir := filepath.Dir(workFile)
	var roots []string
	for _, u := range wf.Use {
		p := u.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, filepath.FromSlash(p))
		}
		roots = append(roots, p)
	}
	return roots
}

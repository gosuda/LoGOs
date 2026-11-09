// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cfg

import (
	"os"
	"path/filepath"
	"strings"

	"cmd/go/internal/logos/settings"
	"cmd/internal/pathcache"
)

// Logos reports whether the current process was invoked as the logos
// command. The logos command is the same binary as the go command,
// selected by argv[0]: the base name of os.Args[0] with a ".exe"
// suffix removed must equal "logos". It is a package-level initializer,
// so defaultContext observes it when BuildContext is first built.
var Logos = strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe") == "logos"

// Pedantic opts in to pedantic diagnostics for logos builds. It is set
// by the logos-only "logos build --pedantic" flag (registered in
// cmd/go/internal/logos.ConfigureCommand) and overrides the logos.toml
// pedantic policy (M9); this slice only stores it. Plain go never sets
// it: the flag is undefined there, so "go help build" keeps the
// upstream text.
var Pedantic bool

// PedanticSet reports whether --pedantic appeared on the command line, so
// --pedantic=false can override a pedantic key of logos.toml.
var PedanticSet bool

// LogosJSON is set by the logos-only --json-diagnostics flag of logos build
// and logos lint: compile diagnostics print as one JSON object per line
// (SPEC Sec 14). Plain go never sets it.
var LogosJSON bool

// BuildWasmLoader is the value of the --wasm-loader build flag (SPEC Sec
// 14.2): when true and the build writes a .wasm output with -o, the build
// also writes the host loader beside it. The flag defaults to true under
// logos and to false under plain go until the run.bash flip (R-U1), and it
// carries no help text, so "go help build" keeps the upstream text.
var BuildWasmLoader bool

// WasmLoader is the text of the host loader a wasm build writes beside its
// output. cmd/go/internal/logos sets it at program start, because that
// package imports the packages that import work and so work cannot import
// it.
var WasmLoader string

// LogosLintCmd is set while logos lint runs: every main-module package is
// linted by its own lint action, so an ordinary compile in the same run
// passes no lint policy to the compiler.
var LogosLintCmd bool

// logosInitCC snapshots the user CC at process startup, covering both the
// process environment and the GOENV file. main.invoke later injects every
// MkEnv value (including a CC default) into the process environment, so a
// post-invoke Getenv can never observe an unset CC; the zig fallback and
// any other consumer must read this snapshot instead.
var logosInitCC = Getenv("CC")

// logosInitCXX snapshots the user CXX at process startup, covering both the
// process environment and the GOENV file, for the same reason as
// logosInitCC: the zig c++ fallback must tell an explicit user setting
// (including `go env -w CXX=...`) from an injected MkEnv default.
var logosInitCXX = Getenv("CXX")

// logosInitCGOEnabled snapshots the user CGO_ENABLED at process startup,
// covering both the process environment and the GOENV file. main.invoke
// injects the effective CGO_ENABLED into the process environment before
// commands run, so per-package precedence must read this snapshot instead
// of the live environment; see StartupCgoEnabled.
var logosInitCGOEnabled = Getenv("CGO_ENABLED")

var logosInitGOEXPERIMENT = RawGOEXPERIMENT

var logosInitGOAMD64 = Getenv("GOAMD64")

var logosInitGOARM64 = Getenv("GOARM64")

func LogosStartupEnv() settings.Env {
	return settings.Env{
		GOEXPERIMENT: logosInitGOEXPERIMENT,
		CGO_ENABLED:  logosInitCGOEnabled,
		GOAMD64:      logosInitGOAMD64,
		GOARM64:      logosInitGOARM64,
	}
}

// logosToolTags returns tags plus the "logos" tool tag in logos mode.
// defaultContext calls it, so every rebuilt BuildContext keeps the tag.
func logosToolTags(tags []string) []string {
	if !Logos {
		return tags
	}
	return append(tags, "logos")
}

// IsLogosFile reports whether name is a loGo/s source file. It is
// mode-independent: it matches ".lgo" and the three alias extensions
// after stripping trailing U+FE0E and U+FE0F variation selectors.
// It reads no mode state, so it may be called at any time.
func IsLogosFile(name string) bool {
	trimmed := strings.TrimRight(name, "\uFE0E\uFE0F")
	if strings.HasSuffix(trimmed, ".lgo") {
		return true
	}
	if strings.HasSuffix(trimmed, ".📜") {
		return true
	}
	if strings.HasSuffix(trimmed, ".⚡") {
		return true
	}
	if strings.HasSuffix(trimmed, ".ʕ◔ϖ◔ʔ") {
		return true
	}
	return false
}

// IsGoFile reports whether name is a Go-family source file: a ".go"
// file, or a loGo/s file when the current process is the logos command.
// It is valid only at run time, after init has set Logos.
func IsGoFile(name string) bool {
	if strings.HasSuffix(name, ".go") {
		return true
	}
	return Logos && IsLogosFile(name)
}

// logosZigArch maps GOARCH to the zig cc -target architecture name.
// logosZigOS maps GOOS to the zig cc -target operating system name;
// the linux entry carries -musl, matching the plan's mapping table.
var logosZigArch = map[string]string{
	"amd64":   "x86_64",
	"arm64":   "aarch64",
	"386":     "x86",
	"arm":     "arm",
	"riscv64": "riscv64",
}

var logosZigOS = map[string]string{
	"linux":   "linux-musl",
	"darwin":  "macos",
	"windows": "windows-gnu",
	"freebsd": "freebsd",
}

// ZigTarget maps a GOOS/GOARCH pair to the zig cc -target triple used by
// the M0-S5 fallback (for example linux/amd64 becomes
// "x86_64-linux-musl"). Only the pairs in the plan's table map; any other
// pair reports false and gets no fallback.
func ZigTarget(goos, goarch string) (string, bool) {
	arch, ok := logosZigArch[goarch]
	if !ok {
		return "", false
	}
	osName, ok := logosZigOS[goos]
	if !ok {
		return "", false
	}
	return arch + "-" + osName, true
}

// StartupCgoEnabled reports the user CGO_ENABLED observed at process
// startup, before main.invoke injects the effective value into the process
// environment. Per-package cgo precedence must use this, not the live
// environment: after invoke the live value always looks explicitly set.
// It returns "" when the user set nothing; only "0" and "1" count as set.
func StartupCgoEnabled() string {
	return logosInitCGOEnabled
}

func ApplyLogosEnv(s *settings.Settings) {
	if !Logos {
		return
	}
	if s.Cgo.Opinion {
		BuildContext.CgoEnabled = s.Cgo.Enabled
	}
	CGOChanged = s.Cgo.Changed
	logosApplyRawExperiment(s.Experiment)
	RefreshLogosPreemptible()
}

// zigCompiler resolves one C-family compiler command for a cgo build given
// its current value: the zig fallback "zig <kind> -target <triple>" when
// cgo is on, the variable was unset in both the process environment and
// the GOENV file at startup, the current compiler is missing from PATH,
// and zig is on PATH; otherwise cmd unchanged. kind is "cc" for CC and
// "c++" for CXX. The startup snapshot tells an injected MkEnv default
// (which main.invoke writes into the process environment before builds
// run) from an explicit user setting, so only the former may fall back. It
// never writes the process environment, so "go env CC" and "go env CXX"
// keep showing the default or user compiler. It runs under both go and
// logos for Go and loGo/s packages alike. The triple follows the command
// target, which ForceHost already reset for host tool builds.
func zigCompiler(cmd, startup, kind string) string {
	if !BuildContext.CgoEnabled {
		return cmd
	}
	if startup != "" {
		return cmd
	}
	if compilerOnPath(cmd) {
		return cmd
	}
	if _, err := pathcache.LookPath("zig"); err != nil {
		return cmd
	}
	if target, ok := ZigTarget(Goos, Goarch); ok {
		return "zig " + kind + " -target " + target
	}
	return cmd
}

// compilerOnPath reports whether cmd is the compiler a cgo build would
// actually run under the current PATH. The injected default may be an
// absolute path baked in at toolchain build time; an absolute path is
// present only when its own directory is on PATH, so hiding the default
// compiler from PATH still enables the zig fallback.
func compilerOnPath(cmd string) bool {
	found, err := pathcache.LookPath(cmd)
	if err != nil {
		return false
	}
	if !filepath.IsAbs(cmd) {
		return true
	}
	viaPath, err := pathcache.LookPath(filepath.Base(cmd))
	if err != nil {
		return false
	}
	want, err1 := os.Stat(found)
	got, err2 := os.Stat(viaPath)
	if err1 != nil || err2 != nil {
		return false
	}
	return os.SameFile(want, got)
}

func ZigCC(cc string) string {
	return zigCompiler(cc, logosInitCC, "cc")
}

func ZigCXX(cxx string) string {
	return zigCompiler(cxx, logosInitCXX, "c++")
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package goparity holds the offensive differential harness for SPEC §0.1
// rules R-GO ("The go command MUST keep Go language semantics") and R-LOGOS
// ("The logos command MUST build Go modules with Go language semantics").
//
// # Corpus layout
//
// One program per directory: testdata/run/<id>_<name>/ with <id> the design's
// attack ID lower-cased without the dash (at01). Every directory is package
// main inside the staged module example.com/parity (go 1.27); extra packages
// of one program live in subdirectories and are imported as
// example.com/parity/<id>_<name>/<pkg>. A directory may carry a generator
// gen.go with //go:build ignore; the harness runs `go run gen.go` inside the
// staged copy before building and generator output is never committed.
// Invalid-Go cases live in testdata/fail/<id>_<name>.go, one defect per file.
//
// # Program contract
//
// A program reads no input, uses no network and no wall clock in its output,
// prints deterministic text, and exits 0. A broken law prints a line starting
// with LAW and exits 1. Programs must build and run identically under
// upstream Go: they may not use the logos build tag, //logos: directives,
// runtime.Version(), loGo/s packages, or any sanctioned SPEC §0.1 difference.
// They are float-free or pass every float result through an explicit
// conversion (fusion-free), print decimal values and never pointers, and use
// deterministic synchronization only.
//
// # Oracles
//
// Tier U compares each fork configuration against the pinned upstream oracle
// (flag -goparity.upstream, default the pinned path). The fork VERSION and
// the oracle VERSION are pinned exactly; any other pair fails with a re-audit
// message. A missing oracle fails the test (owner answer Q2) unless
// -goparity.upstream=off is passed, in which case only Tier M runs and the
// fail corpus is skipped. Tier M asserts metamorphic equality across fork
// configurations with equal effective GOAMD64.
//
// # Configurations
//
// Fast tier (default): K1 go, K3 go -O=3, K4 go -O=3 GOAMD64=v3, K5 logos
// default, K6 logos -O=0, K7 logos -O=3, K8 logos -O=3 arena=false, K10
// logos -O=2 target_cpu=baseline against U1, U1N, U3, U3N. -short runs K1,
// K5, K8, U1, U3. Release tier (flag -goparity.release) adds K2 go -O=0,
// K11 go -O=2, K12 go -O=s, K13 logos -O=1, K14 logos -O=s, K15 logos -O=2
// arena=false, K16 logos -race, and the GOEXPERIMENT rows K17 logosarena,
// K18 logosescape, K19 logosstack, K20 logosfastcgo, K21 preemptibleloops
// on plain go (owner answer Q4). Fork rows compare against the matched
// upstream row (U1 v1, U1N v1 with -gcflags=all=-N -l, U3 v3, U3N v3 with
// -N -l, U3R v3 with -race). Programs importing "C" build with CGO_ENABLED=1
// as an explicit package list; in the fast tier they run under K1, K5, U1,
// and U3 only, in release under every active configuration. A program may
// declare level-dependent stdout lines (currently the unconverted float
// sums that upstream itself fuses at v3) in level-dependent.txt, one line
// prefix per line; those lines compare fork-vs-matched-upstream as usual
// but the invariance and metamorphic gates hold them only within the same
// GOAMD64 level and optimization setting. Gates before comparison: every
// program must give equal observations under all upstream rows present
// (invariance gate) and under upstream U1 at GOMAXPROCS=1 and 4 (stability
// gate); a violation is a corpus defect, never a silent skip.
//
// # Mutants
//
// testdata/mutants/<id>.txt holds one or more negative controls per attack
// in txtar form: header lines kind, file, expect, config, then one -- old --
// / -- new -- pair with the exact replaced text; a later control starts at
// the next column-zero kind line. file is GOROOT-relative; old must occur
// exactly once in the target or the mutant is invalid and the test fails
// loudly, so stale mutants are never silent. kinds compiler, linker, cgo,
// and rules build the named tool through an overlay and swap it into fork
// builds only via -toolexec with the shim in testdata/toolexec (env
// GOPARITY_MUTANT_TOOL=name=path); kinds runtime and std pass the overlay
// to the fork build directly. kind cmdgo is not supported here.
// Mutants never touch the tree and never reach upstream builds.
// TestParityMutants (release tier, flag -goparity.release) applies each
// mutant, requires the build to succeed, and requires the program named
// by expect to turn red in the named configuration: nonzero exit, a LAW
// line, or a difference from the matched upstream row; an expect naming a
// fail-corpus file turns red when the fork build stops rejecting it.
//
// Build caches live under os.TempDir() (goparity-fork-gocache,
// goparity-upstream-gocache, goparity-mutant-<id>-gocache) and persist
// across runs; toolexec mutant builds always use their own cache so a
// mutant compiler can never be served clean cache entries. Program runs are
// bounded by 10 s at GOMAXPROCS=4 with empty GODEBUG and GOTRACEBACK=single;
// outputs are compared after masking hexadecimal addresses and cutting
// stderr at the first traceback line. Under cmd/dist the package is
// discovered by `go list std cmd` and runs in the batched "Testing packages."
// pass with the per-binary -timeout=180s budget, which the fast tier must
// fit.
package goparity

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	forkVersionPin     = "go1.27.2"
	upstreamVersionPin = "go1.27.3"
	upstreamDefault    = "/home/linuxbrew/.linuxbrew/Cellar/go/1.27.3/libexec/bin/go"
)

var (
	upstreamFlag = flag.String("goparity.upstream", upstreamDefault, `upstream oracle go binary, or "off"`)
	releaseFlag  = flag.Bool("goparity.release", false, "add the release tier configurations and the mutation probes")
)

type config struct {
	id       string
	logos    bool
	args     []string
	env      []string
	toml     string
	upstream string
	group    string
	release  bool
}

type upConfig struct {
	id    string
	args  []string
	env   []string
	race  bool
	level string
	noopt bool
}

var forkConfigs = []config{
	{id: "K1", upstream: "U1", group: "v1"},
	{id: "K3", args: []string{"-O=3"}, upstream: "U1", group: "v1"},
	{id: "K4", args: []string{"-O=3"}, env: []string{"GOAMD64=v3"}, upstream: "U3", group: "v3"},
	{id: "K5", logos: true, upstream: "U3", group: "v3"},
	{id: "K6", logos: true, args: []string{"-O=0"}, upstream: "U3N", group: "v3"},
	{id: "K7", logos: true, args: []string{"-O=3"}, upstream: "U3", group: "v3"},
	{id: "K8", logos: true, args: []string{"-O=3"}, toml: "[build]\narena = false\n", upstream: "U3", group: "v3"},
	{id: "K10", logos: true, args: []string{"-O=2"}, toml: "[build]\ntarget_cpu = \"baseline\"\n", upstream: "U1", group: "v1"},
	{id: "K2", args: []string{"-O=0"}, upstream: "U1N", group: "v1", release: true},
	{id: "K11", args: []string{"-O=2"}, upstream: "U1", group: "v1", release: true},
	{id: "K12", args: []string{"-O=s"}, upstream: "U1", group: "v1", release: true},
	{id: "K13", logos: true, args: []string{"-O=1"}, upstream: "U3", group: "v3", release: true},
	{id: "K14", logos: true, args: []string{"-O=s"}, upstream: "U3", group: "v3", release: true},
	{id: "K15", logos: true, args: []string{"-O=2"}, toml: "[build]\narena = false\n", upstream: "U3", group: "v3", release: true},
	{id: "K16", logos: true, args: []string{"-race"}, env: []string{"CGO_ENABLED=1"}, toml: "[build]\ncgo = \"allow\"\n", upstream: "U3R", group: "v3", release: true},
	{id: "K17", env: []string{"GOEXPERIMENT=logosarena"}, upstream: "U1", group: "v1", release: true},
	{id: "K18", env: []string{"GOEXPERIMENT=logosescape"}, upstream: "U1", group: "v1", release: true},
	{id: "K19", env: []string{"GOEXPERIMENT=logosstack"}, upstream: "U1", group: "v1", release: true},
	{id: "K20", env: []string{"GOEXPERIMENT=logosfastcgo"}, upstream: "U1", group: "v1", release: true},
	{id: "K21", env: []string{"GOEXPERIMENT=preemptibleloops"}, upstream: "U1", group: "v1", release: true},
}

var upConfigs = []upConfig{
	{id: "U1", level: "v1"},
	{id: "U1N", args: []string{"-gcflags=all=-N -l"}, level: "v1", noopt: true},
	{id: "U3", env: []string{"GOAMD64=v3"}, level: "v3"},
	{id: "U3N", args: []string{"-gcflags=all=-N -l"}, env: []string{"GOAMD64=v3"}, level: "v3", noopt: true},
	{id: "U3R", args: []string{"-race"}, env: []string{"GOAMD64=v3", "CGO_ENABLED=1"}, race: true, level: "v3"},
}

type toolchain struct {
	bin    string
	logos  string
	goroot string
}

type observation struct {
	exit   int
	stdout string
	stderr string
}

type buildResult struct {
	outDir string
	output string
	err    error
	progs  []string
}

func goVersion(t *testing.T, goroot string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(goroot, "VERSION"))
	if err != nil {
		t.Fatalf("reading VERSION in %s: %v", goroot, err)
	}
	line, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(line)
}

func forkTool(t *testing.T) toolchain {
	t.Helper()
	goroot := os.Getenv("GOROOT")
	if goroot == "" {
		goroot = runtime.GOROOT()
	}
	if goroot == "" {
		t.Fatal("GOROOT is not set")
	}
	bin := filepath.Join(goroot, "bin", "go")
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("fork go binary missing: %v", err)
	}
	if v := goVersion(t, goroot); v != forkVersionPin {
		t.Fatalf("fork version skew not audited: got %s want %s; re-run `git diff --stat %s <oracle> -- src test` and update the pin in parity_test.go", v, forkVersionPin, forkVersionPin)
	}
	logos := filepath.Join(goroot, "bin", "logos")
	if _, err := os.Stat(logos); err != nil {
		logos = filepath.Join(t.TempDir(), "logos")
		cloneBinary(t, bin, logos)
	}
	return toolchain{bin: bin, logos: logos, goroot: goroot}
}

func cloneBinary(t *testing.T, src, dst string) {
	t.Helper()
	if os.Link(src, dst) == nil {
		return
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("reading %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatalf("writing %s: %v", dst, err)
	}
}

func upstreamTool(t *testing.T) (toolchain, bool) {
	t.Helper()
	if *upstreamFlag == "off" {
		return toolchain{}, false
	}
	bin := *upstreamFlag
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("upstream oracle %s missing: %v (pass -goparity.upstream=off to run the metamorphic tier only)", bin, err)
	}
	goroot := filepath.Clean(filepath.Join(filepath.Dir(bin), ".."))
	if v := goVersion(t, goroot); v != upstreamVersionPin {
		t.Fatalf("upstream oracle version skew not audited: got %s want %s; re-run `git diff --stat %s <oracle tag> -- src test` and update the pin in parity_test.go", v, upstreamVersionPin, upstreamVersionPin)
	}
	return toolchain{bin: bin, goroot: goroot}, true
}

func baseEnv(cacheDir string) []string {
	os.MkdirAll(cacheDir, 0o777)
	os.MkdirAll(cacheDir+"-path", 0o777)
	return []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + cacheDir,
		"GOENV=off",
		"GOTOOLCHAIN=local",
		"GOWORK=off",
		"GOPROXY=off",
		"GOMAXPROCS=8",
		"CGO_ENABLED=0",
		"GOCACHE=" + cacheDir,
		"GOPATH=" + cacheDir + "-path",
	}
}

func appendEnv(env []string, kv ...string) []string {
	out := slices.Clone(env)
	return append(out, kv...)
}

func forkCache() string { return filepath.Join(os.TempDir(), "goparity-fork-gocache") }

func upCache() string { return filepath.Join(os.TempDir(), "goparity-upstream-gocache") }

func mutantCache(id string) string {
	return filepath.Join(os.TempDir(), "goparity-mutant-"+id+"-gocache")
}

func corpusPrograms(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir("testdata/run")
	if err != nil {
		t.Fatalf("reading testdata/run: %v", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	slices.Sort(out)
	return out
}

func corpusFails(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob("testdata/fail/*.go")
	if err != nil {
		t.Fatalf("globbing testdata/fail: %v", err)
	}
	var out []string
	for _, m := range matches {
		out = append(out, strings.TrimSuffix(filepath.Base(m), ".go"))
	}
	slices.Sort(out)
	return out
}

func activeForkConfigs() []config {
	var out []config
	for _, c := range forkConfigs {
		if c.release && !*releaseFlag {
			continue
		}
		if testing.Short() && c.id != "K1" && c.id != "K5" && c.id != "K8" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func copyTree(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("reading %s: %w", src, err)
	}
	if err := os.MkdirAll(dst, 0o777); err != nil {
		return fmt.Errorf("creating %s: %w", dst, err)
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyTree(s, d); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(s)
		if err != nil {
			return fmt.Errorf("reading %s: %w", s, err)
		}
		if err := os.WriteFile(d, data, 0o644); err != nil {
			return fmt.Errorf("writing %s: %w", d, err)
		}
	}
	return nil
}

func stageCorpus(tc toolchain, dst, cacheDir string, progs []string) error {
	for _, p := range progs {
		srcDir := filepath.Join("testdata", "run", p)
		if err := copyTree(srcDir, filepath.Join(dst, p)); err != nil {
			return fmt.Errorf("staging %s: %w", p, err)
		}
		if _, err := os.Stat(filepath.Join(srcDir, "gen.go")); err == nil {
			if _, err := runGenerator(tc.bin, p, filepath.Join(dst, p), baseEnv(cacheDir), generatorBudget); err != nil {
				return err
			}
		}
	}
	if err := os.WriteFile(filepath.Join(dst, "go.mod"), []byte("module example.com/parity\n\ngo 1.27\n"), 0o644); err != nil {
		return fmt.Errorf("writing go.mod: %w", err)
	}
	return nil
}

func goBuild(bin, dir string, args, env []string, budget time.Duration) (string, error) {
	ctx, cancel := context.WithTimeoutCause(context.Background(), budget, errors.New("goparity build timeout"))
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = pipeWaitDelay
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const generatorBudget = 5 * time.Minute

const pipeWaitDelay = 10 * time.Second

func runGenerator(bin, prog, dir string, env []string, budget time.Duration) (string, error) {
	ctx, cancel := context.WithTimeoutCause(context.Background(), budget, errors.New("goparity generator timeout"))
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "run", "gen.go")
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = pipeWaitDelay
	setNewProcessGroup(cmd)
	cmd.Cancel = func() error {
		killProcessGroup(cmd)
		return nil
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
				killProcessGroup(cmd)
			}
			return string(out), fmt.Errorf("generator for %s timed out: %w", prog, context.Cause(ctx))
		}
		return string(out), fmt.Errorf("generator for %s failed: %v\n%s", prog, err, clip(string(out)))
	}
	return string(out), nil
}

const moduleBuildBudget = 15 * time.Minute

type stagedBuild struct {
	bin     string
	stage   string
	outDir  string
	args    []string
	env     []string
	progs   []string
	wantCgo bool
}

func (b stagedBuild) build() (built []string, output string, err error) {
	build := func(pkgs, env []string) (string, error) {
		if err := os.MkdirAll(b.outDir, 0o777); err != nil {
			return "", err
		}
		full := append([]string{"build", "-o", b.outDir}, b.args...)
		full = append(full, pkgs...)
		return goBuild(b.bin, b.stage, full, env, moduleBuildBudget)
	}
	plain, cgo, err := splitStaged(b.stage, b.progs)
	if err != nil {
		return nil, "", err
	}
	type buildList struct {
		names []string
		env   []string
	}
	lists := []buildList{{plain, b.env}}
	if b.wantCgo {
		lists = append(lists, buildList{cgo, appendEnv(b.env, "CGO_ENABLED=1")})
	}
	for _, l := range lists {
		if len(l.names) == 0 {
			continue
		}
		out, err := build(pkgArgs(l.names), l.env)
		output += out
		if err != nil {
			return built, output, err
		}
		built = append(built, l.names...)
	}
	if len(built) == 0 && err == nil {
		return built, output, errors.New("no programs built")
	}
	return built, output, nil
}

func splitStaged(stage string, progs []string) (plain, cgo []string, err error) {
	for _, p := range progs {
		isCgo, err := cgoProgram(filepath.Join(stage, p))
		if err != nil {
			return nil, nil, err
		}
		if isCgo {
			cgo = append(cgo, p)
		} else {
			plain = append(plain, p)
		}
	}
	return plain, cgo, nil
}

func cgoProgram(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return false, err
		}
		for ln := range strings.SplitSeq(string(data), "\n") {
			before, _, _ := strings.Cut(strings.TrimSpace(ln), "//")
			if t := strings.TrimSpace(before); t == `import "C"` || t == `"C"` {
				return true, nil
			}
		}
	}
	return false, nil
}

func pkgArgs(progs []string) []string {
	out := make([]string, 0, len(progs))
	for _, p := range progs {
		out = append(out, "./"+p)
	}
	return out
}

type upKey struct {
	level string
	noopt bool
}

func (c config) optOff() bool { return slices.Contains(c.args, "-O=0") }

func levelPrefixes(prog string) []string {
	data, err := os.ReadFile(filepath.Join("testdata", "run", prog, "level-dependent.txt"))
	if err != nil {
		return nil
	}
	var out []string
	for ln := range strings.SplitSeq(string(data), "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			out = append(out, t)
		}
	}
	return out
}

func splitLevel(stdout string, prefixes []string) (global, level string) {
	if len(prefixes) == 0 {
		return stdout, ""
	}
	var g, l []string
	for ln := range strings.SplitSeq(stdout, "\n") {
		isLevel := false
		for _, p := range prefixes {
			if ln == p || strings.HasPrefix(ln, p+" ") {
				isLevel = true
				break
			}
		}
		if isLevel {
			l = append(l, ln)
		} else {
			g = append(g, ln)
		}
	}
	return strings.Join(g, "\n"), strings.Join(l, "\n")
}

var hexMask = regexp.MustCompile(`0[xX][0-9a-fA-F]{6,}`)

func normStd(s string) string {
	return hexMask.ReplaceAllString(s, "0xADDR")
}

func normErr(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		if strings.HasPrefix(ln, "goroutine ") {
			return normStd(strings.Join(lines[:i], "\n"))
		}
	}
	return normStd(s)
}

func runBinary(path, maxProcs string) (observation, error) {
	return runBinaryTimeout(path, maxProcs, 10*time.Second)
}

func runSweepBinary(path string) (observation, error) {
	return runBinaryTimeout(path, "4", 60*time.Second)
}

func runBinaryTimeout(path, maxProcs string, budget time.Duration) (observation, error) {
	ctx, cancel := context.WithTimeoutCause(context.Background(), budget, errors.New("goparity run timeout"))
	defer cancel()
	cmd := exec.CommandContext(ctx, path)
	cmd.Env = []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + os.TempDir(),
		"GOMAXPROCS=" + maxProcs,
		"GODEBUG=",
		"GOTRACEBACK=single",
	}
	cmd.WaitDelay = pipeWaitDelay
	setNewProcessGroup(cmd)
	cmd.Cancel = func() error {
		killProcessGroup(cmd)
		return nil
	}
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	runErr := cmd.Run()
	obs := observation{stdout: normStd(out.String()), stderr: normErr(errb.String())}
	if runErr == nil {
		obs.exit = 0
		return obs, nil
	}
	if errors.Is(runErr, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Exited() {
		obs.exit = cmd.ProcessState.ExitCode()
		return obs, nil
	}
	if ctx.Err() != nil {
		if cmd.ProcessState == nil || !cmd.ProcessState.Exited() {
			killProcessGroup(cmd)
		}
		return obs, fmt.Errorf("running %s: %w", path, context.Cause(ctx))
	}
	if ee, ok := errors.AsType[*exec.ExitError](runErr); ok {
		obs.exit = ee.ExitCode()
		return obs, nil
	}
	return obs, fmt.Errorf("running %s: %w", path, runErr)
}

func TestRunBinaryTimeoutKillsSleeper(t *testing.T) {
	if !processGroupKillSupported() {
		skipUnsupportedProcessGroupKill(t)
	}
	fork := forkTool(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	src := fmt.Sprintf(`package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	_ = os.WriteFile(%q, []byte(fmt.Sprint(os.Getpid())), 0o644)
	time.Sleep(8 * time.Second)
}
`, pidFile)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module sleeper\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "sleeper")
	if out, err := goBuild(fork.bin, dir, []string{"build", "-o", bin, "."}, baseEnv(forkCache()), 5*time.Minute); err != nil {
		t.Fatalf("building sleeper: %v\n%s", err, clip(out))
	}
	start := time.Now()
	errCh := make(chan error, 1)
	go func() {
		_, err := runBinaryTimeout(bin, "4", 2*time.Second)
		errCh <- err
	}()
	var pid int
	for {
		if b, rerr := os.ReadFile(pidFile); rerr == nil {
			if p, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil {
				pid = p
				break
			}
		}
		if time.Now().After(start.Add(3 * time.Second)) {
			t.Fatalf("sleeper wrote no readable pid file within %s", time.Since(start).Round(time.Second))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !procGroupGone(pid, time.Until(start.Add(6*time.Second))) {
		t.Fatalf("sleeper process group %d still alive %s after the deadline: child survived the kill", pid, time.Since(start).Round(time.Second))
	}
	err := <-errCh
	if err == nil || !strings.Contains(err.Error(), "goparity run timeout") {
		t.Fatalf("sleeper run = %v, want the goparity run timeout error", err)
	}
}

func TestRunBinaryTimeoutReleasesGrandchildPipe(t *testing.T) {
	if !processGroupKillSupported() {
		skipUnsupportedProcessGroupKill(t)
	}
	fork := forkTool(t)
	dir := t.TempDir()
	heldFile := filepath.Join(dir, "held.marker")
	heldDir := filepath.Join(dir, "heldmod")
	heldSrc := fmt.Sprintf(`package main

import (
	"os"
	"time"
)

func main() {
	time.Sleep(25 * time.Second)
	_ = os.WriteFile(%q, []byte("held"), 0o644)
}
`, heldFile)
	if err := os.MkdirAll(heldDir, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(heldDir, "go.mod"), []byte("module held\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(heldDir, "main.go"), []byte(heldSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	heldBin := filepath.Join(heldDir, "held")
	if out, err := goBuild(fork.bin, heldDir, []string{"build", "-o", heldBin, "."}, baseEnv(forkCache()), 5*time.Minute); err != nil {
		t.Fatalf("building pipe holder: %v\n%s", err, clip(out))
	}
	src := fmt.Sprintf(`package main

import (
	"fmt"
	"os"
	"os/exec"
)

func main() {
	fmt.Println("done")
	c := exec.Command(%q)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	_ = c.Start()
}
`, heldBin)
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module daemon\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "daemon")
	if out, err := goBuild(fork.bin, dir, []string{"build", "-o", bin, "."}, baseEnv(forkCache()), 5*time.Minute); err != nil {
		t.Fatalf("building daemon: %v\n%s", err, clip(out))
	}
	obs, err := runBinaryTimeout(bin, "4", 15*time.Second)
	if err != nil {
		t.Fatalf("daemon run = %v, want the exited parent observation", err)
	}
	if obs.exit != 0 {
		t.Fatalf("daemon exit = %d, want 0", obs.exit)
	}
	if !strings.Contains(obs.stdout, "done") {
		t.Fatalf("daemon stdout = %q, want the parent output drained before return", obs.stdout)
	}
	if _, serr := os.Stat(heldFile); serr == nil {
		t.Fatalf("pipe holder wrote its marker before the run returned: the run waited for the pipe-holding grandchild")
	}
}

func TestRunGeneratorTimeoutKillsSleeper(t *testing.T) {
	if !processGroupKillSupported() {
		skipUnsupportedProcessGroupKill(t)
	}
	fork := forkTool(t)
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pgid")
	src := fmt.Sprintf(`package main

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

func main() {
	pgid, _ := syscall.Getpgid(0)
	_ = os.WriteFile(%q, []byte(fmt.Sprint(pgid)), 0o644)
	time.Sleep(10 * time.Second)
}
`, pidFile)
	if err := os.WriteFile(filepath.Join(dir, "gen.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	type runRes struct {
		out string
		err error
	}
	ch := make(chan runRes, 1)
	go func() {
		out, err := runGenerator(fork.bin, "sleepgen", dir, baseEnv(forkCache()), 2*time.Second)
		ch <- runRes{out, err}
	}()
	var pgid int
	for {
		if b, rerr := os.ReadFile(pidFile); rerr == nil {
			if p, perr := strconv.Atoi(strings.TrimSpace(string(b))); perr == nil {
				pgid = p
				break
			}
		}
		if time.Now().After(start.Add(3 * time.Second)) {
			t.Fatalf("sleeping generator wrote no readable pgid file within %s", time.Since(start).Round(time.Second))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !procGroupGone(pgid, time.Until(start.Add(7*time.Second))) {
		t.Fatalf("generator process group %d still alive %s after the deadline: child survived the kill", pgid, time.Since(start).Round(time.Second))
	}
	res := <-ch
	if res.err == nil || !strings.Contains(res.err.Error(), "timed out") {
		t.Fatalf("sleeping generator = %v, want a timeout error", res.err)
	}
}

func obsEqual(a, b observation) bool {
	return a.exit == b.exit && a.stdout == b.stdout && a.stderr == b.stderr
}

func firstDiff(a, b string) string {
	al := strings.Split(a, "\n")
	bl := strings.Split(b, "\n")
	for i := range max(len(al), len(bl)) {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			return fmt.Sprintf("line %d: got %q want %q", i+1, clip(x), clip(y))
		}
	}
	return ""
}

func firstDivergence(a, b observation) string {
	return cmp.Or(firstDiff(a.stdout, b.stdout), firstDiff(a.stderr, b.stderr), fmt.Sprintf("exit %d vs %d", a.exit, b.exit))
}

func clip(s string) string {
	if len(s) > 120 {
		return s[:117] + "..."
	}
	return s
}

func obsDesc(o observation) string {
	return fmt.Sprintf("exit %d stdout %d bytes stderr %d bytes", o.exit, len(o.stdout), len(o.stderr))
}

func runAll(r buildResult, maxProcs string) (map[string]observation, error) {
	out := make(map[string]observation, len(r.progs))
	var firstErr error
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, p := range r.progs {
		wg.Go(func() {
			o, err := runBinary(filepath.Join(r.outDir, p), maxProcs)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			out[p] = o
		})
	}
	wg.Wait()
	return out, firstErr
}

func activeUpConfigs(ks []config) []upConfig {
	need := make(map[string]bool)
	for _, c := range ks {
		need[c.upstream] = true
	}
	if !testing.Short() {
		need["U1N"] = true
	}
	var out []upConfig
	for _, u := range upConfigs {
		if need[u.id] {
			out = append(out, u)
		}
	}
	return out
}

func buildAllFork(t *testing.T, fork toolchain, base string, progs []string, ks []config) map[string]buildResult {
	t.Helper()
	results := make(map[string]buildResult, len(ks))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, c := range ks {
		wg.Go(func() {
			stage := filepath.Join(base, "fork-"+c.id)
			if err := stageCorpus(fork, stage, forkCache(), progs); err != nil {
				t.Errorf("fork stage %s: %v", c.id, err)
				return
			}
			if c.toml != "" {
				if err := os.WriteFile(filepath.Join(stage, "logos.toml"), []byte(c.toml), 0o644); err != nil {
					t.Errorf("writing logos.toml for %s: %v", c.id, err)
					return
				}
			}
			env := appendEnv(baseEnv(forkCache()), c.env...)
			bin := fork.bin
			if c.logos {
				bin = fork.logos
			}
			outDir := filepath.Join(base, "out-fork-"+c.id)
			wantCgo := *releaseFlag || c.id == "K1" || c.id == "K5"
			sb := stagedBuild{bin: bin, stage: stage, outDir: outDir, args: c.args, env: env, progs: progs, wantCgo: wantCgo}
			built, out, err := sb.build()
			mu.Lock()
			results[c.id] = buildResult{outDir: outDir, output: out, err: err, progs: built}
			mu.Unlock()
		})
	}
	wg.Wait()
	return results
}

func buildAllUpstream(t *testing.T, up toolchain, base string, progs []string, ups []upConfig) map[string]buildResult {
	t.Helper()
	results := make(map[string]buildResult, len(ups))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, u := range ups {
		wg.Go(func() {
			stage := filepath.Join(base, "up-"+u.id)
			if err := stageCorpus(up, stage, upCache(), progs); err != nil {
				t.Errorf("upstream stage %s: %v", u.id, err)
				return
			}
			env := baseEnv(upCache())
			if u.race {
				env = appendEnv(env, "CGO_ENABLED=1")
			}
			env = appendEnv(env, u.env...)
			outDir := filepath.Join(base, "out-up-"+u.id)
			wantCgo := *releaseFlag || u.id == "U1" || u.id == "U3"
			sb := stagedBuild{bin: up.bin, stage: stage, outDir: outDir, args: u.args, env: env, progs: progs, wantCgo: wantCgo}
			built, out, err := sb.build()
			mu.Lock()
			results[u.id] = buildResult{outDir: outDir, output: out, err: err, progs: built}
			mu.Unlock()
		})
	}
	wg.Wait()
	return results
}

func TestParity(t *testing.T) {
	start := time.Now()
	fork := forkTool(t)
	up, upOn := upstreamTool(t)
	progs := corpusPrograms(t)
	if len(progs) == 0 {
		t.Fatal("no corpus programs under testdata/run")
	}
	ks := activeForkConfigs()
	base := t.TempDir()

	forkBuilds := buildAllFork(t, fork, base, progs, ks)
	for _, c := range ks {
		if r := forkBuilds[c.id]; r.err != nil {
			t.Errorf("fork build %s failed: %v\n%s", c.id, r.err, clip(r.output))
		}
	}
	var ups []upConfig
	var upBuilds map[string]buildResult
	if upOn {
		ups = activeUpConfigs(ks)
		upBuilds = buildAllUpstream(t, up, base, progs, ups)
		for _, u := range ups {
			if r := upBuilds[u.id]; r.err != nil {
				t.Errorf("upstream build %s failed: %v\n%s", u.id, r.err, clip(r.output))
			}
		}
	}
	forkObs := map[string]map[string]observation{}
	upObs := map[string]map[string]observation{}
	forkRunErrs := map[string]error{}
	upRunErrs := map[string]error{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, c := range ks {
		if forkBuilds[c.id].err != nil {
			continue
		}
		wg.Go(func() {
			o, err := runAll(forkBuilds[c.id], "4")
			mu.Lock()
			if err != nil {
				forkRunErrs[c.id] = err
			} else {
				forkObs[c.id] = o
			}
			mu.Unlock()
		})
	}
	if upOn {
		for _, u := range ups {
			if upBuilds[u.id].err != nil {
				continue
			}
			wg.Go(func() {
				o, err := runAll(upBuilds[u.id], "4")
				mu.Lock()
				if err != nil {
					upRunErrs[u.id] = err
				} else {
					upObs[u.id] = o
				}
				mu.Unlock()
			})
		}
	}
	wg.Wait()
	for id, err := range forkRunErrs {
		t.Errorf("fork runs %s failed: %v", id, err)
	}
	if upOn {
		for id, err := range upRunErrs {
			t.Errorf("upstream runs %s failed: %v", id, err)
		}
	}
	for _, c := range ks {
		for _, p := range progs {
			if o, ok := forkObs[c.id][p]; ok && (o.exit != 0 || strings.Contains(o.stdout, "LAW")) {
				t.Errorf("corpus defect: program %s failed under fork %s: %s\n%s", p, c.id, obsDesc(o), clip(o.stdout))
			}
		}
	}
	if upOn {
		for _, u := range ups {
			for _, p := range progs {
				if o, ok := upObs[u.id][p]; ok && (o.exit != 0 || strings.Contains(o.stdout, "LAW")) {
					t.Errorf("corpus defect: program %s failed under upstream %s: %s\n%s", p, u.id, obsDesc(o), clip(o.stdout))
				}
			}
		}
	}
	stability := map[string]observation{}
	if upOn && upBuilds["U1"].err == nil {
		stab, stabErr := runAll(upBuilds["U1"], "1")
		if stabErr != nil {
			t.Errorf("upstream stability runs failed: %v", stabErr)
		} else {
			stability = stab
		}
	}
	if !upOn && forkBuilds["K1"].err == nil {
		stab, stabErr := runAll(forkBuilds["K1"], "1")
		if stabErr != nil {
			t.Errorf("fork stability runs failed: %v", stabErr)
		} else {
			stability = stab
		}
	}
	upByID := make(map[string]upConfig, len(ups))
	for _, u := range ups {
		upByID[u.id] = u
	}
	for _, p := range progs {
		prefs := levelPrefixes(p)
		if len(prefs) > 0 && len(ups) > 0 {
			if o, ok := upObs[ups[0].id][p]; ok {
				if gs, _ := splitLevel(o.stdout, prefs); strings.TrimSpace(gs) == "" {
					t.Errorf("program %s: level-dependent prefixes mark every line, no compared lines", p)
				}
			}
		}
		var gref *observation
		lrefs := map[upKey]string{}
		for _, u := range ups {
			o, ok := upObs[u.id][p]
			if !ok {
				continue
			}
			gs, lv := splitLevel(o.stdout, prefs)
			og := observation{exit: o.exit, stdout: gs, stderr: o.stderr}
			if gref == nil {
				cp := og
				gref = &cp
			} else if !obsEqual(*gref, og) {
				t.Errorf("invariance gate: program %s differs between upstream rows: %s vs %s (%s)", p, obsDesc(*gref), obsDesc(og), firstDiff(gref.stdout, og.stdout))
			}
			k := upKey{level: upByID[u.id].level, noopt: upByID[u.id].noopt}
			if prev, ok := lrefs[k]; ok && prev != lv {
				t.Errorf("invariance gate: program %s level-dependent lines differ within upstream %s,noopt=%v rows: %s", p, k.level, k.noopt, firstDiff(prev, lv))
			} else if !ok {
				lrefs[k] = lv
			}
		}
		if o, ok := stability[p]; ok {
			gs, _ := splitLevel(o.stdout, prefs)
			og := observation{exit: o.exit, stdout: gs, stderr: o.stderr}
			if gref != nil {
				if !obsEqual(*gref, og) {
					t.Errorf("stability gate: program %s differs under upstream U1 at GOMAXPROCS=1 vs 4: %s vs %s (%s)", p, obsDesc(*gref), obsDesc(og), firstDiff(gref.stdout, og.stdout))
				}
			} else if !upOn {
				if o4, ok4 := forkObs["K1"][p]; ok4 {
					gs4, _ := splitLevel(o4.stdout, prefs)
					og4 := observation{exit: o4.exit, stdout: gs4, stderr: o4.stderr}
					if !obsEqual(og4, og) {
						t.Errorf("stability gate: program %s differs under fork K1 at GOMAXPROCS=1 vs 4: %s vs %s (%s)", p, obsDesc(og4), obsDesc(og), firstDiff(og4.stdout, og.stdout))
					}
				}
			}
		}
	}
	for _, c := range ks {
		if forkBuilds[c.id].err != nil {
			continue
		}
		uo, ok := upObs[c.upstream]
		if !ok {
			continue
		}
		for _, p := range progs {
			fo, ok1 := forkObs[c.id][p]
			uo1, ok2 := uo[p]
			if !ok1 || !ok2 {
				continue
			}
			if !obsEqual(fo, uo1) {
				d := firstDivergence(fo, uo1)
				t.Errorf("parity: program %s: %s vs %s: %s (%s vs %s)", p, c.id, c.upstream, d, obsDesc(fo), obsDesc(uo1))
			}
		}
	}
	for _, group := range []string{"v1", "v3"} {
		var members []config
		for _, c := range ks {
			if c.group == group && forkBuilds[c.id].err == nil {
				members = append(members, c)
			}
		}
		for _, p := range progs {
			prefs := levelPrefixes(p)
			var gref *observation
			lrefs := map[bool]string{}
			for _, m := range members {
				o, ok := forkObs[m.id][p]
				if !ok {
					continue
				}
				gs, lv := splitLevel(o.stdout, prefs)
				og := observation{exit: o.exit, stdout: gs, stderr: o.stderr}
				if gref == nil {
					cp := og
					gref = &cp
				} else if !obsEqual(*gref, og) {
					t.Errorf("metamorphic: program %s differs between %s and %s (%s group): %s", p, members[0].id, m.id, group, firstDiff(gref.stdout, og.stdout))
				}
				if prev, ok := lrefs[m.optOff()]; ok && prev != lv {
					t.Errorf("metamorphic: program %s level-dependent lines differ between %s and %s (%s group, optOff=%v): %s", p, members[0].id, m.id, group, m.optOff(), firstDiff(prev, lv))
				} else if !ok {
					lrefs[m.optOff()] = lv
				}
			}
		}
	}
	checkFailCorpus(t, fork, up, upOn, base, ks)
	t.Logf("parity: %d programs, %d fork configs, %d upstream rows, wall %s", len(progs), len(ks), len(ups), time.Since(start))
}

func checkFailCorpus(t *testing.T, fork, up toolchain, upOn bool, base string, ks []config) {
	t.Helper()
	stems := corpusFails(t)
	if len(stems) == 0 {
		if upOn {
			t.Fatal("no fail corpus files under testdata/fail")
		}
		return
	}
	if !upOn {
		t.Log("fail corpus skipped: upstream oracle off")
		return
	}
	stage := filepath.Join(base, "failmod")
	if err := os.MkdirAll(stage, 0o777); err != nil {
		t.Fatalf("creating %s: %v", stage, err)
	}
	if err := os.WriteFile(filepath.Join(stage, "go.mod"), []byte("module example.com/parity\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatalf("writing go.mod: %v", err)
	}
	for _, s := range stems {
		data, err := os.ReadFile(filepath.Join("testdata", "fail", s+".go"))
		if err != nil {
			t.Fatalf("reading fail corpus %s: %v", s, err)
		}
		dir := filepath.Join(stage, s)
		if err := os.MkdirAll(dir, 0o777); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
		if err := os.WriteFile(filepath.Join(dir, "main.go"), data, 0o644); err != nil {
			t.Fatalf("writing %s: %v", s, err)
		}
	}
	posPat := regexp.MustCompile(`(?m)(?:^|vet: )(\S+\.go):(\d+):(\d+): `)
	positions := func(s string) []string {
		var out []string
		for _, m := range posPat.FindAllStringSubmatch(s, -1) {
			out = append(out, m[2]+":"+m[3])
		}
		slices.Sort(out)
		return out
	}
	for _, s := range stems {
		upOut, upErr := goBuild(up.bin, stage, []string{"build", "-o", filepath.Join(base, "failbin-"+s), "./" + s}, baseEnv(upCache()), 5*time.Minute)
		if upErr == nil {
			t.Errorf("fail corpus %s: upstream build succeeded, corpus defect", s)
			continue
		}
		if _, err := os.Stat(filepath.Join(base, "failbin-"+s)); err == nil {
			t.Errorf("fail corpus %s: upstream wrote a binary for a rejected program", s)
		}
		refPos := positions(upOut)
		if len(refPos) == 0 {
			t.Errorf("fail corpus %s: upstream reported no file:line:col, corpus defect", s)
			continue
		}
		refVetPos, vetErr := func() ([]string, error) {
			out, err := goBuild(up.bin, stage, []string{"vet", "./" + s}, baseEnv(upCache()), 5*time.Minute)
			if err == nil {
				return nil, errors.New("upstream vet succeeded")
			}
			return positions(out), nil
		}()
		if vetErr != nil {
			t.Errorf("fail corpus %s: %v, corpus defect", s, vetErr)
			continue
		}
		if len(refVetPos) == 0 {
			t.Errorf("fail corpus %s: upstream vet reported no file:line:col, corpus defect", s)
			continue
		}
		for _, c := range ks {
			if c.id != "K1" && c.id != "K5" {
				continue
			}
			bin := fork.bin
			if c.logos {
				bin = fork.logos
			}
			fenv := appendEnv(baseEnv(forkCache()), c.env...)
			fout, ferr := goBuild(bin, stage, []string{"build", "-o", filepath.Join(base, "failbin-fork-"+c.id+"-"+s), "./" + s}, fenv, 5*time.Minute)
			if ferr == nil {
				t.Errorf("fail corpus %s: fork %s accepted invalid Go", s, c.id)
			}
			if _, err := os.Stat(filepath.Join(base, "failbin-fork-"+c.id+"-"+s)); err == nil {
				t.Errorf("fail corpus %s: fork %s wrote a binary for a rejected program", s, c.id)
			}
			if got := positions(fout); len(got) == 0 {
				t.Errorf("fail corpus %s: fork %s reported no file:line:col", s, c.id)
			} else if !slices.Equal(got, refPos) {
				t.Errorf("fail corpus %s: fork %s positions %v differ from upstream %v", s, c.id, got, refPos)
			}
			if c.id != "K1" {
				continue
			}
			vout, verr := goBuild(bin, stage, []string{"vet", "./" + s}, fenv, 5*time.Minute)
			if verr == nil {
				t.Errorf("fail corpus %s: fork vet accepted invalid Go", s)
				continue
			}
			if got := positions(vout); len(got) == 0 {
				t.Errorf("fail corpus %s: fork vet reported no file:line:col", s)
			} else if !slices.Equal(got, refVetPos) {
				t.Errorf("fail corpus %s: fork vet positions %v differ from upstream %v", s, got, refVetPos)
			}
		}
	}
}

type mutant struct {
	id     string
	kind   string
	file   string
	expect string
	cfg    string
	old    string
	new    string
}

func loadMutants(t *testing.T) []mutant {
	t.Helper()
	matches, err := filepath.Glob("testdata/mutants/*.txt")
	if err != nil {
		t.Fatalf("globbing testdata/mutants: %v", err)
	}
	slices.Sort(matches)
	var out []mutant
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		ms, err := parseMutants(string(data))
		if err != nil {
			t.Fatalf("mutant %s: %v", path, err)
		}
		stem := strings.TrimSuffix(filepath.Base(path), ".txt")
		for i := range ms {
			ms[i].id = stem
			if len(ms) > 1 {
				ms[i].id = fmt.Sprintf("%s-%d", stem, i+1)
			}
			out = append(out, ms[i])
		}
	}
	return out
}

func parseMutants(data string) ([]mutant, error) {
	var chunks [][]string
	var cur []string
	seenNew := false
	for ln := range strings.SplitSeq(data, "\n") {
		if ln == "-- new --" {
			seenNew = true
		}
		if seenNew && isMutantHeader(ln) {
			chunks = append(chunks, cur)
			cur = nil
			seenNew = false
		}
		cur = append(cur, ln)
	}
	chunks = append(chunks, cur)
	out := make([]mutant, 0, len(chunks))
	for _, c := range chunks {
		m, err := parseMutant(strings.Join(c, "\n"))
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func isMutantHeader(ln string) bool {
	if strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "\t") {
		return false
	}
	k, _, ok := strings.Cut(ln, ":")
	if !ok {
		return false
	}
	switch k {
	case "kind", "file", "expect", "config":
		return true
	}
	return false
}

func parseMutant(data string) (mutant, error) {
	var m mutant
	var oldLines, newLines []string
	section := ""
	for _, ln := range strings.Split(data, "\n") {
		switch ln {
		case "-- old --":
			section = "old"
			continue
		case "-- new --":
			section = "new"
			continue
		}
		switch section {
		case "":
			if strings.TrimSpace(ln) == "" {
				continue
			}
			k, v, ok := strings.Cut(ln, ":")
			if !ok {
				return m, fmt.Errorf("bad header line %q", ln)
			}
			v = strings.TrimSpace(v)
			switch strings.TrimSpace(k) {
			case "kind":
				m.kind = v
			case "file":
				m.file = v
			case "expect":
				m.expect = v
			case "config":
				m.cfg = v
			default:
				return m, fmt.Errorf("unknown header key %q", strings.TrimSpace(k))
			}
		case "old":
			oldLines = append(oldLines, ln)
		case "new":
			newLines = append(newLines, ln)
		}
	}
	m.old = joinSection(oldLines)
	m.new = joinSection(newLines)
	if m.kind == "" || m.file == "" || m.expect == "" || m.cfg == "" {
		return m, errors.New("missing kind, file, expect, or config header")
	}
	if m.old == "" || m.new == "" {
		return m, errors.New("empty old or new edit")
	}
	return m, nil
}

func joinSection(lines []string) string {
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return strings.Join(lines, "\n")
}

func configIndex() map[string]config {
	out := make(map[string]config, len(forkConfigs))
	for _, c := range forkConfigs {
		out[c.id] = c
	}
	return out
}

var mutantToolNames = map[string]string{"compiler": "compile", "linker": "link", "cgo": "cgo", "rules": "compile"}

type mutBuild struct {
	fork        toolchain
	cfg         config
	stage       string
	scratch     string
	shim        string
	overlayPath string
	env         []string
	args        []string
	outDir      string
	plain       []string
	cgoProg     []string
}

func (mb *mutBuild) driverBin() string {
	bin := mb.fork.bin
	if mb.cfg.logos {
		bin = mb.fork.logos
	}
	return bin
}

func (mb *mutBuild) corpus(t *testing.T, progs []string) {
	t.Helper()
	if err := stageCorpus(mb.fork, mb.stage, forkCache(), progs); err != nil {
		t.Fatalf("staging corpus: %v", err)
	}
	if mb.cfg.toml != "" {
		if err := os.WriteFile(filepath.Join(mb.stage, "logos.toml"), []byte(mb.cfg.toml), 0o644); err != nil {
			t.Fatalf("writing logos.toml: %v", err)
		}
	}
	for _, s := range corpusFails(t) {
		data, err := os.ReadFile(filepath.Join("testdata", "fail", s+".go"))
		if err != nil {
			t.Fatalf("reading fail corpus %s: %v", s, err)
		}
		if err := os.MkdirAll(filepath.Join(mb.stage, s), 0o777); err != nil {
			t.Fatalf("creating fail stage %s: %v", s, err)
		}
		if err := os.WriteFile(filepath.Join(mb.stage, s, "main.go"), data, 0o644); err != nil {
			t.Fatalf("writing fail stage %s: %v", s, err)
		}
	}
	mb.env = appendEnv(baseEnv(forkCache()), mb.cfg.env...)
	mb.args = slices.Clone(mb.cfg.args)
	mb.outDir = filepath.Join(mb.scratch, "out")
	plain, cgoProg, err := splitStaged(mb.stage, progs)
	if err != nil {
		t.Fatalf("classifying corpus: %v", err)
	}
	mb.plain, mb.cgoProg = plain, cgoProg
}

func (mb *mutBuild) apply(t *testing.T, m mutant) {
	t.Helper()
	switch m.kind {
	case "compiler", "linker", "cgo", "rules":
		toolName := mutantToolNames[m.kind]
		toolBin, err := mb.buildTool(t, toolName)
		if err != nil {
			t.Fatalf("mutant %s: building mutant tool failed: %v", m.id, err)
		}
		cache := mutantCache(m.id)
		os.MkdirAll(cache, 0o777)
		os.MkdirAll(cache+"-path", 0o777)
		mb.env = appendEnv(mb.env, "GOPARITY_MUTANT_TOOL="+toolName+"="+toolBin, "GOCACHE="+cache, "GOPATH="+cache+"-path")
		mb.args = append(mb.args, "-toolexec="+mb.shim)
	case "runtime", "std":
		mb.args = append(mb.args, "-overlay="+mb.overlayPath)
	default:
		t.Fatalf("mutant %s: unsupported kind %q", m.id, m.kind)
	}
}

func (mb *mutBuild) buildTool(t *testing.T, toolName string) (string, error) {
	t.Helper()
	outPath := filepath.Join(mb.scratch, toolName)
	ctx, cancel := context.WithTimeoutCause(context.Background(), 15*time.Minute, errors.New("goparity tool build timeout"))
	defer cancel()
	cmd := exec.CommandContext(ctx, mb.fork.bin, "build", "-overlay="+mb.overlayPath, "-o", outPath, "cmd/"+toolName)
	cmd.Dir = filepath.Join(mb.fork.goroot, "src")
	cmd.Env = baseEnv(forkCache())
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%v\n%s", err, clip(string(out)))
	}
	return outPath, nil
}

func (mb *mutBuild) run(t *testing.T) (string, error) {
	t.Helper()
	sb := stagedBuild{bin: mb.driverBin(), stage: mb.stage, outDir: mb.outDir, args: mb.args, env: mb.env, progs: slices.Concat(mb.plain, mb.cgoProg), wantCgo: true}
	_, out, err := sb.build()
	return out, err
}

func (mb *mutBuild) buildFile(t *testing.T, stem string) error {
	t.Helper()
	args := append([]string{"build"}, append(slices.Clone(mb.args), "-o", filepath.Join(mb.scratch, "failbin-"+stem), "./"+stem)...)
	ctx, cancel := context.WithTimeoutCause(context.Background(), 5*time.Minute, errors.New("goparity fail build timeout"))
	defer cancel()
	cmd := exec.CommandContext(ctx, mb.driverBin(), args...)
	cmd.Dir = mb.stage
	cmd.Env = mb.env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Logf("mutant build of %s failed: %v\n%s", stem, err, clip(string(out)))
	}
	return err
}

func (mb *mutBuild) cleanExpect(t *testing.T, prog string) (observation, error) {
	t.Helper()
	outDir := filepath.Join(mb.scratch, "out-clean")
	env := appendEnv(baseEnv(forkCache()), mb.cfg.env...)
	sb := stagedBuild{bin: mb.driverBin(), stage: mb.stage, outDir: outDir, args: slices.Clone(mb.cfg.args), env: env, progs: []string{prog}, wantCgo: true}
	if _, out, err := sb.build(); err != nil {
		return observation{}, fmt.Errorf("clean build failed: %v\n%s", err, clip(out))
	}
	return runBinary(filepath.Join(outDir, prog), "4")
}

func (mb *mutBuild) cleanFile(t *testing.T, stem string) error {
	t.Helper()
	env := appendEnv(baseEnv(forkCache()), mb.cfg.env...)
	args := append([]string{"build"}, append(slices.Clone(mb.cfg.args), "-o", filepath.Join(mb.scratch, "failbin-clean-"+stem), "./"+stem)...)
	ctx, cancel := context.WithTimeoutCause(context.Background(), 5*time.Minute, errors.New("goparity fail build timeout"))
	defer cancel()
	cmd := exec.CommandContext(ctx, mb.driverBin(), args...)
	cmd.Dir = mb.stage
	cmd.Env = env
	return cmd.Run()
}

func TestParityMutants(t *testing.T) {
	if !*releaseFlag {
		t.Skip("release tier only; pass -goparity.release")
	}
	fork := forkTool(t)
	up, upOn := upstreamTool(t)
	progs := corpusPrograms(t)
	fails := corpusFails(t)
	ms := loadMutants(t)
	if len(ms) == 0 {
		t.Fatal("no mutants under testdata/mutants")
	}
	base := t.TempDir()
	cfgs := configIndex()
	var shim string
	needShim := false
	for _, m := range ms {
		if _, ok := mutantToolNames[m.kind]; ok {
			needShim = true
		} else if m.kind != "runtime" && m.kind != "std" {
			t.Errorf("mutant %s: unsupported kind %q (the harness applies compiler, linker, cgo, rules, runtime, std)", m.id, m.kind)
		}
	}
	if needShim {
		shim = filepath.Join(base, "toolexec-shim")
		pkgDir, err := filepath.Abs("testdata/toolexec")
		if err != nil {
			t.Fatalf("resolving testdata/toolexec: %v", err)
		}
		ctx, cancel := context.WithTimeoutCause(context.Background(), 5*time.Minute, errors.New("goparity shim build timeout"))
		defer cancel()
		cmd := exec.CommandContext(ctx, fork.bin, "build", "-o", shim, pkgDir)
		cmd.Env = baseEnv(forkCache())
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("building toolexec shim: %v\n%s", err, clip(string(out)))
		}
	}
	upOnce := &upOnceBuild{up: up, base: base, progs: progs, on: upOn}
	for _, m := range ms {
		t.Run(m.id+"-"+m.expect, func(t *testing.T) {
			cfg, ok := cfgs[m.cfg]
			if !ok {
				t.Fatalf("mutant %s names unknown config %s", m.id, m.cfg)
			}
			target := filepath.Join(fork.goroot, filepath.FromSlash(m.file))
			src, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("reading mutant target: %v", err)
			}
			if n := strings.Count(string(src), m.old); n != 1 {
				t.Fatalf("mutant %s: old text occurs %d times in %s: stale mutant", m.id, n, m.file)
			}
			scratch := t.TempDir()
			mutPath := filepath.Join(scratch, "with-"+filepath.Base(target))
			if err := os.WriteFile(mutPath, []byte(strings.Replace(string(src), m.old, m.new, 1)), 0o644); err != nil {
				t.Fatalf("writing mutant copy: %v", err)
			}
			overlayBytes, err := json.Marshal(map[string]map[string]string{"Replace": {target: mutPath}})
			if err != nil {
				t.Fatalf("marshalling overlay: %v", err)
			}
			overlayPath := filepath.Join(scratch, "overlay.json")
			if err := os.WriteFile(overlayPath, overlayBytes, 0o644); err != nil {
				t.Fatalf("writing overlay: %v", err)
			}
			mb := &mutBuild{fork: fork, cfg: cfg, stage: filepath.Join(scratch, "src"), scratch: scratch, shim: shim, overlayPath: overlayPath}
			mb.corpus(t, progs)
			mb.apply(t, m)
			failStem := strings.TrimSuffix(m.expect, ".go")
			if slices.Contains(fails, failStem) {
				if cerr := mb.cleanFile(t, failStem); cerr == nil {
					t.Errorf("mutant %s unevaluable: fail corpus file %s already builds under %s without the mutant", m.id, m.expect, m.cfg)
					return
				}
				if err := mb.buildFile(t, failStem); err != nil {
					t.Errorf("mutant %s survived: fail corpus file %s is still rejected under %s", m.id, m.expect, m.cfg)
					return
				}
				t.Logf("mutant killed: fail corpus file %s now builds under %s", m.expect, m.cfg)
				return
			}
			if !slices.Contains(progs, m.expect) {
				t.Fatalf("mutant %s: expect target %q is neither a run program nor a fail corpus file", m.id, m.expect)
			}
			buildOut, buildErr := mb.run(t)
			if buildErr != nil {
				t.Fatalf("mutant %s: corpus build failed under mutant (invalid probe): %v\n%s", m.id, buildErr, clip(buildOut))
			}
			fobs, ferr := runBinary(filepath.Join(mb.outDir, m.expect), "4")
			if ferr != nil {
				t.Fatalf("mutant %s: %v", m.id, ferr)
			}
			red := fobs.exit != 0 || strings.Contains(fobs.stdout, "LAW")
			if upOn {
				ub, err := upOnce.build(t, cfg.upstream)
				if err != nil {
					t.Fatalf("upstream build %s failed: %v\n%s", cfg.upstream, err, clip(ub.output))
				}
				uobs, uerr := runBinary(filepath.Join(ub.outDir, m.expect), "4")
				if uerr != nil {
					t.Fatalf("mutant %s: upstream run: %v", m.id, uerr)
				}
				clean, cerr := mb.cleanExpect(t, m.expect)
				if cerr != nil {
					t.Fatalf("mutant %s: clean baseline: %v", m.id, cerr)
				}
				if !obsEqual(clean, uobs) {
					t.Errorf("mutant %s unevaluable: %s already red under %s without the mutant (clean %s vs upstream %s)", m.id, m.expect, m.cfg, obsDesc(clean), obsDesc(uobs))
					return
				}
				if !obsEqual(fobs, uobs) {
					red = true
				}
			}
			if !red {
				if !upOn {
					t.Skipf("mutant %s: cannot evaluate detection with the upstream oracle off: the fork stayed green under %s, so a surviving mutant is indistinguishable from an equivalent one", m.id, m.cfg)
				}
				t.Errorf("mutant %s survived: %s stayed green under %s (exit %d)", m.id, m.expect, m.cfg, fobs.exit)
			}
		})
	}
}

type upOnceBuild struct {
	mu    sync.Mutex
	up    toolchain
	base  string
	progs []string
	on    bool
	done  map[string]buildResult
}

func (uo *upOnceBuild) build(t *testing.T, uID string) (buildResult, error) {
	t.Helper()
	if !uo.on {
		return buildResult{}, errors.New("upstream oracle off")
	}
	var u *upConfig
	for i := range upConfigs {
		if upConfigs[i].id == uID {
			u = &upConfigs[i]
		}
	}
	if u == nil {
		return buildResult{}, fmt.Errorf("unknown upstream config %s", uID)
	}
	uo.mu.Lock()
	defer uo.mu.Unlock()
	if b, ok := uo.done[u.id]; ok {
		return b, b.err
	}
	if uo.done == nil {
		uo.done = map[string]buildResult{}
	}
	stage := filepath.Join(uo.base, "mut-up-"+u.id)
	if err := stageCorpus(uo.up, stage, upCache(), uo.progs); err != nil {
		return buildResult{}, fmt.Errorf("staging corpus: %w", err)
	}
	env := baseEnv(upCache())
	if u.race {
		env = appendEnv(env, "CGO_ENABLED=1")
	}
	env = appendEnv(env, u.env...)
	outDir := filepath.Join(uo.base, "mut-out-up-"+u.id)
	sb := stagedBuild{bin: uo.up.bin, stage: stage, outDir: outDir, args: u.args, env: env, progs: uo.progs, wantCgo: true}
	built, out, err := sb.build()
	b := buildResult{outDir: outDir, output: out, err: err, progs: built}
	uo.done[u.id] = b
	return b, b.err
}

func sweepEligible(t *testing.T, goroot string) []string {
	t.Helper()
	var out []string
	for _, dir := range []string{".", "ken", "chan", "interface", "fixedbugs", "typeparam"} {
		matches, err := filepath.Glob(filepath.Join(goroot, "test", dir, "*.go"))
		if err != nil {
			t.Fatalf("globbing sweep dir %s: %v", dir, err)
		}
		for _, m := range matches {
			data, err := os.ReadFile(m)
			if err != nil {
				t.Fatalf("reading sweep file %s: %v", m, err)
			}
			if hasBareRun(data) {
				out = append(out, m)
			}
		}
	}
	slices.Sort(out)
	return out
}

func hasBareRun(data []byte) bool {
	bare := false
	for ln := range strings.SplitSeq(string(data), "\n") {
		t := strings.TrimSpace(ln)
		if t == "// run" {
			bare = true
			continue
		}
		for _, p := range []string{"// run", "// runoutput", "// rundir", "// runindir", "// buildrundir"} {
			if t == p || strings.HasPrefix(t, p+" ") || strings.HasPrefix(t, p+"\t") {
				return false
			}
		}
	}
	return bare
}

const sweepExclusionFrac = 0.10

type sweepFile struct {
	pkg   string
	fname string
	data  []byte
}

type sweepJob struct {
	fork toolchain
	up   toolchain
	base string
	cfgs []config
	ups  []upConfig
}

func maskBin(o observation, bin string) observation {
	o.stdout = strings.ReplaceAll(o.stdout, bin, "BIN")
	o.stderr = strings.ReplaceAll(o.stderr, bin, "BIN")
	return o
}

var sweepPanicHead = regexp.MustCompile(`^(panic: |fatal error: |\[recovered\]|\s+panic: |\s*\[signal SIG)`)

func sweepObs(o observation, bin string) observation {
	o = maskBin(o, bin)
	var kept []string
	for ln := range strings.SplitSeq(o.stderr, "\n") {
		if sweepPanicHead.MatchString(ln) {
			kept = append(kept, ln)
		}
	}
	o.stderr = strings.Join(kept, "\n")
	return o
}

func stageSweepPkg(dir, pkg string, data []byte, fname, toml string) error {
	if err := os.MkdirAll(filepath.Join(dir, pkg), 0o777); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module sweep\n\ngo 1.27\n"), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, pkg, fname), data, 0o644); err != nil {
		return err
	}
	if toml != "" {
		if err := os.WriteFile(filepath.Join(dir, "logos.toml"), []byte(toml), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func (j sweepJob) buildRow(bin, stage, outDir string, args, env []string, pkg string) (string, error) {
	sb := stagedBuild{bin: bin, stage: stage, outDir: outDir, args: args, env: env, progs: []string{pkg}, wantCgo: true}
	_, out, err := sb.build()
	return out, err
}

type sweepPlaced struct {
	bin string
	obs observation
}

func (j sweepJob) compareRow(c config, fbin string, fobs observation, upObs map[string]sweepPlaced) string {
	uo := upObs[c.upstream]
	if obsEqual(fobs, uo.obs) {
		return ""
	}
	fo2, err := runSweepBinary(fbin)
	if err != nil {
		return fmt.Sprintf("fork %s rerun failed: %v", c.id, err)
	}
	uo2, err := runSweepBinary(uo.bin)
	if err != nil {
		return fmt.Sprintf("upstream %s rerun failed: %v", c.upstream, err)
	}
	if obsEqual(sweepObs(fo2, fbin), sweepObs(uo2, uo.bin)) {
		return ""
	}
	d := firstDivergence(fobs, uo.obs)
	return fmt.Sprintf("%s vs %s: %s (%s vs %s)", c.id, c.upstream, d, obsDesc(fobs), obsDesc(uo.obs))
}

func (j sweepJob) check(f sweepFile) (excluded, failed string) {
	var dirs []string
	defer func() {
		for _, d := range dirs {
			os.RemoveAll(d)
		}
	}()
	upObs := make(map[string]sweepPlaced, len(j.ups))
	for _, u := range j.ups {
		st := filepath.Join(j.base, "st-"+f.pkg+"-"+u.id)
		if err := stageSweepPkg(st, f.pkg, f.data, f.fname, ""); err != nil {
			return fmt.Sprintf("staging failed: %v", err), ""
		}
		dirs = append(dirs, st)
		outDir := filepath.Join(j.base, "out-"+f.pkg+"-"+u.id)
		dirs = append(dirs, outDir)
		env := baseEnv(upCache())
		if u.race {
			env = appendEnv(env, "CGO_ENABLED=1")
		}
		env = appendEnv(env, u.env...)
		out, err := j.buildRow(j.up.bin, st, outDir, u.args, env, f.pkg)
		if err != nil {
			return "upstream build failed: " + clip(err.Error()+" "+out), ""
		}
		bin := filepath.Join(outDir, f.pkg)
		o, err := runSweepBinary(bin)
		if err != nil {
			return "", fmt.Sprintf("upstream %s run failed: %v", u.id, err)
		}
		upObs[u.id] = sweepPlaced{bin, sweepObs(o, bin)}
	}
	for _, u := range j.ups {
		if u.race {
			continue
		}
		if !obsEqual(upObs[u.id].obs, upObs[j.ups[0].id].obs) {
			return "upstream rows differ (no stable oracle)", ""
		}
	}
	for _, c := range j.cfgs {
		st := filepath.Join(j.base, "st-"+f.pkg+"-"+c.id)
		if err := stageSweepPkg(st, f.pkg, f.data, f.fname, c.toml); err != nil {
			return fmt.Sprintf("staging failed: %v", err), ""
		}
		dirs = append(dirs, st)
		outDir := filepath.Join(j.base, "out-"+f.pkg+"-"+c.id)
		dirs = append(dirs, outDir)
		bin := j.fork.bin
		if c.logos {
			bin = j.fork.logos
		}
		out, err := j.buildRow(bin, st, outDir, c.args, appendEnv(baseEnv(forkCache()), c.env...), f.pkg)
		if err != nil {
			return "", fmt.Sprintf("fork %s build failed where upstream builds: %v\n%s", c.id, err, clip(out))
		}
		fbin := filepath.Join(outDir, f.pkg)
		fo, err := runSweepBinary(fbin)
		if err != nil {
			return "", fmt.Sprintf("fork %s run failed: %v", c.id, err)
		}
		if failed := j.compareRow(c, fbin, sweepObs(fo, fbin), upObs); failed != "" {
			return "", failed
		}
	}
	return "", ""
}

func TestParityUpstreamSweep(t *testing.T) {
	if !*releaseFlag {
		t.Skip("release tier only; pass -goparity.release")
	}
	start := time.Now()
	fork := forkTool(t)
	up, upOn := upstreamTool(t)
	if !upOn {
		t.Skip("upstream oracle off: the sweep compares against live upstream output")
	}
	files := sweepEligible(t, up.goroot)
	if len(files) == 0 {
		t.Fatal("no sweep-eligible files under upstream test/")
	}
	sfiles := make([]sweepFile, 0, len(files))
	for i, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("reading sweep file %s: %v", f, err)
		}
		sfiles = append(sfiles, sweepFile{pkg: fmt.Sprintf("f%03d", i), fname: filepath.Base(f), data: data})
	}
	ups := activeUpConfigs(forkConfigs)
	base := t.TempDir()
	type outcome struct {
		excluded string
		failed   string
	}
	outcomes := make([]outcome, len(files))
	var wg sync.WaitGroup
	sem := make(chan struct{}, runtime.GOMAXPROCS(0))
	job := sweepJob{fork: fork, up: up, base: base, cfgs: forkConfigs, ups: ups}
	for i := range files {
		wg.Go(func() {
			sem <- struct{}{}
			defer func() { <-sem }()
			ex, fa := job.check(sfiles[i])
			outcomes[i] = outcome{ex, fa}
		})
	}
	wg.Wait()
	ran, skipped := 0, 0
	for i, o := range outcomes {
		rel, _ := filepath.Rel(filepath.Join(up.goroot, "test"), files[i])
		if o.failed != "" {
			t.Errorf("sweep: %s: %s", rel, o.failed)
			continue
		}
		if o.excluded != "" {
			t.Logf("sweep: excluded %s: %s", rel, o.excluded)
			skipped++
			continue
		}
		ran++
	}
	if ran == 0 {
		t.Errorf("sweep: no files compared")
	}
	if budget := int(sweepExclusionFrac * float64(len(files))); skipped > budget {
		t.Errorf("sweep: %d exclusions exceed budget %d (%d files)", skipped, budget, len(files))
	}
	t.Logf("sweep: %d files compared, %d excluded, %d fork configs, %d upstream rows, wall %s", ran, skipped, len(forkConfigs), len(ups), time.Since(start))
}

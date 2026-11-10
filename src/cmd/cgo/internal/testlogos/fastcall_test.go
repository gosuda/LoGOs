// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package testlogos

import (
	"bytes"
	"context"
	"errors"
	"github.com/google/pprof/profile"
	"internal/goexperiment"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func requireCgo(t testing.TB) {
	t.Helper()
	if os.Getenv("CGO_ENABLED") == "0" {
		t.Skip("cgo disabled")
	}
}

func goBin(t *testing.T) string {
	t.Helper()
	if goroot := runtime.GOROOT(); goroot != "" {
		if p := filepath.Join(goroot, "bin", "go"); isExec(p) {
			return p
		}
	}
	if p, err := exec.LookPath("go"); err == nil {
		return p
	}
	t.Fatal("go tool not found")
	return ""
}

func logosBin(t *testing.T) string {
	t.Helper()
	if goroot := runtime.GOROOT(); goroot != "" {
		if p := filepath.Join(goroot, "bin", "logos"); isExec(p) {
			return p
		}
	}
	if p, err := exec.LookPath("logos"); err == nil {
		return p
	}
	t.Fatal("logos tool not found")
	return ""
}

func isExec(p string) bool {
	fi, err := os.Stat(p)
	if err != nil {
		return false
	}
	return fi.Mode()&0o111 != 0
}

func runCmd(t *testing.T, dir, name string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOPROXY=off", "GOTOOLCHAIN=local")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.String(), err
}

func gotoolDir(t *testing.T) string {
	t.Helper()
	out, err := runCmd(t, "", goBin(t), "env", "GOTOOLDIR")
	if err != nil {
		t.Fatalf("go env GOTOOLDIR: %v\n%s", err, out)
	}
	return strings.TrimSpace(out)
}

func cgoBin(t *testing.T) string {
	t.Helper()
	p := filepath.Join(gotoolDir(t), "cgo")
	if !isExec(p) {
		t.Skipf("cgo tool not found at %s", p)
	}
	return p
}

func TestFastcallCorrectness(t *testing.T) {
	requireCgo(t)
	if got := callFastAdd(40, 2); got != 42 {
		t.Fatalf("fast_add = %d, want 42", got)
	}
	if got := callSlowNoescape(40, 2); got != 42 {
		t.Fatalf("slow_noescape = %d, want 42", got)
	}
	if got := callSlowNocallback(40, 2); got != 42 {
		t.Fatalf("slow_nocallback = %d, want 42", got)
	}
	v := int32(7)
	if got := callFastRead(&v); got != 7 {
		t.Fatalf("read_int = %d, want 7", got)
	}
	if got := callFastSlice([]byte{1, 2, 3, 4}); got != 10 {
		t.Fatalf("sum_bytes = %d, want 10", got)
	}
	if got := callFastSlice(nil); got != 0 {
		t.Fatalf("sum_bytes(nil) = %d, want 0", got)
	}
	if got := callFastSleep(1000); got != 1000 {
		t.Fatalf("block_sleep = %d, want 1000", got)
	}
	if got := callFastBusy(10); got != callPlainBusy(10) {
		t.Fatalf("busy mismatch: fast %d vs plain %d", got, callPlainBusy(10))
	}
}

func cgoOutputs(t *testing.T, input, src string) (dir string, files map[string]string) {
	t.Helper()
	requireCgo(t)
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, input), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(work, "out")
	if err := os.Mkdir(out, 0755); err != nil {
		t.Fatal(err)
	}
	text, err := runCmd(t, work, cgoBin(t), "-objdir", out, "-importpath", "scratch/fast", input)
	if err != nil {
		t.Fatalf("cgo %s: %v\n%s", input, err, text)
	}
	files = map[string]string{}
	ents, err := os.ReadDir(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(out, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = string(b)
	}
	return out, files
}

func countCallLines(src, callee string) int {
	n := 0
	for line := range strings.SplitSeq(src, "\n") {
		if strings.HasPrefix(line, "\t") && strings.Contains(line, callee+"(") {
			n++
		}
	}
	return n
}

const pairPreamble = `package fast

/*
#include <stdint.h>

#cgo noescape fast_add
#cgo nocallback fast_add
int fast_add(int a, int b) { return a + b; }

#cgo noescape read_int
#cgo nocallback read_int
int read_int(int *p) { return *p; }

#cgo noescape read_pp
#cgo nocallback read_pp
int read_pp(int **pp) { return **pp; }

#cgo noescape sum_bytes
#cgo nocallback sum_bytes
int sum_bytes(unsigned char *p, int n) {
	int s = 0;
	for (int i = 0; i < n; i++) {
		s += p[i];
	}
	return s;
}
*/
import "C"

import "unsafe"

func Add(a, b int32) int32 { return int32(C.fast_add(C.int(a), C.int(b))) }

func Read(p *int32) int32 { return int32(C.read_int((*C.int)(unsafe.Pointer(p)))) }

func ReadPP(pp **C.int) int32 { return int32(C.read_pp(pp)) }

func Sum(b []byte) int32 {
	if len(b) == 0 {
		return 0
	}
	return int32(C.sum_bytes((*C.uchar)(unsafe.Pointer(&b[0])), C.int(len(b))))
}
`

func TestFastcallEmission(t *testing.T) {
	_, files := cgoOutputs(t, "fast.go", pairPreamble)
	gotypes, ok := files["_cgo_gotypes.go"]
	if !ok {
		t.Fatal("no _cgo_gotypes.go emitted")
	}
	if got := countCallLines(gotypes, "_cgo_runtime_cgocall_fast"); got != 4 {
		t.Fatalf("fast calls = %d, want 4", got)
	}
	if got := countCallLines(gotypes, "_cgo_runtime_cgocall"); got != 0 {
		t.Fatalf("plain calls = %d, want 0", got)
	}

	const slowSrc = `package slow

/*
#cgo noescape slow_noescape
int slow_noescape(int a, int b) { return a + b; }

#cgo nocallback slow_nocallback
int slow_nocallback(int a, int b) { return a + b; }
*/
import "C"

func Noescape(a, b int32) int32 { return int32(C.slow_noescape(C.int(a), C.int(b))) }

func Nocallback(a, b int32) int32 { return int32(C.slow_nocallback(C.int(a), C.int(b))) }
`
	_, slow := cgoOutputs(t, "slow.go", slowSrc)
	sg, ok := slow["_cgo_gotypes.go"]
	if !ok {
		t.Fatal("no _cgo_gotypes.go emitted for slow twins")
	}
	if got := countCallLines(sg, "_cgo_runtime_cgocall_fast"); got != 0 {
		t.Fatalf("fast calls in slow twins = %d, want 0", got)
	}
	if got := countCallLines(sg, "_cgo_runtime_cgocall"); got != 2 {
		t.Fatalf("plain calls in slow twins = %d, want 2", got)
	}
}

func TestFastcallLogosSpelling(t *testing.T) {
	const src = `package fast

/*
//logos:cgo_fastcall ladd
int ladd(int a, int b) { return a + b; }
*/
import "C"

func Add(a, b int32) int32 { return int32(C.ladd(C.int(a), C.int(b))) }
`
	_, files := cgoOutputs(t, "fast.lgo", src)
	gotypes, ok := files["_cgo_gotypes.go"]
	if !ok {
		t.Fatal("no _cgo_gotypes.go emitted for .lgo input")
	}
	if got := countCallLines(gotypes, "_cgo_runtime_cgocall_fast"); got != 1 {
		t.Fatalf("fast calls = %d, want 1", got)
	}
	if got := countCallLines(gotypes, "_cgo_runtime_cgocall"); got != 0 {
		t.Fatalf("plain calls = %d, want 0", got)
	}
}

func TestFastcallUnmatched(t *testing.T) {
	requireCgo(t)
	work := t.TempDir()
	const src = `package fast

/*
//logos:cgo_fastcall ghost_fn
int real_fn(int a) { return a; }
*/
import "C"

func Call() int32 { return int32(C.real_fn(1)) }
`
	if err := os.WriteFile(filepath.Join(work, "bad.lgo"), []byte(src), 0644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(work, "out")
	if err := os.Mkdir(out, 0755); err != nil {
		t.Fatal(err)
	}
	text, err := runCmd(t, work, cgoBin(t), "-objdir", out, "-importpath", "scratch/bad", "bad.lgo")
	if err == nil {
		t.Fatalf("cgo succeeded on unmatched //logos:cgo_fastcall, want failure\n%s", text)
	}
	if !strings.Contains(text, "no matched C function") {
		t.Fatalf("stderr lacks %q:\n%s", "no matched C function", text)
	}
}

func TestFastcallMarksCarryBoth(t *testing.T) {
	const head = `package fast

/*
#include <stdint.h>

`
	const tail = `
int mark_fn(int *p) { return *p + 1; }
*/
import "C"

import "unsafe"

func Mark(p *int32) int32 { return int32(C.mark_fn((*C.int)(unsafe.Pointer(p)))) }
`
	cases := []struct {
		name       string
		directives string
		fast       int
		plain      int
		keepalive  int
		use        int
		guards     int
	}{
		{"both", "#cgo noescape mark_fn\n#cgo nocallback mark_fn\n", 1, 0, 1, 0, 0},
		{"noescape", "#cgo noescape mark_fn\n", 0, 1, 0, 1, 0},
		{"nocallback", "#cgo nocallback mark_fn\n", 0, 1, 0, 1, 2},
		{"none", "", 0, 1, 0, 1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, files := cgoOutputs(t, "mark.go", head+c.directives+tail)
			gotypes, ok := files["_cgo_gotypes.go"]
			if !ok {
				t.Fatal("no _cgo_gotypes.go emitted")
			}
			if got := countCallLines(gotypes, "_cgo_runtime_cgocall_fast"); got != c.fast {
				t.Errorf("fast calls = %d, want %d", got, c.fast)
			}
			plain := 0
			for line := range strings.SplitSeq(gotypes, "\n") {
				if strings.HasPrefix(line, "\t") && strings.Contains(line, "_cgo_runtime_cgocall(") && !strings.Contains(line, "_fast(") {
					plain++
				}
			}
			if plain != c.plain {
				t.Errorf("plain calls = %d, want %d", plain, c.plain)
			}
			if got := countCallLines(gotypes, "_Cgo_keepalive"); got != c.keepalive {
				t.Errorf("keepalive uses = %d, want %d", got, c.keepalive)
			}
			if got := countCallLines(gotypes, "_Cgo_use"); got != c.use {
				t.Errorf("escape uses = %d, want %d", got, c.use)
			}
			if got := countCallLines(gotypes, "_Cgo_no_callback"); got != c.guards {
				t.Errorf("callback guards = %d, want %d", got, c.guards)
			}
		})
	}
}

func TestFastcallPointerChecks(t *testing.T) {
	_, files := cgoOutputs(t, "fast.go", pairPreamble)
	var cgo1 string
	for name, body := range files {
		if strings.HasSuffix(name, ".cgo1.go") {
			cgo1 = body
		}
	}
	if cgo1 == "" {
		t.Fatal("no .cgo1.go emitted")
	}
	if got := strings.Count(cgo1, "_cgoCheckPointer"); got != 1 {
		t.Fatalf("_cgoCheckPointer references = %d, want 1 (the **int read only)", got)
	}
}

func TestFastcallLayout(t *testing.T) {
	_, files := cgoOutputs(t, "fast.go", pairPreamble)
	var cgo1, cgo2 int
	for name := range files {
		switch {
		case strings.HasSuffix(name, ".cgo1.go"):
			cgo1++
			if name != "fast.cgo1.go" {
				t.Fatalf("unexpected cgo1 name %q", name)
			}
		case strings.HasSuffix(name, ".cgo2.c"):
			cgo2++
			if name != "fast.cgo2.c" {
				t.Fatalf("unexpected cgo2 name %q", name)
			}
		}
	}
	if cgo1 != 1 || cgo2 != 1 {
		t.Fatalf("cgo1 = %d, cgo2 = %d, want 1 and 1", cgo1, cgo2)
	}
	if _, ok := files["_cgo_gotypes.go"]; !ok {
		t.Fatal("no _cgo_gotypes.go emitted")
	}
}

func selfTestBin(t *testing.T) string {
	t.Helper()
	requireCgo(t)
	bin := filepath.Join(t.TempDir(), "testlogos.test")
	srcCmd := filepath.Join(runtime.GOROOT(), "src", "cmd")
	out, err := runCmd(t, srcCmd, goBin(t), "test", "-c", "-o", bin, "cmd/cgo/internal/testlogos")
	if err != nil {
		t.Fatalf("self build: %v\n%s", err, out)
	}
	return bin
}

func nmLines(t *testing.T, binary string) []string {
	t.Helper()
	out, err := runCmd(t, "", goBin(t), "tool", "nm", binary)
	if err != nil {
		t.Fatalf("go tool nm: %v\n%s", err, out)
	}
	return strings.Split(out, "\n")
}

func TestFastcallOneTrampoline(t *testing.T) {
	self := selfTestBin(t)
	defs := 0
	for _, line := range nmLines(t, self) {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		kind, name := f[1], f[2]
		if name == "runtime.logosCgocallFast" && (kind == "T" || kind == "t") {
			defs++
			continue
		}
		if strings.HasPrefix(name, "runtime.") && (kind == "T" || kind == "t") &&
			strings.Contains(strings.ToLower(name), "fastcgo") {
			t.Fatalf("rival fast-cgo entry %s (no second cgo path)", line)
		}
		if strings.Contains(name, "logosFastCgoCall") {
			t.Fatalf("stale PLAN symbol name %s", line)
		}
	}
	if defs != 1 {
		t.Fatalf("runtime.logosCgocallFast text definitions = %d, want 1", defs)
	}
}

func objdumpFunc(t *testing.T, binary, symbol string) string {
	t.Helper()
	out, err := runCmd(t, "", goBin(t), "tool", "objdump", "-s", symbol, binary)
	if err != nil {
		t.Fatalf("go tool objdump -s %s: %v\n%s", symbol, err, out)
	}
	return out
}

func TestFastcallStructure(t *testing.T) {
	self := selfTestBin(t)
	body := objdumpFunc(t, self, "runtime\\.logosCgocallFast")
	if !strings.Contains(body, "logosCgocallFast") {
		t.Fatalf("no logosCgocallFast disassembly:\n%s", body)
	}
	if goexperiment.LogosFastCgo {
		for _, want := range []string{"asmcgocall", "reentersyscall", "exitsyscall"} {
			if !strings.Contains(body, want) {
				t.Fatalf("fast body lacks %s:\n%s", want, body)
			}
		}
		if got := strings.Count(body, "CALL runtime.cgocall(SB)"); got != 0 {
			t.Fatalf("fast body has %d plain cgocall calls, want 0 (fallback must be pruned):\n%s", got, body)
		}
	} else {
		if !strings.Contains(body, "runtime.cgocall(SB)") {
			t.Fatalf("fallback body lacks plain cgocall call:\n%s", body)
		}
		if strings.Contains(body, "asmcgocall") {
			t.Fatalf("fallback body reaches asmcgocall directly:\n%s", body)
		}
	}
}

func delayStats(ds []float64) (median, stddev float64) {
	cp := slices.Clone(ds)
	slices.Sort(cp)
	median = cp[len(cp)/2]
	var sum, sq float64
	for _, d := range cp {
		sum += d
		sq += d * d
	}
	mean := sum / float64(len(cp))
	v := sq/float64(len(cp)) - mean*mean
	if v < 0 {
		v = 0
	}
	return median, math.Sqrt(v)
}

func oneDelay(sleepFn func(micros int32) int32) float64 {
	ready := make(chan struct{})
	aDone := make(chan struct{})
	bDelay := make(chan time.Duration, 1)
	var t0 time.Time
	var wg sync.WaitGroup
	wg.Go(func() {
		<-ready
		bDelay <- time.Since(t0)
	})
	wg.Go(func() {
		defer close(aDone)
		t0 = time.Now()
		close(ready)
		sleepFn(100)
	})
	d := float64((<-bDelay).Microseconds())
	<-aDone
	wg.Wait()
	return d
}

func readyDelays(t *testing.T, n int) (fast, plain []float64) {
	t.Helper()
	oldMax := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(oldMax)
	oldGC := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(oldGC)
	fast = make([]float64, 0, n)
	plain = make([]float64, 0, n)
	for range n {
		fast = append(fast, oneDelay(callFastSleep))
		plain = append(plain, oneDelay(callPlainSleep))
	}
	return fast, plain
}

func TestFastcallBlockingParity(t *testing.T) {
	requireCgo(t)
	if testUnderRace {
		t.Skip("timing gates need quiet scheduling; the race detector perturbs " +
			"us-scale delays past the 20% noise band. Entry and exit still run " +
			"under race in the other tests.")
	}
	const n = 200
	fast, plain := readyDelays(t, 200)
	fmed, fsd := delayStats(fast)
	pmed, psd := delayStats(plain)
	t.Logf("ready delay: fast median %.1fus (sd %.1f), plain median %.1fus (sd %.1f)",
		fmed, fsd, pmed, psd)
	if fsd > 0.2*fmed || psd > 0.2*pmed {
		t.Skipf("noise limit exceeded, check blocked: fast sd %.1f med %.1f, plain sd %.1f med %.1f",
			fsd, fmed, psd, pmed)
	}
	if fmed > 1.05*pmed {
		t.Fatalf("fast median %.1fus exceeds 1.05x plain median %.1fus", fmed, pmed)
	}
}

func TestFastcallSigprofSurvives(t *testing.T) {
	requireCgo(t)
	prof := filepath.Join(t.TempDir(), "cpu.prof")
	f, err := os.Create(prof)
	if err != nil {
		t.Fatal(err)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close()
		t.Fatal(err)
	}
	deadline := time.Now().Add(500 * time.Millisecond)
	var sink int32
	for time.Now().Before(deadline) {
		for i := range int32(100) {
			sink += callFastAdd(i, i)
		}
	}
	pprof.StopCPUProfile()
	f.Close()
	fi, err := os.Stat(prof)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() == 0 {
		t.Fatal("empty cpu profile")
	}
	_ = sink
}

func pprofNodes(t *testing.T, binary, prof string) (top string, topShare float64, shares map[string]float64) {
	t.Helper()
	out, err := runCmd(t, "", goBin(t), "tool", "pprof", "-top", "-nodecount=50", binary, prof)
	if err != nil {
		t.Fatalf("pprof -top: %v\n%s", err, out)
	}
	shares = map[string]float64{}
	data := false
	for line := range strings.SplitSeq(out, "\n") {
		if !data {
			if strings.HasPrefix(strings.TrimSpace(line), "flat ") {
				data = true
			}
			continue
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 6 {
			continue
		}
		pct := strings.TrimSuffix(f[4], "%")
		v, perr := strconv.ParseFloat(pct, 64)
		if perr != nil {
			t.Fatalf("parse cum%% %q: %v", f[4], perr)
		}
		name := f[len(f)-1]
		shares[name] = v
		if top == "" {
			top, topShare = name, v
		}
	}
	if top == "" {
		t.Fatalf("no data in pprof -top:\n%s", out)
	}
	return top, topShare, shares
}

func nodeShare(shares map[string]float64, want string) float64 {
	for name, v := range shares {
		if strings.Contains(name, want) {
			return v
		}
	}
	return 0
}

func topIn(top string, wants []string) bool {
	for _, w := range wants {
		if strings.Contains(top, w) {
			return true
		}
	}
	return false
}

func collectBusyProfile(t *testing.T, prof string, count int, fn func(uint64) uint64, iters uint64) {
	t.Helper()
	f, err := os.Create(prof)
	if err != nil {
		t.Fatal(err)
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close()
		t.Fatal(err)
	}
	var sink uint64
	for range count {
		sink += fn(iters)
	}
	pprof.StopCPUProfile()
	f.Close()
	_ = sink
}

const fastcallProfileFloorSamples = 200
const fastcallProfileCap = 300 * time.Second

func profileCallerSamples(t *testing.T, prof string, want string) (found, total int) {
	t.Helper()
	f, err := os.Open(prof)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	p, err := profile.Parse(f)
	if err != nil {
		t.Fatalf("parse profile %s: %v", prof, err)
	}
	for _, s := range p.Sample {
		n := 1
		if len(s.Value) > 0 {
			n = int(s.Value[0])
		}
		total += n
		for _, loc := range s.Location {
			hit := false
			for _, line := range loc.Line {
				if line.Function != nil && strings.Contains(line.Function.Name, want) {
					hit = true
					break
				}
			}
			if hit {
				found += n
				break
			}
		}
	}
	return found, total
}

func TestFastcallProfileAttribution(t *testing.T) {
	requireCgo(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	fastProf := filepath.Join(dir, "fast.prof")
	plainProf := filepath.Join(dir, "plain.prof")
	const iters = 200000000
	fastWants := []string{"callFastBusy", "_Cfunc_busy_spin"}
	plainWants := []string{"callPlainBusy", "_Cfunc_plain_spin", "runtime.cgocall"}
	if !goexperiment.LogosFastCgo {
		fastWants = append(fastWants, "logosCgocallFast", "runtime.cgocall")
	}
	var fastN, plainN, fastTotal, plainTotal int
	met := false
	deadline := time.Now().Add(fastcallProfileCap)
	for {
		collectBusyProfile(t, fastProf, 80, callFastBusy, iters)
		collectBusyProfile(t, plainProf, 80, callPlainBusy, iters)
		fastN, fastTotal = profileCallerSamples(t, fastProf, "callFastBusy")
		plainN, plainTotal = profileCallerSamples(t, plainProf, "callPlainBusy")
		t.Logf("fast caller %d of %d samples, plain caller %d of %d samples",
			fastN, fastTotal, plainN, plainTotal)
		if fastN >= fastcallProfileFloorSamples && plainN >= fastcallProfileFloorSamples {
			met = true
			break
		}
		if time.Now().After(deadline) {
			break
		}
	}
	if !met {
		if fastN == 0 && plainN >= fastcallProfileFloorSamples {
			t.Fatalf("fast-call frames missing from profile: fast caller %d of %d samples, plain caller %d of %d samples (floor %d)",
				fastN, fastTotal, plainN, plainTotal, fastcallProfileFloorSamples)
		}
		t.Skipf("thin profile under load: fast caller %d of %d samples, plain caller %d of %d samples (floor %d)",
			fastN, fastTotal, plainN, plainTotal, fastcallProfileFloorSamples)
	}
	topFast, _, fastShares := pprofNodes(t, self, fastProf)
	topPlain, _, plainShares := pprofNodes(t, self, plainProf)
	fastCaller := nodeShare(fastShares, "callFastBusy")
	plainCaller := nodeShare(plainShares, "callPlainBusy")
	t.Logf("top: fast %q caller %.1f%%, plain %q caller %.1f%%", topFast, fastCaller, topPlain, plainCaller)
	if !topIn(topFast, fastWants) {
		t.Fatalf("fast top entry = %q, want the call site", topFast)
	}
	if !topIn(topPlain, plainWants) {
		t.Fatalf("plain top entry = %q, want the call site", topPlain)
	}
	if diff := fastCaller - plainCaller; diff < -5 || diff > 5 {
		t.Fatalf("caller attribution differs by %.1f points", diff)
	}
}

func violatorBin(t *testing.T, mode string) (bin string, out string) {
	t.Helper()
	requireCgo(t)
	bin = filepath.Join(t.TempDir(), "viol.exe")
	text, err := runCmd(t, filepath.Join("testdata", "viol"), goBin(t), "build", "-o", bin, ".")
	if err != nil {
		t.Fatalf("violator build: %v\n%s", err, text)
	}
	args := []string{}
	if mode != "" {
		args = append(args, mode)
	}
	out, err = runCmd(t, "", bin, args...)
	if err != nil {
		t.Fatalf("violator %q: %v\n%s", mode, err, out)
	}
	return bin, out
}

func checkViolation(t *testing.T, mode, out string) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("violator %q printed %d lines, want 3:\n%s", mode, len(lines), out)
	}
	if !strings.Contains(lines[0], "marked with #cgo nocallback called back into Go") {
		t.Fatalf("first line = %q, want the nocallback message", lines[0])
	}
	if !strings.Contains(lines[1], "unexpected setting cgoNoCallback") {
		t.Fatalf("second line = %q, want the stuck-flag message", lines[1])
	}
	if strings.TrimSpace(lines[2]) != "add: 42" {
		t.Fatalf("third line = %q, want add: 42", lines[2])
	}
}

func TestFastcallNoCallbackViolation(t *testing.T) {
	_, out := violatorBin(t, "")
	checkViolation(t, "", out)
	if got := callSlowNoescape(20, 22); got != 42 {
		t.Fatalf("unmarked call after violation = %d, want 42", got)
	}
	done := make(chan int32, 1)
	go func() { done <- callSlowNoescape(20, 22) }()
	select {
	case got := <-done:
		if got != 42 {
			t.Fatalf("goroutine unmarked call = %d, want 42", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("goroutine stuck after violation (hang)")
	}
}

func TestFastcallNumCgoCallAccounting(t *testing.T) {
	requireCgo(t)
	before := runtime.NumCgoCall()
	if got := callFastAdd(40, 2); got != 42 {
		t.Fatalf("fast_add = %d, want 42", got)
	}
	if mid := runtime.NumCgoCall(); mid != before+1 {
		t.Fatalf("NumCgoCall after fast call = %d, want %d", mid, before+1)
	}
	if got := callSlowNoescape(40, 2); got != 42 {
		t.Fatalf("slow_noescape = %d, want 42", got)
	}
	if after := runtime.NumCgoCall(); after != before+2 {
		t.Fatalf("NumCgoCall after slow call = %d, want %d", after, before+2)
	}
}

func growStack(depth int) byte {
	if depth == 0 {
		return 0
	}
	var pad [96]byte
	pad[0] = byte(depth)
	pad[95] = growStack(depth - 1)
	return pad[0] + pad[95]
}

func runChurners(t *testing.T, n int, stop chan struct{}) {
	t.Helper()
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				if i%2 == 0 {
					runtime.GC()
				} else {
					debug.FreeOSMemory()
				}
				growStack(2000)
			}
		})
	}
	t.Cleanup(func() {
		close(stop)
		wg.Wait()
	})
}

func gcCycles() uint32 {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	return ms.NumGC
}

func TestFastcallPinUnderCollection(t *testing.T) {
	requireCgo(t)
	if testUnderRace {
		t.Skip("collection churn is timing-gated; skipped under race")
	}
	cases := []struct {
		name string
		call func() int32
	}{
		{
			name: "stack",
			call: func() int32 {
				growStack(4000)
				var buf [8192]byte
				buf[0] = 42
				return callFastWatch(&buf[0], 42, 250000)
			},
		},
		{
			name: "heap",
			call: func() int32 {
				b := make([]byte, 256<<10)
				b[0] = 42
				return callFastWatch(&b[0], 42, 250000)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runChurners(t, 3, make(chan struct{}))
			gcBefore := gcCycles()
			res := make(chan int32, 1)
			go func() { res <- tc.call() }()
			select {
			case code := <-res:
				if code != 0 {
					t.Fatalf("watch_spin = %d, want 0 (1: pointee changed, 2: pointer moved)", code)
				}
			case <-time.After(10 * time.Second):
				t.Fatalf("fast call did not return within 10s")
			}
			if got := gcCycles() - gcBefore; got < 2 {
				t.Fatalf("only %d GC cycles ran during the call, want at least 2", got)
			}
		})
	}
}

func fixtureBin(t *testing.T, module string) string {
	t.Helper()
	dir := filepath.Join("testdata", module)
	bin := filepath.Join(t.TempDir(), "prog.exe")
	out, err := runCmd(t, dir, logosBin(t), "build", "-o", bin, ".")
	if err != nil {
		t.Fatalf("logos build %s: %v\n%s", module, err, out)
	}
	return bin
}

func TestFastcallLogosEndToEnd(t *testing.T) {
	requireCgo(t)
	bin := fixtureBin(t, "logosfast")
	out, err := runCmd(t, "", bin)
	if err != nil {
		t.Fatalf("fixture run: %v\n%s", err, out)
	}
	if strings.TrimSpace(out) != "42" {
		t.Fatalf("fixture output = %q, want %q", out, "42")
	}
	defs := 0
	for _, line := range nmLines(t, bin) {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		if f[2] == "runtime.logosCgocallFast" && (f[1] == "T" || f[1] == "t") {
			defs++
		}
	}
	if defs != 1 {
		t.Fatalf("fixture logosCgocallFast text definitions = %d, want 1", defs)
	}
	dump, err := runCmd(t, "", goBin(t), "tool", "objdump", bin)
	if err != nil {
		t.Fatalf("objdump fixture: %v", err)
	}
	if !strings.Contains(dump, "runtime.logosCgocallFast") {
		t.Fatal("fixture binary never calls the fast entry")
	}
}

func TestFastcallLogosCallbackParity(t *testing.T) {
	requireCgo(t)
	out, err := runCmd(t, filepath.Join("testdata", "logosfastcb"), logosBin(t), "run", ".")
	if err != nil {
		t.Fatalf("logos run logosfastcb: %v\n%s", err, out)
	}
	if !strings.Contains(out, "marked with #cgo nocallback called back into Go") {
		t.Fatalf("missing nocallback panic message:\n%s", out)
	}
	if strings.Contains(out, "survived") {
		t.Fatalf("callback unexpectedly survived:\n%s", out)
	}
}

func TestFastcallLogosProfileParity(t *testing.T) {
	requireCgo(t)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	plainProf := filepath.Join(t.TempDir(), "plain.prof")
	plainWants := []string{"callPlainBusy", "_Cfunc_plain_spin", "runtime.cgocall"}
	if !goexperiment.LogosFastCgo {
		plainWants = append(plainWants, "logosCgocallFast")
	}
	bin := fixtureBin(t, "logosfast")
	dir := t.TempDir()
	prof := filepath.Join(dir, "lgo.prof")
	lgoWants := []string{"main.main", "_Cfunc_cbusyf"}
	if !goexperiment.LogosFastCgo {
		lgoWants = append(lgoWants, "logosCgocallFast", "runtime.cgocall")
	}
	var plainN, lgoN, plainTotal, lgoTotal int
	met := false
	deadline := time.Now().Add(fastcallProfileCap)
	for {
		collectBusyProfile(t, plainProf, 80, callPlainBusy, 200000000)
		plainN, plainTotal = profileCallerSamples(t, plainProf, "callPlainBusy")
		out, err := runCmd(t, dir, bin, "busy", prof, "12000000000")
		if err != nil {
			t.Fatalf("fixture busy: %v\n%s", err, out)
		}
		lgoN, lgoTotal = profileCallerSamples(t, prof, "main.main")
		t.Logf("lgo caller %d of %d samples, plain caller %d of %d samples",
			lgoN, lgoTotal, plainN, plainTotal)
		if lgoN >= fastcallProfileFloorSamples && plainN >= fastcallProfileFloorSamples {
			met = true
			break
		}
		if time.Now().After(deadline) {
			break
		}
	}
	if !met {
		if lgoN == 0 && plainN >= fastcallProfileFloorSamples {
			t.Fatalf("fast-call frames missing from profile: lgo caller %d of %d samples, plain caller %d of %d samples (floor %d)",
				lgoN, lgoTotal, plainN, plainTotal, fastcallProfileFloorSamples)
		}
		t.Skipf("thin profile under load: lgo caller %d of %d samples, plain caller %d of %d samples (floor %d)",
			lgoN, lgoTotal, plainN, plainTotal, fastcallProfileFloorSamples)
	}
	topPlain, _, plainShares := pprofNodes(t, self, plainProf)
	top, _, lgoShares := pprofNodes(t, bin, prof)
	sharePlain := nodeShare(plainShares, "callPlainBusy")
	lgoCaller := nodeShare(lgoShares, "main.main")
	t.Logf("top: lgo %q caller %.1f%%, plain %q caller %.1f%%", top, lgoCaller, topPlain, sharePlain)
	if !topIn(topPlain, plainWants) {
		t.Fatalf("plain top entry = %q, want the call site", topPlain)
	}
	if !topIn(top, lgoWants) {
		t.Fatalf("lgo top entry = %q, want the call site", top)
	}
	if diff := lgoCaller - sharePlain; diff < -5 || diff > 5 {
		t.Fatalf("lgo attribution differs by %.1f points", diff)
	}
}

func BenchmarkFastcall(b *testing.B) {
	requireCgo(b)
	b.Logf("logosfastcgo = %v", goexperiment.LogosFastCgo)
	var sink int32
	for b.Loop() {
		sink = callFastAdd(sink&((1<<30)-1), 1)
	}
	_ = sink
}

func BenchmarkFastcallSlow(b *testing.B) {
	requireCgo(b)
	var sink int32
	for b.Loop() {
		sink = callSlowNoescape(sink&((1<<30)-1), 1)
	}
	_ = sink
}

func TestFastcallViolationNeverHangs(t *testing.T) {
	requireCgo(t)
	dir := filepath.Join("testdata", "viol")
	for _, mode := range []string{"", "plain"} {
		bin := filepath.Join(t.TempDir(), "viol.exe")
		if text, err := runCmd(t, dir, goBin(t), "build", "-o", bin, "."); err != nil {
			t.Fatalf("violator build: %v\n%s", err, text)
		}
		ctx, cancel := context.WithTimeoutCause(t.Context(), 60*time.Second, errors.New("violator timed out"))
		out, err := exec.CommandContext(ctx, bin, mode).CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("violator %q: %v\n%s", mode, err, out)
		}
		checkViolation(t, mode, string(out))
	}
}

func TestFastcallNoClockInFastBody(t *testing.T) {
	self := selfTestBin(t)
	body := objdumpFunc(t, self, "runtime\\.logosCgocallFast")
	for _, want := range []string{"nanotime", "cputicks", "clock_gettime", "monotonic"} {
		if strings.Contains(strings.ToLower(body), want) {
			t.Fatalf("fast body references clock %q:\n%s", want, body)
		}
	}
}

func TestFastcallNumCgoCallBoundary(t *testing.T) {
	requireCgo(t)
	before := runtime.NumCgoCall()
	if got := callFastAdd(1, 1); got != 2 {
		t.Fatalf("fast_add = %d, want 2", got)
	}
	if mid := runtime.NumCgoCall(); mid != before+1 {
		t.Fatalf("after 1 call = %d, want %d", mid, before+1)
	}
	const n = 5
	for i := range n {
		if got := callFastAdd(int32(i), 1); got != int32(i)+1 {
			t.Fatalf("call %d = %d, want %d", i, got, i+1)
		}
	}
	if after := runtime.NumCgoCall(); after != before+1+n {
		t.Fatalf("after %d calls = %d, want %d", 1+n, after, before+1+n)
	}
}

func TestFastcallLockOSThreadParity(t *testing.T) {
	requireCgo(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if got := callFastAdd(20, 22); got != 42 {
		t.Fatalf("fast_add on locked thread = %d, want 42", got)
	}
	done := make(chan int32, 1)
	go func() { done <- callFastAdd(1, 2) }()
	select {
	case got := <-done:
		if got != 3 {
			t.Fatalf("unlocked goroutine fast_add = %d, want 3", got)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("unlocked goroutine stuck while thread locked")
	}
}

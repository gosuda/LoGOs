// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"internal/testenv"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestConstmapGeneratedLookupShape(t *testing.T) {
	keys := constmapKeys(8)
	keyTexts := make([]string, len(keys))
	valTexts := make([]string, len(keys))
	for i := range keys {
		keyTexts[i] = `"` + keys[i].canon + `"`
		valTexts[i] = `"` + keys[i].canon + `"`
	}
	order := []int{0, 1, 2, 3, 4, 5, 6, 7}
	sw := emitConstMapDecls(constMapEmission{
		name: "testsw", kT: "string", vT: "string",
		mapT:     "map[string]string",
		keyTexts: keyTexts,
		valTexts: valTexts,
		order:    order,
		isSwitch: true,
		bearing:  false,
	})
	idx := strings.Index(sw, "func testsw_lookup")
	if idx < 0 {
		t.Fatalf("switch decls lack lookup:\n%s", sw)
	}
	lookup := sw[idx:]
	if !strings.Contains(lookup, "switch _logos_") {
		t.Fatalf("switch lookup lacks prefixed switch:\n%s", lookup)
	}
	if got := strings.Count(lookup, "case "); got != 8 {
		t.Fatalf("switch lookup has %d case lines, want 8", got)
	}
	runConstmapLookupBehavior(t, lookup, keys)
	if strings.Contains(lookup, "for ") || strings.Contains(lookup, ".Get(") {
		t.Fatalf("switch lookup must not loop or delegate:\n%s", lookup)
	}
	keys9 := constmapKeys(9)
	layout, err := buildConstMap(keys9, defaultConstMapBudget)
	if err != nil {
		t.Fatalf("buildConstMap 9 keys: %v", err)
	}
	keyTexts9 := make([]string, len(keys9))
	valTexts9 := make([]string, len(keys9))
	for i := range keys9 {
		keyTexts9[i] = `"` + keys9[i].canon + `"`
		valTexts9[i] = `"` + keys9[i].canon + `"`
	}
	order9 := []int{0, 1, 2, 3, 4, 5, 6, 7, 8}
	tab := emitConstMapDecls(constMapEmission{
		name: "testtab", kT: "string", vT: "string",
		mapT:     "map[string]string",
		keyTexts: keyTexts9,
		valTexts: valTexts9,
		hashExpr: "rt.HashString(_logos_k)",
		order:    order9,
		layout:   layout,
		isSwitch: false,
		bearing:  false,
	})
	tidx := strings.Index(tab, "func testtab_lookup")
	if tidx < 0 {
		t.Fatalf("table decls lack lookup:\n%s", tab)
	}
	tlookup := tab[tidx:]
	if got := strings.Count(tlookup, ".Get("); got != 1 {
		t.Fatalf("table lookup has %d Get calls, want exactly 1:\n%s", got, tlookup)
	}
	if strings.Contains(tlookup, "for ") {
		t.Fatalf("table lookup must not loop:\n%s", tlookup)
	}
	for _, banned := range []string{"sort.", "BinarySearch", "binarySearch"} {
		if strings.Contains(tlookup, banned) || strings.Contains(lookup, banned) {
			t.Fatalf("generated lookup contains %q: no logarithmic fallback", banned)
		}
	}
}
func TestConstmapEmissionRodataNoInit(t *testing.T) {
	keys := constmapKeys(9)
	layout, err := buildConstMap(keys, defaultConstMapBudget)
	if err != nil {
		t.Fatalf("buildConstMap 9 keys: %v", err)
	}
	keyTexts := make([]string, len(keys))
	valTexts := make([]string, len(keys))
	for i := range keys {
		keyTexts[i] = `"` + keys[i].canon + `"`
		valTexts[i] = `"` + keys[i].canon + `"`
	}
	tab := emitConstMapDecls(constMapEmission{
		name: "testro", kT: "string", vT: "string",
		mapT:     "map[string]string",
		keyTexts: keyTexts,
		valTexts: valTexts,
		hashExpr: "rt.HashString(k)",
		order:    []int{0, 1, 2, 3, 4, 5, 6, 7, 8},
		layout:   layout,
		isSwitch: false,
		bearing:  false,
	})
	if got := strings.Count(tab, "//logos:rodata"); got != 2 {
		t.Fatalf("table emission holds %d rodata markers, want 2 (sorted and table):\n%s", got, tab)
	}
	if strings.Contains(tab, "func init(") {
		t.Fatalf("table emission declares init:\n%s", tab)
	}
	sw := emitConstMapDecls(constMapEmission{
		name: "testrosw", kT: "string", vT: "string",
		mapT:     "map[string]string",
		keyTexts: keyTexts[:8],
		valTexts: valTexts[:8],
		order:    []int{0, 1, 2, 3, 4, 5, 6, 7},
		isSwitch: true,
		bearing:  false,
	})
	if got := strings.Count(sw, "//logos:rodata"); got != 1 {
		t.Fatalf("switch emission holds %d rodata markers, want 1 (sorted):\n%s", got, sw)
	}
	if strings.Contains(sw, "func init(") {
		t.Fatalf("switch emission declares init:\n%s", sw)
	}
}
func runConstmapLookupBehavior(t *testing.T, lookup string, keys []constMapKey) {
	t.Helper()
	testenv.MustHaveGoBuild(t)
	dir := t.TempDir()
	var b strings.Builder
	b.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\n")
	b.WriteString(lookup)
	b.WriteString("\nfunc main() {\n")
	for i := range keys {
		fmt.Fprintf(&b, "\tif v, ok := testsw_lookup(%q); !ok || v != %q {\n\t\tfmt.Printf(\"FAIL present %%q = %%q,%%v\\n\", %q, v, ok)\n\t\tos.Exit(1)\n\t}\n", keys[i].canon, keys[i].canon, keys[i].canon)
	}
	b.WriteString("\tif v, ok := testsw_lookup(\"__missing__\"); ok || v != \"\" {\n\t\tfmt.Printf(\"FAIL absent = %q,%v\\n\", v, ok)\n\t\tos.Exit(1)\n\t}\n\tfmt.Println(\"ok constmap-lookup\")\n}\n")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module constmaplookup\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(testenv.GoToolPath(t), "run", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "ok constmap-lookup" {
		t.Fatalf("lookup behavior = %q, err %v, want ok constmap-lookup", out, err)
	}
}
func constmapLogosTool(t *testing.T) string {
	t.Helper()
	testenv.MustHaveGoBuild(t)
	logosTool := filepath.Join(filepath.Dir(testenv.GoToolPath(t)), "logos")
	if runtime.GOOS == "windows" {
		logosTool += ".exe"
	}
	if _, err := os.Stat(logosTool); err != nil {
		t.Skipf("no logos command beside the go tool: %v", err)
	}
	return logosTool
}

func constmapWriteModule(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o777); err != nil {
		t.Fatal(err)
	}
	for name, text := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o666); err != nil {
			t.Fatal(err)
		}
	}
}

func constmapBuildRun(t *testing.T, dir, exe string, runEnv []string) string {
	t.Helper()
	build := testenv.Command(t, constmapLogosTool(t), "build", "-o", exe, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOFLAGS=", "GOPROXY=off", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("logos build: %v\n%s", err, out)
	}
	run := testenv.Command(t, filepath.Join(dir, exe))
	run.Dir = dir
	run.Env = append(append(os.Environ(), "GOFLAGS=", "GOPROXY=off", "GOTOOLCHAIN=local"), runEnv...)
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("run %s: %v\n%s", exe, err, out)
	}
	return string(out)
}

func TestConstmapLookupCostFlat(t *testing.T) {
	sizes := []int{16, 1024, 16384}
	const misses = 1024
	lookups := make(map[int]string, len(sizes))
	type probeCount struct{ slots, cmps int64 }
	counts := make(map[int]probeCount, len(sizes))
	for _, n := range sizes {
		keys := constmapKeys(n)
		layout, err := buildConstMap(keys, defaultConstMapBudget)
		if err != nil {
			t.Fatalf("buildConstMap %d keys: %v", n, err)
		}
		keyTexts := make([]string, n)
		valTexts := make([]string, n)
		order := make([]int, n)
		for i := range keys {
			keyTexts[i] = `"` + keys[i].canon + `"`
			valTexts[i] = fmt.Sprintf("%d", i)
			order[i] = i
		}
		decls := emitConstMapDecls(constMapEmission{
			name: "testflat", kT: "string", vT: "int",
			mapT:     "map[string]int",
			keyTexts: keyTexts,
			valTexts: valTexts,
			hashExpr: "_logos_rt.HashString(_logos_k)",
			order:    order,
			layout:   layout,
			isSwitch: false,
			bearing:  false,
		})
		idx := strings.Index(decls, "func testflat_lookup")
		if idx < 0 {
			t.Fatalf("size %d: table decls lack lookup", n)
		}
		lookup := decls[idx:]
		if got := strings.Count(lookup, ".Get("); got != 1 {
			t.Fatalf("size %d: table lookup has %d Get calls, want exactly 1 (one-probe contract)", n, got)
		}
		if strings.Contains(lookup, "for ") {
			t.Fatalf("size %d: table lookup must not loop (one-probe contract)", n)
		}
		for _, banned := range []string{"sort.", "BinarySearch", "binarySearch"} {
			if strings.Contains(lookup, banned) {
				t.Fatalf("size %d: generated lookup contains %q: no logarithmic fallback", n, banned)
			}
		}
		lookups[n] = lookup
		var slots, cmps int64
		probe := func(h uint64, canon string) bool {
			slots++
			ki := layout.slots[layout.probe(h)]
			if ki < 0 {
				return false
			}
			cmps++
			return keys[ki].canon == canon
		}
		for i, k := range keys {
			if !probe(k.hash, k.canon) {
				t.Fatalf("size %d: one-probe lookup missed key %d", n, i)
			}
		}
		for i := range misses {
			m := fmt.Sprintf("z%05d", i)
			if probe(constHashString(m), m) {
				t.Fatalf("size %d: one-probe lookup hit absent key %q", n, m)
			}
		}
		counts[n] = probeCount{slots: slots, cmps: cmps}
		checkConstmapRuntimeGetString(t, n)
		hashes := make([]uint64, n)
		strs := make([]string, n)
		for i := range strs {
			strs[i] = keys[i].canon
			hashes[i] = keys[i].hash
		}
		tab := constmapRuntimeTable(t, hashes, strs)
		if got := testing.AllocsPerRun(100, func() { _, _ = tab.Get(hashes[7], strs[7]) }); got != 0 {
			t.Fatalf("size %d: hit lookup averages %v allocations, want 0", n, got)
		}
		mh, mk := constHashString("z00007"), "z00007"
		if got := testing.AllocsPerRun(100, func() { _, _ = tab.Get(mh, mk) }); got != 0 {
			t.Fatalf("size %d: miss lookup averages %v allocations, want 0", n, got)
		}
	}
	for _, n := range sizes {
		if want := int64(n + misses); counts[n].slots != want {
			t.Fatalf("size %d: %d lookups read %d slots, want exactly one probe per lookup", n, n+misses, counts[n].slots)
		}
		if got := counts[n].cmps; got < int64(n) || got > int64(n+misses) {
			t.Fatalf("size %d: %d lookups made %d key comparisons, want one per hit and at most one per lookup", n, n+misses, got)
		}
	}
	for _, n := range sizes[1:] {
		if lookups[n] != lookups[sizes[0]] {
			t.Fatalf("generated lookup differs between %d and %d keys: lookup cost is not table-size independent", sizes[0], n)
		}
	}
}

func constmapHeapCtlSrc() string {
	return "package main\n\nimport (\n\t\"fmt\"\n\t\"runtime\"\n)\n\nfunc main() {\n\tvar ms runtime.MemStats\n\truntime.ReadMemStats(&ms)\n\tfmt.Println(ms.HeapAlloc)\n}\n"
}

func constmapHeapBigSrc(n int) string {
	var b strings.Builder
	b.WriteString("package main\n\nimport (\n\t\"fmt\"\n\t\"runtime\"\n)\n\nconst big = map[string]int{")
	for i := range n {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q: %d", fmt.Sprintf("k%05d", i), i)
	}
	b.WriteString("}\n\nfunc main() {\n\tvar ms runtime.MemStats\n\truntime.ReadMemStats(&ms)\n\tfmt.Println(ms.HeapAlloc)\n\t_ = big[\"k00031\"]\n}\n")
	return b.String()
}

func TestConstmapInitNoHeap(t *testing.T) {
	if testing.Short() {
		t.Skip("builds loGo/s programs")
	}
	root := t.TempDir()
	ctlDir := filepath.Join(root, "ctl")
	bigDir := filepath.Join(root, "big")
	constmapWriteModule(t, ctlDir, map[string]string{
		"go.mod":   "module ctl\n\ngo 1.27\n",
		"main.lgo": constmapHeapCtlSrc(),
	})
	constmapWriteModule(t, bigDir, map[string]string{
		"go.mod":   "module bigmap\n\ngo 1.27\n",
		"main.lgo": constmapHeapBigSrc(20000),
	})
	minHeap := func(dir string) uint64 {
		lo := uint64(1 << 63)
		for range 5 {
			out := constmapBuildRun(t, dir, "heap.exe", []string{"GOGC=off"})
			v, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
			if err != nil {
				t.Fatalf("heap program printed %q", out)
			}
			lo = min(lo, v)
		}
		return lo
	}
	ctl, big := minHeap(ctlDir), minHeap(bigDir)
	if big > ctl+512*1024 {
		t.Fatalf("package init of a 20000-key const map added %d heap bytes (big %d, control %d); the table must sit in read-only data and init must add no heap bytes", big-ctl, big, ctl)
	}
}

func TestConstmapThousandKeyInitNoHeap(t *testing.T) {
	if testing.Short() {
		t.Skip("builds loGo/s programs")
	}
	root := t.TempDir()
	dirs := []string{filepath.Join(root, "ctl"), filepath.Join(root, "map")}
	source := strings.Replace(constmapHeapBigSrc(1000), `_ = big["k00031"]`, `if (big[fmt.Sprintf("k%05d", 31)] ?? -1) != 31 { panic("lookup") }`, 1)
	supportOnly := strings.Replace(constmapHeapCtlSrc(), "\t\"runtime\"\n", "\t_ \"logos/rt\"\n\t\"runtime\"\n", 1)
	sources := []string{supportOnly, source}
	heaps := make([]uint64, len(dirs))
	for i, dir := range dirs {
		constmapWriteModule(t, dir, map[string]string{
			"go.mod":   "module heapcheck\n\ngo 1.27\n",
			"main.lgo": sources[i],
		})
		heaps[i] = ^uint64(0)
		for range 5 {
			out := constmapBuildRun(t, dir, "heap.exe", []string{"GOGC=off", "GOMAXPROCS=1"})
			n, err := strconv.ParseUint(strings.TrimSpace(out), 10, 64)
			if err != nil {
				t.Fatalf("heap program printed %q: %v", out, err)
			}
			heaps[i] = min(heaps[i], n)
		}
	}
	if heaps[1] != heaps[0] {
		t.Fatalf("1000-key map init heap = %d, control that imports only the support package = %d; want exactly zero added bytes", heaps[1], heaps[0])
	}
	out, err := testenv.Command(t, testenv.GoToolPath(t), "tool", "nm", filepath.Join(dirs[1], "heap.exe")).CombinedOutput()
	if err != nil {
		t.Fatalf("go tool nm: %v\n%s", err, out)
	}
	found := false
	for line := range strings.SplitSeq(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && strings.HasPrefix(fields[2], "main.") && strings.HasSuffix(fields[2], "_tab") {
			found = true
			if fields[1] != "R" && fields[1] != "r" {
				t.Errorf("constant map table is not read-only: %s", line)
			}
		}
	}
	if !found {
		t.Fatalf("no constant map table symbol in go tool nm output:\n%s", out)
	}
}

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

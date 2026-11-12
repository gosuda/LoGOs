// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func chainGrowthSrc(n int) string {
	var sb strings.Builder
	sb.WriteString("package main\n\nfunc bump(x int) int { return 2 * x }\n\nfunc main() {\nxs := []int{3, 1, 4, 1, 5, 9, 2, 6}\n")
	for range n {
		sb.WriteString("_ = xs.filter(x => x > 2).map(x => bump(x)).take(2)\n")
	}
	sb.WriteString("}\n")
	return sb.String()
}

func interpGrowthSrc(n int) string {
	var sb strings.Builder
	sb.WriteString("package main\n\nfunc main() {\nname := \"go\"\nx := 7\n")
	for range n {
		sb.WriteString("_ = $\"hello {name} n={x}\"\n")
	}
	sb.WriteString("}\n")
	return sb.String()
}

func lambdaGrowthSrc(n int) string {
	var sb strings.Builder
	sb.WriteString("package main\n\nfunc main() {\nn := 3\n")
	for i := range n {
		fmt.Fprintf(&sb, "var f%d func(int) int = x => { var g func(int) int = y => x + y + n; return g(1) }\n_ = f%d(2)\n", i, i)
	}
	sb.WriteString("}\n")
	return sb.String()
}

func lowerBytes(t *testing.T, name, src string, lowered func(out string) bool) float64 {
	t.Helper()
	if !lowered(lowerSnippetSrc(t, name, src)) {
		t.Fatalf("%s did not lower", name)
	}
	best := float64(1 << 62)
	for range 5 {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		lowerSnippetSrc(t, name, src)
		runtime.ReadMemStats(&after)
		if d := float64(after.TotalAlloc - before.TotalAlloc); d < best {
			best = d
		}
	}
	return best
}

func assertGrowthBound(t *testing.T, name string, gen func(n int) string, lowered func(out string) bool) {
	t.Helper()
	small := lowerBytes(t, name, gen(80), lowered)
	large := lowerBytes(t, name, gen(320), lowered)
	t.Logf("lower bytes per run: small=%v large=%v", small, large)
	if small == 0 {
		t.Fatal("no allocations measured at small size")
	}
	if ratio := large / small; ratio > 6 {
		t.Fatalf("%s lowering allocated %.1fx bytes for 4x input, want at most 6x", name, ratio)
	}
}

func TestChainLowerGrowthBound(t *testing.T) {
	nested := lowerSnippetSrc(t, "lambdanest.lgo", "package main\n\nfunc main() {\nvar f func(int) int = x => { var g func(int) int = y => x + y; return g(1) }\n_ = f(2)\n}\n")
	if strings.Contains(nested, "=>") {
		t.Fatal("nested lambda survived lowering")
	}
	assertGrowthBound(t, "growth.lgo", chainGrowthSrc, func(out string) bool { return strings.Contains(out, "for _, ") })
}

func TestInterpLowerGrowthBound(t *testing.T) {
	assertGrowthBound(t, "interpgrowth.lgo", interpGrowthSrc, func(out string) bool { return !strings.Contains(out, "$\"") })
}

func TestLambdaLowerGrowthBound(t *testing.T) {
	assertGrowthBound(t, "lambdagrowth.lgo", lambdaGrowthSrc, func(out string) bool { return !strings.Contains(out, "=>") })
}

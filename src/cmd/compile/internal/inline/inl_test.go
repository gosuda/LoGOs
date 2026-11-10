// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package inline

import (
	"testing"

	"cmd/compile/internal/base"
	"cmd/compile/internal/ir"
)

// TestLogosConservativeBudget verifies the policy governing conservative
// call-cost retry under different -O tiers.
func TestLogosConservativeBudget(t *testing.T) {
	oldInlBudget := base.Debug.InlBudget
	oldCompilingRuntime := base.Flag.CompilingRuntime
	t.Cleanup(func() {
		base.Debug.InlBudget = oldInlBudget
		base.Flag.CompilingRuntime = oldCompilingRuntime
	})

	base.Flag.CompilingRuntime = false
	fn := &ir.Func{}

	// O1 (base.Debug.InlBudget == 0): upstream behavior preserved, no retry.
	base.Debug.InlBudget = 0
	if got := logosConservativeBudget(fn); got != 0 {
		t.Errorf("O1: logosConservativeBudget(fn) = %d, want 0", got)
	}
	if got := logosConservativeBudget(nil); got != 0 {
		t.Errorf("O1: logosConservativeBudget(nil) = %d, want 0", got)
	}

	// O2 (base.Debug.InlBudget == 120): retry budget is upstream O1 budget (80).
	base.Debug.InlBudget = 120
	if got := logosConservativeBudget(fn); got != int32(inlineMaxBudget) {
		t.Errorf("O2: logosConservativeBudget(fn) = %d, want %d", got, inlineMaxBudget)
	}
	if got := logosConservativeBudget(nil); got != 0 {
		t.Errorf("O2: logosConservativeBudget(nil) = %d, want 0", got)
	}

	// O3 (base.Debug.InlBudget == 400): retry budget is upstream O1 budget (80).
	base.Debug.InlBudget = 400
	if got := logosConservativeBudget(fn); got != int32(inlineMaxBudget) {
		t.Errorf("O3: logosConservativeBudget(fn) = %d, want %d", got, inlineMaxBudget)
	}

	// CompilingRuntime: upstream behavior preserved, no retry.
	base.Flag.CompilingRuntime = true
	if got := logosConservativeBudget(fn); got != 0 {
		t.Errorf("CompilingRuntime: logosConservativeBudget(fn) = %d, want 0", got)
	}
}

// TestLogosConservativeCallCost verifies that logosConservativeCallCost
// caps expanded callee costs at extraCost when conservativeCalls is active,
// preventing wrapper inlining inversion while retaining cheaper callee costs.
func TestLogosConservativeCallCost(t *testing.T) {
	const extraCost = int32(57)

	for _, tc := range []struct {
		name       string
		calleeCost int32
		want       int32
	}{
		{"callee exceeds extraCost", 105, extraCost},
		{"callee equal to extraCost", extraCost, extraCost},
		{"callee below extraCost", 20, 20},
		{"callee zero cost", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := logosConservativeCallCost(tc.calleeCost, extraCost); got != tc.want {
				t.Errorf("logosConservativeCallCost(%d, %d) = %d, want %d", tc.calleeCost, extraCost, got, tc.want)
			}
		})
	}
}
func TestLogosBudgetForTiers(t *testing.T) {
	oldBudget := base.Debug.InlBudget
	oldRecursive := base.Debug.InlRecursiveBudget
	oldRuntime := base.Flag.CompilingRuntime
	oldMarks := logosRecursiveMarks
	logosRecursiveMarks = make(map[*ir.Func]bool)
	t.Cleanup(func() {
		base.Debug.InlBudget = oldBudget
		base.Debug.InlRecursiveBudget = oldRecursive
		base.Flag.CompilingRuntime = oldRuntime
		logosRecursiveMarks = oldMarks
	})
	base.Flag.CompilingRuntime = false
	plain := new(ir.Func)
	rec := new(ir.Func)
	logosNoteSCC([]*ir.Func{rec})
	const def = int32(inlineMaxBudget)
	base.Debug.InlBudget = 0
	base.Debug.InlRecursiveBudget = 0
	if got := logosBudgetFor(plain, def); got != def {
		t.Errorf("O1 plain budget = %d, want %d", got, def)
	}
	if got := logosBudgetFor(rec, def); got != def {
		t.Errorf("O1 recursive budget = %d, want %d", got, def)
	}
	base.Debug.InlBudget = 120
	base.Debug.InlRecursiveBudget = 0
	if got := logosBudgetFor(plain, def); got != 120 {
		t.Errorf("O2 plain budget = %d, want 120", got)
	}
	if got := logosBudgetFor(rec, def); got != def {
		t.Errorf("O2 recursive budget = %d, want %d", got, def)
	}
	base.Debug.InlBudget = 400
	base.Debug.InlRecursiveBudget = 80
	if got := logosBudgetFor(plain, def); got != 400 {
		t.Errorf("O3 plain budget = %d, want 400", got)
	}
	if got := logosBudgetFor(rec, def); got != 80 {
		t.Errorf("O3 recursive budget = %d, want 80", got)
	}
	base.Debug.InlBudget = 15
	base.Debug.InlRecursiveBudget = 0
	if got := logosBudgetFor(plain, def); got != 15 {
		t.Errorf("Os plain budget = %d, want 15", got)
	}
	if got := logosBudgetFor(rec, def); got != def {
		t.Errorf("Os recursive budget = %d, want %d", got, def)
	}
	base.Debug.InlBudget = 400
	base.Debug.InlRecursiveBudget = 80
	base.Flag.CompilingRuntime = true
	if got := logosBudgetFor(plain, def); got != def {
		t.Errorf("runtime plain budget = %d, want %d", got, def)
	}
	if got := logosBudgetFor(rec, def); got != def {
		t.Errorf("runtime recursive budget = %d, want %d", got, def)
	}
}

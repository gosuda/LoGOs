// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package noder

import (
	"encoding/json"
	"fmt"
	"internal/testenv"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type diagSpan struct {
	ByteOffset int `json:"byte_offset"`
	ByteLen    int `json:"byte_len"`
}

type diagFix struct {
	Replacement      string `json:"replacement"`
	InsertBeforeLine int    `json:"insert_before_line"`
}

type diagRecord struct {
	RuleID            string    `json:"rule_id"`
	Severity          string    `json:"severity"`
	Span              diagSpan  `json:"span"`
	AutofixCandidates []diagFix `json:"autofix_candidates"`
}

const (
	misplacedTryRuleID = "correctness::misplaced_try"
	severityForbid     = "forbid"
)

func logosToolPath(t *testing.T) string {
	t.Helper()
	testenv.MustHaveGoBuild(t)
	goTool := testenv.GoToolPath(t)
	name := "logos"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(filepath.Dir(goTool), name)
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("logos binary not found at %s: %v", p, err)
	}
	return p
}

func compileLgoJSON(t *testing.T, src string) ([]diagRecord, int) {
	t.Helper()
	testenv.MustHaveGoBuild(t)

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "x.lgo")
	policyPath := filepath.Join(dir, "policy.json")
	objPath := filepath.Join(dir, "out.a")

	if err := os.WriteFile(srcPath, []byte(src), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(policyPath, []byte(`{"version":1}`), 0o666); err != nil {
		t.Fatal(err)
	}

	args := []string{
		"tool", "compile",
		"-p", "main",
		"-logos.cfg=" + policyPath,
		"-logos.json",
		"-o", objPath,
		srcPath,
	}
	cmd := testenv.Command(t, testenv.GoToolPath(t), args...)
	out, err := cmd.CombinedOutput()

	exitCode := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exitCode = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("compile failed: %v\noutput: %s", err, out)
	}

	var diags []diagRecord
	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec diagRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("failed to decode JSON diagnostic: %v\nline: %s", err, line)
		}
		diags = append(diags, rec)
	}
	return diags, exitCode
}

func applyFix(t *testing.T, src string, span diagSpan, fix diagFix) string {
	t.Helper()
	if fix.InsertBeforeLine > 0 {
		lines := strings.Split(src, "\n")
		idx := fix.InsertBeforeLine - 1
		if idx < 0 || idx > len(lines) {
			t.Fatalf("invalid InsertBeforeLine %d for %d lines", fix.InsertBeforeLine, len(lines))
		}
		var b strings.Builder
		for i, l := range lines {
			if i == idx {
				b.WriteString(fix.Replacement)
			}
			b.WriteString(l)
			b.WriteByte('\n')
		}
		return strings.TrimSuffix(b.String(), "\n")
	}

	start := span.ByteOffset
	end := span.ByteOffset + span.ByteLen
	if start < 0 || end > len(src) || start > end {
		t.Fatalf("invalid byte span [%d:%d] for src length %d", start, end, len(src))
	}
	return src[:start] + fix.Replacement + src[end:]
}

func runLgoSourceOut(t *testing.T, src string) string {
	t.Helper()
	testenv.MustHaveGoRun(t)

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.lgo")
	modPath := filepath.Join(dir, "go.mod")

	if err := os.WriteFile(srcPath, []byte(src), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modPath, []byte("module testprog\n\ngo 1.27\n"), 0o666); err != nil {
		t.Fatal(err)
	}

	cmd := testenv.Command(t, logosToolPath(t), "run", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("execution failed: %v\noutput:\n%s\nsource:\n%s", err, out, src)
	}
	return string(out)
}

func misplacedTryDiag(t *testing.T, diags []diagRecord) *diagRecord {
	t.Helper()
	var found *diagRecord
	for i := range diags {
		if diags[i].RuleID == misplacedTryRuleID {
			if found != nil {
				t.Fatalf("expected one %s diagnostic, got several", misplacedTryRuleID)
			}
			found = &diags[i]
		}
	}
	if found == nil {
		t.Fatalf("expected diagnostic with rule_id %q, got: %+v", misplacedTryRuleID, diags)
	}
	return found
}

func runLgoSource(t *testing.T, src string) {
	t.Helper()
	testenv.MustHaveGoRun(t)

	dir := t.TempDir()
	srcPath := filepath.Join(dir, "main.lgo")
	modPath := filepath.Join(dir, "go.mod")

	if err := os.WriteFile(srcPath, []byte(src), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modPath, []byte("module testprog\n\ngo 1.27\n"), 0o666); err != nil {
		t.Fatal(err)
	}

	cmd := testenv.Command(t, logosToolPath(t), "run", ".")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("execution failed: %v\noutput:\n%s\nsource:\n%s", err, out, src)
	}
}

func TestMisplacedTryAutofix(t *testing.T) {
	const safeSrc = `package main

type myError struct{}

func (myError) Error() string { return "step error" }

var (
	stepCalls int
	stepFail  bool
)

func step() (int, error) {
	stepCalls++
	if stepFail {
		return 0, myError{}
	}
	return 42, nil
}

func compute() (int, error) {
	v := step()? + 1
	return v, nil
}

func main() {
	// Verify success path: result value 43 and exactly-once evaluation
	stepCalls = 0
	stepFail = false
	val, err := compute()
	if err != nil {
		panic("unexpected error on success path")
	}
	if stepCalls != 1 {
		panic("expected callcount 1")
	}
	if val != 43 {
		panic("expected value 43")
	}

	// Verify error path: error propagation and exactly-once evaluation
	stepCalls = 0
	stepFail = true
	_, err = compute()
	if err == nil {
		panic("expected error on error path, got nil")
	}
	if err.Error() != "step error" {
		panic("unexpected error message on error path")
	}
	if stepCalls != 1 {
		panic("expected callcount 1 on error path")
	}
}
`

	diags, exitCode := compileLgoJSON(t, safeSrc)
	if exitCode == 0 {
		t.Fatalf("expected compile failure on misplaced '?', got exit code 0")
	}

	var targetDiag *diagRecord
	for i := range diags {
		if diags[i].RuleID == misplacedTryRuleID {
			targetDiag = &diags[i]
			break
		}
	}

	if targetDiag == nil {
		t.Fatalf("expected diagnostic with rule_id %q, got: %+v", misplacedTryRuleID, diags)
	}
	if targetDiag.Severity != severityForbid {
		t.Errorf("expected severity %q, got %q", severityForbid, targetDiag.Severity)
	}

	if len(targetDiag.AutofixCandidates) == 0 {
		t.Fatalf("missing autofix candidate for %s: got 0 candidates, want split statement autofix", misplacedTryRuleID)
	}

	for i, fix := range targetDiag.AutofixCandidates {
		fixedSrc := applyFix(t, safeSrc, targetDiag.Span, fix)
		runLgoSource(t, fixedSrc)
		t.Logf("candidate %d compiled and executed with verified runtime assertions", i)
	}
}

func TestMisplacedTryAutofixOrdering(t *testing.T) {
	const unsafeSrc = `package main

type myError struct{}

func (myError) Error() string { return "step error" }

var (
	trace     []string
	nextCalls int
	stepCalls int
	stepFail  bool
)

func next() int {
	nextCalls++
	trace = append(trace, "next")
	return 1
}

func step() (int, error) {
	stepCalls++
	trace = append(trace, "step")
	if stepFail {
		return 0, myError{}
	}
	return 42, nil
}

func computeUnsafe() (int, error) {
	v := next() + step()?
	return v, nil
}

func main() {
	// Verify success path: left-to-right evaluation order (next before step)
	trace = nil
	nextCalls = 0
	stepCalls = 0
	stepFail = false
	val, err := computeUnsafe()
	if err != nil {
		panic("unexpected error on success path")
	}
	if val != 43 {
		panic("expected value 43")
	}
	if nextCalls != 1 || stepCalls != 1 {
		panic("expected nextCalls=1 stepCalls=1")
	}
	if len(trace) != 2 || trace[0] != "next" || trace[1] != "step" {
		panic("evaluation order violated")
	}

	// Verify error path: next() must execute before step() fails and returns
	trace = nil
	nextCalls = 0
	stepCalls = 0
	stepFail = true
	_, err = computeUnsafe()
	if err == nil {
		panic("expected error on error path, got nil")
	}
	if err.Error() != "step error" {
		panic("unexpected error message on error path")
	}
	if nextCalls != 1 || stepCalls != 1 {
		panic("expected nextCalls=1 stepCalls=1 on error path")
	}
	if len(trace) != 2 || trace[0] != "next" || trace[1] != "step" {
		panic("evaluation order violated on error path")
	}
}
`

	diags, exitCode := compileLgoJSON(t, unsafeSrc)
	if exitCode == 0 {
		t.Fatalf("expected compile failure on unsafe misplaced '?', got exit code 0")
	}

	var targetDiag *diagRecord
	for i := range diags {
		if diags[i].RuleID == misplacedTryRuleID {
			targetDiag = &diags[i]
			break
		}
	}
	if targetDiag == nil {
		t.Fatalf("expected diagnostic with rule_id %q, got: %+v", misplacedTryRuleID, diags)
	}
	if targetDiag.Severity != severityForbid {
		t.Errorf("expected severity %q, got %q", severityForbid, targetDiag.Severity)
	}

	if len(targetDiag.AutofixCandidates) == 0 {
		t.Logf("conservative refusal: no autofix candidate offered for order-sensitive expression")
		return
	}

	for i, fix := range targetDiag.AutofixCandidates {
		fixedSrc := applyFix(t, unsafeSrc, targetDiag.Span, fix)
		runLgoSource(t, fixedSrc)
		t.Logf("candidate %d preserved evaluation order and error behavior", i)
	}
}

func TestMisplacedTrySplitOffered(t *testing.T) {
	const stepHelpers = `type myErr string

func (e myErr) Error() string { return string(e) }

var (
	calls int
	fail  bool
)

func step() (int, error) {
	calls++
	if fail {
		return 0, myErr("boom")
	}
	return 42, nil
}
`

	const mainSrc = `func main() {
	calls, fail = 0, false
	v, err := compute()
	if err != nil || v != %d {
		println("bad")
	} else {
		println("ok")
	}
	calls, fail = 0, true
	v, err = compute()
	if err == nil || err.Error() != "boom" || v != 0 {
		println("bad")
	} else {
		println("err")
	}
	if calls != 1 {
		println("bad")
	} else {
		println("once")
	}
}
`

	tests := []struct {
		name    string
		decls   string
		compute string
		wantVal int
	}{
		{
			name: "call_argument",
			decls: `var got int

func g(x int) error {
	got = x
	return nil
}
`,
			compute: `func compute() (int, error) {
	err := g(step()?)
	return got, err
}
`,
			wantVal: 42,
		},
		{
			name: "binary_operand",
			compute: `func compute() (int, error) {
	a := 2 + step()?
	return a, nil
}
`,
			wantVal: 44,
		},
		{
			name: "chain_argument",
			decls: `type S struct{}

var gotM int

func (S) M(x int) S {
	gotM = x
	return S{}
}

func (S) N() int {
	return gotM
}
`,
			compute: `func compute() (int, error) {
	v := S{}.M(step()?).N()
	return v, nil
}
`,
			wantVal: 42,
		},
		{
			name: "if_condition",
			compute: `func compute() (int, error) {
	if step()? == 42 {
		return 43, nil
	}
	return 0, nil
}
`,
			wantVal: 43,
		},
		{
			name: "return_value",
			compute: `func compute() (int, error) {
	return step()?, nil
}
`,
			wantVal: 42,
		},
		{
			name: "composite_literal",
			compute: `func compute() (int, error) {
	xs := []int{step()?, 1}
	return xs[0] + xs[1], nil
}
`,
			wantVal: 43,
		},
		{
			name: "multivalue_argument",
			decls: `var (
	gotN int
	gotB bool
)

func step2() (int, bool, error) {
	calls++
	if fail {
		return 0, false, myErr("boom")
	}
	return 42, true, nil
}

func g2(n int, b bool) error {
	gotN, gotB = n, b
	return nil
}
`,
			compute: `func compute() (int, error) {
	err := g2(step2()?)
	return gotN, err
}
`,
			wantVal: 42,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := "package main\n\n" + tc.decls + "\n" + stepHelpers + tc.compute + fmt.Sprintf(mainSrc, tc.wantVal)
			diags, exitCode := compileLgoJSON(t, src)
			if exitCode == 0 {
				t.Fatalf("expected compile failure on misplaced '?', got exit code 0")
			}
			d := misplacedTryDiag(t, diags)
			if d.Severity != severityForbid {
				t.Errorf("expected severity %q, got %q", severityForbid, d.Severity)
			}
			if len(d.AutofixCandidates) != 1 {
				t.Fatalf("expected exactly one autofix candidate for %s position, got %d", tc.name, len(d.AutofixCandidates))
			}
			fix := d.AutofixCandidates[0]
			if strings.Contains(fix.Replacement, "_logos_") || strings.Contains(fix.Replacement, "Logos_") {
				t.Fatalf("candidate replacement uses a reserved prefix: %q", fix.Replacement)
			}
			fixedSrc := applyFix(t, src, d.Span, fix)
			got := runLgoSourceOut(t, fixedSrc)
			const wantOut = "ok\nerr\nonce\n"
			if got != wantOut {
				t.Fatalf("applied candidate output = %q, want %q\nfixed source:\n%s", got, wantOut, fixedSrc)
			}
		})
	}
}

func TestMisplacedTrySplitRefused(t *testing.T) {
	const stepHelpers = `type myErr string

func (e myErr) Error() string { return string(e) }

func step() (int, error) {
	if fail {
		return 0, myErr("boom")
	}
	return 42, nil
}

var fail bool

func g() int { return 5 }
`

	tests := []struct {
		name    string
		compute string
	}{
		{
			name: "right_of_call",
			compute: `func compute() (int, error) {
	a := g() + step()?
	return a, nil
}
`,
		},
		{
			name: "short_circuit",
			compute: `func compute() (int, error) {
	if fail && step()? > 0 {
		return 43, nil
	}
	return 0, nil
}
`,
		},
		{
			name: "loop_condition",
			compute: `func compute() (int, error) {
	for step()? > 100 {
		break
	}
	return 0, nil
}
`,
		},
		{
			name: "case_expression",
			compute: `func compute() (int, error) {
	switch step()? {
	case 42:
		return 43, nil
	}
	return 0, nil
}
`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := "package main\n\n" + stepHelpers + tc.compute
			diags, exitCode := compileLgoJSON(t, src)
			if exitCode == 0 {
				t.Fatalf("expected compile failure on misplaced '?', got exit code 0")
			}
			d := misplacedTryDiag(t, diags)
			if len(d.AutofixCandidates) != 0 {
				t.Fatalf("expected no autofix candidate for %s position, got %d: %+v", tc.name, len(d.AutofixCandidates), d.AutofixCandidates)
			}
		})
	}
}

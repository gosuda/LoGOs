// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conccmd

import (
	"encoding/json"
	"io"
	"maps"
	"slices"
	"strings"
	"testing"
)

const inversion = `{"rule":"conc::lock_order_inversion","message":"two goroutines took the same two locks in opposite orders","positions":[{"file":"main.lgo","line":12,"role":"first goroutine: outer lock"},{"file":"main.lgo","line":13,"role":"first goroutine: inner lock"}],"fix":{"hint":"take the two locks in one order"}}` + "\n"

type signalWriter struct {
	text    strings.Builder
	written chan struct{}
}

func (w *signalWriter) Write(p []byte) (int, error) {
	n, err := w.text.Write(p)
	w.written <- struct{}{}
	return n, err
}

func TestRelayPrintsAFindingBeforeTheStreamEnds(t *testing.T) {
	r, w := io.Pipe()
	out := &signalWriter{written: make(chan struct{})}
	count := make(chan int)
	go func() { count <- relay(r, out, false) }()

	io.WriteString(w, inversion)
	<-out.written // The stream is still open here.
	want := "main.lgo:12: [conc::lock_order_inversion] two goroutines took the same two locks in opposite orders\n" +
		"\tmain.lgo:12: first goroutine: outer lock\n" +
		"\tmain.lgo:13: first goroutine: inner lock\n" +
		"\tfix: take the two locks in one order\n"
	if got := out.text.String(); got != want {
		t.Fatalf("printed\n%s\nwant\n%s", got, want)
	}

	io.WriteString(w, endOfFindings)
	if got := <-count; got != 1 {
		t.Fatalf("relay counted %d findings, want 1", got)
	}
}

func relayAll(input string, asJSON bool) (string, int) {
	var out strings.Builder
	n := relay(strings.NewReader(input), &out, asJSON)
	return out.String(), n
}

func TestRelayCountsDamagedLines(t *testing.T) {
	for name, input := range map[string]string{
		"not JSON":     "panic: something else\n",
		"no rule":      `{"positions":[{"file":"a.lgo","line":1,"role":"r"}]}` + "\n",
		"no position":  `{"rule":"conc::lock_order_inversion","positions":[]}` + "\n",
		"cut short":    inversion[:40],
		"over the cap": `{"rule":"` + strings.Repeat("r", maxFindingLine) + `","positions":[{"file":"a.lgo","line":1,"role":"r"}]}` + "\n",
	} {
		out, n := relayAll(input, false)
		if n != 1 || !strings.HasPrefix(out, "logos conc: malformed finding") {
			t.Errorf("%s: relay printed %q and counted %d; want one malformed finding", name, out, n)
		}
	}
}

func TestRelayStopsAtTheEndLine(t *testing.T) {
	out, n := relayAll(inversion+inversion+endOfFindings+inversion, false)
	if n != 2 || strings.Count(out, "[conc::lock_order_inversion]") != 2 {
		t.Fatalf("relay printed %q and counted %d; want the two findings before the end line", out, n)
	}
	if out, n := relayAll("", false); n != 0 || out != "" {
		t.Fatalf("an empty stream printed %q and counted %d", out, n)
	}
}

func TestRelayWritesDiagnosticRecords(t *testing.T) {
	out, n := relayAll(inversion+"not a finding\n", true)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if n != 2 || len(lines) != 2 {
		t.Fatalf("relay printed %d lines and counted %d; want 2 and 2:\n%s", len(lines), n, out)
	}
	wantKeys := []string{"autofix_candidates", "file", "message", "note", "provenance", "rule_id", "schema_version", "severity", "span"}
	for _, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("%v: %s", err, line)
		}
		if keys := slices.Sorted(maps.Keys(record)); !slices.Equal(keys, wantKeys) {
			t.Errorf("keys = %v, want %v", keys, wantKeys)
		}
		if record["schema_version"] != "1.0" || record["severity"] != "deny" {
			t.Errorf("schema_version and severity = %v, %v", record["schema_version"], record["severity"])
		}
	}
	var first struct {
		RuleID string `json:"rule_id"`
		File   string
		Note   string
		Span   struct {
			StartLine int `json:"start_line"`
			EndLine   int `json:"end_line"`
		}
	}
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	wantNote := "\tmain.lgo:12: first goroutine: outer lock\n\tmain.lgo:13: first goroutine: inner lock\n\tfix: take the two locks in one order\n"
	if first.RuleID != "conc::lock_order_inversion" || first.File != "main.lgo" || first.Span.StartLine != 12 || first.Span.EndLine != 12 || first.Note != wantNote {
		t.Errorf("record = %+v", first)
	}
	if !strings.Contains(lines[1], `"rule_id":""`) || !strings.Contains(lines[1], "malformed finding") {
		t.Errorf("a damaged line gave %s", lines[1])
	}
}

func TestChildArguments(t *testing.T) {
	// Stub flag table: run, -o and -exec take values; the rest is
	// unknown, which only matters after the package list of `test`.
	takesValue := func(name string) (bool, bool) {
		takes := name == "run" || name == "o" || name == "exec"
		return takes, takes
	}
	tests := []struct {
		verb      string
		args      string
		want      string
		asJSON    bool
		durations map[string]string
	}{
		{"run", ".", "run -tags=logosconc .", false, nil},
		{"run", "-race ./cmd/app -tags x -json-diagnostics -stall 1s", "run -tags=logosconc -race ./cmd/app -tags x -json-diagnostics -stall 1s", false, nil},
		{"run", "-tags=a,b -json-diagnostics . serve", "run -tags=a,b,logosconc . serve", true, nil},
		{"run", "-tags a -exec wrap .", "run -tags=a,logosconc -exec wrap .", false, nil},
		{"run", "-tags logosconc .", "run -tags=logosconc .", false, nil},
		{"run", "-stall 200ms -race .", "run -tags=logosconc -race .", false, map[string]string{"stall": "200ms"}},
		{"run", "-hold=20ms --stall 3s . -hold 9s", "run -tags=logosconc . -hold 9s", false, map[string]string{"hold": "20ms", "stall": "3s"}},
		{"run", "-C sub .", "run -C sub -tags=logosconc .", false, nil},
		{"run", "-C=sub -race .", "run -C=sub -tags=logosconc -race .", false, nil},
		{"test", "-run count ./a", "test -count=1 -tags=logosconc -run count ./a", false, nil},
		{"test", "-C sub -run T ./a", "test -C sub -count=1 -tags=logosconc -run T ./a", false, nil},
		{"test", "-run TestA ./a ./b -race", "test -count=1 -tags=logosconc -run TestA ./a ./b -race", false, nil},
		{"test", "--json-diagnostics=false ./a -args -tags z", "test -count=1 -tags=logosconc ./a -args -tags z", false, nil},
		{"test", "./a --stall=1m30s -hold 1s -args -stall 9s", "test -count=1 -tags=logosconc ./a -args -stall 9s", false, map[string]string{"stall": "1m30s", "hold": "1s"}},
		{"test", "", "test -count=1 -tags=logosconc", false, nil},
	}
	for _, tt := range tests {
		got := childArguments(tt.verb, strings.Fields(tt.args), takesValue, "")
		if want := strings.Fields(tt.want); !slices.Equal(got.arguments, want) || got.asJSON != tt.asJSON || !maps.Equal(got.durations, tt.durations) {
			t.Errorf("logos conc %s %s:\n got %q asJSON=%v durations=%v\nwant %q asJSON=%v durations=%v", tt.verb, tt.args, got.arguments, got.asJSON, got.durations, want, tt.asJSON, tt.durations)
		}
	}
}

func TestChildArgumentsShake(t *testing.T) {
	neverTakesValue := func(name string) (bool, bool) { return false, false }
	tests := []struct {
		verb      string
		args      string
		want      string
		shake     string
		shakeBare bool
	}{
		{"test", "-shake=7 -count=1 ./ord", "test -tags=logosconc -count=1 ./ord", "7", false},
		{"test", "-shake 7 -count=1 ./ord", "test -tags=logosconc -count=1 ./ord", "7", false},
		{"test", "-count=1 ./ord -shake", "test -tags=logosconc -count=1 ./ord", "", true},
		{"test", "-shake ./ord", "test -count=1 -tags=logosconc ./ord", "", true},
		{"test", "-shake -shake=7 -count=1 ./ord", "test -tags=logosconc -count=1 ./ord", "7", false},
		{"test", "-shake -shake 7 -count=1 ./ord", "test -tags=logosconc -count=1 ./ord", "7", false},
		{"test", "-shake=7 -shake -count=1 ./ord", "test -tags=logosconc -count=1 ./ord", "", true},
		{"run", "-shake=7 .", "run -tags=logosconc .", "7", false},
		{"run", "-shake .", "run -tags=logosconc .", "", true},
		{"run", "-shake -shake=7 .", "run -tags=logosconc .", "7", false},
		{"test", "-count=1 ./ord", "test -tags=logosconc -count=1 ./ord", "", false},
	}
	for _, tt := range tests {
		got := childArguments(tt.verb, strings.Fields(tt.args), neverTakesValue, "")
		if want := strings.Fields(tt.want); !slices.Equal(got.arguments, want) || got.shake != tt.shake || got.shakeBare != tt.shakeBare {
			t.Errorf("logos conc %s %s:\n got %q shake=%q bare=%v\nwant %q shake=%q bare=%v", tt.verb, tt.args, got.arguments, got.shake, got.shakeBare, want, tt.shake, tt.shakeBare)
		}
	}
}

func TestChildArgumentsKeepsGOFLAGSCount(t *testing.T) {
	neverTakesValue := func(name string) (bool, bool) { return false, false }
	tests := []struct {
		goflags string
		args    []string
		want    []string
	}{
		{"-count=10", []string{"./ord"}, []string{"test", "-count=10", "-tags=logosconc", "./ord"}},
		{"-test.count=10", []string{"./ord"}, []string{"test", "-count=10", "-tags=logosconc", "./ord"}},
		{"-tags=x", []string{"./ord"}, []string{"test", "-count=1", "-tags=x,logosconc", "./ord"}},
		{"", []string{"./ord"}, []string{"test", "-count=1", "-tags=logosconc", "./ord"}},
		{"-count=10", []string{"-count=3", "./ord"}, []string{"test", "-tags=logosconc", "-count=3", "./ord"}},
	}
	for _, tt := range tests {
		if got := childArguments("test", tt.args, neverTakesValue, tt.goflags); !slices.Equal(got.arguments, tt.want) {
			t.Errorf("GOFLAGS=%q logos conc test %q:\n got %q\nwant %q", tt.goflags, tt.args, got.arguments, tt.want)
		}
	}
}

func TestChildArgumentsKeepsGOFLAGSTags(t *testing.T) {
	neverTakesValue := func(name string) (bool, bool) { return false, false }
	tests := []struct {
		goflags string
		args    []string
		want    []string
	}{
		{"-tags=foo", []string{"."}, []string{"run", "-tags=foo,logosconc", "."}},
		{"-tags=a,b", []string{"."}, []string{"run", "-tags=a,b,logosconc", "."}},
		{"--tags=a", []string{"."}, []string{"run", "-tags=a,logosconc", "."}},
		{"-tags=a -tags=b", []string{"."}, []string{"run", "-tags=b,logosconc", "."}},
		{"'-tags=a b'", []string{"."}, []string{"run", "-tags=a,b,logosconc", "."}},
		{"-tags=foo", []string{"-tags=bar", "."}, []string{"run", "-tags=bar,logosconc", "."}},
		{"-tags=foo", []string{"--tags=bar", "."}, []string{"run", "-tags=bar,logosconc", "."}},
		{"-tags foo", []string{"."}, []string{"run", "-tags=logosconc", "."}},
		{"", []string{"."}, []string{"run", "-tags=logosconc", "."}},
	}
	for _, tt := range tests {
		if got := childArguments("run", tt.args, neverTakesValue, tt.goflags); !slices.Equal(got.arguments, tt.want) {
			t.Errorf("GOFLAGS=%q logos conc run %q:\n got %q\nwant %q", tt.goflags, tt.args, got.arguments, tt.want)
		}
	}
}

func TestChildArgumentsReadsTestJSON(t *testing.T) {
	// Mirrors the command's flag table: -run takes its value, -json
	// and -v are known bool flags, and -custom stands in for a flag
	// only the test binary registers. The scan must consume TestJobs
	// instead of treating it as positional and stopping the package
	// list there, and must keep the list open past one possible value
	// of an unknown flag, as the child scan does.
	takesValue := func(name string) (bool, bool) { return name == "run", name == "run" || name == "json" || name == "v" }
	tests := []struct {
		verb    string
		goflags string
		args    string
		want    bool
	}{
		{"test", "", "-json ./a", true},
		{"test", "", "./a -json", true},
		{"test", "", "--json=true ./a", true},
		{"test", "", "-json=false ./a", false},
		{"test", "", "./a", false},
		{"test", "", "./a -args -json", false},
		{"test", "-json", "./a", true},
		{"test", "-json=1", "./a", true},
		{"test", "-json", "-json=false ./a", false},
		{"test", "-json -json=false", "./a", false},
		{"test", "", "-json . -run TestJobs sentinel -json=false", true},
		{"test", "", "-json . -run -json=false sentinel", true},
		{"test", "", "./a sentinel -json=false", false},
		{"test", "", "-json ./a -tags=x sentinel -json=false", true},
		{"test", "", ". -custom value -json", true},
		{"test", "", ". -custom -stall=1s value -json", true},
		{"test", "", ". -custom=value -json", true},
		{"test", "", "./a -custom sentinel -json", true},
		{"test", "-json", "-custom=value sentinel -json=false", true},
		{"test", "", ". -custom --ai-patch value -json", true},
		{"test", "", "./a -v sentinel -json", false},
		{"test", "", ". -custom -json=false ./a -json", false},
		{"run", "", "-json .", false},
		{"run", "-json", ".", false},
	}
	for _, tt := range tests {
		got := childArguments(tt.verb, strings.Fields(tt.args), takesValue, tt.goflags)
		if got.testJSON != tt.want {
			t.Errorf("GOFLAGS=%q logos conc %s %s: testJSON = %v, want %v", tt.goflags, tt.verb, tt.args, got.testJSON, tt.want)
		}
		if strings.Contains(tt.args, "json") && !slices.ContainsFunc(got.arguments, func(arg string) bool { return strings.Contains(arg, "json") }) {
			t.Errorf("logos conc %s %s: child arguments %q lost the -json flag", tt.verb, tt.args, got.arguments)
		}
	}
}

// TestOutputsKeepReviewOnStdout runs one finding and one brief record
// through the outputs of the four flag combinations: text findings and
// the brief go to stdout, diagnostic records to stderr, and under
// `test -json` every stdout line is a test2json output event.
func TestOutputsKeepReviewOnStdout(t *testing.T) {
	text, _ := relayAll(inversion, false)
	record, _ := relayAll(inversion, true)
	brief := `{"file":"main.lgo","line":12,"edit":"\tselect {"}` + "\n"
	tests := []struct {
		asJSON, testJSON bool
		stdout, stderr   string
	}{
		{false, false, text + brief, ""},
		{true, false, brief, record},
		{false, true, text + brief, ""},
		{true, true, brief, record},
	}
	for _, tt := range tests {
		var stdout, stderr strings.Builder
		inv := invocation{asJSON: tt.asJSON, testJSON: tt.testJSON}
		findings, briefOut := inv.outputs(&stdout, &stderr)
		relay(strings.NewReader(inversion), findings, inv.asJSON)
		printStalledBrief([]stalledFix{{file: "main.lgo", line: 12, replacement: "\tselect {"}}, briefOut)
		got := stdout.String()
		if tt.testJSON {
			var output strings.Builder
			for line := range strings.Lines(got) {
				var event struct{ Action, Output string }
				if err := json.Unmarshal([]byte(line), &event); err != nil || event.Action != "output" {
					t.Fatalf("--json-diagnostics=%v -json: stdout line %q is not a test2json output event", tt.asJSON, line)
				}
				output.WriteString(event.Output)
			}
			got = output.String()
		}
		if got != tt.stdout || stderr.String() != tt.stderr {
			t.Errorf("--json-diagnostics=%v -json=%v:\nstdout %q\nwant   %q\nstderr %q\nwant   %q", tt.asJSON, tt.testJSON, got, tt.stdout, stderr.String(), tt.stderr)
		}
	}
}

func TestWithMonitorTagJoinsSpaceSeparatedTags(t *testing.T) {
	tests := []struct{ in, want string }{
		{"debug trace", "debug,trace,logosconc"},
		{"debug,trace", "debug,trace,logosconc"},
		{"debug trace,logosconc", "debug,trace,logosconc"},
		{"logosconc", "logosconc"},
	}
	for _, tt := range tests {
		if got := withMonitorTag(tt.in); got != tt.want {
			t.Errorf("withMonitorTag(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

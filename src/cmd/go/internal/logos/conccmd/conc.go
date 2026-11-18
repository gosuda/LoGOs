// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package conccmd implements the `logos conc` command: it runs a
// program or its tests in a build that carries the concurrency monitor
// of logos/internal/conc, and prints each finding while the program
// runs.
package conccmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"cmd/go/internal/base"
)

const (
	// monitorTag selects the monitored files of logos/rt and logos/sync.
	monitorTag = "logosconc"

	// sinkEnv and maxFindingLine equal SinkEnv and MaxLine of
	// logos/internal/conc, which cmd/go may not import.
	sinkEnv        = "LOGOSCONC_SINK"
	maxFindingLine = 4000
)

var CmdConc = &base.Command{
	UsageLine: "logos conc",
	Short:     "watch a running program for concurrency faults",
	Long: `
Conc runs a loGo/s program, or the tests of loGo/s packages, in a build
that carries a monitor, and reports concurrency faults while the program
runs. It reports faults that 'logos lint' and 'logos test -race' do not
see, because no memory is raced and nothing has failed yet.

	logos conc run [build flags] [--json-diagnostics] package [arguments...]
	logos conc test [build and test flags] [--json-diagnostics] [packages]

The finding conc::lock_order_inversion names two goroutines that took
the same two logos/sync.Locked values in opposite orders. Run at the
same moment, each goroutine waits for the other.

A finding is printed on standard error as soon as the monitor sees it:

	file:line: [rule] message
		file:line: what this position is
		fix: how to repair it

The --json-diagnostics flag prints each finding as one JSON object per
line instead, in the schema that 'logos lint --json-diagnostics' uses.
The field 'note' holds the position lines and the fix line.

The exit status is the status of the program or of 'logos test' when
that is not zero. Otherwise it is 1 when a finding was printed, and 0
when none was.

See also: logos lint, logos test.
	`,
	Commands: []*base.Command{cmdRun, cmdTest},
}

var cmdRun = &base.Command{
	UsageLine:   "logos conc run [build flags] [--json-diagnostics] package [arguments...]",
	Short:       "run a program under the concurrency monitor",
	Long:        "Run is 'logos run' under the concurrency monitor. See 'logos help conc'.\n",
	CustomFlags: true,
}

var cmdTest = &base.Command{
	UsageLine:   "logos conc test [build and test flags] [--json-diagnostics] [packages]",
	Short:       "test packages under the concurrency monitor",
	Long:        "Test is 'logos test' under the concurrency monitor. See 'logos help conc'.\n",
	CustomFlags: true,
}

// Register adds the command to root. The flag sets of the run and test
// commands of root tell which of their flags take a value.
func Register(root *base.Command) {
	cmdRun.Run = func(ctx context.Context, cmd *base.Command, args []string) {
		monitor("run", childArguments("run", args, valueFlags(root.Lookup("run"))))
	}
	cmdTest.Run = func(ctx context.Context, cmd *base.Command, args []string) {
		monitor("test", childArguments("test", args, valueFlags(root.Lookup("test"))))
	}
	root.Commands = append(root.Commands, CmdConc)
}

type invocation struct {
	arguments []string
	asJSON    bool
}

// valueFlags reports, for each flag of cmd, whether it takes its value
// from the next argument.
func valueFlags(cmd *base.Command) func(name string) bool {
	return func(name string) bool {
		if cmd == nil {
			return false
		}
		f := cmd.Flag.Lookup(name)
		if f == nil {
			return false
		}
		b, isBool := f.Value.(interface{ IsBoolFlag() bool })
		return !isBool || !b.IsBoolFlag()
	}
}

// childArguments turns the arguments of `logos conc <verb>` into the
// arguments of `logos <verb>`: it removes -json-diagnostics and adds the
// monitor tag to the build tags. It reads flags up to the package of
// `run`, and up to -args or -- of `test`; later arguments belong to the
// program.
func childArguments(verb string, args []string, takesValue func(string) bool) invocation {
	inv := invocation{arguments: []string{verb}}
	tagged := false
	i := 0
	for ; i < len(args); i++ {
		arg := args[i]
		if arg == "--" || arg == "-args" || arg == "--args" {
			break
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" {
			if verb == "run" {
				break
			}
			inv.arguments = append(inv.arguments, arg)
			continue
		}
		name, value, hasValue := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		switch {
		case name == "json-diagnostics":
			inv.asJSON = !hasValue || value == "true"
		case name == "tags":
			if !hasValue && i+1 < len(args) {
				i++
				value = args[i]
			}
			inv.arguments = append(inv.arguments, "-tags="+withMonitorTag(value))
			tagged = true
		default:
			inv.arguments = append(inv.arguments, arg)
			if !hasValue && takesValue(name) && i+1 < len(args) {
				i++
				inv.arguments = append(inv.arguments, args[i])
			}
		}
	}
	if !tagged {
		inv.arguments = append([]string{verb, "-tags=" + monitorTag}, inv.arguments[1:]...)
	}
	inv.arguments = append(inv.arguments, args[i:]...)
	return inv
}

func withMonitorTag(tags string) string {
	if tags == "" {
		return monitorTag
	}
	for _, tag := range strings.FieldsFunc(tags, func(r rune) bool { return r == ',' || r == ' ' }) {
		if tag == monitorTag {
			return tags
		}
	}
	return tags + "," + monitorTag
}

type findingLine struct {
	Rule      string `json:"rule"`
	Message   string `json:"message"`
	Positions []struct {
		File string `json:"file"`
		Line int    `json:"line"`
		Role string `json:"role"`
	} `json:"positions"`
	Fix struct {
		Hint string `json:"hint"`
	} `json:"fix"`
}

// endOfFindings ends the stream. The monitor never writes an empty
// line, so the command writes one after the program has exited.
const endOfFindings = "\n"

// relay prints each finding line of r on w as soon as it arrives and
// returns how many it printed. It stops at endOfFindings or at the end
// of r. A line that breaks the contract of the monitor counts as a
// finding: the run is not clean when the report is damaged.
func relay(r io.Reader, w io.Writer, asJSON bool) int {
	findings := 0
	lines := bufio.NewReaderSize(r, 2*maxFindingLine)
	for {
		line, err := lines.ReadString('\n')
		if line == endOfFindings {
			return findings
		}
		if line != "" {
			findings++
			printFinding(w, line, asJSON)
		}
		if err != nil {
			return findings
		}
	}
}

// diagnostic is the record of SPEC Section 14. The monitor knows lines
// only, so the columns and the byte range of the span are zero.
type diagnostic struct {
	SchemaVersion string `json:"schema_version"`
	Severity      string `json:"severity"`
	RuleID        string `json:"rule_id"`
	File          string `json:"file"`
	Span          struct {
		StartLine  int `json:"start_line"`
		StartCol   int `json:"start_col"`
		EndLine    int `json:"end_line"`
		EndCol     int `json:"end_col"`
		ByteOffset int `json:"byte_offset"`
		ByteLen    int `json:"byte_len"`
	} `json:"span"`
	Message    string `json:"message"`
	Note       string `json:"note"`
	Provenance struct {
		OriginExpr   string `json:"origin_expr"`
		OriginLine   int    `json:"origin_line"`
		InferredType string `json:"inferred_type"`
	} `json:"provenance"`
	AutofixCandidates []struct{} `json:"autofix_candidates"`
}

func printFinding(w io.Writer, line string, asJSON bool) {
	var f findingLine
	var d diagnostic
	d.SchemaVersion, d.Severity, d.AutofixCandidates = "1.0", "deny", []struct{}{}
	complete := strings.HasSuffix(line, "\n")
	if !complete || len(line) > maxFindingLine || json.Unmarshal([]byte(line), &f) != nil || f.Rule == "" || len(f.Positions) == 0 {
		d.Message = fmt.Sprintf("logos conc: malformed finding (%d bytes)", len(line))
	} else {
		var note strings.Builder
		for _, p := range f.Positions {
			fmt.Fprintf(&note, "\t%s:%d: %s\n", p.File, p.Line, p.Role)
		}
		fmt.Fprintf(&note, "\tfix: %s\n", f.Fix.Hint)
		first := f.Positions[0]
		d.RuleID, d.File, d.Message, d.Note = f.Rule, first.File, f.Message, note.String()
		d.Span.StartLine, d.Span.EndLine = first.Line, first.Line
	}
	switch {
	case asJSON:
		record, err := json.Marshal(d)
		if err != nil {
			base.Fatalf("logos conc: %v", err)
		}
		w.Write(append(record, '\n'))
	case d.RuleID == "":
		fmt.Fprintln(w, d.Message)
	default:
		fmt.Fprintf(w, "%s:%d: [%s] %s\n%s", d.File, d.Span.StartLine, d.RuleID, d.Message, d.Note)
	}
}

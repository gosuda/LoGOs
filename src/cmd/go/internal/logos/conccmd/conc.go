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
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"cmd/go/internal/base"
	"cmd/go/internal/cfg"
	"cmd/go/internal/load"
	"cmd/internal/quoted"
)

const (
	monitorTag = load.LogosMonitorTag

	// sinkEnv and maxFindingLine equal SinkEnv and MaxLine of
	// logos/internal/conc, which cmd/go may not import.
	sinkEnv        = "LOGOSCONC_SINK"
	maxFindingLine = 4000

	// shakeEnv equals ShakeEnv of logos/internal/conc.
	shakeEnv = "LOGOSCONC_SHAKE"
)

// durationFlags maps each flag of `logos conc` that takes a duration to
// the variable that gives the duration to the monitor: StallEnv and
// HoldEnv of logos/internal/conc.
var durationFlags = map[string]string{
	"stall": "LOGOSCONC_STALL",
	"hold":  "LOGOSCONC_HOLD",
}

var CmdConc = &base.Command{
	UsageLine: "logos conc",
	Short:     "watch a running program for concurrency faults",
	Long: `
Conc runs a loGo/s program, or the tests of loGo/s packages, in a build
that carries a monitor, and reports concurrency faults while the program
runs. It reports faults that 'logos lint' and 'logos test -race' do not
see, because no memory is raced and nothing has failed yet.

	logos conc run [build flags] [conc flags] package [arguments...]
	logos conc test [build and test flags] [conc flags] [packages]

The conc flags are:

	-stall duration
		report a group child that runs this long after its group was
		cancelled (default 5s)
	-hold duration
		report a Locked value that one goroutine holds this long while
		another goroutine waits for it (default 100ms)
	-shake seed
		yield at group and lock hook points from this seed, so one seed
		gives one schedule perturbation (default off); without a value
		the seed comes from the clock and is printed on standard error
	--json-diagnostics
		print each finding as one JSON object
	--fix | --ai-patch
		apply the mechanical fixes, or print the patch brief without
		changing files (the two flags are exclusive)

The finding conc::lock_order_inversion names two goroutines that took
the same two logos/sync.Locked values in opposite orders. Run at the
same moment, each goroutine waits for the other.

The finding conc::group_child_stalled names a child of a go group that
did not return after its group was cancelled. The group cannot join
before that child returns. When the child waits on a plain channel
receive in a function that takes a context, --fix rewrites that line
into a select on the receive and the context, and --ai-patch prints the
same edit without changing files. Other waits keep a hint.

The finding conc::group_unbounded names a go group without a limit
whose children reach 10000 at one time. The count of children then
follows the work the program gives the group. A group with a limit
never gets this finding.

The finding conc::lock_held_long names a WithLock call that held a
logos/sync.Locked value for the -hold duration while another goroutine
waited for it. The monitor has no timer for a hold: it prints the
finding when a goroutine comes for the value after that duration, or
when the hold ends.

The finding conc::goroutine_leak names a goroutine that waits on a
channel or a lock which no goroutine that can run reaches. It never
ends. The monitor checks for it six times in the first 6.3 seconds of
the program, and later after a collection of the program, one time per
minute at most.

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
	UsageLine:   "logos conc run [build flags] [-stall duration] [-hold duration] [-shake seed] [--json-diagnostics] [--fix | --ai-patch] package [arguments...]",
	Short:       "run a program under the concurrency monitor",
	Long:        "Run is 'logos run' under the concurrency monitor. See 'logos help conc'.\n",
	CustomFlags: true,
}

var cmdTest = &base.Command{
	UsageLine:   "logos conc test [build and test flags] [-stall duration] [-hold duration] [-shake seed] [--json-diagnostics] [--fix | --ai-patch] [packages]",
	Short:       "test packages under the concurrency monitor",
	Long:        "Test is 'logos test' under the concurrency monitor. See 'logos help conc'.\n",
	CustomFlags: true,
}

// Register adds the command to root. The flag sets of the run and test
// commands of root tell which of their flags take a value.
func Register(root *base.Command) {
	for verb, cmd := range map[string]*base.Command{"run": cmdRun, "test": cmdTest} {
		cmd.Run = func(ctx context.Context, _ *base.Command, args []string) {
			inv := childArguments(verb, args, valueFlags(root.Lookup(verb)), cfg.Getenv("GOFLAGS"))
			for _, name := range slices.Sorted(maps.Keys(inv.durations)) {
				if d, err := time.ParseDuration(inv.durations[name]); err != nil || d <= 0 {
					base.Fatalf("logos conc %s: -%s %q is not a positive duration", verb, name, inv.durations[name])
				}
			}
			if inv.shakeBare {
				seed := uint64(time.Now().UnixNano())
				if seed == 0 {
					seed = 1
				}
				inv.shake = strconv.FormatUint(seed, 10)
			}
			if inv.shake != "" {
				if seed, err := strconv.ParseUint(inv.shake, 10, 64); err != nil || seed == 0 {
					base.Fatalf("logos conc %s: -shake %q is not a seed", verb, inv.shake)
				}
				fmt.Fprintf(os.Stderr, "logos conc: shake seed %s\n", inv.shake)
			}
			if inv.fix && inv.aiPatch {
				base.Fatalf("logos conc %s: --fix and --ai-patch are exclusive", verb)
			}
			monitor(verb, inv)
		}
	}
	root.Commands = append(root.Commands, CmdConc)
}

type invocation struct {
	arguments []string
	asJSON    bool
	// durations holds the value of each duration flag that the user gave.
	durations map[string]string
	// shake holds the value of -shake. shakeBare reports -shake without
	// a value, which takes its seed from the clock in Register.
	shake     string
	shakeBare bool
	// fix applies the mechanical fixes after the run; aiPatch prints the
	// patch brief without changing files. The two are exclusive.
	fix     bool
	aiPatch bool
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
// arguments of `logos <verb>`: it removes -json-diagnostics and the
// duration flags, and adds the monitor tag to the build tags. It reads
// flags up to the package of `run`, and up to -args or -- of `test`;
// later arguments belong to the program. The goflags argument carries the
// effective GOFLAGS, so the test repeat count and the GOFLAGS build tags
// are decided from explicit values instead of the ambient process
// configuration. An explicit -tags overrides GOFLAGS in the go command,
// so the monitor joins the explicit set when one is present and the
// GOFLAGS set otherwise.
func childArguments(verb string, args []string, takesValue func(string) bool, goflags string) invocation {
	inv := invocation{arguments: []string{verb}, durations: map[string]string{}}
	tagged := false
	countSet := false
	i := 0
	// The -C flag must stay first: the go command consumes it only in
	// the leading position, so injected flags go behind it.
	if i < len(args) && (args[i] == "-C" || args[i] == "--C") {
		inv.arguments = append(inv.arguments, args[i])
		i++
		if i < len(args) {
			inv.arguments = append(inv.arguments, args[i])
			i++
		}
	} else if i < len(args) && (strings.HasPrefix(args[i], "-C=") || strings.HasPrefix(args[i], "--C=")) {
		inv.arguments = append(inv.arguments, args[i])
		i++
	}
	// injectAt is where injected flags (-tags, -count) join the
	// arguments: behind -C, before everything else.
	injectAt := len(inv.arguments)
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
		if name == "count" || name == "test.count" {
			countSet = true
		}
		switch {
		case name == "json-diagnostics":
			inv.asJSON = !hasValue || value == "true"
		case name == "fix":
			inv.fix = !hasValue || value == "true"
		case name == "ai-patch":
			inv.aiPatch = !hasValue || value == "true"
		case durationFlags[name] != "":
			if !hasValue && i+1 < len(args) {
				i++
				value = args[i]
			}
			inv.durations[name] = value
		case name == "shake":
			if hasValue && value != "" {
				inv.shake = value
				inv.shakeBare = false
			} else if i+1 < len(args) {
				if _, err := strconv.ParseUint(args[i+1], 10, 64); err == nil {
					i++
					inv.shake = args[i]
					inv.shakeBare = false
				} else {
					inv.shake = ""
					inv.shakeBare = true
				}
			} else {
				inv.shake = ""
				inv.shakeBare = true
			}
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
		if tags, ok := goflagsTags(goflags); ok {
			inv.arguments = slices.Insert(inv.arguments, injectAt, "-tags="+withMonitorTag(tags))
		} else {
			inv.arguments = slices.Insert(inv.arguments, injectAt, "-tags="+monitorTag)
		}
	}
	if verb == "test" {
		// Without an explicit count on the command line, repeat the
		// count the effective GOFLAGS configures as an explicit flag,
		// so the child runs the configured repeat count even when it
		// resolves the configuration from a different directory, and
		// the explicit flag keeps the monitored run out of the test
		// result cache. A bare -count entry with no value keeps the old
		// behavior: the child reports the malformed flag itself.
		if !countSet {
			if count, ok := goflagsTestCount(goflags); ok && count != "" {
				inv.arguments = slices.Insert(inv.arguments, injectAt, "-count="+count)
				countSet = true
			}
		}
		if !countSet {
			inv.arguments = slices.Insert(inv.arguments, injectAt, "-count=1")
		}
	}

	inv.arguments = append(inv.arguments, args[i:]...)
	return inv
}

// goflagsTestCount returns the test repeat count the effective GOFLAGS
// configures. GOFLAGS holds one flag per entry in -name=value form, so a
// bare name check covers both -count and -test.count. The second result
// is false when no entry names a count.
func goflagsTestCount(goflags string) (string, bool) {
	flags, err := quoted.Split(goflags)
	if err != nil {
		return "", false
	}
	for _, flag := range flags {
		name, value, _ := strings.Cut(strings.TrimLeft(flag, "-"), "=")
		if name == "count" || name == "test.count" {
			return value, true
		}
	}
	return "", false
}

// goflagsTags returns the build tags the effective GOFLAGS configures.
// GOFLAGS holds one flag per entry in -name=value form, so only the
// -tags=value and --tags=value spellings count; a bare -tags entry is
// invalid and the child reports it. The last entry wins, as in the go
// command. The second result is false when no entry names tags.
func goflagsTags(goflags string) (string, bool) {
	flags, err := quoted.Split(goflags)
	if err != nil {
		return "", false
	}
	tags := ""
	ok := false
	for _, flag := range flags {
		switch {
		case strings.HasPrefix(flag, "--tags="):
			tags, ok = flag[len("--tags="):], true
		case strings.HasPrefix(flag, "-tags="):
			tags, ok = flag[len("-tags="):], true
		default:
			continue
		}
	}
	return tags, ok
}

func withMonitorTag(tags string) string {
	if tags == "" {
		return monitorTag
	}
	fields := strings.FieldsFunc(tags, func(r rune) bool { return r == ',' || r == ' ' })
	for _, tag := range fields {
		if tag == monitorTag {
			return strings.Join(fields, ",")
		}
	}
	return strings.Join(fields, ",") + "," + monitorTag
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

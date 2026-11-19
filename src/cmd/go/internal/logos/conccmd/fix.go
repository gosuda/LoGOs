// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conccmd

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

// stalledFix is one mechanical repair of a stalled group child that waits
// on a plain channel receive. line is 1-based.
type stalledFix struct {
	file        string
	line        int
	replacement string
}

// collectStalledFixes reads the raw finding lines of one run and returns
// one fix per stalled-child finding whose primary position is a plain
// channel receive in a function that takes a context. Findings of other
// kinds, and stalls the edit cannot name safely, keep their hint and get
// no fix.
func collectStalledFixes(raw string) []stalledFix {
	var out []stalledFix
	seen := map[string]bool{}
	for _, text := range strings.Split(raw, "\n") {
		if text == "" {
			continue
		}
		var f findingLine
		if err := json.Unmarshal([]byte(text), &f); err != nil {
			continue
		}
		if f.Rule != "conc::group_child_stalled" || len(f.Positions) == 0 {
			continue
		}
		pos := f.Positions[0]
		key := fmt.Sprintf("%s:%d", pos.File, pos.Line)
		if seen[key] {
			continue
		}
		seen[key] = true
		replacement, ok := stalledReplacement(pos.File, pos.Line)
		if !ok {
			continue
		}
		out = append(out, stalledFix{file: pos.File, line: pos.Line, replacement: replacement})
	}
	return out
}

// stalledReplacement returns the select that lets the wait end with the
// group context when the line is a plain channel receive statement and
// the enclosing function takes a context. Otherwise it reports false and
// the finding keeps its hint.
func stalledReplacement(file string, line int) (string, bool) {
	if !strings.HasSuffix(file, ".lgo") {
		return "", false
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", false
	}
	lines := strings.Split(string(data), "\n")
	if line < 1 || line > len(lines) {
		return "", false
	}
	text := lines[line-1]
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "<-") {
		return "", false
	}
	expr := strings.TrimSpace(strings.TrimPrefix(trimmed, "<-"))
	if expr == "" || strings.ContainsAny(expr, " \t;") {
		return "", false
	}
	ctx, ok := enclosingContext(lines, line)
	if !ok {
		return "", false
	}
	indent := text[:len(text)-len(strings.TrimLeft(text, " \t"))]
	var b strings.Builder
	b.WriteString(indent)
	b.WriteString("select {\n")
	b.WriteString(indent)
	b.WriteString("case <-")
	b.WriteString(expr)
	b.WriteString(":\n")
	b.WriteString(indent)
	b.WriteString("case <-")
	b.WriteString(ctx)
	b.WriteString(".Done():\n")
	b.WriteString(indent)
	b.WriteString("}")
	return b.String(), true
}

// enclosingContext returns the name of the context.Context parameter of
// the function that holds the 1-based line. Only a single-line func
// header counts; anything else gets no fix. It stops at the nearest func
// header above the line, so a context from an outer function is never
// taken.
func enclosingContext(lines []string, line int) (string, bool) {
	for idx := line - 2; idx >= 0 && idx >= line-22; idx-- {
		header := lines[idx]
		if !strings.Contains(header, "func ") {
			continue
		}
		pos := strings.Index(header, "context.Context")
		if pos < 0 {
			return "", false
		}
		before := strings.TrimSpace(header[:pos])
		sep := strings.LastIndexAny(before, " \t(,")
		name := before
		if sep >= 0 {
			name = before[sep+1:]
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return "", false
		}
		for i := 0; i < len(name); i++ {
			c := name[i]
			if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
				return "", false
			}
		}
		if name[0] >= '0' && name[0] <= '9' {
			return "", false
		}
		return name, true
	}
	return "", false
}

// applyStalledFixes writes the replacements, latest line first per file
// so earlier lines keep their numbers. It returns one "file:line" per
// write.
func applyStalledFixes(fixes []stalledFix) []string {
	slices.SortFunc(fixes, func(a, b stalledFix) int {
		if a.file < b.file {
			return -1
		}
		if a.file > b.file {
			return 1
		}
		return b.line - a.line
	})
	var applied []string
	var cur string
	var lines []string
	var pending []string
	dirty := false
	flush := func() {
		if !dirty {
			pending = nil
			return
		}
		if err := os.WriteFile(cur, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "logos conc: cannot fix %s: %v\n", cur, err)
		} else {
			applied = append(applied, pending...)
		}
		pending = nil
		dirty = false
	}
	for _, f := range fixes {
		if f.file != cur {
			flush()
			data, err := os.ReadFile(f.file)
			if err != nil {
				fmt.Fprintf(os.Stderr, "logos conc: cannot fix %s: %v\n", f.file, err)
				cur = f.file
				lines = nil
				continue
			}
			cur = f.file
			lines = strings.Split(string(data), "\n")
		}
		if f.line < 1 || f.line > len(lines) {
			continue
		}
		lines[f.line-1] = f.replacement
		dirty = true
		pending = append(pending, fmt.Sprintf("%s:%d", f.file, f.line))
	}
	flush()
	return applied
}

// printStalledBrief prints one JSON object per fix to standard output
// without changing files.
func printStalledBrief(fixes []stalledFix) {
	for _, f := range fixes {
		rec, err := json.Marshal(struct {
			File string `json:"file"`
			Line int    `json:"line"`
			Edit string `json:"edit"`
		}{File: f.file, Line: f.line, Edit: f.replacement})
		if err != nil {
			continue
		}
		fmt.Fprintf(os.Stdout, "%s\n", rec)
	}
}

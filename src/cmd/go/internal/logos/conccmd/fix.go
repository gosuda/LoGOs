// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conccmd

import (
	"encoding/json"
	"fmt"
	"go/token"
	"os"
	"path/filepath"
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

// overlayFiles returns the from→to map of the -overlay flag the child
// arguments carry, keyed and valued by absolute paths the way cmd/go's
// fsys package reads it. The compiler reports a finding at the logical
// file while it compiled the replacement, so a fix must read and write
// the replacement. A declared but unreadable overlay returns false so
// no fix is offered for files the map might hide.
func overlayFiles(args []string) (map[string]string, bool) {
	name := ""
	for i, arg := range args {
		if v, ok := strings.CutPrefix(arg, "-overlay="); ok {
			name = v
			continue
		}
		if v, ok := strings.CutPrefix(arg, "--overlay="); ok {
			name = v
			continue
		}
		if (arg == "-overlay" || arg == "--overlay") && i+1 < len(args) {
			name = args[i+1]
		}
	}
	if name == "" {
		return nil, true
	}
	var doc struct {
		Replace map[string]string
	}
	data, err := os.ReadFile(name)
	if err != nil || json.Unmarshal(data, &doc) != nil {
		return nil, false
	}
	files := map[string]string{}
	for from, to := range doc.Replace {
		fromAbs, err := filepath.Abs(from)
		if err != nil {
			continue
		}
		toAbs, err := filepath.Abs(to)
		if err != nil {
			continue
		}
		files[fromAbs] = toAbs
	}
	return files, true
}

// collectStalledFixes reads the raw finding lines of one run and returns
// one fix per stalled-child finding whose primary position is a plain
// channel receive in a function that takes a context. Findings of other
// kinds, and stalls the edit cannot name safely, keep their hint and get
// no fix. overlay maps the file names the compiler reports to the files
// it actually compiled under -overlay; nil means no overlay was used.
func collectStalledFixes(raw string, overlay map[string]string) []stalledFix {
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
		file := pos.File
		if overlay != nil {
			abs, err := filepath.Abs(file)
			if err != nil {
				continue
			}
			to, ok := overlay[abs]
			if !ok {
				// The file is not replaced, but its reported name may
				// already be an overlay key resolved earlier; leave it.
			} else if to == "" {
				continue // replaced by nothing: the file appears deleted
			} else {
				file = to
			}
		}
		replacement, ok := stalledReplacement(file, pos.Line)
		if !ok {
			continue
		}
		out = append(out, stalledFix{file: file, line: pos.Line, replacement: replacement})
	}
	return out
}

// stalledReplacement returns the select that lets the wait end with the
// group context when the line is a plain channel receive statement and
// the enclosing function takes a context it can return on. Otherwise it
// reports false and the finding keeps its hint.
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
	if expr == "" || strings.ContainsAny(expr, " \t;") || strings.Contains(expr, "//") {
		return "", false
	}
	ctx, exit, ok := enclosingChild(lines, line)
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
	b.WriteString("\t")
	b.WriteString(exit)
	b.WriteString("\n")
	b.WriteString(indent)
	b.WriteString("}")
	return b.String(), true
}

// enclosingChild returns the name of the context.Context parameter of
// the function that holds the 1-based line and the statement that ends
// the child on cancellation: "return" for a function without results
// and "return ctx.Err()" for one whose only result is an error. Any
// other shape — a blank or absent context, a context inside a nested
// function type, or results that need values the fix cannot name — gets
// no fix. Only a single-line func header counts; it stops at the
// nearest header above the line, including a function literal, so a
// context from an outer function is never taken.
// headerEncloses reports whether the function header on lines[idx]
// opens a body that still holds the wait on 1-based line: braces on
// the lines between the header and the wait stay open. A literal that
// already closed is a sibling statement, not the enclosing function.
func headerEncloses(lines []string, idx, line int) bool {
	depth := 0
	var inBlock, inRaw, inStr, inRune bool
	for i := idx; i < line-1; i++ {
		s := lines[i]
		for j := 0; j < len(s); j++ {
			c := s[j]
			switch {
			case inBlock:
				if c == '*' && j+1 < len(s) && s[j+1] == '/' {
					inBlock = false
					j++
				}
			case inRaw:
				if c == '`' {
					inRaw = false
				}
			case inStr:
				if c == '\\' {
					j++
				} else if c == '"' {
					inStr = false
				}
			case inRune:
				if c == '\\' {
					j++
				} else if c == '\'' {
					inRune = false
				}
			case c == '/' && j+1 < len(s) && s[j+1] == '/':
				j = len(s)
			case c == '/' && j+1 < len(s) && s[j+1] == '*':
				inBlock = true
				j++
			case c == '`':
				inRaw = true
			case c == '"':
				inStr = true
			case c == '\'':
				inRune = true
			case c == '{':
				depth++
			case c == '}':
				depth--
			}
		}
	}
	return depth > 0
}

func enclosingChild(lines []string, line int) (ctx, exit string, ok bool) {
	for idx := line - 2; idx >= 0 && idx >= line-22; idx-- {
		header := lines[idx]
		if !strings.Contains(header, "func ") && !strings.Contains(header, "func(") {
			continue
		}
		// Only a header whose body still encloses the wait counts: a
		// deferred or assigned literal that closed above the line is a
		// sibling, not the enclosing child.
		if !headerEncloses(lines, idx, line) {
			continue
		}
		ctx, exit, ok = childContext(header)
		return ctx, exit, ok
	}
	return "", "", false
}

// childContext reads one func header line. It returns the context
// parameter name and the cancellation exit, or ok false when no
// context.Context sits at the parameter list's depth.
func childContext(header string) (ctx, exit string, ok bool) {
	// The parameter's name ends before context.Context at paren depth 1;
	// a match deeper than that belongs to a nested function type.
	pos := -1
	depth := 0
	openAt := -1 // index of the "(" whose depth-1 contents hold pos
	depthOneOpen := -1
	for i := 0; i < len(header); i++ {
		switch header[i] {
		case '(':
			depth++
			if depth == 1 {
				depthOneOpen = i
			}
		case ')':
			depth--
		case ' ', '\t', ',':
		}
		if depth == 1 && strings.HasPrefix(header[i:], "context.Context") {
			pos = i
			openAt = depthOneOpen
			break
		}
	}
	if pos < 0 {
		return "", "", false
	}
	before := strings.TrimSpace(header[:pos])
	sep := strings.LastIndexAny(before, " \t(,")
	name := before
	if sep >= 0 {
		name = before[sep+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "_" {
		return "", "", false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c != '_' && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return "", "", false
		}
	}
	if name[0] >= '0' && name[0] <= '9' {
		return "", "", false
	}
	// The result clause follows the ")" that closes the parameter list.
	end := -1
	depth = 0
	for i := openAt; i < len(header); i++ {
		switch header[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return "", "", false
	}
	result := strings.TrimSpace(header[end+1:])
	if i := strings.Index(result, "//"); i >= 0 {
		result = strings.TrimSpace(result[:i])
	}
	result = strings.TrimSpace(strings.TrimSuffix(result, "{"))
	switch {
	case result == "":
		return name, "return", true
	case result == "error":
		return name, "return " + name + ".Err()", true
	}
	// A named error result like "(err error)" or "err error" also exits.
	inner := result
	if strings.HasPrefix(inner, "(") && strings.HasSuffix(inner, ")") {
		inner = strings.TrimSpace(inner[1 : len(inner)-1])
	}
	if !strings.ContainsAny(inner, ",") {
		// A single named error result looks like `err error`. Result
		// types that merely end with the word — func() error, chan
		// error, <-chan error — must not pass: the rewrite returns a
		// bare ctx.Err() that would not compile against them.
		fields := strings.Fields(inner)
		if len(fields) == 2 && fields[1] == "error" && token.IsIdentifier(fields[0]) && !token.IsKeyword(fields[0]) {
			return name, "return " + name + ".Err()", true
		}
	}
	return "", "", false
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
		if err := writeFixedFile(cur, []byte(strings.Join(lines, "\n"))); err != nil {
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

// writeFixedFile stages the new contents next to the file and renames
// over it, so a failed write cannot leave the source truncated.
func writeFixedFile(file string, data []byte) error {
	// Rename over the resolved target, not the link itself: renaming
	// onto a symlink path would replace the link with a regular file
	// and leave the shared source unchanged.
	if target, err := filepath.EvalSymlinks(file); err == nil {
		file = target
	}
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".logosconc-fix-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			os.Remove(name)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(info.Mode()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, file); err != nil {
		return err
	}
	keep = true
	return nil
}

// printStalledBrief prints one JSON object per fix to standard output
// without changing files. Under -json-diagnostics it writes each brief
// inside a test2json output record so the stream keeps one schema.
func printStalledBrief(fixes []stalledFix, asJSON bool) {
	for _, f := range fixes {
		rec, err := json.Marshal(struct {
			File string `json:"file"`
			Line int    `json:"line"`
			Edit string `json:"edit"`
		}{File: f.file, Line: f.line, Edit: f.replacement})
		if err != nil {
			continue
		}
		if asJSON {
			event, err := json.Marshal(struct {
				Action string `json:"Action"`
				Output string `json:"Output"`
			}{Action: "output", Output: string(rec) + "\n"})
			if err != nil {
				continue
			}
			fmt.Fprintf(os.Stdout, "%s\n", event)
			continue
		}
		fmt.Fprintf(os.Stdout, "%s\n", rec)
	}
}

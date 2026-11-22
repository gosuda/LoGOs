// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conccmd

import (
	"encoding/json"
	"fmt"
	"go/scanner"
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
	return receiveReplacement(data, line)
}

// receiveReplacement is stalledReplacement over the bytes of the file,
// so a write can prove the edit again on the bytes it replaces.
func receiveReplacement(data []byte, line int) (string, bool) {
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
	ctx, exit, header, ok := enclosingChild(lines, line)
	if !ok || !receiveStatement(data, header, line, ctx) {
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

// enclosingChild returns the name of the context.Context parameter of
// the function that holds the 1-based line, the statement that ends
// the child on cancellation, and the 1-based line of the func header.
// The statement is "return" for a function without results and
// "return ctx.Err()" for one whose only result is an error. A blank or
// absent context, a context inside a nested function type, or results
// that need values the fix cannot name get no fix. Only a single-line
// func header counts. The search stops at the nearest header above the
// line, including a function literal, so a context from an outer
// function is never taken.
func enclosingChild(lines []string, line int) (ctx, exit string, header int, ok bool) {
	for idx := line - 2; idx >= 0 && idx >= line-22; idx-- {
		text := lines[idx]
		if !strings.Contains(text, "func ") && !strings.Contains(text, "func(") {
			continue
		}
		// Only a header whose body still encloses the wait counts: a
		// deferred or assigned literal that closed above the line is a
		// sibling, not the enclosing child.
		if !headerEncloses(lines, idx, line) {
			continue
		}
		ctx, exit, ok = childContext(text)
		return ctx, exit, idx + 1, ok
	}
	return "", "", 0, false
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

// lineToken is one token of go/scanner and its 1-based line.
type lineToken struct {
	tok  token.Token
	lit  string
	line int
}

// receiveStatement proves with go/scanner tokens what the text checks
// assume, so a comment or a literal cannot fake a token: the header on
// 1-based line header is a func that declares ctx as context.Context,
// the receive starts a statement on line and its operand ends the line,
// and no statement between the two lines binds ctx again.
func receiveStatement(src []byte, header, line int, ctx string) bool {
	toks := scanLines(src, header, line)
	w := slices.IndexFunc(toks, func(t lineToken) bool { return t.line == line })
	if w < 1 || toks[w].tok != token.ARROW || !statementEnds(toks[:w]) {
		return false
	}
	return declaresContext(toks, header, ctx) && operandEndsLine(toks[w+1:], line) && !rebinds(toks[:w], ctx)
}

// scanLines returns the tokens of src on the 1-based lines first through
// last, with the semicolons go/scanner inserts at line ends.
func scanLines(src []byte, first, last int) []lineToken {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, src, nil, 0)
	var toks []lineToken
	for {
		pos, tok, lit := s.Scan()
		if tok == token.EOF {
			return toks
		}
		line := file.Line(pos)
		if line > last {
			return toks
		}
		if line >= first {
			toks = append(toks, lineToken{tok: tok, lit: lit, line: line})
		}
	}
}

// statementEnds reports whether the last of toks ends a statement or
// opens a block or a case, so the next token starts a statement. The
// ?? operator of loGo/s scans as two illegal tokens, and go/scanner
// inserts a semicolon after it although the expression goes on.
func statementEnds(toks []lineToken) bool {
	n := len(toks)
	switch toks[n-1].tok {
	case token.LBRACE, token.COLON:
		return true
	case token.SEMICOLON:
		return n < 3 || toks[n-2].tok != token.ILLEGAL || toks[n-3].tok != token.ILLEGAL
	}
	return false
}

// declaresContext reports whether the header line holds a func token
// and, after it, ctx followed by context.Context.
func declaresContext(toks []lineToken, header int, ctx string) bool {
	sawFunc := false
	for i := 0; i+3 < len(toks) && toks[i].line == header; i++ {
		switch {
		case toks[i].tok == token.FUNC:
			sawFunc = true
		case sawFunc && toks[i].tok == token.IDENT && toks[i].lit == ctx &&
			toks[i+1].lit == "context" && toks[i+2].tok == token.PERIOD && toks[i+3].lit == "Context":
			return true
		}
	}
	return false
}

// operandEndsLine reports whether toks start with the operand of the
// receive and the line ends right after it: names, selectors, calls and
// index expressions on the one line, then the semicolon go/scanner
// inserts at the line end.
func operandEndsLine(toks []lineToken, line int) bool {
	depth := 0
	for i, t := range toks {
		if t.line != line {
			return false
		}
		switch t.tok {
		case token.LPAREN, token.LBRACK:
			depth++
		case token.RPAREN, token.RBRACK:
			depth--
			if depth < 0 {
				return false
			}
		case token.IDENT, token.PERIOD, token.INT, token.STRING, token.ARROW:
		case token.COMMA:
			if depth == 0 {
				return false
			}
		case token.SEMICOLON:
			return i > 0 && depth == 0 && t.lit == "\n"
		default:
			return false
		}
	}
	return false
}

// rebinds reports whether a var or const declaration, an assignment, or
// a short variable declaration in toks gives name a new value.
func rebinds(toks []lineToken, name string) bool {
	depth, declDepth := 0, -1
	for i, t := range toks {
		switch t.tok {
		case token.LPAREN, token.LBRACK, token.LBRACE:
			depth++
		case token.RPAREN, token.RBRACK, token.RBRACE:
			depth--
			if depth < declDepth {
				declDepth = -1
			}
		case token.VAR, token.CONST:
			declDepth = depth
		case token.SEMICOLON:
			if depth == declDepth {
				declDepth = -1
			}
		case token.IDENT:
			if t.lit == name && (declDepth >= 0 || assigned(toks, i)) {
				return true
			}
		}
	}
	return false
}

// assigned reports whether the name at toks[i] is in the name list on
// the left of = or := in a statement, an if, for or switch header, or a
// select case. A list after "(" is a parameter list, as in
// go group (ctx, limit = 8), and binds nothing.
func assigned(toks []lineToken, i int) bool {
	start := i
	for start >= 2 && toks[start-1].tok == token.COMMA && toks[start-2].tok == token.IDENT {
		start -= 2
	}
	end := i + 1
	for end+1 < len(toks) && toks[end].tok == token.COMMA && toks[end+1].tok == token.IDENT {
		end += 2
	}
	if end >= len(toks) {
		return false
	}
	if t := toks[end].tok; t != token.DEFINE && t != token.ASSIGN {
		return false
	}
	if start == 0 {
		return true
	}
	switch toks[start-1].tok {
	case token.SEMICOLON, token.LBRACE, token.COLON, token.FOR, token.IF, token.SWITCH, token.CASE:
		return true
	}
	return false
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

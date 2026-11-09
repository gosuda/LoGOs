// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lsp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"sync"

	"cmd/go/internal/cfg"
)

// compiledArgs are the arguments of the one logos list run that lowers a
// package and its closure: -compiled builds the export data, -json prints
// the packages with the logos fields, the overlay carries the unsaved
// buffers, and --json-diagnostics turns the compile diagnostics of the
// build into JSON lines on stderr.
var compiledArgs = []string{"list", "-e", "-deps", "-export", "-compiled", "-json", "--json-diagnostics"}

// compiledPkg is one package of a logos list -compiled -json run.
type compiledPkg struct {
	Dir        string
	Standard   bool
	GoFiles    []string
	LogosFiles []compiledFile
	LogosHints json.RawMessage
	LogosDiags []json.RawMessage
}

// compiledFile is the lowering of one loGo/s source file.
type compiledFile struct {
	File         string
	LogosLowered string
	LogosEditMap json.RawMessage
}

// listRun is the result of one logos list -compiled run over a version
// set: the lowering of every package of the closure, the diagnostics of
// the build by package directory, and the hints JSON by package directory.
type listRun struct {
	key   string
	pkgs  []compiledPkg
	diags map[string][]Diagnostic
	hints map[string]json.RawMessage
}

// toolchain is the production Compiler. It runs the logos command, the
// executable of the process itself, as a subprocess, so a fatal error of
// the go command never takes the proxy down. One logos list -compiled
// -json -overlay=<file> run lowers every loGo/s file of the closure, and
// returns the lowered texts, the edit maps, the hints JSON, and the
// compiler diagnostics of the build. Unsaved buffers reach the run through
// its -overlay flag. The result of a run is kept for its version set: a
// Lower, a Diagnose, and a Hints call on the same inputs share one run.
type toolchain struct {
	exe string

	mu   sync.Mutex
	last *listRun
}

// newToolchain returns a toolchain that runs exe as logos.
func newToolchain(exe string) *toolchain {
	return &toolchain{exe: exe}
}

// overlay is the -overlay file of one run, with the backing copies of the
// buffers it replaces.
type overlay struct {
	json string
	back map[string]string
}

// newOverlay writes the buffers to backing files and the overlay JSON
// that maps each source path to its backing file. A backing file keeps the
// base name of its source, so the compiler still sees a loGo/s file and
// names its output after the source; each lives in a directory of its own
// so equal base names cannot collide.
func newOverlay(buffers map[string][]byte) (*overlay, error) {
	root, err := os.MkdirTemp("", "logoslsp")
	if err != nil {
		return nil, err
	}
	ov := &overlay{json: filepath.Join(root, "overlay.json"), back: make(map[string]string, len(buffers))}
	replace := make(map[string]string, len(buffers))
	for i, path := range slices.Sorted(maps.Keys(buffers)) {
		dir := filepath.Join(root, strconv.Itoa(i))
		if err := os.MkdirAll(dir, 0o777); err != nil {
			os.RemoveAll(root)
			return nil, err
		}
		backing := filepath.Join(dir, filepath.Base(path))
		if err := os.WriteFile(backing, buffers[path], 0o666); err != nil {
			os.RemoveAll(root)
			return nil, err
		}
		replace[path] = backing
		ov.back[backing] = path
	}
	data, err := json.Marshal(map[string]map[string]string{"Replace": replace})
	if err != nil {
		os.RemoveAll(root)
		return nil, err
	}
	if err := os.WriteFile(ov.json, data, 0o666); err != nil {
		os.RemoveAll(root)
		return nil, err
	}
	return ov, nil
}

// release removes the overlay files.
func (ov *overlay) release() error {
	return os.RemoveAll(filepath.Dir(ov.json))
}

// source returns the source path a compiler path refers to: the path of
// the buffer when file is one of the backing files, else file itself.
func (ov *overlay) source(file string) string {
	if path, ok := ov.back[file]; ok {
		return path
	}
	return file
}

// run executes the logos command with args in dir and returns what it
// wrote to stdout and stderr. The command runs as "logos" whatever the
// executable is named, so it keeps logos mode. A command that ran and
// failed is not an error here: its diagnostics are the answer.
func (t *toolchain) run(ctx context.Context, dir string, args ...string) (stdout, stderr []byte, err error) {
	cmd := exec.CommandContext(ctx, t.exe, args...)
	cmd.Args[0] = "logos"
	cmd.Dir = dir
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err = cmd.Run()
	if _, ok := errors.AsType[*exec.ExitError](err); ok && ctx.Err() == nil {
		err = nil
	}
	return so.Bytes(), se.Bytes(), err
}

// runKey identifies the inputs of one run: the package directory and the
// unsaved buffers.
func runKey(dir string, buffers map[string][]byte) string {
	h := sha256.New()
	h.Write([]byte(filepath.Clean(dir)))
	h.Write([]byte{0})
	for _, path := range slices.Sorted(maps.Keys(buffers)) {
		h.Write([]byte(path))
		h.Write([]byte{0})
		h.Write([]byte(fmt.Sprintf("%d:", len(buffers[path]))))
		h.Write(buffers[path])
	}
	return string(h.Sum(nil))
}

// runOnce performs the one logos list run for dir over buffers, or returns
// the result of the last run over the same inputs.
func (t *toolchain) runOnce(ctx context.Context, dir string, buffers map[string][]byte) (*listRun, error) {
	key := runKey(dir, buffers)
	t.mu.Lock()
	last := t.last
	t.mu.Unlock()
	if last != nil && last.key == key {
		return last, nil
	}
	ov, err := newOverlay(buffers)
	if err != nil {
		return nil, err
	}
	defer func() {
		if rerr := ov.release(); rerr != nil && err == nil {
			err = rerr
		}
	}()
	stdout, stderr, err := t.run(ctx, dir, append(compiledArgs, "-overlay="+ov.json, ".")...)
	if err != nil {
		return nil, err
	}
	var pkgs []compiledPkg
	dec := json.NewDecoder(bytes.NewReader(stdout))
	for dec.More() {
		var p compiledPkg
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("decoding logos list -compiled output: %v", err)
		}
		pkgs = append(pkgs, p)
	}
	last = &listRun{
		key:   key,
		pkgs:  pkgs,
		diags: parseCompiledDiags(stderr, ov, pkgs),
		hints: make(map[string]json.RawMessage, len(pkgs)),
	}
	for _, p := range pkgs {
		if len(p.LogosHints) > 0 {
			last.hints[filepath.Clean(p.Dir)] = p.LogosHints
		}
	}
	t.mu.Lock()
	t.last = last
	t.mu.Unlock()
	return last, nil
}

// parseCompiledDiags decodes the diagnostics of the run: the JSON lines it
// wrote to stderr, and the LogosDiags lines of each package, which carry
// the compile diagnostics of a package that failed to build. Each file is
// mapped back from its overlay backing copy to the source, and the
// diagnostics are grouped by package directory.
func parseCompiledDiags(stderr []byte, ov *overlay, pkgs []compiledPkg) map[string][]Diagnostic {
	diags := make(map[string][]Diagnostic)
	add := func(line []byte) {
		d, ok := parseDiagnostic(line)
		if !ok {
			return
		}
		d.File = ov.source(d.File)
		dir := filepath.Dir(d.File)
		if d.RuleID == "" && hasDiagnostic(diags[dir], d) {
			return
		}
		diags[dir] = append(diags[dir], d)
	}
	for line := range bytes.SplitSeq(stderr, []byte("\n")) {
		add(line)
	}
	for _, p := range pkgs {
		for _, line := range p.LogosDiags {
			add(line)
		}
	}
	return diags
}

// hasDiagnostic reports whether out already holds the rule-less diagnostic
// d: same mapped file, span, severity, and message, and no rule on either
// side. Every lint and helper finding carries a rule, so only the native
// type errors both compile paths emit can match here.
func hasDiagnostic(out []Diagnostic, d Diagnostic) bool {
	if d.RuleID != "" {
		return false
	}
	return slices.ContainsFunc(out, func(e Diagnostic) bool {
		return e.RuleID == "" && e.File == d.File && e.Span == d.Span && e.Severity == d.Severity && e.Message == d.Message
	})
}

// Lower implements Compiler. It takes the lowering of dir and its closure
// from the one logos list run: every loGo/s file of the closure with a
// lowered text and an edit map is a file of the result, one whose package
// failed to compile is missing.
func (t *toolchain) Lower(ctx context.Context, dir string, buffers map[string][]byte) (*Lowering, error) {
	last, err := t.runOnce(ctx, dir, buffers)
	if err != nil {
		return nil, err
	}
	low := &Lowering{}
	for _, p := range last.pkgs {
		if p.Standard {
			continue
		}
		low.Dirs = append(low.Dirs, filepath.Clean(p.Dir))
		names := make(map[string]compiledFile, len(p.LogosFiles))
		for _, f := range p.LogosFiles {
			names[f.File] = f
		}
		for _, name := range p.GoFiles {
			if !cfg.IsLogosFile(name) {
				continue
			}
			path := filepath.Join(p.Dir, name)
			f, ok := names[name]
			if !ok {
				low.Missing = append(low.Missing, path)
				continue
			}
			file, err := compiledLoweredFile(path, f, buffers)
			if err != nil {
				low.Missing = append(low.Missing, path)
				continue
			}
			low.Files = append(low.Files, file)
		}
	}
	return low, nil
}

// compiledLoweredFile assembles the LoweredFile of one compiled file. The
// source text is the buffer when the document is open, else the file on
// disk.
func compiledLoweredFile(path string, f compiledFile, buffers map[string][]byte) (LoweredFile, error) {
	var file LoweredFile
	var err error
	file.Out = []byte(f.LogosLowered)
	if err := json.Unmarshal(f.LogosEditMap, &file.Map); err != nil {
		return LoweredFile{}, err
	}
	file.Src, err = sourceText(path, buffers)
	if err != nil {
		return LoweredFile{}, err
	}
	file.Path = path
	return file, nil
}

// sourceText returns the text of the source at path: the buffer when the
// document is open, else the file on disk.
func sourceText(path string, buffers map[string][]byte) ([]byte, error) {
	if data, ok := buffers[path]; ok {
		return data, nil
	}
	return os.ReadFile(path)
}

// Diagnose implements Compiler. The diagnostics come from the same logos
// list run that lowered the package: --json-diagnostics turned them into
// JSON lines on stderr.
func (t *toolchain) Diagnose(ctx context.Context, dir string, buffers map[string][]byte) ([]Diagnostic, error) {
	last, err := t.runOnce(ctx, dir, buffers)
	if err != nil {
		return nil, err
	}
	return last.diags[filepath.Clean(dir)], nil
}

// Hints returns the hints JSON of the package in dir, from the same logos
// list run that lowered it: -compiled computed the hints of every package
// that a client of the closure needs.
func (t *toolchain) Hints(ctx context.Context, dir string, buffers map[string][]byte) (json.RawMessage, error) {
	last, err := t.runOnce(ctx, dir, buffers)
	if err != nil {
		return nil, err
	}
	return last.hints[filepath.Clean(dir)], nil
}

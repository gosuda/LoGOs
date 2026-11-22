// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix

package conccmd

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"cmd/go/internal/base"
	"cmd/go/internal/cfg"
	"cmd/go/internal/modload"
)

// monitor runs `logos <verb>` with the monitor tag and relays the
// findings of the program from a named pipe to standard error while the
// program runs.
func monitor(verb string, inv invocation) {
	self, err := os.Executable()
	if err != nil {
		base.Fatalf("logos conc %s: %v", verb, err)
	}
	dir, err := os.MkdirTemp("", "logosconc")
	if err != nil {
		base.Fatalf("logos conc %s: %v", verb, err)
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	base.AtExit(func() { os.RemoveAll(dir) })

	// The command holds the pipe open for reading and writing. A program
	// can then open it for writing at any time without blocking, and the
	// command can end the stream itself after the program has exited.
	path := filepath.Join(dir, "findings")
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		base.Fatalf("logos conc %s: %v", verb, err)
	}
	pipe, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		base.Fatalf("logos conc %s: %v", verb, err)
	}
	defer pipe.Close()

	stream := io.Reader(pipe)
	var raw strings.Builder
	if inv.fix || inv.aiPatch {
		stream = io.TeeReader(pipe, &raw)
	}
	findings := make(chan int, 1)
	go func() { findings <- relay(stream, os.Stderr, inv.asJSON) }()

	child := exec.Command(self, inv.arguments...)
	child.Args[0] = os.Args[0]
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	scanner := &exitScanWriter{}
	var stderrR, stderrW *os.File
	copyDone := make(chan struct{})
	if verb == "run" {
		// `logos run` reports the program's status as a line on
		// standard error and itself exits 1; watch for the line so
		// the command can forward the program's own status. The child
		// gets a real pipe, not a copy goroutine: a background helper
		// that inherits the writer keeps it open forever, and exec.Wait
		// would hang on the goroutine draining it.
		var err error
		stderrR, stderrW, err = os.Pipe()
		if err != nil {
			base.Fatalf("logos conc %s: %v", verb, err)
		}
		defer stderrR.Close()
		child.Stderr = stderrW
		go func() {
			io.Copy(scanner, stderrR)
			close(copyDone)
		}()
	} else {
		child.Stderr = os.Stderr
	}
	child.Env = append(slices.Clip(cfg.OrigEnv), sinkEnv+"="+path)
	if verb == "run" {
		child.Env = append(child.Env, base.ConcRunExitEnv+"=1")
	}
	for name, value := range inv.durations {
		child.Env = append(child.Env, durationFlags[name]+"="+value)
	}
	if _, ok := inv.durations["stall"]; !ok {
		child.Env = append(child.Env, durationFlags["stall"]+"=5s")
	}
	if _, ok := inv.durations["hold"]; !ok {
		child.Env = append(child.Env, durationFlags["hold"]+"=100ms")
	}
	// Always set the shake seed, even when empty: the child inherits
	// OrigEnv, and an inherited nonzero value would shake the run without
	// printing a seed. An empty value turns shaking off, matching the
	// omitted -shake default. Later entries win over inherited ones.
	child.Env = append(child.Env, shakeEnv+"="+inv.shake)
	base.StartSigHandlers()
	started := time.Now()
	runErr := child.Run()
	if stderrW != nil {
		// The run is over and the exit-status line is already in the
		// pipe, but inherited writers keep it open — drain only what
		// is buffered, never wait for an EOF that may never come.
		stderrW.Close()
		// An expired deadline wakes the background copy but also
		// prevents it from reading the buffered tail, so retire it
		// first, then drain under a short grace deadline: buffered
		// bytes return at once and the wait stays bounded.
		stderrR.SetReadDeadline(time.Now())
		<-copyDone
		stderrR.SetReadDeadline(time.Now().Add(3 * time.Second))
		io.Copy(scanner, stderrR)
	}

	if _, err := pipe.WriteString(endOfFindings); err != nil {
		base.Fatalf("logos conc %s: %v", verb, err)
	}
	found := <-findings
	if inv.fix || inv.aiPatch {
		var fixes []stalledFix
		// A declared -overlay that cannot be read hides which file the
		// compiler really compiled, so no fix is offered at all.
		// The monitored build also resolves -overlay from GOFLAGS, so
		// seed the flag scan with it; a command-line -overlay still
		// wins because overlayFiles keeps the last flag it sees.
		args := inv.arguments
		if name, ok := goflagsOverlay(inv.goflags); ok {
			args = append([]string{"-overlay=" + name}, args...)
		}
		if overlay, ok := overlayFiles(args); ok {
			fixes = collectStalledFixes(raw.String(), overlay)
		}
		if inv.aiPatch {
			printStalledBrief(fixes, inv.asJSON)
		} else {
			for _, done := range applyStalledFixes(fixes, started) {
				fmt.Fprintf(os.Stderr, "logos conc: fixed %s\n", done)
			}
		}
	}

	var exit *exec.ExitError
	switch {
	case errors.As(runErr, &exit):
		status := exit.ExitCode()
		if status <= 0 {
			status = 1
		}
		// The wrapper exits 1 for a failed program; the line it
		// prints names the program's own status.
		if status == 1 {
			if code := scanner.status(); code > 0 {
				status = code
			}
		}
		base.SetExitStatus(status)
	case runErr != nil:
		base.Fatalf("logos conc %s: %v", verb, runErr)
	case found > 0:
		base.SetExitStatus(1)
	}
}

// exitLine matches the line `logos run` prints when the program it ran
// exits nonzero.
var exitLine = regexp.MustCompile(`exit status ([0-9]+)$`)

// exitScanWriter passes the child's standard error through to standard
// error while it watches for the "exit status N" line `logos run`
// prints. The wrapper's own status is 1, so the last such line is the
// only way to recover the program's status.
type exitScanWriter struct {
	mu   sync.Mutex
	tail []byte
	code int
}

func (s *exitScanWriter) Write(p []byte) (int, error) {
	n, err := os.Stderr.Write(p)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tail = append(s.tail, p...)
	for {
		i := bytes.IndexByte(s.tail, '\n')
		if i < 0 {
			break
		}
		line := s.tail[:i]
		s.tail = s.tail[i+1:]
		if m := exitLine.FindSubmatchIndex(line); m != nil &&
			(m[0] == 0 || line[m[0]-1] != ':') {
			// A build diagnostic such as "error obtaining VCS status:
			// exit status 128" ends with the same phrase; the colon
			// keeps it from passing for the wrapper's own report.
			s.code, _ = strconv.Atoi(string(line[m[2]:m[3]]))
		}
	}
	if len(s.tail) > 4096 {
		s.tail = s.tail[len(s.tail)-4096:]
	}
	return n, err
}

func (s *exitScanWriter) status() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.code
}

// applyStalledFixes writes the edits of each file in one rename, latest
// line first so earlier lines keep their numbers. A file reached through
// two names gets one edit per line. A file that fixScope.fixFile does
// not admit keeps its bytes, and its findings keep their hints. It
// returns one "file:line" per edit written.
func applyStalledFixes(fixes []stalledFix, started time.Time) []string {
	byFile := map[string][]stalledFix{}
	for _, f := range fixes {
		resolved, err := filepath.EvalSymlinks(f.file)
		if err != nil {
			fmt.Fprintf(os.Stderr, "logos conc: cannot fix %s: %v\n", f.file, err)
			continue
		}
		byFile[resolved] = append(byFile[resolved], f)
	}
	scope, scopeErr := newFixScope()
	var applied []string
	for _, resolved := range slices.Sorted(maps.Keys(byFile)) {
		edits := distinctLines(byFile[resolved])
		err := scopeErr
		if err == nil {
			err = scope.fixFile(resolved, edits, started)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "logos conc: cannot fix %s: %v\n", edits[0].file, err)
			continue
		}
		for _, f := range edits {
			applied = append(applied, fmt.Sprintf("%s:%d", f.file, f.line))
		}
	}
	return applied
}

// distinctLines orders the edits of one file latest line first and keeps
// one edit per line.
func distinctLines(edits []stalledFix) []stalledFix {
	slices.SortFunc(edits, func(a, b stalledFix) int {
		if a.line != b.line {
			return b.line - a.line
		}
		return strings.Compare(a.file, b.file)
	})
	return slices.CompactFunc(edits, func(a, b stalledFix) bool { return a.line == b.line })
}

// fixScope holds the resolved directories that decide where a fix may
// write: inside the main module, outside GOROOT and the module cache.
type fixScope struct {
	module, goroot, modcache string
}

func newFixScope() (fixScope, error) {
	gomod := modload.FindGoMod(base.Cwd())
	if gomod == "" {
		return fixScope{}, errors.New("no main module")
	}
	module, err := filepath.EvalSymlinks(filepath.Dir(gomod))
	if err != nil {
		return fixScope{}, err
	}
	return fixScope{module: module, goroot: resolvedDir(cfg.GOROOT), modcache: resolvedDir(cfg.GOMODCACHE)}, nil
}

func resolvedDir(dir string) string {
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		return resolved
	}
	return dir
}

func within(dir, file string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(dir, file)
	return err == nil && filepath.IsLocal(rel)
}

// fixFile writes the edits of the resolved file. It admits only a
// regular .lgo file of the main module, outside GOROOT and the module
// cache, with one link, not generated, and not changed since the run
// started, and it proves each edit again on the bytes it replaces.
func (s fixScope) fixFile(resolved string, edits []stalledFix, started time.Time) error {
	switch {
	case !within(s.module, resolved):
		return fmt.Errorf("outside the main module %s", s.module)
	case within(s.goroot, resolved):
		return errors.New("inside GOROOT")
	case within(s.modcache, resolved):
		return errors.New("inside the module cache")
	case !strings.HasSuffix(resolved, ".lgo"):
		return errors.New("not a .lgo file")
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		return err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	switch {
	case !info.Mode().IsRegular():
		return errors.New("not a regular file")
	case !ok:
		return errors.New("unknown owner")
	case owner.Nlink > 1:
		return fmt.Errorf("the file has %d hard links", owner.Nlink)
	case !info.ModTime().Before(started):
		return errors.New("changed during the run")
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return err
	}
	text := string(data)
	if generated(text) {
		return errors.New("generated file")
	}
	for _, f := range edits {
		if got, ok := receiveReplacement(data, f.line); !ok || got != f.replacement {
			return fmt.Errorf("line %d no longer holds the receive the run reported", f.line)
		}
	}
	lines := strings.Split(text, "\n")
	for _, f := range edits {
		lines[f.line-1] = f.replacement
	}
	return writeFixedFile(resolved, data, []byte(strings.Join(lines, "\n")), info)
}

// generated reports whether a "// Code generated ... DO NOT EDIT." line
// comes before the package clause.
func generated(text string) bool {
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.HasPrefix(line, "package ") {
			return false
		}
		if strings.HasPrefix(line, "// Code generated ") && strings.HasSuffix(line, " DO NOT EDIT.") {
			return true
		}
	}
	return false
}

// writeFixedFile stages data next to file and renames it over file only
// while file still holds before, the bytes the edits were proved on, so
// a failure at any step leaves file whole.
func writeFixedFile(file string, before, data []byte, info os.FileInfo) error {
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
	err = stageFixedFile(tmp, data, info)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	current, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if !bytes.Equal(current, before) {
		return errors.New("changed while the fix was staged")
	}
	if err := os.Rename(name, file); err != nil {
		return err
	}
	keep = true
	return nil
}

// stageFixedFile writes data to tmp, gives tmp the owner and then the
// mode of the file it replaces, since a change of owner clears the
// set-user-ID bit, and syncs tmp to disk.
func stageFixedFile(tmp *os.File, data []byte, info os.FileInfo) error {
	want, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("unknown owner")
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	staged, err := tmp.Stat()
	if err != nil {
		return err
	}
	if got, ok := staged.Sys().(*syscall.Stat_t); !ok || got.Uid != want.Uid || got.Gid != want.Gid {
		if err := tmp.Chown(int(want.Uid), int(want.Gid)); err != nil {
			return err
		}
	}
	if err := tmp.Chmod(info.Mode()); err != nil {
		return err
	}
	return tmp.Sync()
}

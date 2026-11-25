// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package proc runs child processes with structured termination.
//
// Ported ideas from Zig standard library process types at revision
// c7826aa7a096270e4de8a1c7b8b4343650950f30 (MIT licence):
// lib/std/process.zig SpawnOptions and StdIo, and
// lib/std/process/Child.zig Term, Cwd, kill idempotence,
// and the wait cleans resources contract. The code below is a
// from-scratch Go implementation over os.StartProcess, not a copy.
package proc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// StdIOKind names which stream a child standard descriptor gets.
type StdIOKind int

// Standard descriptor kinds, matching Zig StdIo members.
const (
	StdIOInherit StdIOKind = iota
	StdIOFile
	StdIONull
	StdIOPipe
	StdIOClose
)

// StdIO describes one child standard descriptor.
type StdIO struct {
	Kind StdIOKind
	File *os.File
}

// InheritStdIO connects the child descriptor to the parent descriptor.
func InheritStdIO() StdIO {
	return StdIO{Kind: StdIOInherit}
}

// FileStdIO connects the child descriptor directly to f.
func FileStdIO(f *os.File) StdIO {
	return StdIO{Kind: StdIOFile, File: f}
}

// NullStdIO connects the child descriptor to the null device.
func NullStdIO() StdIO {
	return StdIO{Kind: StdIONull}
}

// PipeStdIO creates a pipe for the descriptor.
func PipeStdIO() StdIO {
	return StdIO{Kind: StdIOPipe}
}

// CloseStdIO starts the child with the descriptor closed.
func CloseStdIO() StdIO {
	return StdIO{Kind: StdIOClose}
}

// Cwd describes the child working directory.
type Cwd struct {
	Path    string
	HasPath bool
}

// InheritCwd keeps the parent working directory.
func InheritCwd() Cwd {
	return Cwd{}
}

// PathCwd starts the child in path.
func PathCwd(path string) Cwd {
	return Cwd{Path: path, HasPath: true}
}

// Options holds spawn configuration.
type Options struct {
	Cwd             Cwd
	Env             []string
	Stdin           StdIO
	Stdout          StdIO
	Stderr          StdIO
	NewProcessGroup bool
}

// TermKind names how a child terminated.
type TermKind int

// Termination kinds, matching Zig Term members.
const (
	TermExited TermKind = iota
	TermSignaled
	TermStopped
	TermUnknown
)

// Term is how a child terminated.
type Term struct {
	Kind   TermKind
	Code   int
	Signal os.Signal
}

// IsSuccess reports whether the child exited with code zero.
func (t Term) IsSuccess() bool {
	return t.Kind == TermExited && t.Code == 0
}

// String formats the termination the way Zig Term does.
func (t Term) String() string {
	switch t.Kind {
	case TermExited:
		return fmt.Sprintf("exited with code %d", t.Code)
	case TermSignaled:
		return fmt.Sprintf("terminated with signal %s", t.Signal)
	case TermStopped:
		return fmt.Sprintf("stopped with signal %s", t.Signal)
	default:
		return "terminated unexpectedly"
	}
}

// Match calls the handler for the termination kind.
// Every handler must be non-nil, so a missing arm fails at the call site.
func (t Term) Match(exited func(int), signaled func(os.Signal), stopped func(os.Signal), unknown func(int)) {
	if exited == nil || signaled == nil || stopped == nil || unknown == nil {
		panic("proc: Match needs four non-nil handlers")
	}
	switch t.Kind {
	case TermExited:
		exited(t.Code)
	case TermSignaled:
		signaled(t.Signal)
	case TermStopped:
		stopped(t.Signal)
	default:
		unknown(t.Code)
	}
}

// Child is a spawned process.
type Child struct {
	mu          sync.Mutex
	proc        *os.Process
	pid         int
	newGroup    bool
	waited      bool
	waiting     bool
	killed      bool
	groupKilled bool
	term        Term
	waitErr     error
	stdinW      *os.File
	stdoutR     *os.File
	stderrR     *os.File
}

// Spawn starts path with args. Parent pipe ends stay open on the
// handle: drain them before Wait, then release them with Close.
// Inherited and caller-supplied files are never closed.
func Spawn(path string, args []string, opts Options) (*Child, error) {
	resolved, err := resolvePath(path, opts.Cwd)
	if err != nil {
		return nil, err
	}
	stdinChild, stdinCaller, stdinOwned, err := openStdIO(opts.Stdin, os.Stdin, true)
	if err != nil {
		return nil, err
	}
	stdoutChild, stdoutCaller, stdoutOwned, err := openStdIO(opts.Stdout, os.Stdout, false)
	if err != nil {
		closeOwned(stdinChild, stdinCaller, stdinOwned)
		return nil, err
	}
	stderrChild, stderrCaller, stderrOwned, err := openStdIO(opts.Stderr, os.Stderr, false)
	if err != nil {
		closeOwned(stdinChild, stdinCaller, stdinOwned)
		closeOwned(stdoutChild, stdoutCaller, stdoutOwned)
		return nil, err
	}
	attr := &os.ProcAttr{
		Dir:   cwdDir(opts.Cwd),
		Env:   opts.Env,
		Files: []*os.File{stdinChild, stdoutChild, stderrChild},
		Sys:   sysProcAttr(opts.NewProcessGroup),
	}
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, resolved)
	argv = append(argv, args...)
	p, err := os.StartProcess(resolved, argv, attr)
	if err != nil {
		closeOwned(stdinChild, stdinCaller, stdinOwned)
		closeOwned(stdoutChild, stdoutCaller, stdoutOwned)
		closeOwned(stderrChild, stderrCaller, stderrOwned)
		return nil, fmt.Errorf("proc: spawn %s: %w", path, err)
	}
	closeChildEnd(stdinChild, stdinOwned)
	closeChildEnd(stdoutChild, stdoutOwned)
	closeChildEnd(stderrChild, stderrOwned)
	return &Child{
		proc:     p,
		pid:      p.Pid,
		newGroup: opts.NewProcessGroup,
		stdinW:   stdinCaller,
		stdoutR:  stdoutCaller,
		stderrR:  stderrCaller,
	}, nil
}

// Pid returns the child process id.
func (c *Child) Pid() int {
	return c.pid
}

// StdinPipe returns the parent write end when stdin used PipeStdIO.
// Close releases it.
func (c *Child) StdinPipe() *os.File {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stdinW
}

// StdoutPipe returns the parent read end when stdout used PipeStdIO.
// Close releases it.
func (c *Child) StdoutPipe() *os.File {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stdoutR
}

// StderrPipe returns the parent read end when stderr used PipeStdIO.
// Close releases it.
func (c *Child) StderrPipe() *os.File {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stderrR
}

// Close releases the parent ends of pipes opened for the child. It is
// idempotent and reports nil on a handle with no pipes. Wait never
// closes pipes, so a handle that outlives its output must be closed.
func (c *Child) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var errs []error
	for _, f := range []*os.File{c.stdinW, c.stdoutR, c.stderrR} {
		if f == nil {
			continue
		}
		if err := f.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	c.stdinW, c.stdoutR, c.stderrR = nil, nil, nil
	return errors.Join(errs...)
}

// Kill terminates the child. It is idempotent.
func (c *Child) Kill() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.waited || c.killed {
		return nil
	}
	c.killed = true
	if c.proc == nil {
		return nil
	}
	if err := c.proc.Kill(); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("proc: kill: %w", err)
	}
	return nil
}

// KillGroup terminates the child process group. It is idempotent.
// Without NewProcessGroup it kills the single child. A leader-only
// Kill never suppresses the group signal: group termination subsumes a
// leader kill, so KillGroup signals the group unless the group itself
// was already signalled or the child was reaped.
func (c *Child) KillGroup() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.waited || c.groupKilled {
		return nil
	}
	c.killed = true
	c.groupKilled = true
	if c.proc == nil {
		return nil
	}
	if !c.newGroup {
		if err := c.proc.Kill(); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("proc: killgroup: %w", err)
		}
		return nil
	}
	if err := killGroup(c.pid); err != nil {
		return fmt.Errorf("proc: killgroup: %w", err)
	}
	return nil
}

// Wait blocks until the child terminates and reports its Term.
// It is idempotent. The context must be non-nil and should carry a deadline.
// Wait never closes pipes; Close releases them.
func (c *Child) Wait(ctx context.Context) (Term, error) {
	if ctx == nil {
		return Term{}, fmt.Errorf("proc: wait needs non-nil context")
	}
	c.mu.Lock()
	if c.waited {
		term := c.term
		err := c.waitErr
		c.mu.Unlock()
		return term, err
	}
	if c.waiting {
		c.mu.Unlock()
		return Term{}, fmt.Errorf("proc: wait called concurrently")
	}
	c.waiting = true
	c.mu.Unlock()
	stop := context.AfterFunc(ctx, func() {
		_ = c.Kill()
	})
	state, err := c.proc.Wait()
	fired := !stop()
	var term Term
	var waitErr error
	if err != nil {
		term = Term{}
		waitErr = fmt.Errorf("proc: wait: %w", err)
	} else {
		term = termFromState(state)
		if fired && ctx.Err() != nil {
			waitErr = fmt.Errorf("proc: wait: %w", ctx.Err())
		}
	}
	c.mu.Lock()
	c.waited = true
	c.waiting = false
	c.term = term
	c.waitErr = waitErr
	c.mu.Unlock()
	return term, waitErr
}

// resolvePath locates the executable for Spawn. Bare names use PATH.
// An explicit relative path with a child directory set resolves against
// that directory first: LookPath checks the parent directory, but the
// child runs in its own, so ./tool must be looked up where it runs.
func resolvePath(path string, cwd Cwd) (string, error) {
	if path == "" {
		return "", fmt.Errorf("proc: spawn needs non-empty path")
	}
	if cwd.HasPath && isExplicitRelative(path) {
		if cand := filepath.Join(cwd.Path, path); isExecutableFile(cand) {
			if abs, err := filepath.Abs(cand); err == nil {
				return abs, nil
			}
			return cand, nil
		}
	}
	resolved, err := exec.LookPath(path)
	if err != nil {
		return "", fmt.Errorf("proc: spawn %s: %w", path, err)
	}
	return resolved, nil
}

// isExplicitRelative reports whether path names a file relative to a
// directory instead of through PATH lookup.
func isExplicitRelative(path string) bool {
	return strings.ContainsRune(path, '/') || strings.ContainsRune(path, filepath.Separator)
}

// isExecutableFile reports whether cand exists and is not a directory.
// Execute permission itself is the kernel's verdict at spawn time.
func isExecutableFile(cand string) bool {
	fi, err := os.Stat(cand)
	return err == nil && !fi.IsDir()
}

func cwdDir(c Cwd) string {
	if !c.HasPath {
		return ""
	}
	return c.Path
}

func openStdIO(s StdIO, inherit *os.File, forStdin bool) (*os.File, *os.File, bool, error) {
	switch s.Kind {
	case StdIOInherit:
		if inherit == nil {
			return openNull()
		}
		return inherit, nil, false, nil
	case StdIOFile:
		if s.File == nil {
			return nil, nil, false, fmt.Errorf("proc: file stdio needs non-nil file")
		}
		return s.File, nil, false, nil
	case StdIONull:
		return openNull()
	case StdIOPipe:
		return openPipe(forStdin)
	case StdIOClose:
		return nil, nil, false, nil
	default:
		return nil, nil, false, fmt.Errorf("proc: unknown stdio kind")
	}
}

func openNull() (*os.File, *os.File, bool, error) {
	f, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return nil, nil, false, fmt.Errorf("proc: open null: %w", err)
	}
	return f, nil, true, nil
}

func openPipe(forStdin bool) (*os.File, *os.File, bool, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, nil, false, fmt.Errorf("proc: pipe: %w", err)
	}
	if forStdin {
		return r, w, true, nil
	}
	return w, r, true, nil
}

func closeChildEnd(child *os.File, owned bool) {
	if owned && child != nil {
		_ = child.Close()
	}
}

func closeOwned(child *os.File, caller *os.File, owned bool) {
	if owned && child != nil {
		_ = child.Close()
	}
	if caller != nil {
		_ = caller.Close()
	}
}

func termFromState(state *os.ProcessState) Term {
	if state == nil {
		return Term{Kind: TermUnknown}
	}
	if state.Exited() {
		return Term{Kind: TermExited, Code: state.ExitCode()}
	}
	if ws, ok := state.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			return Term{Kind: TermSignaled, Signal: ws.Signal()}
		}
		if ws.Stopped() {
			return Term{Kind: TermStopped, Signal: ws.StopSignal()}
		}
	}
	return Term{Kind: TermUnknown, Code: state.ExitCode()}
}

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
			for _, done := range applyStalledFixes(fixes) {
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

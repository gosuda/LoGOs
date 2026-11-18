// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build unix

package conccmd

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"

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

	findings := make(chan int, 1)
	go func() { findings <- relay(pipe, os.Stderr, inv.asJSON) }()

	child := exec.Command(self, inv.arguments...)
	child.Args[0] = os.Args[0]
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Env = append(slices.Clip(cfg.OrigEnv), sinkEnv+"="+path)
	base.StartSigHandlers()
	runErr := child.Run()

	if _, err := pipe.WriteString(endOfFindings); err != nil {
		base.Fatalf("logos conc %s: %v", verb, err)
	}
	found := <-findings

	var exit *exec.ExitError
	switch {
	case errors.As(runErr, &exit):
		status := exit.ExitCode()
		if status <= 0 {
			status = 1
		}
		base.SetExitStatus(status)
	case runErr != nil:
		base.Fatalf("logos conc %s: %v", verb, runErr)
	case found > 0:
		base.SetExitStatus(1)
	}
}

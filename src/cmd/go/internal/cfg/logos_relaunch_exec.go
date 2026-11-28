// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !js && !wasip1

package cfg

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"syscall"
)

// relaunchExec replaces this process with the logos executable, or runs
// it as a subprocess where the platform has no exec. It returns only
// when the new process could not be started.
func relaunchExec(exe, argv0 string) error {
	if exe == "" {
		return errors.New("no logos executable found")
	}
	argv := append([]string{argv0}, os.Args[1:]...)
	if runtime.GOOS == "windows" {
		cmd := exec.Command(exe, argv[1:]...)
		cmd.Args[0] = argv[0]
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if e, ok := err.(*exec.ExitError); ok && e.ProcessState != nil && e.ProcessState.Exited() {
				os.Exit(e.ProcessState.ExitCode())
			}
			return fmt.Errorf("exec %s: %v", exe, err)
		}
		os.Exit(0)
		return nil
	}
	return syscall.Exec(exe, argv, os.Environ())
}

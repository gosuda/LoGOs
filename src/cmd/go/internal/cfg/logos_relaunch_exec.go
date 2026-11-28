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

// relaunchExecCode replaces this process with the logos executable, or runs
// it as a subprocess where the platform has no exec. It passes the
// environment as it was when the process started: invoke writes computed
// Go defaults into the live environment before package loading runs, and
// the child must not read those as user settings. OrigEnv holds that
// startup snapshot once invoke has run; it is nil for the early triggers,
// which run before invoke and pass the current environment. It returns the
// child's status and true once the child has run, and never returns on
// Unix; otherwise it returns false with the start error.
func relaunchExecCode(exe, argv0 string) (int, bool, error) {
	if exe == "" {
		return 0, false, errors.New("no logos executable found")
	}
	argv := append([]string{argv0}, os.Args[1:]...)
	env := OrigEnv
	if env == nil {
		env = os.Environ()
	}
	if runtime.GOOS == "windows" {
		cmd := exec.Command(exe, argv[1:]...)
		cmd.Args[0] = argv[0]
		cmd.Env = env
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			if e, ok := err.(*exec.ExitError); ok && e.ProcessState != nil && e.ProcessState.Exited() {
				return e.ProcessState.ExitCode(), true, nil
			}
			return 0, false, fmt.Errorf("exec %s: %v", exe, err)
		}
		return 0, true, nil
	}
	return 0, false, syscall.Exec(exe, argv, env)
}

//go:build unix

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package proc

import "syscall"

func sysProcAttr(newGroup bool) *syscall.SysProcAttr {
	if !newGroup {
		return nil
	}
	return &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(pid int) error {
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		if err == syscall.ESRCH {
			return nil
		}
		return err
	}
	return nil
}

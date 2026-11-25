//go:build !unix

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package proc

import (
	"os"
	"syscall"
)

func sysProcAttr(newGroup bool) *syscall.SysProcAttr {
	return nil
}

func killGroup(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return nil
	}
	if err := p.Kill(); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return nil
}

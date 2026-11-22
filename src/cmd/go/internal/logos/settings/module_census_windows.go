// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package settings

import "syscall"

// Win32 error codes that leave a census walk untrustworthy. The syscall
// package exports no names for most of them, and Errno.Is does not map
// them to the POSIX compatibility values, so they are compared directly.
const (
	errorTooManyOpenFiles = syscall.Errno(4)
	errorNotEnoughMemory  = syscall.Errno(8)
	errorOutOfMemory      = syscall.Errno(14)
	errorHandleDiskFull   = syscall.Errno(39)
	errorDiskFull         = syscall.Errno(112)
	errorIODevice         = syscall.Errno(1117)
	errorNotEnoughQuota   = syscall.Errno(1816)
)

func censusFatalErrno(e syscall.Errno) bool {
	switch e {
	case errorTooManyOpenFiles,
		errorNotEnoughMemory,
		errorOutOfMemory,
		errorHandleDiskFull,
		errorDiskFull,
		errorIODevice,
		errorNotEnoughQuota:
		return true
	}
	return false
}

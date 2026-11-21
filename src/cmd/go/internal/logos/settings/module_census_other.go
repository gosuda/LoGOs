// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !windows

package settings

import "syscall"

// Non-Windows hosts report resource failures through the POSIX errno
// values censusFatal already classifies.
func censusFatalErrno(syscall.Errno) bool {
	return false
}

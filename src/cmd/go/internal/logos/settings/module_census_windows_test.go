// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build windows

package settings

import (
	"fmt"
	"io/fs"
	"syscall"
	"testing"
)

// Native Win32 resource errors carry no POSIX compatibility value, so a
// wrapped Errno exercises censusFatalErrno where TestCensusFatal cannot.
func TestCensusFatalWin32Errno(t *testing.T) {
	pathErr := func(errno syscall.Errno) error {
		return &fs.PathError{Op: "open", Path: "pkg", Err: errno}
	}
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"tooManyOpenFiles", pathErr(errorTooManyOpenFiles), true},
		{"notEnoughMemory", pathErr(errorNotEnoughMemory), true},
		{"outOfMemory", pathErr(errorOutOfMemory), true},
		{"handleDiskFull", pathErr(errorHandleDiskFull), true},
		{"diskFull", pathErr(errorDiskFull), true},
		{"ioDevice", pathErr(errorIODevice), true},
		{"notEnoughQuota", pathErr(errorNotEnoughQuota), true},
		{"wrappedDiskFull", fmt.Errorf("walk pkg: %w", pathErr(errorDiskFull)), true},
		{"fileNotFound", pathErr(syscall.Errno(2)), false},
		{"accessDenied", pathErr(syscall.Errno(5)), false},
		{"sharingViolation", pathErr(syscall.Errno(32)), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := censusFatal(tt.err); got != tt.want {
				t.Errorf("censusFatal(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

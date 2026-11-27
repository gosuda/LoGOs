// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build js || wasip1

package cfg

import "errors"

// relaunchExec reports that re-execution is unsupported on this
// platform. The caller prints one line and returns.
func relaunchExec(exe, argv0 string) error {
	return errors.New("re-execution is not supported on this platform")
}

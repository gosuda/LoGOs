// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package fsys

// ResetOverlay drops a previously loaded -overlay map, restoring the
// pre-initialization behavior in which the file system is the disk.
// The early go-as-logos decision loads the overlay to read markers through
// it and calls ResetOverlay before returning without relaunching, so
// toolchain selection and everything after it observe the disk exactly as
// a plain go invocation would. A relaunch replaces or exits the process,
// so the reset never runs on that path.
func ResetOverlay() {
	overlay = nil
}

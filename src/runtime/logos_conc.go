// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package runtime

import _ "unsafe"

// logosGoid reports the id of the calling goroutine to
// logos/internal/conc, which keys the locks a goroutine holds by it.
//
//go:linkname logosGoid
func logosGoid() uint64 {
	return getg().goid
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package escape

import "cmd/internal/src"

// LogosConstViewDecl reports whether pos declares a const view (SPEC
// T-201). The noder sets it for a loGo/s package from the lowering side
// table and leaves it nil otherwise, so the group read-sharing check
// (M18-S8) can treat a captured const view as a read while any other
// capture escapes.
var LogosConstViewDecl func(pos src.XPos) bool

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package leak_test

import (
	"testing"

	"logos/leak"
)

func TestCheckPassesWithoutLeak(t *testing.T) {
	defer leak.Check(t)
}

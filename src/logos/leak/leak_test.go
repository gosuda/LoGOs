// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package leak_test

import (
	"os"
	"testing"

	"logos/leak"
)

func TestCheckPassesWithoutLeak(t *testing.T) {
	leak.Check(t)
}

func TestCheckSeesCleanupReleases(t *testing.T) {
	leak.Check(t)
	f, err := os.CreateTemp("", "leak-check")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	name := f.Name()
	t.Cleanup(func() {
		f.Close()
		os.Remove(name)
	})
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"cmd/go/internal/load"
)

func TestInactiveGoImportsSkipIgnoredNames(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"extra.go", ".ignored.go", "_ignored.go"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("package p\nimport _ \"logos/leaf\"\n"), 0666); err != nil {
			t.Fatal(err)
		}
	}
	pkg := &load.Package{PackagePublic: load.PackagePublic{
		Dir: dir, ImportPath: "logos/p", Standard: true,
	}}
	plan := &ejectPlan{provenance: map[string][]*load.Package{"logos/p": {pkg}}}
	var got []string
	for _, file := range collectInactiveGoImports(plan) {
		got = append(got, file.name)
	}
	if want := []string{"extra.go"}; !slices.Equal(got, want) {
		t.Fatalf("inactive Go imports = %q, want %q", got, want)
	}
}

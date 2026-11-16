// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"internal/testenv"
	"strings"
	"testing"
)

func TestWasmRunIgnoresHostEnvironment(t *testing.T) {
	testenv.MustHaveGoBuild(t)
	wasmNode(t)

	// Build once before changing the host environment so only the runner sees it.
	wasm := buildWasm(t, testenv.GoToolPath(t), "js", `package main

import "os"

func main() {
	if os.Getenv("LOGOS_WASM_TEST_PADDING") != "" {
		panic("host-only LOGOS_WASM_TEST_PADDING reached the wasm guest")
	}
	if os.Getenv("GOMAXPROCS") != "1" {
		panic("wasm guest GOMAXPROCS is not 1")
	}
	f, err := os.CreateTemp("", "logos-wasm-env-")
	if err != nil {
		panic("create temporary file: " + err.Error())
	}
	if err := f.Close(); err != nil {
		panic("close temporary file: " + err.Error())
	}
	if err := os.Remove(f.Name()); err != nil {
		panic("remove temporary file: " + err.Error())
	}
	println("wasm env ok")
}
`, "")

	for _, tc := range []struct {
		name    string
		padding string
	}{
		{"clean", ""},
		{"sentinel", "host-only sentinel"},
		{"large", strings.Repeat("x", 16<<10)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LOGOS_WASM_TEST_PADDING", tc.padding)
			got, err := runWasmNode(t, wasm)
			const want = "wasm env ok\n"
			if err != nil || got != want {
				t.Fatalf("run: %v\ngot %q\nwant %q", err, got, want)
			}
		})
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package conccmd

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeLGO(t *testing.T, body string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "main.lgo")
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return file
}

func TestStalledReplacementExitsOnCancel(t *testing.T) {
	file := writeLGO(t, `package main

import "context"

func slow(ctx context.Context) error {
	<-inner.Done()
	return nil
}
`)
	got, ok := stalledReplacement(file, 6)
	if !ok {
		t.Fatal("no fix offered")
	}
	want := "\tselect {\n\tcase <-inner.Done():\n\tcase <-ctx.Done():\n\t\treturn ctx.Err()\n\t}"
	if got != want {
		t.Fatalf("replacement = %q, want %q", got, want)
	}
}

func TestStalledReplacementVoidReturnsBare(t *testing.T) {
	file := writeLGO(t, `package main

import "context"

func slow(ctx context.Context) {
	<-inner.Done()
}
`)
	got, ok := stalledReplacement(file, 6)
	if !ok {
		t.Fatal("no fix offered")
	}
	if !strings.Contains(got, "\n\t\treturn\n") {
		t.Fatalf("replacement = %q, want a bare return", got)
	}
}

func TestStalledReplacementDeclinesUnsafeShapes(t *testing.T) {
	for name, body := range map[string]string{
		"blank context": `package main

import "context"

func slow(_ context.Context) error {
	<-inner.Done()
	return nil
}
`,
		"nested func type": `package main

import "context"

func run(fn func(ctx context.Context) error) error {
	<-inner.Done()
	return nil
}
`,
		"trailing comment": `package main

import "context"

func slow(ctx context.Context) error {
	<-inner.Done() // wait for the inner deadline
	return nil
}
`,
		"multi result": `package main

import "context"

func slow(ctx context.Context) (int, error) {
	<-inner.Done()
	return 0, nil
}
`,
	} {
		file := writeLGO(t, body)
		if got, ok := stalledReplacement(file, 6); ok {
			t.Errorf("%s: offered %q, want no fix", name, got)
		}
	}
}

func TestStalledReplacementUsesLiteralContext(t *testing.T) {
	file := writeLGO(t, `package main

import "context"

func outer(parent context.Context) error {
	worker := func(ctx context.Context) error {
		<-inner.Done()
		return nil
	}
	return worker(parent)
}
`)
	got, ok := stalledReplacement(file, 7)
	if !ok {
		t.Fatal("no fix offered")
	}
	if !strings.Contains(got, "case <-ctx.Done():") {
		t.Fatalf("replacement = %q, want the literal's ctx", got)
	}
}

func TestStalledReplacementIgnoresBracesInLiterals(t *testing.T) {
	file := writeLGO(t, `package main

import (
	"context"
	"fmt"
)

func slow(ctx context.Context) error {
	worker := func() {
		fmt.Println("}")
		<-inner.Done()
	}
	worker()
	return nil
}
`)
	got, ok := stalledReplacement(file, 10)
	if ok {
		t.Fatalf("offered fix %q into a void closure", got)
	}
}

func TestCollectStalledFixesFollowsOverlay(t *testing.T) {
	dir := t.TempDir()
	logical := filepath.Join(dir, "main.lgo")
	actual := filepath.Join(dir, "gen.lgo")
	src := `package main

import "context"

func slow(ctx context.Context) error {
	<-inner.Done()
	return nil
}
`
	if err := os.WriteFile(logical, []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(actual, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := map[string]string{logical: actual}
	raw := `{"rule":"conc::group_child_stalled","message":"stalled","positions":[{"file":` + strconv.Quote(logical) + `,"line":6,"role":"receive"}]}` + "\n"
	fixes := collectStalledFixes(raw, overlay)
	if len(fixes) != 1 || fixes[0].file != actual || fixes[0].line != 6 {
		t.Fatalf("fixes = %+v, want one fix on the overlay replacement %s:6", fixes, actual)
	}
}

func TestOverlayFilesParsesFlag(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "overlay.json")
	logical := filepath.Join(dir, "a.lgo")
	actual := filepath.Join(dir, "b.lgo")
	if err := os.WriteFile(doc, []byte(`{"Replace":{`+strconv.Quote(logical)+`:`+strconv.Quote(actual)+`}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"test", "-overlay", doc, "./x"},
		{"test", "-overlay=" + doc, "./x"},
	} {
		files, ok := overlayFiles(args)
		if !ok || files[logical] != actual {
			t.Errorf("overlayFiles(%v) = %v, %v; want %s -> %s", args, files, ok, logical, actual)
		}
	}
	if files, ok := overlayFiles([]string{"test", "./x"}); !ok || files != nil {
		t.Errorf("overlayFiles without the flag = %v, %v; want nil, true", files, ok)
	}
	if _, ok := overlayFiles([]string{"test", "-overlay", filepath.Join(dir, "missing.json")}); ok {
		t.Error("an unreadable overlay still offered fixes")
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"strings"
	"testing"
)

func TestNextSprintfLastUseStaysGuidance(t *testing.T) {
	imp := newTestImporter(t)
	src := "package p\n\nimport \"fmt\"\n\nfunc g(ok bool) string {\n\treturn fmt.Sprintf(\"%t\", ok)\n}\n"
	edits, tasks := scanSnippet(t, imp, src)
	if len(edits) != 0 {
		t.Fatalf("last-use %%t: got edits %v, want none so the fmt import stays used", edits)
	}
	if len(tasks) != 1 {
		t.Fatalf("last-use %%t: got tasks %v, want one guidance task", tasks)
	}
	if tasks[0].Rule != "sprintf-interp" {
		t.Fatalf("last-use %%t: got rule %q want sprintf-interp", tasks[0].Rule)
	}
}

func TestNextSprintfMixedUseStillEdits(t *testing.T) {
	imp := newTestImporter(t)
	src := "package p\n\nimport \"fmt\"\n\nfunc g(ok bool) string {\n\treturn fmt.Sprintf(\"%t\", ok)\n}\n\nfunc h() {\n\tfmt.Println(\"x\")\n}\n"
	edits, tasks := scanSnippet(t, imp, src)
	if len(edits) != 1 {
		t.Fatalf("mixed-use %%t: got %d edits %v, want 1", len(edits), edits)
	}
	if edits[0].Replacement != `$"{ok}"` {
		t.Fatalf("mixed-use %%t: got replacement %q", edits[0].Replacement)
	}
	for _, task := range tasks {
		if task.Rule == "sprintf-interp" {
			t.Fatalf("mixed-use %%t: unexpected sprintf-interp task %v", task)
		}
	}
}

func TestNextSprintfTwoConvertibleCallsDowngradeLast(t *testing.T) {
	imp := newTestImporter(t)
	src := "package p\n\nimport \"fmt\"\n\nfunc g(name string) string {\n\treturn fmt.Sprintf(\"hello %s\", name)\n}\n\nfunc h(n int) string {\n\treturn fmt.Sprintf(\"n=%d\", n)\n}\n"
	edits, tasks := scanSnippet(t, imp, src)
	if len(edits) != 1 {
		t.Fatalf("two convertible calls: got %d edits %v, want 1 (the non-last call)", len(edits), edits)
	}
	if edits[0].Replacement != `$"hello {name}"` {
		t.Fatalf("two convertible calls: got replacement %q", edits[0].Replacement)
	}
	var found bool
	for _, task := range tasks {
		if task.Rule == "sprintf-interp" {
			found = true
		}
	}
	if !found {
		t.Fatalf("two convertible calls: got tasks %v, want a sprintf-interp task for the last call", tasks)
	}
}

func TestNextSprintfDeclinedGetsGuidance(t *testing.T) {
	imp := newTestImporter(t)
	cases := []struct {
		name string
		body string
	}{
		{"unknownVerb", "func g(n int) string {\n\treturn fmt.Sprintf(\"hex %x\", n)\n}"},
		{"bytesS", "func g(b []byte) string {\n\treturn fmt.Sprintf(\"v=%s\", b)\n}"},
		{"dotArgs", "func g(n int) string {\n\treturn fmt.Sprintf(\"%d\", []any{n}...)\n}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := "package p\n\nimport \"fmt\"\n\n" + tc.body + "\nfunc h() { fmt.Println(\"x\") }\n"
			edits, tasks := scanSnippet(t, imp, src)
			if len(edits) != 0 {
				t.Fatalf("%s: got edits %v, want none", tc.name, edits)
			}
			var found bool
			for _, task := range tasks {
				if task.Rule == "sprintf-interp" {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: got tasks %v, want a sprintf-interp guidance task", tc.name, tasks)
			}
		})
	}
}

func TestNextSprintfDeclinedTaskInstruction(t *testing.T) {
	imp := newTestImporter(t)
	src := "package p\n\nimport \"fmt\"\n\nfunc g(b []byte) string {\n\treturn fmt.Sprintf(\"v=%s\", b)\n}\nfunc h() { fmt.Println(\"x\") }\n"
	_, tasks := scanSnippet(t, imp, src)
	for _, task := range tasks {
		if task.Rule == "sprintf-interp" && strings.Contains(task.Instruction, "by hand") {
			return
		}
	}
	t.Fatalf("got tasks %v, want a sprintf-interp task with a manual instruction", tasks)
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"strings"
	"testing"
)

func TestGroupLowersToRtNewGroup(t *testing.T) {
	plain := "package main\n\nimport \"context\"\n\nfunc work(ctx context.Context) error { return nil }\n\nfunc run(ctx context.Context) error {\n\tgo group (ctx) {\n\t\tgo work(ctx)\n\t}\n\treturn nil\n}\n\nfunc main() {}\n"
	out := lowerSnippetSrc(t, "grouplower.lgo", plain)
	if n := strings.Count(out, "_logos_rt.NewGroup("); n != 1 {
		t.Errorf("plain go group: got %d _logos_rt.NewGroup( calls, want 1:\n%s", n, out)
	}
	limited := "package main\n\nimport \"context\"\n\nfunc work(ctx context.Context) error { return nil }\n\nfunc run(ctx context.Context) error {\n\tgo group (ctx, limit = 8) {\n\t\tgo work(ctx)\n\t}\n\treturn nil\n}\n\nfunc main() {}\n"
	out = lowerSnippetSrc(t, "grouplowerlimit.lgo", limited)
	if n := strings.Count(out, "_logos_rt.NewGroup("); n != 1 {
		t.Errorf("limited go group: got %d _logos_rt.NewGroup( calls, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, ", 8)") {
		t.Errorf("limited go group: limit 8 missing from NewGroup call:\n%s", out)
	}
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"strings"
	"testing"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

func TestCtEjectNameShape(t *testing.T) {
	pkg := types2.NewPackage("example.com/p", "p")
	obj := types2.NewFunc(syntax.Pos{}, pkg, "Autowire", nil)
	names := ctEjectNames(map[string]*types2.Func{"id1": obj, "id0": obj}, nil)
	if got, want := names["id0"], "Autowire_logosct0"; got != want {
		t.Fatalf("ctEjectNames[id0] = %q, want %q", got, want)
	}
	if got, want := names["id1"], "Autowire_logosct1"; got != want {
		t.Fatalf("ctEjectNames[id1] = %q, want %q", got, want)
	}

	parse := func(name string) error {
		var errs []error
		_, _ = syntax.Parse(syntax.NewFileBase("cteject.lgo"),
			strings.NewReader("package p\nfunc "+name+"() {}\n"),
			func(err error) { errs = append(errs, err) },
			nil, syntax.CheckBranches|syntax.LogosMode)
		if len(errs) == 0 {
			return nil
		}
		return errs[0]
	}
	if err := parse(names["id0"]); err != nil {
		t.Fatalf("ejected instance name does not parse: %v", err)
	}
	if err := parse("Autowire·logosct0"); err == nil {
		t.Fatalf("middle-dot instance name parses, want a parse error")
	}
}

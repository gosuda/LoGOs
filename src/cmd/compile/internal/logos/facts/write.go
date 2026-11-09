// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package facts

import (
	"bytes"
	"strings"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// Write returns the source-phase records of pkg: every record derived
// from the first, unlowered check. It never returns nil.
func Write(pkg *types2.Package, info *types2.Info, files []*syntax.File, src map[*syntax.File][]byte) *Facts {
	f := &Facts{}
	if pkg == nil || pkg.Scope() == nil {
		return f
	}
	writeOpts(pkg, f)
	writeConsts(pkg, info, files, f)
	writeDefaults(pkg, info, f)
	writeConstFuncs(pkg, info, files, src, f)
	writeProps(pkg, info, f)
	return f
}

// WriteChecked folds the checked-phase records of the final (rechecked)
// pkg into f and returns f. When f is nil and nothing is recorded it
// returns nil, so a plain-Go package without records gains no member.
func WriteChecked(pkg *types2.Package, info *types2.Info, files []*syntax.File, f *Facts) *Facts {
	if pkg == nil || pkg.Scope() == nil {
		return f
	}
	out := f
	if out == nil {
		out = &Facts{}
	}
	writeAsserts(pkg, out)
	writeLocks(pkg, info, files, out)
	if f == nil && out.empty() {
		return nil
	}
	return out
}

func findOrCreateFnFact(f *Facts, object string) *FnFact {
	for i := range f.Fns {
		if f.Fns[i].Object == object {
			return &f.Fns[i]
		}
	}
	f.Fns = append(f.Fns, FnFact{Object: object})
	return &f.Fns[len(f.Fns)-1]
}

func constView(t types2.Type) *types2.ConstView {
	if t == nil {
		return nil
	}
	if c, ok := types2.Unalias(t).(*types2.ConstView); ok {
		return c
	}
	if n, ok := types2.Unalias(t).(*types2.Named); ok {
		if u := n.Underlying(); u != nil {
			if c, ok := u.(*types2.ConstView); ok {
				return c
			}
		}
	}
	return nil
}

func optionOf(t types2.Type) *types2.Option {
	if t == nil {
		return nil
	}
	if o, ok := types2.Unalias(t).(*types2.Option); ok {
		return o
	}
	if n, ok := types2.Unalias(t).(*types2.Named); ok {
		if u := n.Underlying(); u != nil {
			if o, ok := u.(*types2.Option); ok {
				return o
			}
		}
	}
	return nil
}

func joinPath(path []string) string {
	return strings.Join(path, ".")
}

func renderExpr(e syntax.Expr) string {
	var buf bytes.Buffer
	_, _ = syntax.Fprint(&buf, e, syntax.LineForm)
	return buf.String()
}

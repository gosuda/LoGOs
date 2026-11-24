// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"testing"

	"cmd/compile/internal/logos/facts"
	"cmd/compile/internal/types2"
	"cmd/internal/goobj"
)

// Exported variables with an inferred option type must keep their
// option fact across the export boundary. The Go-facing export data
// stays a plain pointer; the overlay restores the option from the V fact.
func TestCloneWithOverlaysRestoresExportedVarOption(t *testing.T) {
	const src = `package overlaytest

type MyError struct {
	Msg string
}

func GetOpt(ok bool) *MyError? {
	if ok {
		return &MyError{Msg: "x"}
	}
	return nil
}

var Value = GetOpt(false)
`
	lowered, opts, _ := checkLoweredForOverlay(t, src)
	if !hasPath(opts, "Value", "V") {
		t.Fatalf("missing var option fact Value V in %v", opts)
	}
	enc := facts.Encode(goobj.FingerprintType{1, 2, 3, 4, 5, 6, 7, 8}, &facts.Facts{Opts: opts})
	back, err := facts.Decode(enc)
	if err != nil {
		t.Fatalf("Decode(Encode) failed: %v", err)
	}
	if !hasPath(back.Opts, "Value", "V") {
		t.Fatalf("V fact did not survive encode/decode in %v", back.Opts)
	}
	loweredObj := lowered.Scope().Lookup("Value")
	loweredVar, ok := loweredObj.(*types2.Var)
	if !ok {
		t.Fatalf("lowered Value is not a var: %T", loweredObj)
	}
	if optAsOption(loweredVar.Type()) != nil {
		t.Fatalf("lowered var already carries an option: %s", loweredVar.Type())
	}
	cloned := CloneWithOverlays(lowered, back.Opts, nil)
	obj := cloned.Scope().Lookup("Value")
	v, ok := obj.(*types2.Var)
	if !ok {
		t.Fatalf("cloned Value is not a var: %T", obj)
	}
	if optAsOption(v.Type()) == nil {
		t.Fatalf("CloneWithOverlays did not restore the var option, got %s", v.Type())
	}
}

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

func clonedVar(t *testing.T, pkg *types2.Package, name string) *types2.Var {
	t.Helper()
	obj := pkg.Scope().Lookup(name)
	v, ok := obj.(*types2.Var)
	if !ok {
		t.Fatalf("cloned %s is not a var: %T", name, obj)
	}
	return v
}

func namedFieldOption(t *testing.T, v *types2.Var) *types2.Option {
	t.Helper()
	named, ok := types2.Unalias(v.Type()).(*types2.Named)
	if !ok {
		t.Fatalf("var %s type is %T, want an instantiated named type", v.Name(), v.Type())
	}
	u, ok := named.Underlying().(*types2.Struct)
	if !ok {
		t.Fatalf("var %s underlying is %T, want a struct", v.Name(), named.Underlying())
	}
	if u.NumFields() != 1 {
		t.Fatalf("var %s has %d fields, want 1", v.Name(), u.NumFields())
	}
	return optAsOption(u.Field(0).Type())
}

// An option fact for one generic instantiation must not leak into the
// shared origin: Box[int].Value stays int while Box[int?].Value is
// restored as an option on that variable only.
func TestCloneWithOverlaysGenericInstanceOption(t *testing.T) {
	const src = `package overlaytest

type Box[T any] struct {
	Value T
}

var Optional Box[int?]

var Plain Box[int]
`
	lowered, opts, _ := checkLoweredForOverlay(t, src)
	if !hasPath(opts, "Optional", "V.FValue") {
		t.Fatalf("missing var option fact Optional V.FValue in %v", opts)
	}
	cloned := CloneWithOverlays(lowered, opts, nil)
	if opt := namedFieldOption(t, clonedVar(t, cloned, "Optional")); opt == nil {
		t.Fatal("CloneWithOverlays did not restore Optional.Value as an option")
	}
	if opt := namedFieldOption(t, clonedVar(t, cloned, "Plain")); opt != nil {
		t.Fatalf("CloneWithOverlays leaked the option into Plain.Value, got %s", opt)
	}
}

// A variable of a defined option type keeps the declaration's type: the
// option lives on the named declaration (restored from its O fact), so
// the variable must not gain a second option around it. The anonymous
// option around a named payload still wraps as before.
func TestCloneWithOverlaysDefinedOptionVar(t *testing.T) {
	const src = `package overlaytest

type MaybeInt int?

var Default MaybeInt

type User struct {
	Name string
}

var Current User?
`
	lowered, opts, _ := checkLoweredForOverlay(t, src)
	if !hasPath(opts, "MaybeInt", "O") {
		t.Fatalf("missing declaration option fact MaybeInt O in %v", opts)
	}
	if !hasPath(opts, "Default", "V") {
		t.Fatalf("missing var option fact Default V in %v", opts)
	}
	cloned := CloneWithOverlays(lowered, opts, nil)
	dflt := clonedVar(t, cloned, "Default")
	if _, ok := types2.Unalias(dflt.Type()).(*types2.Option); ok {
		t.Fatalf("CloneWithOverlays wrapped Default in a second option, got %s", dflt.Type())
	}
	if optAsOption(dflt.Type()) == nil {
		t.Fatalf("CloneWithOverlays lost the defined option on Default, got %s", dflt.Type())
	}
	decl, ok := cloned.Scope().Lookup("MaybeInt").(*types2.TypeName)
	if !ok {
		t.Fatalf("cloned MaybeInt is not a type name: %T", cloned.Scope().Lookup("MaybeInt"))
	}
	if !types2.Identical(dflt.Type(), decl.Type()) {
		t.Fatalf("Default %s does not match its declaration %s", dflt.Type(), decl.Type())
	}
	cur := clonedVar(t, cloned, "Current")
	if _, ok := types2.Unalias(cur.Type()).(*types2.Option); !ok {
		t.Fatalf("CloneWithOverlays did not restore Current as an option, got %s", cur.Type())
	}
}

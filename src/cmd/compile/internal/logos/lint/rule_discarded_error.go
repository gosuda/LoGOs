// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lint

import (
	"slices"
	"strings"

	"cmd/compile/internal/logos/facts"
	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

// discardedError is slop::discarded_error (SPEC Sec 1.2, #164): an
// expression statement or a blank assignment that drops an error result.
var discardedError = Rule{
	ID:    "slop::discarded_error",
	Group: Slop,
	Go:    true,
	Check: checkDiscardedError,
}

// discardCheck walks one file and reports dropped error results.
type discardCheck struct {
	pass   *Pass
	file   *syntax.File
	nilErr map[*types2.Func]bool
	// returnsError tells whether the function being walked returns error
	// as its last result.
	returnsError bool
	// group is the depth of go group blocks and child blocks around the
	// statement being walked; a go statement at depth above zero hands its
	// error to the group.
	group int
}

// checkDiscardedError is the Check of discardedError.
func checkDiscardedError(pass *Pass) {
	nilErr := facts.AlwaysNilErrorSet(pass.Pkg, pass.Info, pass.Files)
	for _, f := range pass.Files {
		c := &discardCheck{pass: pass, file: f, nilErr: nilErr}
		c.walk(f)
	}
}

// walk visits the statements under n. A function starts a new walk with its
// own signature.
func (c *discardCheck) walk(n syntax.Node) {
	syntax.Inspect(n, func(m syntax.Node) bool {
		switch m := m.(type) {
		case *syntax.FuncDecl:
			if m.Body != nil {
				c.walkFunc(m.Body, m.Type)
			}
			return false
		case *syntax.FuncLit:
			c.walkFunc(m.Body, m.Type)
			return false
		case *syntax.GroupStmt:
			c.groupBody(m.Body)
			return false
		case *syntax.GroupChild:
			c.groupBody(m.Body)
			return false
		case *syntax.ExprStmt:
			c.exprStmt(m)
		case *syntax.AssignStmt:
			c.assignStmt(m)
		case *syntax.CallStmt:
			c.callStmt(m)
		}
		return true
	})
}

func (c *discardCheck) walkFunc(body *syntax.BlockStmt, sig *syntax.FuncType) {
	outer := c.returnsError
	outerGroup := c.group
	c.returnsError = lastResultIsError(sig)
	c.group = 0
	c.walk(body)
	c.returnsError = outer
	c.group = outerGroup
}

func (c *discardCheck) exprStmt(s *syntax.ExprStmt) {
	call := callOf(s.X)
	if call == nil || !c.dropsError(call) {
		return
	}
	d := c.dropped(call)
	if c.returnsError && c.pass.Kind == KindLgo {
		d.Fixes = []Fix{{Description: "add ? to propagate the error", Replacement: syntax.String(call) + "?"}}
	}
	c.pass.Report(d)
}

func (c *discardCheck) groupBody(body *syntax.BlockStmt) {
	c.group++
	c.walk(body)
	c.group--
}

func (c *discardCheck) callStmt(s *syntax.CallStmt) {
	if s.Tok == syntax.Go && c.group > 0 {
		return
	}
	call := callOf(s.Call)
	if call == nil || !c.dropsError(call) {
		return
	}
	c.pass.Report(c.dropped(call))
}

func (c *discardCheck) dropsError(call *syntax.CallExpr) bool {
	if c.benignCall(call) {
		return false
	}
	return slices.ContainsFunc(elemTypes(TypeOf(call)), isErrorType)
}

// assignStmt reports a blank identifier that receives an error result.
func (c *discardCheck) assignStmt(s *syntax.AssignStmt) {
	if s.Rhs == nil || (s.Op != 0 && s.Op != syntax.Def) {
		return
	}
	lhs, rhs := exprList(s.Lhs), exprList(s.Rhs)
	var call *syntax.CallExpr
	switch {
	case len(lhs) == len(rhs):
		call = c.pairedBlankCall(lhs, rhs)
	case len(rhs) == 1:
		call = c.tupleBlankCall(lhs, rhs[0])
	}
	if call != nil {
		c.pass.Report(c.dropped(call))
	}
}

// pairedBlankCall returns the call whose single error result a blank
// identifier receives in a one-to-one assignment.
func (c *discardCheck) pairedBlankCall(lhs, rhs []syntax.Expr) *syntax.CallExpr {
	for i := range lhs {
		call := callOf(rhs[i])
		if call == nil || !isBlank(lhs[i]) || c.benignCall(call) {
			continue
		}
		if res := elemTypes(TypeOf(call)); len(res) == 1 && isErrorType(res[0]) {
			return call
		}
	}
	return nil
}

// tupleBlankCall returns the call whose tuple result has an error value
// at the position of a blank identifier.
func (c *discardCheck) tupleBlankCall(lhs []syntax.Expr, e syntax.Expr) *syntax.CallExpr {
	call := callOf(e)
	if call == nil || c.benignCall(call) {
		return nil
	}
	res := elemTypes(TypeOf(call))
	if len(res) != len(lhs) {
		return nil
	}
	for i, l := range lhs {
		if isBlank(l) && isErrorType(res[i]) {
			return call
		}
	}
	return nil
}

// dropped builds the finding for a call whose error is dropped. Its span is
// the call, through the closing parenthesis.
func (c *discardCheck) dropped(call *syntax.CallExpr) Diagnostic {
	text := syntax.String(call)
	return Diagnostic{
		Pos:     syntax.StartPos(call),
		End:     c.pass.CallEnd(c.file, call),
		Message: "error result of " + text + " is dropped; handle or propagate it",
		Provenance: Provenance{
			OriginExpr:   text,
			OriginLine:   int(call.Pos().Line()),
			InferredType: "error",
		},
	}
}

// lastResultIsError reports whether the last result of sig is error.
func lastResultIsError(sig *syntax.FuncType) bool {
	if sig == nil || len(sig.ResultList) == 0 {
		return false
	}
	return isErrorType(TypeOf(sig.ResultList[len(sig.ResultList)-1].Type))
}

func (c *discardCheck) benignCall(call *syntax.CallExpr) bool {
	fn := calleeFunc(c.pass, call)
	if fn == nil {
		return false
	}
	switch name := fn.FullName(); name {
	case "fmt.Print", "fmt.Printf", "fmt.Println":
		return true
	case "fmt.Fprint", "fmt.Fprintf", "fmt.Fprintln":
		return len(call.ArgList) > 0 && isStdoutOrStderr(c.pass, call.ArgList[0])
	default:
		if strings.HasPrefix(name, "(*bytes.Buffer).Write") ||
			strings.HasPrefix(name, "(*strings.Builder).Write") {
			return true
		}
	}
	return c.alwaysNil(fn)
}

func (c *discardCheck) alwaysNil(fn *types2.Func) bool {
	if c.nilErr[fn] {
		return true
	}
	if fn.Pkg() == nil || fn.Pkg() == c.pass.Pkg {
		return false
	}
	return facts.AlwaysNilCallee(fn)
}

// isStdoutOrStderr reports whether w is os.Stdout or os.Stderr.
func isStdoutOrStderr(pass *Pass, w syntax.Expr) bool {
	sel, ok := w.(*syntax.SelectorExpr)
	if !ok {
		return false
	}
	if v, ok := pass.Info.Uses[sel.Sel].(*types2.Var); ok && v.Pkg() != nil && v.Pkg().Path() == "os" {
		return v.Name() == "Stdout" || v.Name() == "Stderr"
	}
	return false
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import (
	"strconv"
	"strings"

	"cmd/compile/internal/syntax"
	"cmd/compile/internal/types2"
)

type sprintfEvent struct {
	call     *syntax.CallExpr
	pkg      *types2.PkgName
	start    int
	end      int
	trailing string
	rep      string
	eligible bool
}

func (s *nextScan) sprintfFile(f *syntax.File) {
	type fmtUses struct {
		refs         int
		eligible     int
		lastEligible *syntax.CallExpr
	}
	var events []sprintfEvent
	var uses map[*types2.PkgName]fmtUses
	syntax.Inspect(f, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		if s.insideLines(int(call.Pos().Line())) {
			return false
		}
		pkg := s.sprintfPkg(call)
		if pkg == nil {
			return true
		}
		if rep, ok := s.sprintf(call); ok {
			start, ok := s.startOf(call)
			if !ok {
				return false
			}
			end, ok := s.endOf(call)
			if !ok {
				return false
			}
			trailing, blocked := scanComments(s.src, start, end, end)
			if blocked {
				return false
			}
			events = append(events, sprintfEvent{call: call, pkg: pkg, start: start, end: end, trailing: trailing, rep: rep, eligible: true})
			if uses == nil {
				uses = make(map[*types2.PkgName]fmtUses)
			}
			use := uses[pkg]
			use.eligible++
			use.lastEligible = call
			uses[pkg] = use
			return false
		}
		events = append(events, sprintfEvent{call: call, pkg: pkg})
		return true
	})
	if len(uses) > 0 {
		syntax.Inspect(f, func(n syntax.Node) bool {
			name, ok := n.(*syntax.Name)
			if !ok {
				return true
			}
			pkg, ok := s.info.Uses[name].(*types2.PkgName)
			if !ok {
				return true
			}
			if use, tracked := uses[pkg]; tracked {
				use.refs++
				uses[pkg] = use
			}
			return true
		})
	}
	for _, ev := range events {
		if !ev.eligible {
			s.sprintfTask(ev.call, "the fmt.Sprintf call cannot be rewritten automatically; convert it by hand")
			continue
		}
		use := uses[ev.pkg]
		if use.refs == use.eligible && use.lastEligible == ev.call {
			s.sprintfTask(ev.call, "fmt.Sprintf is the file's last fmt use; convert it by hand and drop the fmt import if it becomes unused")
			continue
		}
		s.emitInterpolation(ev.start, ev.end, ev.rep+ev.trailing)
	}
}

func (s *nextScan) sprintfTask(call *syntax.CallExpr, instruction string) {
	s.tasks = append(s.tasks, NextTask{
		Category:    "ai-patch",
		Rule:        nextRuleSprintf,
		StartLine:   int(call.Pos().Line()),
		EndLine:     int(syntax.EndPos(call).Line()),
		Instruction: instruction,
	})
}

func (s *nextScan) sprintf(call *syntax.CallExpr) (string, bool) {
	cooked, ok := sprintfCooked(call)
	if !ok {
		return "", false
	}
	items, ok := parseVerbs(cooked)
	if !ok {
		return "", false
	}
	srcs, ok := s.sprintfOperands(items, call.ArgList[1:])
	if !ok {
		return "", false
	}
	return buildInterp(items, srcs), true
}

func (s *nextScan) sprintfPkg(call *syntax.CallExpr) *types2.PkgName {
	fun, ok := call.Fun.(*syntax.SelectorExpr)
	if !ok {
		return nil
	}
	if fun.Sel.Value != "Sprintf" {
		return nil
	}
	base, ok := fun.X.(*syntax.Name)
	if !ok {
		return nil
	}
	pkg, ok := s.info.Uses[base].(*types2.PkgName)
	if !ok || pkg.Imported().Path() != "fmt" {
		return nil
	}
	return pkg
}

func sprintfCooked(call *syntax.CallExpr) (string, bool) {
	if call.HasDots {
		return "", false
	}
	if len(call.ArgList) == 0 {
		return "", false
	}
	lit, ok := call.ArgList[0].(*syntax.BasicLit)
	if !ok {
		return "", false
	}
	if lit.Kind != syntax.StringLit {
		return "", false
	}
	if lit.Bad {
		return "", false
	}
	cooked, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return cooked, true
}

func (s *nextScan) sprintfOperands(items []verbItem, args []syntax.Expr) ([]string, bool) {
	nverbs := 0
	for _, it := range items {
		if it.verb != 0 {
			nverbs++
		}
	}
	if nverbs != len(args) {
		return nil, false
	}
	srcs := make([]string, 0, len(args))
	k := 0
	for _, it := range items {
		if it.verb == 0 {
			continue
		}
		src, ok := s.sprintfOperand(args[k], it.verb)
		if !ok {
			return nil, false
		}
		srcs = append(srcs, src)
		k++
	}
	return srcs, true
}

func (s *nextScan) sprintfOperand(arg syntax.Expr, verb byte) (string, bool) {
	t := arg.GetTypeInfo().Type
	if t == nil {
		return "", false
	}
	if implementsFormatter(t) {
		return "", false
	}
	kind, ok := types2.LogosInterpKindOf(t)
	if !ok {
		return "", false
	}
	if !verbKind(verb, kind) {
		return "", false
	}
	end, ok := s.endOf(arg)
	if !ok {
		return "", false
	}
	start, ok := s.startOf(arg)
	if !ok {
		return "", false
	}
	return string(s.src[start:end]), true
}

type verbItem struct {
	lit  string
	verb byte
}

func parseVerbs(cooked string) ([]verbItem, bool) {
	var items []verbItem
	var lit strings.Builder
	flush := func(verb byte) {
		items = append(items, verbItem{lit: lit.String(), verb: verb})
		lit.Reset()
	}
	i := 0
	for i < len(cooked) {
		if cooked[i] != '%' {
			lit.WriteByte(cooked[i])
			i++
			continue
		}
		i++
		if i >= len(cooked) {
			return nil, false
		}
		c := cooked[i]
		i++
		if c == '%' {
			lit.WriteByte('%')
			continue
		}
		if c == 's' || c == 'd' || c == 'v' || c == 't' {
			flush(c)
			continue
		}
		return nil, false
	}
	flush(0)
	return items, true
}

func verbKind(verb byte, kind types2.LogosInterpKind) bool {
	switch verb {
	case 's':
		return kind == types2.LogosInterpString
	case 'd':
		return kind == types2.LogosInterpInt || kind == types2.LogosInterpUint
	case 't':
		return kind == types2.LogosInterpBool
	case 'v':
		return kind == types2.LogosInterpString || kind == types2.LogosInterpInt || kind == types2.LogosInterpUint || kind == types2.LogosInterpBool
	}
	return false
}

func implementsFormatter(t types2.Type) bool {
	obj, _, _ := types2.LookupFieldOrMethod(t, true, nil, "Format")
	fn, ok := obj.(*types2.Func)
	if !ok {
		return false
	}
	sig, ok := types2.Unalias(fn.Type()).(*types2.Signature)
	if !ok || sig.Params().Len() != 2 || sig.Results().Len() != 0 || sig.Variadic() {
		return false
	}
	second := types2.Unalias(sig.Params().At(1).Type()).Underlying()
	b, ok := second.(*types2.Basic)
	if !ok || b.Kind() != types2.Rune {
		return false
	}
	first := types2.Unalias(sig.Params().At(0).Type())
	named, ok := first.(*types2.Named)
	if !ok {
		return false
	}
	if named.Obj().Name() != "State" {
		return false
	}
	pkg := named.Obj().Pkg()
	return pkg != nil && pkg.Path() == "fmt"
}

func buildInterp(items []verbItem, srcs []string) string {
	var out strings.Builder
	out.WriteString(`$"`)
	k := 0
	for _, it := range items {
		out.WriteString(quoteChunk(it.lit))
		if it.verb != 0 {
			out.WriteByte('{')
			out.WriteString(srcs[k])
			out.WriteByte('}')
			k++
		}
	}
	out.WriteByte('"')
	return out.String()
}

func quoteChunk(chunk string) string {
	doubled := strings.ReplaceAll(strings.ReplaceAll(chunk, "{", "{{"), "}", "}}")
	quoted := strconv.Quote(doubled)
	return quoted[1 : len(quoted)-1]
}

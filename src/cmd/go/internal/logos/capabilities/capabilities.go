// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package capabilities

import (
	"fmt"
	"path"
	"slices"
	"strings"
)

type Severity string

const (
	Deny  Severity = "deny"
	Warn  Severity = "warn"
	Allow Severity = "allow"
)

type Node struct {
	ImportPath string
	Imports    []string
	Goroot     bool
	Main       bool
}

type Violation struct {
	Dep   string
	Chain []string
}

func CheckUnsafe(sev Severity, allowlist []string, all []Node) []Violation {
	if sev == Allow {
		return nil
	}
	byPath := make(map[string]Node, len(all))
	for _, n := range all {
		if _, ok := byPath[n.ImportPath]; !ok {
			byPath[n.ImportPath] = n
		}
	}
	parent := make(map[string]string, len(all))
	var queue []string
	for _, n := range all {
		if n.Main {
			if _, ok := parent[n.ImportPath]; !ok {
				parent[n.ImportPath] = ""
				queue = append(queue, n.ImportPath)
			}
		}
	}
	var out []Violation
	seen := make(map[string]bool)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		n, ok := byPath[cur]
		if !ok {
			continue
		}
		if !exempt(n) && !seen[n.ImportPath] && slices.Contains(n.Imports, "unsafe") &&
			!Allowlisted(allowlist, n.ImportPath) {
			seen[n.ImportPath] = true
			chain := chainTo(parent, n.ImportPath)
			chain = append(chain, "unsafe")
			out = append(out, Violation{Dep: n.ImportPath, Chain: chain})
		}
		for _, imp := range n.Imports {
			if _, ok := parent[imp]; !ok {
				parent[imp] = cur
				queue = append(queue, imp)
			}
		}
	}
	slices.SortFunc(out, func(a, b Violation) int { return strings.Compare(a.Dep, b.Dep) })
	return out
}

func exempt(n Node) bool {
	return n.Goroot || n.Main || n.ImportPath == "" || n.ImportPath == "unsafe"
}

func chainTo(parent map[string]string, dep string) []string {
	var rev []string
	for cur := dep; cur != ""; cur = parent[cur] {
		rev = append(rev, cur)
	}
	slices.Reverse(rev)
	return rev
}

func Allowlisted(allowlist []string, importPath string) bool {
	for _, pattern := range allowlist {
		if matchGlob(pattern, importPath) {
			return true
		}
	}
	return false
}

func matchGlob(pattern, importPath string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "/**"); ok {
		if importPath == prefix || strings.HasPrefix(importPath, prefix+"/") {
			return true
		}
	}
	if !strings.Contains(pattern, "**") {
		ok, err := path.Match(pattern, importPath)
		return err == nil && ok
	}
	return matchSegPatterns(strings.Split(pattern, "/"), strings.Split(importPath, "/"))
}

func matchSegPatterns(pats, segs []string) bool {
	if len(pats) == 0 {
		return len(segs) == 0
	}
	if pats[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegPatterns(pats[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	ok, err := path.Match(pats[0], segs[0])
	if err != nil || !ok {
		return false
	}
	return matchSegPatterns(pats[1:], segs[1:])
}

func FormatViolation(v Violation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "ERROR [capabilities::unauthorized_unsafe]: Transitive dependency '%s' imports 'unsafe'\n", v.Dep)
	b.WriteString("  --> Import chain (cmd/go capabilities audit):\n")
	for i, frame := range v.Chain {
		if i == 0 {
			fmt.Fprintf(&b, "   %d. %s\n", i+1, frame)
			continue
		}
		fmt.Fprintf(&b, "   %d.   -> %s\n", i+1, frame)
	}
	fmt.Fprintf(&b, " = Help: Remove the dependency or explicitly authorize \"%s/**\" under 'transitive_unsafe_allowlist' in logos.toml.", v.Dep)
	return b.String()
}

func FormatWarning(v Violation) string {
	return "go: warning: " + FormatViolation(v)
}

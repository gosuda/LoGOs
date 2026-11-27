// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"strings"
	"testing"
)

func TestH2ExtendedConnectEmitsStockForm(t *testing.T) {
	const in = `package main

import "net/http"

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect()") {
		t.Errorf("direct call survives:\n%s", got)
	}
	if !strings.Contains(got, "http2xconnect=1") {
		t.Errorf("no GODEBUG guard:\n%s", got)
	}
	if !strings.Contains(got, `"os"`) || !strings.Contains(got, `"strings"`) {
		t.Errorf("missing injected imports:\n%s", got)
	}
	if !strings.Contains(got, "_ = s") {
		t.Errorf("receiver not preserved:\n%s", got)
	}
	if !strings.Contains(got, `\"s\"`) {
		t.Errorf("panic does not name the server:\n%s", got)
	}
}

func TestH2ExtendedConnectSkipsOverlappingImportEdit(t *testing.T) {
	const in = `package main

import "net/http"
import "comptime"

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "http2xconnect=1") {
		t.Errorf("overlapping rewrite applied:\n%s", got)
	}
	if strings.Contains(got, `"comptime"`) {
		t.Errorf("non-overlapping import edit dropped:\n%s", got)
	}
	if !strings.Contains(got, "s.EnableHTTP2ExtendedConnect()") {
		t.Errorf("skipped call lost:\n%s", got)
	}
}

func TestH2ExtendedConnectKeepsExistingImports(t *testing.T) {
	const in = `package main

import (
	"net/http"
	"os"
	"strings"
)

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Count(got, `"os"`) != 1 || strings.Count(got, `"strings"`) != 1 {
		t.Errorf("duplicate imports:\n%s", got)
	}
}

func TestH2ExtendedConnectSkipsForeignFiles(t *testing.T) {
	const in = `package main

func enable(s *thing) {
	s.EnableHTTP2ExtendedConnect()
}

type thing struct{}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	if string(out) != in {
		t.Errorf("file without net/http changed:\n%s", out)
	}
}

func TestH2ExtendedConnectSkipsTempReceivers(t *testing.T) {
	for _, src := range []string{
		"package main\n\nimport \"net/http\"\n\nfunc enable() {\n\t_logos_site0.EnableHTTP2ExtendedConnect()\n}\n",
		"package main\n\nimport \"net/http\"\n\nfunc enable(s *http.Server) {\n\ts.EnableHTTP2ExtendedConnect(x)\n}\n",
	} {
		out, _, ok := remapEjectedFile([]byte(src), "", nil)
		if !ok {
			t.Fatal("remapEjectedFile failed")
		}
		if string(out) != src {
			t.Errorf("call wrongly rewritten:\n%s", out)
		}
	}
}

func TestH2ExtendedConnectSkipsDotBlankBindings(t *testing.T) {
	const in = `package main

import (
	"net/http"
	_ "os"
	. "strings"
)

func enable(s *http.Server) {
	_ = TrimSpace("x")
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect()") {
		t.Errorf("direct call survives:\n%s", got)
	}
	if !strings.Contains(got, "logosH2Os \"os\"") || !strings.Contains(got, "logosH2Strings \"strings\"") {
		t.Errorf("missing fresh named imports:\n%s", got)
	}
	if strings.Contains(got, "..") || strings.Contains(got, "_.") {
		t.Errorf("dot or blank binding used as qualifier:\n%s", got)
	}
}

func TestH2ExtendedConnectClosureAvoidsShadowableAny(t *testing.T) {
	const in = `package main

import "net/http"

type any = string

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect()") {
		t.Errorf("direct call survives:\n%s", got)
	}
	if !strings.Contains(got, "interface{})") {
		t.Errorf("closure parameter does not use the unshadowable type:\n%s", got)
	}
	if strings.Contains(got, " any)") {
		t.Errorf("closure parameter uses shadowable any:\n%s", got)
	}
}

func TestH2ExtendedConnectPanicReceiver(t *testing.T) {
	const in = `package main

import "net/http"

func enable(panic *http.Server) {
	panic.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "", nil)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect()") {
		t.Errorf("direct call survives:\n%s", got)
	}
	if strings.Contains(got, "func(panic ") {
		t.Errorf("parameter shadows builtin panic:\n%s", got)
	}
	if !strings.Contains(got, "panic(") {
		t.Errorf("generated guard lost its panic call:\n%s", got)
	}
}

func TestH2ExtendedConnectReservesSiblingNames(t *testing.T) {
	const in = `package main

import "net/http"

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	extra := map[string]bool{"logosH2Os": true, "logosH2Strings": true}
	out, _, ok := remapEjectedFile([]byte(in), "", extra)
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect()") {
		t.Errorf("direct call survives:\n%s", got)
	}
	if !strings.Contains(got, "logosH2Os2 \"os\"") || !strings.Contains(got, "logosH2Strings2 \"strings\"") {
		t.Errorf("aliases do not avoid sibling names:\n%s", got)
	}
}

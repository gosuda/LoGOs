// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package eject

import (
	"strings"
	"testing"
)

func TestH2ExtendedConnectEmitsSupportDelegation(t *testing.T) {
	const in = `package main

import "net/http"

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	out, uses, ok := remapEjectedFile([]byte(in), "")
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	if !uses {
		t.Error("support import not reported as used")
	}
	got := string(out)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect()") {
		t.Errorf("direct call survives:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp.RequireExtendedConnect("s", s)`) {
		t.Errorf("no support delegation naming the server:\n%s", got)
	}
	if strings.Contains(got, "strings.Contains(os.Getenv") {
		t.Errorf("inline GODEBUG parsing survives:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp "github.com/gosuda/loGos/rt/net/http"`) {
		t.Errorf("missing aliased support import:\n%s", got)
	}
}

func TestH2ExtendedConnectReceiverEvaluatesAtCallSite(t *testing.T) {
	// Planted fault: the rewrite moved receiver evaluation into the
	// generated closure, so go and defer calls evaluated it late.
	const in = `package main

import "net/http"

func enable(s *http.Server) {
	go s.EnableHTTP2ExtendedConnect()
	defer s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "")
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if !strings.Contains(got, "go ejectedhttp.RequireExtendedConnect(\"s\", s)") {
		t.Errorf("go call keeps late evaluation:\n%s", got)
	}
	if !strings.Contains(got, "defer ejectedhttp.RequireExtendedConnect(\"s\", s)") {
		t.Errorf("defer call keeps late evaluation:\n%s", got)
	}
}

func TestH2ExtendedConnectOverlapFailsRemap(t *testing.T) {
	const in = `package main

import "net/http"
import "comptime"

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	// An overlap must fail the remap so the caller preserves the
	// original bytes; reporting success writes a partial rewrite.
	out, _, ok := remapEjectedFile([]byte(in), "")
	if ok || out != nil {
		t.Errorf("overlapping remap reported success (ok=%v out=%q); copyDataFile would write a partial rewrite", ok, out)
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
	out, _, ok := remapEjectedFile([]byte(in), "")
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Count(got, `"os"`) != 1 || strings.Count(got, `"strings"`) != 1 {
		t.Errorf("existing imports disturbed:\n%s", got)
	}
	if strings.Count(got, `ejectedhttp "github.com/gosuda/loGos/rt/net/http"`) != 1 {
		t.Errorf("support import missing or duplicated:\n%s", got)
	}
}

func TestH2ExtendedConnectReusesExistingSupportImport(t *testing.T) {
	const in = `package main

import (
	"net/http"
	ejectedhttp "github.com/gosuda/loGos/rt/net/http"
)

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "")
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Count(got, `"github.com/gosuda/loGos/rt/net/http"`) != 1 {
		t.Errorf("support import duplicated:\n%s", got)
	}
}

func TestH2ExtendedConnectSkipsForeignFiles(t *testing.T) {
	const in = `package main

func enable(s *thing) {
	s.EnableHTTP2ExtendedConnect()
}

type thing struct{}
`
	out, _, ok := remapEjectedFile([]byte(in), "")
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
		out, _, ok := remapEjectedFile([]byte(src), "")
		if !ok {
			t.Fatal("remapEjectedFile failed")
		}
		if string(out) != src {
			t.Errorf("call wrongly rewritten:\n%s", out)
		}
	}
}

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
	out, _, ok := remapEjectedFile([]byte(in), "")
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "EnableHTTP2ExtendedConnect()") {
		t.Errorf("direct call survives:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp.RequireExtendedConnect("s", s)`) {
		t.Errorf("no helper call with the receiver as argument:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp "github.com/gosuda/loGos/rt/net/http"`) {
		t.Errorf("missing ejectedhttp support import:\n%s", got)
	}
	if strings.Contains(got, "os.Getenv") || strings.Contains(got, "strings.Contains") {
		t.Errorf("guard inlined with file bindings instead of the helper:\n%s", got)
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
	out, _, ok := remapEjectedFile([]byte(in), "")
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
	out, _, ok := remapEjectedFile([]byte(in), "")
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Count(got, `"os"`) != 1 || strings.Count(got, `"strings"`) != 1 {
		t.Errorf("duplicate imports:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp "github.com/gosuda/loGos/rt/net/http"`) {
		t.Errorf("missing ejectedhttp support import:\n%s", got)
	}
	if strings.Contains(got, "os.Getenv") || strings.Contains(got, "strings.Contains") {
		t.Errorf("guard inlined with file bindings instead of the helper:\n%s", got)
	}
}

func TestH2ExtendedConnectAvoidsFileBindings(t *testing.T) {
	const in = `package main

import (
	"net/http"
	sys "os"
)

var mode = sys.Getenv("H2XCONNECT_MODE")

func enable(s *http.Server) {
	strings := "shadow"
	_ = strings
	_ = mode
	s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "")
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "os.Getenv") || strings.Contains(got, "strings.Contains") {
		t.Errorf("rewrite uses the file's os/strings bindings:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp.RequireExtendedConnect("s", s)`) {
		t.Errorf("no hygienic helper call with the receiver as argument:\n%s", got)
	}
	if !strings.Contains(got, `ejectedhttp "github.com/gosuda/loGos/rt/net/http"`) {
		t.Errorf("missing ejectedhttp support import:\n%s", got)
	}
}

func TestH2ExtendedConnectEvaluatesReceiverEagerly(t *testing.T) {
	const in = `package main

import "net/http"

func enable(s *http.Server) {
	s.EnableHTTP2ExtendedConnect()
	defer s.EnableHTTP2ExtendedConnect()
	go s.EnableHTTP2ExtendedConnect()
}
`
	out, _, ok := remapEjectedFile([]byte(in), "")
	if !ok {
		t.Fatal("remapEjectedFile failed")
	}
	got := string(out)
	if strings.Contains(got, "func() {") {
		t.Errorf("receiver moved inside a deferred closure:\n%s", got)
	}
	if !strings.Contains(got, `defer ejectedhttp.RequireExtendedConnect("s", s)`) {
		t.Errorf("deferred call does not evaluate the receiver as an argument:\n%s", got)
	}
	if !strings.Contains(got, `go ejectedhttp.RequireExtendedConnect("s", s)`) {
		t.Errorf("go call does not evaluate the receiver as an argument:\n%s", got)
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

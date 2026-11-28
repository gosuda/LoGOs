// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build logosejected

package http

import (
	"errors"
	stdhttp "net/http"
	"strings"
	"testing"
)

// setEjectedGodebug pins the startup GODEBUG snapshot for one test and
// restores it after, so each test starts from a known snapshot no matter
// what environment the test process started with.
func setEjectedGodebug(t *testing.T, env string) {
	t.Helper()
	old := ejectedStartupGodebug
	ejectedStartupGodebug = env
	t.Cleanup(func() { ejectedStartupGodebug = old })
}

func TestRequireExtendedConnectFailsClosed(t *testing.T) {
	setEjectedGodebug(t, "")
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("RequireExtendedConnect did not fail")
		}
		if msg, ok := r.(string); !ok || !strings.Contains(msg, "http2xconnect=1") {
			t.Fatalf("panic = %v, want GODEBUG notice", r)
		}
	}()
	RequireExtendedConnect("srv", nil)
}

func TestRequireExtendedConnectIgnoresLateSetenv(t *testing.T) {
	setEjectedGodebug(t, "")
	// Planted fault: the ejected opt-in reads GODEBUG at call time, so a
	// value set in main after HTTP/2 already sampled the flag reports
	// success while the server never advertises extended CONNECT.
	t.Setenv("GODEBUG", "http2xconnect=1")
	defer func() {
		if recover() == nil {
			t.Fatal("late GODEBUG reported success")
		}
	}()
	RequireExtendedConnect("srv", nil)
}

func TestH2ExtendedConnectEjectedNeedsGodebug(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		want error
	}{
		{name: "unset", env: "", want: errHTTP2ExtendedConnectNeedsGodebug},
		{name: "off", env: "http2xconnect=0", want: errHTTP2ExtendedConnectNeedsGodebug},
		{name: "other key", env: "http2debug=1", want: errHTTP2ExtendedConnectNeedsGodebug},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setEjectedGodebug(t, tc.env)
			got := EnableHTTP2ExtendedConnect(&stdhttp.Server{Addr: "127.0.0.1:0"})
			if !errors.Is(got, tc.want) {
				t.Fatalf("EnableHTTP2ExtendedConnect with startup GODEBUG=%q = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

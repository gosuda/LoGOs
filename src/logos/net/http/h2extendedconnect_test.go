// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	stdhttp "net/http"
	"strings"
	"testing"
)

func TestH2ExtendedConnectEnableReturnsNil(t *testing.T) {
	s := &stdhttp.Server{Addr: "127.0.0.1:0"}
	if err := EnableHTTP2ExtendedConnect(s); err != nil {
		t.Fatalf("EnableHTTP2ExtendedConnect = %v, want nil", err)
	}
}

func TestGodebugHas(t *testing.T) {
	for _, tc := range []struct {
		name  string
		env   string
		key   string
		value string
		want  bool
	}{
		{name: "empty", env: "", key: "http2xconnect", value: "1", want: false},
		{name: "exact", env: "http2xconnect=1", key: "http2xconnect", value: "1", want: true},
		{name: "among others", env: "http2debug=1,http2xconnect=1", key: "http2xconnect", value: "1", want: true},
		{name: "disabled", env: "http2xconnect=0", key: "http2xconnect", value: "1", want: false},
		{name: "other key", env: "http2debug=1", key: "http2xconnect", value: "1", want: false},
		{name: "prefix key", env: "xhttp2xconnect=1", key: "http2xconnect", value: "1", want: false},
		{name: "last wins on", env: "http2xconnect=0,http2xconnect=1", key: "http2xconnect", value: "1", want: true},
		{name: "last wins off", env: "http2xconnect=1,http2xconnect=0", key: "http2xconnect", value: "1", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GODEBUG", tc.env)
			if got := godebugHas(tc.key, tc.value); got != tc.want {
				t.Fatalf("godebugHas(%q, %q) with GODEBUG=%q = %t, want %t", tc.key, tc.value, tc.env, got, tc.want)
			}
		})
	}
}

func TestGodebugHasUpstreamParity(t *testing.T) {
	// Upstream HTTP/2 enables extended CONNECT when the startup GODEBUG
	// merely contains "http2xconnect=1" (a substring match in
	// net/http/internal/http2), while the ejected guard requires the
	// last setting to equal it. The guard must never allow what
	// upstream would refuse to advertise; where it refuses what
	// upstream would advertise, it fails closed and names the setting.
	for _, tc := range []struct {
		name     string
		env      string
		ejected  bool
		upstream bool
	}{
		{name: "empty", env: "", ejected: false, upstream: false},
		{name: "exact", env: "http2xconnect=1", ejected: true, upstream: true},
		{name: "among others", env: "http2debug=1,http2xconnect=1", ejected: true, upstream: true},
		{name: "disabled", env: "http2xconnect=0", ejected: false, upstream: false},
		{name: "other key", env: "http2debug=1", ejected: false, upstream: false},
		{name: "prefix key", env: "xhttp2xconnect=1", ejected: false, upstream: true},
		{name: "last wins on", env: "http2xconnect=0,http2xconnect=1", ejected: true, upstream: true},
		{name: "last wins off", env: "http2xconnect=1,http2xconnect=0", ejected: false, upstream: true},
		{name: "substring value", env: "http2xconnect=10", ejected: false, upstream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ejected := godebugHasOn(tc.env, "http2xconnect", "1")
			upstream := strings.Contains(tc.env, "http2xconnect=1")
			if ejected != tc.ejected {
				t.Fatalf("godebugHasOn(%q) = %t, want %t", tc.env, ejected, tc.ejected)
			}
			if upstream != tc.upstream {
				t.Fatalf("upstream advertisement for %q = %t, want %t", tc.env, upstream, tc.upstream)
			}
			if ejected && !upstream {
				t.Fatalf("guard allows %q while upstream would not advertise extended CONNECT", tc.env)
			}
		})
	}
}

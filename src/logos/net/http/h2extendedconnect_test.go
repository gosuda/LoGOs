// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	stdhttp "net/http"
	"testing"
)

func TestH2ExtendedConnectEnableReturnsNil(t *testing.T) {
	t.Setenv("GODEBUG", "http2xconnect=1")
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

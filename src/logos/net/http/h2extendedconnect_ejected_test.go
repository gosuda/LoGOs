// Copyright 2026 The Go Authors. All rights reserved.
// Use of this code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build logosejected

package http

import (
	"errors"
	stdhttp "net/http"
	"testing"
)

func TestH2ExtendedConnectEjectedNeedsGodebug(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  string
		want error
	}{
		{name: "unset", env: "", want: errHTTP2ExtendedConnectNeedsGodebug},
		{name: "off", env: "http2xconnect=0", want: errHTTP2ExtendedConnectNeedsGodebug},
		{name: "other key", env: "http2debug=1", want: errHTTP2ExtendedConnectNeedsGodebug},
		{name: "on", env: "http2xconnect=1", want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("GODEBUG", tc.env)
			got := EnableHTTP2ExtendedConnect(&stdhttp.Server{Addr: "127.0.0.1:0"})
			if !errors.Is(got, tc.want) {
				t.Fatalf("EnableHTTP2ExtendedConnect with GODEBUG=%q = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

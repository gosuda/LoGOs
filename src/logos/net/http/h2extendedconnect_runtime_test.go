// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !logosejected

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

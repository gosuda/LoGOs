// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !logosejected

package http

import (
	"fmt"
	stdhttp "net/http"
)

func h2extendedConnectEnable(s *stdhttp.Server) error {
	s.EnableHTTP2ExtendedConnect()
	return nil
}

// RequireExtendedConnect enables extended CONNECT on the server recv
// names. It is the native spelling of the ejected guard: natively the
// per-server opt-in applies directly, so no process-startup GODEBUG
// setting is needed; ejected, the same call requires that setting and
// fails fast without it. Either way the named server serves extended
// CONNECT when the call returns.
func RequireExtendedConnect(server string, recv any) {
	s, ok := recv.(*stdhttp.Server)
	if !ok {
		panic(fmt.Sprintf("logos/net/http: RequireExtendedConnect needs (*net/http.Server) for server %q, got %T", server, recv))
	}
	s.EnableHTTP2ExtendedConnect()
}

// RequireExtendedConnectFunc is the native spelling of a method value
// over (*net/http.Server).EnableHTTP2ExtendedConnect. The receiver passes
// once as an argument, so it keeps its single eager evaluation; the
// returned closure runs the same guard when called.
func RequireExtendedConnectFunc(name string, recv any) func() {
	return func() { RequireExtendedConnect(name, recv) }
}

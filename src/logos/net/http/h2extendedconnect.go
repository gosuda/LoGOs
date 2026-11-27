// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package http

import (
	stdhttp "net/http"
	"os"
	"strings"
)

// EnableHTTP2ExtendedConnect opts one HTTP server into extended CONNECT
// (RFC 8441), so handlers can serve WebSocket over HTTP/2 on that server.
//
// Native builds call (*net/http.Server).EnableHTTP2ExtendedConnect, which is
// a per-server opt-in the fork added: the server then advertises
// SETTINGS_ENABLE_CONNECT_PROTOCOL and passes ":protocol" requests to the
// handler. Call it before serving, and only when the handler supports
// extended CONNECT.
//
// Ejected builds compile with stock Go, which has no per-server opt-in and
// offers only the process-global GODEBUG=http2xconnect=1. The ejected form
// keeps the call site compiling and requires that setting in the process
// startup environment, failing fast otherwise, so ejected servers keep the
// behavior instead of silently serving without it. Run ejected binaries and
// their tests with GODEBUG=http2xconnect=1 in the environment.
//
// Extended CONNECT over HTTP/3 stays unbuilt: the H3 server in this package
// answers it with 501 and never passes it to the router.
func EnableHTTP2ExtendedConnect(s *stdhttp.Server) error {
	return h2extendedConnectEnable(s)
}

// godebugHasOn reports whether key's last setting in env equals value.
func godebugHasOn(env, key, value string) bool {
	seen := false
	matched := false
	for _, setting := range strings.Split(env, ",") {
		name, val, _ := strings.Cut(setting, "=")
		if name != key {
			continue
		}
		seen = true
		matched = val == value
	}
	return seen && matched
}

// godebugHas reports whether key's last GODEBUG setting equals value.
// Shared by both variants so the parsing stays identical.
func godebugHas(key, value string) bool {
	return godebugHasOn(os.Getenv("GODEBUG"), key, value)
}

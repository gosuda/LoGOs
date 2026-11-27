// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build logosejected

package http

import (
	"errors"
	stdhttp "net/http"
	"os"
	"strconv"
)

// ejectedStartupGodebug captures the process-start GODEBUG value. Stock
// HTTP/2 samples the flag once at initialization, so a value set later in
// main never takes effect; the ejected checks consult the startup value so
// a late opt-in fails fast instead of reporting success.
var ejectedStartupGodebug = os.Getenv("GODEBUG")

// errHTTP2ExtendedConnectNeedsGodebug reports the missing opt-in stock Go
// requires. The ejected form cannot enable extended CONNECT per server,
// so it fails fast instead of serving without the feature.
var errHTTP2ExtendedConnectNeedsGodebug = errors.New("logos/net/http: extended CONNECT needs GODEBUG=http2xconnect=1 in the ejected process environment")

// RequireExtendedConnect fails fast unless the stock process-global
// GODEBUG=http2xconnect=1 was in effect at process startup. The server
// name comes from the rewritten call site and only names the failure.
func RequireExtendedConnect(server string, recv any) {
	_ = recv
	if !godebugHasOn(ejectedStartupGodebug, "http2xconnect", "1") {
		panic("net/http: extended CONNECT on server " + strconv.Quote(server) + " needs GODEBUG=http2xconnect=1 in the ejected process environment")
	}
}

// RequireExtendedConnectFunc is the eject spelling of a method value over
// (*net/http.Server).EnableHTTP2ExtendedConnect, which group lowering binds
// before spawning its child. The receiver passes once as an argument, so it
// keeps its single eager evaluation; the returned closure runs the same
// guard when the spawned child calls it.
func RequireExtendedConnectFunc(name string, recv any) func() {
	return func() { RequireExtendedConnect(name, recv) }
}

func h2extendedConnectEnable(s *stdhttp.Server) error {
	_ = s
	if !godebugHasOn(ejectedStartupGodebug, "http2xconnect", "1") {
		return errHTTP2ExtendedConnectNeedsGodebug
	}
	return nil
}

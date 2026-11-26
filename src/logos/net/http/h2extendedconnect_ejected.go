// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build logosejected

package http

import (
	"errors"
	stdhttp "net/http"
)

// errHTTP2ExtendedConnectNeedsGodebug reports the missing opt-in stock Go
// requires. The ejected form cannot enable extended CONNECT per server,
// so it fails fast instead of serving without the feature.
var errHTTP2ExtendedConnectNeedsGodebug = errors.New("logos/net/http: extended CONNECT needs GODEBUG=http2xconnect=1 in the ejected process environment")

func h2extendedConnectEnable(s *stdhttp.Server) error {
	_ = s
	if !godebugHas("http2xconnect", "1") {
		return errHTTP2ExtendedConnectNeedsGodebug
	}
	return nil
}

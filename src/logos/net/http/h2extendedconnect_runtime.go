// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:build !logosejected

package http

import (
	stdhttp "net/http"
)

func h2extendedConnectEnable(s *stdhttp.Server) error {
	s.EnableHTTP2ExtendedConnect()
	return nil
}

// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package logos

import "testing"

func TestUpstreamAPIFields(t *testing.T) {
	// These shapes occur in api/go1.txt and api/go1.22.txt. Braces in
	// a field type or generic constraint do not introduce type members.
	data := []byte(`pkg container/list, type Element struct
pkg container/list, type Element struct, Value interface{}
pkg crypto/x509, type Certificate struct, PublicKey interface{}
pkg database/sql, type Null[$0 interface{}] struct #60370
pkg database/sql, type Null[$0 interface{}] struct, V $0 #60370
pkg database/sql, type Null[$0 interface{}] struct, Valid bool #60370
pkg example/generic, type Pair[$0 interface{}, $1 interface{}] struct, Item $1
pkg io, type Reader interface { Read }
pkg io, type ReadCloser interface { Close, Read }
`)
	table := &upstreamAPI{pkgs: make(map[string]struct{}), names: make(map[string]struct{})}
	parseUpstreamAPIFile(data, table)
	for _, test := range []struct{ path, name string }{
		{"container/list", "Element"},
		{"container/list", "Value"},
		{"crypto/x509", "PublicKey"},
		{"database/sql", "Null"},
		{"database/sql", "V"},
		{"database/sql", "Valid"},
		{"example/generic", "Item"},
		{"io", "Read"},
		{"io", "Close"},
	} {
		t.Run(test.path+"."+test.name, func(t *testing.T) {
			if !table.hasName(test.path, test.name) {
				t.Errorf("upstream API omitted public member %s.%s", test.path, test.name)
			}
		})
	}
	if table.hasName("net/http", "EnableHTTP2ExtendedConnect") {
		t.Fatal("unmapped fork member was added to upstream API")
	}
}

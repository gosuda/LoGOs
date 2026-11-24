// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ld

import (
	"strings"
	"testing"
)

var testLogosCtID = strings.Repeat("0123456789abcdef", 4)

func TestLogosCloneBaseName(t *testing.T) {
	id := testLogosCtID
	checks := []struct {
		label string
		input string
		want  string
	}{
		{label: "instance", input: "main.autowire.logosct." + id, want: "main.autowire"},
		{label: "packagePath", input: "example.com/p.Autowire.logosct." + id, want: "example.com/p.Autowire"},
		{label: "closure", input: "example.com/p.Autowire.logosct." + id + ".func1", want: "example.com/p.Autowire.func1"},
		{label: "abiWrapper", input: "example.com/p.Autowire.logosct." + id + ".abi0", want: "example.com/p.Autowire.abi0"},
		{label: "nestedInstances", input: "p.F.logosct." + id + ".G.logosct." + id, want: "p.F.G"},
		{label: "shortID", input: "p.F.logosct." + id[:63], want: "p.F.logosct." + id[:63]},
		{label: "longID", input: "p.F.logosct." + id + "0", want: "p.F.logosct." + id + "0"},
		{label: "upperHex", input: "p.F.logosct." + strings.ToUpper(id), want: "p.F.logosct." + strings.ToUpper(id)},
		{label: "letterAfterID", input: "p.F.logosct." + id + "x", want: "p.F.logosct." + id + "x"},
		{label: "underscoreAfterID", input: "p.F.logosct." + id + "_x", want: "p.F.logosct." + id + "_x"},
		{label: "noBase", input: ".logosct." + id, want: ".logosct." + id},
		{label: "noID", input: "p.F.logosct.", want: "p.F.logosct."},
		{label: "methodNamedLogosct", input: "p.T.logosct.func1", want: "p.T.logosct.func1"},
		{label: "ordinaryFunction", input: "main.main", want: "main.main"},
		{label: "runtimeWrapper", input: "runtime.cgocall.abi0", want: "runtime.cgocall.abi0"},
		{label: "sourceSuffix", input: "example.com/p.Foo_logosct1", want: "example.com/p.Foo_logosct1"},
	}

	for _, tc := range checks {
		t.Run(tc.label, func(t *testing.T) {
			if got := logosCloneBaseName(tc.input); got != tc.want {
				t.Fatalf("logosCloneBaseName(%q) = %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

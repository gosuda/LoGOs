// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package facts_test

import (
	"strings"
	"testing"

	"cmd/compile/internal/logos/facts"
	"cmd/internal/goobj"
)

func TestDefaultHelperWireRoundTrip(t *testing.T) {
	const wire = "logos-facts 3\nfingerprint 0001020304050607\n" +
		"default Config Port 4\n8080\n" +
		"default hidden Value 1\n7\n" +
		"defaulthelper Config Logos_Default_Config\n" +
		"defaulthelper hidden Logos_Default_hidden\n"
	f, err := facts.Decode([]byte(wire))
	if err != nil {
		t.Fatalf("decode producer default helpers: %v", err)
	}
	if len(f.Defaults) != 2 {
		t.Fatalf("decoded default descriptions = %d, want 2", len(f.Defaults))
	}
	if got := string(facts.Encode(f.Fingerprint, f)); got != wire {
		t.Fatalf("default helper round trip:\n got %q\nwant %q", got, wire)
	}
	const path = "example.com/defaulthelper/complete"
	if _, err := facts.Import(path, string(f.Fingerprint[:]), []byte(wire)); err != nil {
		t.Fatalf("import complete producer default helpers: %v", err)
	}
	if _, ok := facts.Lookup(path); !ok {
		t.Fatal("complete imported default helpers were not registered")
	}
}

func TestDefaultHelperWireRejectsDuplicateType(t *testing.T) {
	for _, second := range []string{"Logos_Default_Config", "Logos_Default_Other"} {
		t.Run(second, func(t *testing.T) {
			wire := dupHead() + "defaulthelper Config Logos_Default_Config\n" +
				"defaulthelper Config " + second + "\n"
			_, err := facts.Decode([]byte(wire))
			if err == nil || !strings.Contains(err.Error(), "uplicate") ||
				!strings.Contains(err.Error(), "Config") {
				t.Fatalf("duplicate helper mapping error = %v, want Config duplication", err)
			}
		})
	}
}

func TestDefaultHelperWireRejectsMalformed(t *testing.T) {
	for _, record := range []string{
		"defaulthelper",
		"defaulthelper Config",
		"defaulthelper Config Logos_Default_Config extra",
		"defaulthelper (*Config).Method Logos_Default_Config",
		"defaulthelper Config.Make Logos_Default_Config",
		"defaulthelper Config 1helper",
		"defaulthelper Config privateHelper",
		"defaulthelper Config MakeConfig",
		"defaulthelper Config other.Logos_Default_Config",
		"defaulthelper Config Logos_Default_Config()",
		"defaulthelper Config Logos_Default_Config@simd0",
	} {
		t.Run(record, func(t *testing.T) {
			_, err := facts.Decode([]byte(dupHead() + record + "\n"))
			if err == nil {
				t.Fatal("malformed default helper record was accepted")
			}
			msg := err.Error()
			if strings.Contains(msg, "unknown record") || !strings.Contains(msg, "line 3") ||
				(!strings.Contains(msg, "bad") && !strings.Contains(msg, "invalid")) {
				t.Fatalf("malformed helper error = %q, want a line 3 syntax or identifier diagnostic", msg)
			}
		})
	}
}

func TestDefaultHelperImportRejectsMissingMapping(t *testing.T) {
	fp := goobj.FingerprintType{0, 1, 2, 3, 4, 5, 6, 7}
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "legacy expression only",
			body: "default Config Port 4\n8080\n",
			want: "Config",
		},
		{
			name: "second type lacks helper",
			body: "default Config Port 4\n8080\n" +
				"default Other Value 1\n7\n" +
				"defaulthelper Config Logos_Default_Config\n",
			want: "Other",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := "example.com/defaulthelper/" + strings.ReplaceAll(tc.name, " ", "-")
			wire := dupHead() + tc.body
			_, err := facts.Import(path, string(fp[:]), []byte(wire))
			if err == nil || !strings.Contains(err.Error(), tc.want) ||
				!strings.Contains(err.Error(), "helper") || strings.Contains(err.Error(), "unknown record") {
				t.Errorf("missing helper import error = %v, want missing %s helper", err, tc.want)
			}
			if _, ok := facts.Lookup(path); ok {
				t.Error("rejected default metadata was registered")
			}
		})
	}
}

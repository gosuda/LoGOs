// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package lsp

import (
	"encoding/json"

	"cmd/go/internal/logos/hints"
)

func codeActionsAdapter(diags [][]byte, src []byte, rng json.RawMessage) ([]hints.CodeAction, error) {
	var r hints.Range
	if err := json.Unmarshal(rng, &r); err != nil {
		return nil, err
	}
	ds := make([]hints.Diagnostic, 0, len(diags))
	for _, line := range diags {
		d, err := hints.DecodeDiagnostic(line)
		if err != nil {
			continue
		}
		ds = append(ds, d)
	}
	return hints.CodeActions(ds, src, r), nil
}

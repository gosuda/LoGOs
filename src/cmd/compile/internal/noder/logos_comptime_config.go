// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package noder

import (
	"cmd/compile/internal/base"
	"cmd/compile/internal/types2"
)

func logosConfigureComptime(conf *types2.Config, info *types2.Info) {
	if !base.Logos || base.LogosCfg == "" {
		return
	}
	policy := logosLintPolicy()
	conf.LogosCTFEMaxSteps = policy.Thresholds.MaxCTFESteps
	info.LogosCTFEMaxSteps = conf.LogosCTFEMaxSteps
	conf.LogosComptimeNodeLimit = base.LogosComptimeNodes
}

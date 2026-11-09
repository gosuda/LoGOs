// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package cfg

import (
	"os"
	"strings"

	"cmd/go/internal/logos/settings"
)

func recomputeExperiment() {
	kept := BuildContext.ToolTags[:0:0]
	for _, tag := range BuildContext.ToolTags {
		if !strings.HasPrefix(tag, "goexperiment.") {
			kept = append(kept, tag)
		}
	}
	BuildContext.ToolTags = kept
	computeExperiment()
}

// logosApplyRawExperiment installs raw as the effective GOEXPERIMENT,
// recomputes the experiment tags and the clean cache-key value, and
// publishes the value to the process and tool environments. Both the arena
// composition and the preemptible refresh propagate to the compiler
// subprocesses through it.
func logosApplyRawExperiment(raw string) {
	if raw == RawGOEXPERIMENT {
		return
	}
	RawGOEXPERIMENT = raw
	recomputeExperiment()
	os.Setenv("GOEXPERIMENT", RawGOEXPERIMENT)
	for i := range CmdEnv {
		if CmdEnv[i].Name == "GOEXPERIMENT" {
			CmdEnv[i].Value = RawGOEXPERIMENT
		}
	}
	// Mirror into OrigEnv: main.invoke snapshots OrigEnv before MkEnv
	// publishes the managed defaults, and user binaries (go test, go run)
	// execute with OrigEnv, not CmdEnv. Without this, test-spawned child
	// builds never see the default: the outer test binary compiles with
	// the default on while an inner plain-go build compiles the fallback.
	mirrored := false
	for i := range OrigEnv {
		if strings.HasPrefix(OrigEnv[i], "GOEXPERIMENT=") {
			OrigEnv[i] = "GOEXPERIMENT=" + raw
			mirrored = true
			break
		}
	}
	if !mirrored {
		OrigEnv = append(OrigEnv, "GOEXPERIMENT="+raw)
	}
}

func RefreshLogosPreemptible() {
	if ExperimentErr != nil {
		return
	}
	if raw, ok := settings.PreemptibleDefault(RawGOEXPERIMENT, logosInitGOEXPERIMENT, Experiment.PreemptibleLoops, Logos, Goarch); ok {
		logosApplyRawExperiment(raw)
	}
}

func DropLogosExperiments() {
	os.Setenv("GOEXPERIMENT", settings.DropLogosExperiments(os.Getenv("GOEXPERIMENT")))
}

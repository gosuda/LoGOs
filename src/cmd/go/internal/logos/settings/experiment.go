// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package settings resolves every logos build setting in one place: the
// command-line flags, the startup environment, and the logos.toml of each
// main module root. It imports only cmd/go/internal/logos/config and the
// standard library, so cfg can import it and keep writing the environment
// state it owns.
package settings

import "strings"

const (
	arenaExperiment            = "logosarena"
	preemptibleLoopsExperiment = "preemptibleloops"
)

// WithArena appends the arena experiment to a GOEXPERIMENT value. The
// later entry wins over an earlier nologosarena, so a repeated append
// keeps the arena enabled.
func WithArena(raw string) string {
	return appendExperiment(raw, arenaExperiment)
}

// PreemptibleDefault resolves the logos wasm default for the
// preemptibleloops experiment: it appends the experiment under
// GOARCH=wasm unless startup mentions it, and strips the tokens it could
// have appended once the target stops wanting it. It reports whether the
// caller must apply wantRaw as the effective GOEXPERIMENT value.
func PreemptibleDefault(raw, startup string, has, logos bool, goarch string) (wantRaw string, apply bool) {
	mentioned, _ := preemptibleUserState(startup)
	want := logos && goarch == "wasm" && !mentioned
	switch {
	case want && !has:
		return appendExperiment(raw, preemptibleLoopsExperiment), true
	case !want && has && !mentioned:
		return stripExperiment(raw, preemptibleLoopsExperiment), true
	}
	return "", false
}

// DropLogosExperiments removes every logos and nologos experiment token
// from a GOEXPERIMENT value, leaving the user's own experiments alone.
func DropLogosExperiments(raw string) string {
	var kept []string
	for f := range strings.SplitSeq(raw, ",") {
		if f != "" && !strings.HasPrefix(f, "logos") && !strings.HasPrefix(f, "nologos") {
			kept = append(kept, f)
		}
	}
	return strings.Join(kept, ",")
}

func appendExperiment(raw, exp string) string {
	if raw == "" {
		return exp
	}
	return raw + "," + exp
}

func stripExperiment(raw, exp string) string {
	var kept []string
	for f := range strings.SplitSeq(raw, ",") {
		if f != exp {
			kept = append(kept, f)
		}
	}
	return strings.Join(kept, ",")
}

func preemptibleUserState(raw string) (mentioned, enabled bool) {
	for f := range strings.SplitSeq(raw, ",") {
		switch f {
		case preemptibleLoopsExperiment:
			mentioned, enabled = true, true
		case "no" + preemptibleLoopsExperiment, "none":
			mentioned, enabled = true, false
		}
	}
	return mentioned, enabled
}

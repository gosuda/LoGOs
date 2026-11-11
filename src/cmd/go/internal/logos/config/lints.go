// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package config

import (
	"encoding/json"
	"maps"
	"slices"
)

const LintPolicyVersion = 1

var lintGroups = []string{
	"correctness", "concurrency", "slop", "complexity", "perf",
	"style", "suspicious", "portability", "pedantic",
}

var lintThresholds = []string{
	"max_nesting_depth", "max_cognitive_complexity", "max_function_lines",
	"max_function_params", "max_comptime_ast_nodes", "max_ctfe_steps",
}

var forbidGroups = []string{"correctness", "concurrency"}

var defaultLintLevels = map[string]string{
	"correctness": "forbid",
	"concurrency": "forbid",
	"slop":        "deny",
	"complexity":  "deny",
	"perf":        "warn",
	"style":       "warn",
	"suspicious":  "warn",
	"portability": "warn",
	"pedantic":    "allow",
}

var defaultLintThresholds = map[string]int{
	"max_nesting_depth":        3,
	"max_cognitive_complexity": 15,
	"max_function_lines":       60,
	"max_function_params":      4,
	"max_comptime_ast_nodes":   4096,
	"max_ctfe_steps":           100000,
}

type Lints struct {
	Groups     map[string]string
	Thresholds map[string]int
}

type lintPolicy struct {
	Version    int               `json:"version"`
	Groups     map[string]string `json:"groups"`
	Thresholds map[string]int    `json:"thresholds"`
}

func (l Lints) PolicyJSON(pedantic PedanticMode) ([]byte, error) {
	p := lintPolicy{
		Version:    LintPolicyVersion,
		Groups:     map[string]string{},
		Thresholds: map[string]int{},
	}
	for k, v := range l.Groups {
		p.Groups[k] = v
	}
	maps.Copy(p.Thresholds, l.Thresholds)
	switch pedantic {
	case PedanticOn:
		if cur, ok := p.Groups["pedantic"]; !ok || cur == "allow" {
			p.Groups["pedantic"] = "warn"
		}
	case PedanticOff:
		p.Groups["pedantic"] = "allow"
	}
	return json.Marshal(p)
}

type PedanticMode uint8

const (
	PedanticDefault PedanticMode = iota
	PedanticOn
	PedanticOff
)

func isLintGroup(key string) bool {
	return slices.Contains(lintGroups, key)
}

func isLintThreshold(key string) bool {
	return slices.Contains(lintThresholds, key)
}

func isForbidGroup(group string) bool {
	return slices.Contains(forbidGroups, group)
}

#!/usr/bin/env bash
# Autovec suites for the ci.yml `autovec` job.
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .github/scripts/env.sh; cd src
t=../.github/scripts/logos-test-env.sh
go test -count=1 -short -timeout=5m cmd/internal/goparity -run '^TestRedteamLedgerRows$'
go test -count=1 -timeout=10m cmd/go -run '^TestScript$/^logos_cgo_shared$'
$t go test -count=1 -short cmd/compile/internal/ssa
GOEXPERIMENT=simd GOAMD64=v3 $t go test -count=1 -timeout 20m cmd/internal/testdir -run '^Test$/^logos$/^(codegen|run)$/^autovec' -all_codegen
GOEXPERIMENT=simd GOAMD64=v3 $t go run -gcflags=-d=ssa/autovec/off ../test/logos/run/autovec.go
GOEXPERIMENT=none $t go run ../test/logos/run/autovec.go
GOEXPERIMENT=simd,preemptibleloops GOAMD64=v3 $t go run ../test/logos/run/autovec.go

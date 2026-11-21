#!/usr/bin/env bash
# SIMD gate and dominance scripts, plain and race runs. Step of the ci.yml `ssa` job (`ssa.sh zero3`).
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .github/scripts/env.sh; cd src
selection='^TestScript$/^(logos_simd_gate_avx512|logos_simd_feature_gate|logos_simd_gate_branch|logos_simd_gate_noavx_qemu|logos_simd_zero_dominance|logos_eject_simd_slice)$'
go test -count=1 -short -timeout 40m -v cmd/go -run "$selection" 2>&1 | tee "$RUNNER_TEMP/simd-zero-evidence/scripts.txt"
go test -count=1 -short -race -timeout 40m -v cmd/go -run "$selection" 2>&1 | tee "$RUNNER_TEMP/simd-zero-evidence/scripts-race.txt"
sha256sum --check --strict "$RUNNER_TEMP/simd-zero-evidence/attacks.sha256"
wasm_cache=$(go env GOCACHE)
env -i \
  PATH="$PATH" GOROOT="$GITHUB_WORKSPACE" GOCACHE="$wasm_cache" \
  TMPDIR="$RUNNER_TEMP" GOTMPDIR="$RUNNER_TEMP" \
  GOENV=off GOTOOLCHAIN=local GOTELEMETRY=off \
  GOMAXPROCS=2 GOFLAGS=-p=2 GOMEMLIMIT=2GiB \
  go test -count=1 -timeout=10m -v cmd/compile/internal/logos \
    -run '^TestWasm(RunIgnoresHostEnvironment|ProgramRunsUnderNode|WrongMayYieldTraps|YieldFreeSourcesRunUnderNode)$'
test -z "$(gofmt -l cmd/compile/internal/ssa/*logos*.go cmd/compile/internal/logos/wasm*_test.go)"
go vet cmd/compile/internal/ssa cmd/compile/internal/logos

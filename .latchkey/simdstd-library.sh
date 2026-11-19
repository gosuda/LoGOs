#!/usr/bin/env bash
# SIMD stdlib runs for the ci.yml `simdstd` job.
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh; cd src
test "$(go env GOROOT)" = "$GITHUB_WORKSPACE"
# go test prints nothing while a slow package runs; Latchkey stops a job after 15 quiet minutes
(
  trap 'kill "$!" 2>/dev/null || :; exit 0' TERM
  while sleep 240 & wait "$!"; do echo "[heartbeat] $(date -u +%T)"; done
) & heartbeat=$!
trap 'kill "$heartbeat" 2>/dev/null || :; wait "$heartbeat" || :' EXIT
env GOMAXPROCS=4 GOFLAGS=-p=2 GOEXPERIMENT=simd GOAMD64=v1 go test -count=1 -timeout 30m std
GOEXPERIMENT=simd GOAMD64=v3 go test -count=1 -timeout 30m std

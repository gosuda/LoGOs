#!/usr/bin/env bash
# Full cgo dist for the ci.yml `cgofull` job.
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .github/scripts/env.sh
node --input-type=module -e 'import { snapshot } from "./misc/cgo/common.mjs"; snapshot(process.env.RUNNER_TEMP + "/cgo-evidence/full/environment");'
cd src
env -i \
  HOME="$HOME" PATH="$PATH" TMPDIR="$RUNNER_TEMP" LANG=C.UTF-8 TZ=UTC \
  GOROOT="$GITHUB_WORKSPACE" GOENV=off GOTOOLCHAIN=local \
  CGO_ENABLED=1 CC=gcc CXX=g++ GCCGO="$GCCGO" \
  GOMAXPROCS=2 GOFLAGS=-p=2 GO_TEST_TIMEOUT_SCALE=6 GO_TEST_SHORT=1 \
  GOEXPERIMENT="$CGO_EXPERIMENT" \
  UPROOT1272="$UPROOT1272" UPROOT1273="$UPROOT1273" UPROOT1274="$UPROOT1274" \
  bash ./run.bash --no-rebuild -k

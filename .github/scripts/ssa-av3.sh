#!/usr/bin/env bash
# Autovec native: formatting and generated builtins enforced. Step of the ci.yml `ssa` job (`ssa.sh av3`).
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .github/scripts/env.sh; cd src
ev=$RUNNER_TEMP/autovec-evidence
test ! -f "$ev/preflight.failed"
test -f "$ev/gofmt.diff" && test -f "$ev/builtin.diff"
test ! -s "$ev/gofmt.diff" && test ! -s "$ev/builtin.diff"

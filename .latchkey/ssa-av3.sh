#!/usr/bin/env bash
# Autovec native: formatting and generated builtins enforced. Step of the ci.yml `ssa` job (`ssa.sh av3`).
# Latchkey runs each step as `bash -e -o pipefail`; this file sets the same,
# so the moved body behaves exactly like the inline block. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh; cd src
ev=$RUNNER_TEMP/autovec-evidence
test ! -f "$ev/preflight.failed"
test -f "$ev/gofmt.diff" && test -f "$ev/builtin.diff"
test ! -s "$ev/gofmt.diff" && test ! -s "$ev/builtin.diff"

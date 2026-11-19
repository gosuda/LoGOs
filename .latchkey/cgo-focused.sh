#!/usr/bin/env bash
# Focused cgo suites for the ci.yml `cgo` job.
# Latchkey runs each step as `bash -e -o pipefail`; this file sets the same,
# so the moved body behaves exactly like the inline block. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh
e=$RUNNER_TEMP/cgo-evidence
node --test misc/cgo/*.test.mjs
node misc/cgo/lint.mjs $e/lint
node misc/cgo/compat.mjs $e/compat
node misc/cgo/probe.mjs $e/probes
node misc/cgo/verify.mjs $e/focused

#!/usr/bin/env bash
# Focused cgo suites for the ci.yml `cgo` job.
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .github/scripts/env.sh
e=$RUNNER_TEMP/cgo-evidence
node --test misc/cgo/*.test.mjs
node misc/cgo/lint.mjs $e/lint
node misc/cgo/compat.mjs $e/compat
node misc/cgo/probe.mjs $e/probes
node misc/cgo/verify.mjs $e/focused

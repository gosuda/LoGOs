#!/usr/bin/env bash
# Compiler fixture suites for the ci.yml `unit` job.
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh; cd src
../.github/scripts/logos-test-env.sh go test -count=1 -timeout 60m cmd/internal/testdir -run 'Test/logos/'
../.github/scripts/logos-test-env.sh go test -count=1 cmd/internal/testdir -run '^Test$/logos/codegen/scalar_' -all_codegen

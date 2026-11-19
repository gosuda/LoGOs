#!/usr/bin/env bash
# Compiler fixture suites for the ci.yml `unit` job.
# Latchkey runs each step as `bash -e -o pipefail`; this file sets the same,
# so the moved body behaves exactly like the inline block. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh; cd src
../.github/scripts/logos-test-env.sh go test -count=1 -timeout 60m cmd/internal/testdir -run 'Test/logos/'
../.github/scripts/logos-test-env.sh go test -count=1 cmd/internal/testdir -run '^Test$/logos/codegen/scalar_' -all_codegen

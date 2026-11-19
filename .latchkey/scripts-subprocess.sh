#!/usr/bin/env bash
# Subprocess behavior step for the ci.yml `scripts` job.
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh; cd src
fail=0
sha256sum ../bin/go ../bin/logos ../pkg/tool/linux_amd64/compile ../pkg/tool/linux_amd64/link
../.github/scripts/logos-test-env.sh go test -count=1 -race -parallel=1 -timeout 20m cmd/go -run 'Script/^logos_(cap_xtest_owner|test_experiment_env|test_plugin_experiment|test_cache_experiment)$' || fail=1
cd ..; GITHUB_ACTIONS=true bash logos-doc/validation/capability-inline-context/check_ci.sh "$RUNNER_TEMP/logos-regression-evidence/cap-inline" || fail=1
exit $fail

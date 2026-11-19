#!/usr/bin/env bash
# Routed feature gates for the ci.yml `gates` job.
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh
# feature-gate.sh rebuilds the toolchain in setup(); the shared build replaces that function
sed '/^setup() {/,/^}/c\setup() { cd "$repo/src"; }' .github/scripts/feature-gate.sh > "$RUNNER_TEMP/gate.sh"
fail=0
for g in $GATES; do [ "$g" = - ] || bash "$RUNNER_TEMP/gate.sh" "$g" || fail=1; done
exit $fail

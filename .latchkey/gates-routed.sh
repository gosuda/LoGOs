#!/usr/bin/env bash
# Routed feature gates for the ci.yml `gates` job.
# Latchkey runs each step as `bash -e -o pipefail`; this file sets the same,
# so the moved body behaves exactly like the inline block. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh
# feature-gate.sh rebuilds the toolchain in setup(); the shared build replaces that function
sed '/^setup() {/,/^}/c\setup() { cd "$repo/src"; }' .github/scripts/feature-gate.sh > "$RUNNER_TEMP/gate.sh"
fail=0
for g in $GATES; do [ "$g" = - ] || bash "$RUNNER_TEMP/gate.sh" "$g" || fail=1; done
exit $fail

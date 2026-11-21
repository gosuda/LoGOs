#!/usr/bin/env bash
# Upstream complement for the ci.yml `upstream` job.
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .github/scripts/env.sh; cd src
go test -count=1 -short -timeout=5m cmd/internal/goparity -run '^TestRedteamLedgerRows$'
go test -count=1 -timeout=10m cmd/go -run '^TestScript$/^logos_cgo_shared$'
evidence="$RUNNER_TEMP/logos-regression-evidence/upstream"; mkdir "$evidence"
bash ../.github/scripts/logos-upstream-identity.sh before "$GITHUB_WORKSPACE" "$evidence"
../.github/scripts/logos-test-env.sh ./run.bash --no-rebuild -list > "$evidence/all-tests.txt"
uv run --no-project --no-config --python 3.12 --no-python-downloads python ../logos-doc/validation/upstream-host/partition_dist.py --inventory "$evidence/all-tests.txt" --evidence "$evidence/partition"
printf 'filter=!^(cmd/go|cmd/internal/testdir:(0|[1-9][0-9]*)_([1-9][0-9]*))$\ntimeout_scale=1\n' > "$evidence/complement-profile.txt"
set +e
GO_TEST_TIMEOUT_SCALE=1 ../.github/scripts/logos-test-env.sh ./run.bash --no-rebuild -k -run='!^(cmd/go|cmd/internal/testdir:(0|[1-9][0-9]*)_([1-9][0-9]*))$' 2>&1 | tee "$evidence/complement.log"
statuses=("${PIPESTATUS[@]}")
set -e
printf 'driver=%s\nconsole_capture=%s\n' "${statuses[0]}" "${statuses[1]}" | tee "$evidence/complement-exit.txt"
test "${statuses[0]}" -eq 0
test "${statuses[1]}" -eq 0

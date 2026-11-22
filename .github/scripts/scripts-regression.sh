#!/usr/bin/env bash
# Upstream regression for the ci.yml `scripts` job.
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .github/scripts/env.sh; cd src
go test -count=1 -short -timeout=5m cmd/internal/goparity -run '^TestRedteamLedgerRows$'
go test -count=1 -timeout=10m cmd/go -run '^TestScript$/^logos_cgo_shared$'
GO_TEST_TIMEOUT_SCALE=8 ./run.bash --no-rebuild

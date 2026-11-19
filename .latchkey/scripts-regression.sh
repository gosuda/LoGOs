#!/usr/bin/env bash
# Upstream regression for the ci.yml `scripts` job.
# Latchkey runs each step as `bash -e -o pipefail`; this file sets the same,
# so the moved body behaves exactly like the inline block. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh; cd src
go test -count=1 -short -timeout=5m cmd/internal/goparity -run '^TestRedteamLedgerRows$'
go test -count=1 -timeout=10m cmd/go -run '^TestScript$/^logos_cgo_shared$'
GO_TEST_TIMEOUT_SCALE=8 ./run.bash --no-rebuild

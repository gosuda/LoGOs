#!/usr/bin/env bash
# Autovec benchmark campaign for the ci.yml `bench` job step
# "Autovec benchmarks: base vs candidate, measured sequentially".
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .github/scripts/env.sh
archive=hyperfine-v2.0.0-x86_64-unknown-linux-gnu
curl --fail --silent --show-error --location --output "$RUNNER_TEMP/$archive.tar.gz" "https://github.com/sharkdp/hyperfine/releases/download/v2.0.0/$archive.tar.gz"
printf '%s  %s\n' ae2beda2ac99c098427e4f755244552daf20e514422889cca6d44421e2d93c87 "$RUNNER_TEMP/$archive.tar.gz" | sha256sum --check --strict -
tar -xzf "$RUNNER_TEMP/$archive.tar.gz" -C "$RUNNER_TEMP"
printf '%s\n' "$RUNNER_TEMP/$archive" >> "$GITHUB_PATH"; . .github/scripts/env.sh
git fetch -q --no-tags --depth=1 origin "$GITHUB_BASE_REF"
export AUTOVEC_BASE_SHA=$(git rev-parse FETCH_HEAD) AUTOVEC_CAND_SHA=$GITHUB_SHA AUTOVEC_OUT_ROOT=$GITHUB_WORKSPACE/.outline/optimize/autovec
[[ "$AUTOVEC_BASE_SHA" =~ ^[0-9a-f]{40}$ ]]; [[ "$AUTOVEC_CAND_SHA" =~ ^[0-9a-f]{40}$ ]]
test "$(git rev-parse HEAD)" = "$AUTOVEC_CAND_SHA"; test "$(go env GOARCH)" = amd64
git worktree add --quiet --detach "$RUNNER_TEMP/autovec-base" "$AUTOVEC_BASE_SHA"
(cd "$RUNNER_TEMP/autovec-base/src" && GOROOT_BOOTSTRAP="$RUNNER_TEMP/go1.27.4-ci" ./make.bash)
for benchmark in BenchmarkAutovecFixedPoint BenchmarkAutovecStartFixed BenchmarkAutovecStartVariable; do
  run="$GITHUB_RUN_ID-$GITHUB_RUN_ATTEMPT-$benchmark"
  src=$GITHUB_WORKSPACE/src/cmd/compile/internal/ssa/testdata/autovec_bench_test.go
  bash test/logos/bench/autovec_measure.sh "$run" "$benchmark" "$RUNNER_TEMP/autovec-base/bin/go" "$GITHUB_WORKSPACE/bin/go" "$src" "$AUTOVEC_BASE_SHA" "$AUTOVEC_CAND_SHA"
  n=$(jq -er 'select(.event == "measurement") | .iterations' "$AUTOVEC_OUT_ROOT/$run/events.jsonl")
  AUTOVEC_FIXED_N="$n" bash test/logos/bench/autovec_measure.sh "$run-integration" "$benchmark" "$RUNNER_TEMP/autovec-base/bin/go" "$GITHUB_WORKSPACE/bin/go" "$src" "$AUTOVEC_BASE_SHA" "$AUTOVEC_CAND_SHA"
done

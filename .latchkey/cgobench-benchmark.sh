#!/usr/bin/env bash
# cgo benchmark campaign for the ci.yml `cgobench` job.
# Latchkey runs each step as `bash -e -o pipefail`; this file sets the same,
# so the moved body behaves exactly like the inline block. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh
archive="$RUNNER_TEMP/hyperfine.tar.gz"
curl --fail --location --retry 3 https://github.com/sharkdp/hyperfine/releases/download/v2.0.0/hyperfine-v2.0.0-x86_64-unknown-linux-musl.tar.gz --output "$archive"
echo "38e59087bd65520930a13cf513d8a493b7043741e8ad62349a8a9929eb61bc5c  $archive" | sha256sum --check --strict
tar -xzf "$archive" -C "$RUNNER_TEMP"
echo "HYPERFINE=$RUNNER_TEMP/hyperfine-v2.0.0-x86_64-unknown-linux-musl/hyperfine" >> "$GITHUB_ENV"
. .latchkey/env.sh
node misc/cgo/benchmark.mjs "$RUNNER_TEMP/cgo-evidence/benchmark"

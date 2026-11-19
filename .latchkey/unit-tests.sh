#!/usr/bin/env bash
# Unit test suites for the ci.yml `unit` job.
# Latchkey runs each step as `bash -e -o pipefail`; this file sets the same,
# so the moved body behaves exactly like the inline block. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh; cd src
t=../.github/scripts/logos-test-env.sh
read -r -a packages <<< "$LOGOS_PACKAGES"
$t go test -count=1 -short "${packages[@]}"
$t go test -count=1 -short cmd/compile/internal/ssa
$t go test -count=1 cmd/compile/internal/test -run '^TestLogosScalarOptimizationRuntime$'
for mode in 0 1; do
  GOARCH=386 CGO_ENABLED=0 $t go test -count=1 -gcflags="cmd/compile/internal/test/testdata/scalaropt=-d=logosopt=$mode,ssa/check/on" cmd/compile/internal/test/testdata/scalaropt
done
$t go test -count=1 -race logos/... cmd/go/internal/logos/lsp/...
$t go test -count=1 -short runtime/metrics
GOEXPERIMENT=logosarena $t go test -count=1 -short -race runtime -run '^TestLogos(Arena|Region|GCScan)'
GOEXPERIMENT=logosarena,nogreenteagc $t go test -count=1 -short runtime -run '^TestLogos(Arena|Region|GCScan)'

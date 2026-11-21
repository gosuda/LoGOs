#!/usr/bin/env bash
# GC stability step for the ci.yml `gc` job.
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .github/scripts/env.sh; cd src; export GOEXPERIMENT=$EXP
n=2; [ "$LATCHKEY_CI_EVENT_CLASS" = pr_update ] || n=10
files=(runtime/arena_logos.go runtime/region_logos.go runtime/logos_arena_test.go runtime/export_logos_gc_stability_test.go runtime/logos_gc_stability_test.go runtime/export_logos_gcscan_bounds_test.go runtime/logos_gcscan_bounds_test.go)
test -z "$(gofmt -l "${files[@]}")"
go vet runtime runtime/metrics
go test -count=1 -short -timeout=30m runtime runtime/metrics runtime/pprof runtime/trace
go test -count=1 -short -race -timeout=30m runtime -run '^TestLogos(Arena|Region|GC)'
go test -count=$n -short -cpu=1,2,4 -timeout=30m runtime -run '^(TestLogosRegionReleaseOwnership|TestLogosGCHeapGraphLiveness|TestLogosGCScanLayoutBounds)$'
GODEBUG=asyncpreemptoff=1 go test -count=$n -short -gcflags=runtime=-l -timeout=30m runtime -run '^TestLogosRegionReleaseOwnership$'
GODEBUG=gccheckmark=1 go test -count=$n -short -timeout=30m runtime -run '^(TestLogosGCScanExternalPointers|TestLogosGCHeapGraphLiveness)$'

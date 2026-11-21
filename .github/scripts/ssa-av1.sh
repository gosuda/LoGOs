#!/usr/bin/env bash
# Autovec native: preflight, faults, memory and GC. Step of the ci.yml `ssa` job (`ssa.sh av1`).
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .github/scripts/env.sh; cd src
mkdir -p $RUNNER_TEMP/autovec-evidence
test "$(go env GOARCH)" = amd64
set +e
( set -e
  files=(cmd/compile/internal/ssa/autovec*.go cmd/compile/internal/ssa/testdata/autovec*.go ../test/logos/run/autovec*.go ../test/logos/codegen/autovec*.go runtime/autovec*.go runtime/export_autovec_logos_test.go cmd/compile/internal/ssagen/ssa.go cmd/compile/internal/typecheck/_builtin/runtime.go cmd/compile/internal/typecheck/builtin.go)
  gofmt -d "${files[@]}" | tee "$RUNNER_TEMP/autovec-evidence/gofmt.diff"
  (cd cmd/compile/internal/typecheck && "$UPROOT1274/bin/go" run mkbuiltin.go -stdout) > "$RUNNER_TEMP/autovec-evidence/builtin.generated.go"
  builtin_diff_status=0
  diff -u cmd/compile/internal/typecheck/builtin.go "$RUNNER_TEMP/autovec-evidence/builtin.generated.go" > "$RUNNER_TEMP/autovec-evidence/builtin.diff" || builtin_diff_status=$?
  cat "$RUNNER_TEMP/autovec-evidence/builtin.diff"
  test "$builtin_diff_status" -le 1
)
rc=$?; set -e
if [ $rc -ne 0 ]; then touch "$RUNNER_TEMP/autovec-evidence/preflight.failed"; fi
probe=cmd/compile/internal/ssa/testdata/autovec_fault_probe.go
{
  printf 'upstream optimized\n'
  GOEXPERIMENT=none GOAMD64=v3 "$UPROOT1274/bin/go" run "$probe"
  printf 'upstream unoptimized\n'
  GOEXPERIMENT=none GOAMD64=v3 "$UPROOT1274/bin/go" run -gcflags=-N "$probe"
  printf 'fork SIMD enabled\n'
  GOEXPERIMENT=simd GOAMD64=v3 go run "$probe"
  printf 'fork pass disabled\n'
  GOEXPERIMENT=simd GOAMD64=v3 go run -gcflags=-d=ssa/autovec/off "$probe"
} | tee "$RUNNER_TEMP/autovec-evidence/fault-order.txt"
GOEXPERIMENT=simd GOAMD64=v3 go run ../test/logos/run/autovec_fault.go
go vet cmd/compile/internal/ssa cmd/compile/internal/ssagen cmd/compile/internal/typecheck runtime
go test -count=1 -short runtime -run TestLogosAutovecPanicOnFault
go test -count=1 -short -race runtime -run TestLogosAutovecPanicOnFault
fixture=../test/logos/run/autovec_memory.go
GOEXPERIMENT=simd GOAMD64=v3 go build -gcflags=-d=ssa/autovec/debug=1 -o "$RUNNER_TEMP/memory" "$fixture" 2>&1 | tee "$RUNNER_TEMP/autovec-evidence/memory-vectorization.txt"
for bits in 8 16 32 64; do
  grep -E "autovec_memory.go:25:.*autovectorized $bits-bit integer loop" "$RUNNER_TEMP/autovec-evidence/memory-vectorization.txt"
done
"$RUNNER_TEMP/memory"
GOEXPERIMENT=simd GOAMD64=v3 go run -gcflags=-d=ssa/autovec/off "$fixture"
GOEXPERIMENT=none go run "$fixture"
GOEXPERIMENT=simd GOAMD64=v3 go run -race "$fixture"
GOEXPERIMENT=simd,preemptibleloops GOAMD64=v3 go build -gcflags=-d=ssa/autovec/debug=1 -o "$RUNNER_TEMP/vector-gc" ../test/logos/run/autovec_gc.go 2>&1 | tee "$RUNNER_TEMP/autovec-evidence/gc-vectorization.txt"
grep -E 'autovec_gc.go:48:.*autovectorized 64-bit integer loop' "$RUNNER_TEMP/autovec-evidence/gc-vectorization.txt"
GODEBUG=asyncpreemptoff=1 timeout 90s "$RUNNER_TEMP/vector-gc"

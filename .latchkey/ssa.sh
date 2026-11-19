#!/usr/bin/env bash
# Suites of the ci.yml `ssa` job (former simd-zero-guards, simd-dominance and autovec native, amd64 legs).
# Usage: ssa.sh STEP. Bodies are unindented so the heredocs inside stay byte-exact.
set -eo pipefail
. .latchkey/env.sh; cd src
zero1() {
mkdir -p $RUNNER_TEMP/simd-zero-evidence
cat > "$RUNNER_TEMP/simd-zero-evidence/attacks.sha256" <<'FROZEN'
6814eeeae4f396e103f1178eb211f41406c421f226e1de4d5efc8a96d7a66b97  cmd/compile/internal/ssa/zcse_simd_logos_test.go
7cdbc8e5243c835e234617fd77a97f9184f799054bb33edbade4db5978c7b323  ../test/logos/run/simd_zero_guard.go
fd4f67d871fd0578854e61d239ff21353899fb0440daa94316c8a8ec3d234bdb  ../test/logos/run/simd_zero_guard.out
FROZEN
sha256sum --check --strict "$RUNNER_TEMP/simd-zero-evidence/attacks.sha256"
gofmt -d cmd/compile/internal/ssa/zcse.go cmd/compile/internal/ssa/zcse_simd_logos_test.go ../test/logos/run/simd_zero_guard.go | tee "$RUNNER_TEMP/simd-zero-evidence/gofmt.diff"
test ! -s "$RUNNER_TEMP/simd-zero-evidence/gofmt.diff"
test "$(go env GOROOT)" = "$GITHUB_WORKSPACE"
go version | tee "$RUNNER_TEMP/simd-zero-evidence/toolchain.txt"
go vet cmd/compile/internal/ssa
source="$GITHUB_WORKSPACE/src/cmd/compile/internal/ssa/zcse.go"
mutation="$RUNNER_TEMP/simd-zero-mutation"
mkdir "$mutation"
sha256sum "$source" > "$RUNNER_TEMP/simd-zero-evidence/production.sha256"
test "$(grep -Fc ' && v.Op != OpZeroSIMD' "$source")" -eq 1
test "$(grep -Fc 'A SIMD zero may require a CPU feature checked' "$source")" -eq 1
test "$(grep -Fc 'predecessors. Leave it to dominance-aware CSE instead of' "$source")" -eq 1
test "$(grep -Fc 'moving it outside the guard.' "$source")" -eq 1
sed -e '/^[[:space:]]*\/\/ A SIMD zero may require a CPU feature checked by this block'"'"'s$/d' \
    -e '/^[[:space:]]*\/\/ predecessors\. Leave it to dominance-aware CSE instead of$/d' \
    -e '/^[[:space:]]*\/\/ moving it outside the guard\.$/d' \
    -e 's/ && v\.Op != OpZeroSIMD//' "$source" > "$mutation/zcse.go"
printf '%s  %s\n' 89c0d4f2646b5dc9274534fc0f97ce5873a77db208aab96341309f61bf0ff324 "$mutation/zcse.go" | sha256sum --check --strict -
jq -n --arg source "$source" --arg replacement "$mutation/zcse.go" '{Replace: {($source): $replacement}}' > "$mutation/overlay.json"
if go test -count=1 -short -v -overlay="$mutation/overlay.json" cmd/compile/internal/ssa -run '^TestZCSEPreservesSIMDZeroGuards$' > "$RUNNER_TEMP/simd-zero-evidence/mutation.txt" 2>&1; then
  cat "$RUNNER_TEMP/simd-zero-evidence/mutation.txt"
  printf 'SIMD zero-hoisting mutation unexpectedly passed\n' >&2
  exit 1
fi
cat "$RUNNER_TEMP/simd-zero-evidence/mutation.txt"
for width in 128 256 512; do
  grep -F -- "--- FAIL: TestZCSEPreservesSIMDZeroGuards/$width " "$RUNNER_TEMP/simd-zero-evidence/mutation.txt"
done
test "$(grep -Fc 'independently guarded SIMD stores share a zero' "$RUNNER_TEMP/simd-zero-evidence/mutation.txt")" -eq 3
sha256sum --check --strict "$RUNNER_TEMP/simd-zero-evidence/production.sha256"
go test -count=1 -short -v cmd/compile/internal/ssa -run '^(TestZCSE|TestZCSEPreservesSIMDZeroGuards|TestCSECommonsSIMDZeroWithinGuard)$' 2>&1 | tee "$RUNNER_TEMP/simd-zero-evidence/clean-controls.txt"
}
zero2() {
qemu_dir="$RUNNER_TEMP/simd-zero-qemu"
mkdir "$qemu_dir"
curl --fail --silent --show-error --location --output "$qemu_dir/qemu-x86_64" https://github.com/multiarch/qemu-user-static/releases/download/v7.2.0-1/qemu-x86_64-static
printf '%s  %s\n' 7132ffd39aef71c26d3344cc0c7dffc530e10e3e720c58c8279a97ef6fdd7784 "$qemu_dir/qemu-x86_64" | sha256sum --check --strict -
chmod +x "$qemu_dir/qemu-x86_64"
"$qemu_dir/qemu-x86_64" --version | tee "$RUNNER_TEMP/simd-zero-evidence/qemu-version.txt"
printf '%s\n' "$qemu_dir" >> "$GITHUB_PATH"
expected=../test/logos/run/simd_zero_guard.out
binary="$RUNNER_TEMP/simd-zero-guard"
GOEXPERIMENT=simd GOAMD64=v1 go build -o "$binary" ../test/logos/run/simd_zero_guard.go
"$binary" > "$RUNNER_TEMP/simd-zero-evidence/native.out"
cmp "$expected" "$RUNNER_TEMP/simd-zero-evidence/native.out"
for cpu in Nehalem Haswell; do
  for width in 128 256 512; do
    output="$RUNNER_TEMP/simd-zero-evidence/qemu-$cpu-$width"
    "$qemu_dir/qemu-x86_64" -cpu "$cpu" "$binary" "$width" > "$output.out" 2> "$output.err"
    cmp "$expected" "$output.out"
    printf 'CPU=%s width=%s: exact output passed\n' "$cpu" "$width"
  done
done | tee "$RUNNER_TEMP/simd-zero-evidence/qemu-checks.txt"
fixture=cmd/go/testdata/script/logos_eject_simd_slice.txt
module="$RUNNER_TEMP/simd-zero-evidence/eject-simd-slice"
mkdir "$module"
test "$(grep -Fxc -- '-- m/go.mod --' "$fixture")" -eq 1
test "$(grep -Fxc -- '-- m/main.lgo --' "$fixture")" -eq 1
awk '$0 == "-- m/go.mod --" { copy = 1; next } copy && /^-- .* --$/ { exit } copy { print }' "$fixture" > "$module/go.mod"
awk '$0 == "-- m/main.lgo --" { copy = 1; next } copy && /^-- .* --$/ { exit } copy { print }' "$fixture" > "$module/main.lgo"
sha256sum "$module/go.mod" "$module/main.lgo" | tee "$module/sources.sha256"
printf '%s  %s\n' 6fe5f1a6cc7f1f3f5aa25b429e2b452a4aa088a3646587e5ba14a3e249cc35f0 "$module/main.lgo" | sha256sum --check --strict -
(
  cd "$module"
  export GOENV=off GOTOOLCHAIN=local GOWORK=off GOPROXY=off
  GOEXPERIMENT='' GOAMD64=v1 "$GITHUB_WORKSPACE/bin/logos" build -o "$RUNNER_TEMP/eject-scalar" . 2>&1 | tee "$module/scalar-build.txt"
  GOEXPERIMENT=simd GOAMD64=v1 "$GITHUB_WORKSPACE/bin/logos" build -o "$RUNNER_TEMP/eject-simd" . 2>&1 | tee "$module/simd-build.txt"
)
"$RUNNER_TEMP/eject-scalar" > "$module/scalar.out" 2> "$module/scalar.err"
test "$(wc -l < "$module/scalar.out")" -eq 72
"$RUNNER_TEMP/eject-simd" > "$module/native-simd.out" 2> "$module/native-simd.err"
cmp "$module/scalar.out" "$module/native-simd.out"
for cpu in Nehalem Haswell; do
  "$RUNNER_TEMP/simd-zero-qemu/qemu-x86_64" -cpu "$cpu" "$RUNNER_TEMP/eject-simd" > "$module/$cpu.out" 2> "$module/$cpu.err"
  cmp "$module/scalar.out" "$module/$cpu.out"
  printf 'Original slice-sum program on %s: exact output passed\n' "$cpu"
done | tee "$module/checks.txt"
}
zero3() {
selection='^TestScript$/^(logos_simd_gate_avx512|logos_simd_feature_gate|logos_simd_gate_branch|logos_simd_gate_noavx_qemu|logos_simd_zero_dominance|logos_eject_simd_slice)$'
go test -count=1 -short -timeout 40m -v cmd/go -run "$selection" 2>&1 | tee "$RUNNER_TEMP/simd-zero-evidence/scripts.txt"
go test -count=1 -short -race -timeout 40m -v cmd/go -run "$selection" 2>&1 | tee "$RUNNER_TEMP/simd-zero-evidence/scripts-race.txt"
sha256sum --check --strict "$RUNNER_TEMP/simd-zero-evidence/attacks.sha256"
wasm_cache=$(go env GOCACHE)
env -i \
  PATH="$PATH" GOROOT="$GITHUB_WORKSPACE" GOCACHE="$wasm_cache" \
  TMPDIR="$RUNNER_TEMP" GOTMPDIR="$RUNNER_TEMP" \
  GOENV=off GOTOOLCHAIN=local GOTELEMETRY=off \
  GOMAXPROCS=2 GOFLAGS=-p=2 GOMEMLIMIT=2GiB \
  go test -count=1 -timeout=10m -v cmd/compile/internal/logos \
    -run '^TestWasm(RunIgnoresHostEnvironment|ProgramRunsUnderNode|WrongMayYieldTraps|YieldFreeSourcesRunUnderNode)$'
test -z "$(gofmt -l cmd/compile/internal/ssa/*logos*.go cmd/compile/internal/logos/wasm*_test.go)"
go vet cmd/compile/internal/ssa cmd/compile/internal/logos
}
av1() {
mkdir -p $RUNNER_TEMP/autovec-evidence
test "$(go env GOARCH)" = amd64
( set -e
  files=(cmd/compile/internal/ssa/autovec*.go cmd/compile/internal/ssa/testdata/autovec*.go ../test/logos/run/autovec*.go ../test/logos/codegen/autovec*.go runtime/autovec*.go runtime/export_autovec_logos_test.go cmd/compile/internal/ssagen/ssa.go cmd/compile/internal/typecheck/_builtin/runtime.go cmd/compile/internal/typecheck/builtin.go)
  gofmt -d "${files[@]}" | tee "$RUNNER_TEMP/autovec-evidence/gofmt.diff"
  (cd cmd/compile/internal/typecheck && "$UPROOT1274/bin/go" run mkbuiltin.go -stdout) > "$RUNNER_TEMP/autovec-evidence/builtin.generated.go"
  builtin_diff_status=0
  diff -u cmd/compile/internal/typecheck/builtin.go "$RUNNER_TEMP/autovec-evidence/builtin.generated.go" > "$RUNNER_TEMP/autovec-evidence/builtin.diff" || builtin_diff_status=$?
  cat "$RUNNER_TEMP/autovec-evidence/builtin.diff"
  test "$builtin_diff_status" -le 1
) || touch "$RUNNER_TEMP/autovec-evidence/preflight.failed"
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
}
av2() {
go build -o "$RUNNER_TEMP/autovec-overlay" cmd/compile/internal/ssa/testdata/autovec_overlay.go
mutation="$RUNNER_TEMP/autovec-tail-mutation"
mkdir "$mutation"
sha256sum ../test/logos/run/autovec_memory.go cmd/compile/internal/ssa/autovec_logos.go | tee "$RUNNER_TEMP/autovec-evidence/mutation-inputs.sha256"
"$RUNNER_TEMP/autovec-overlay" cmd/compile/internal/ssa/autovec_logos.go 'h.SetControl(h.NewValue2(pos, OpLess64,' 'h.SetControl(h.NewValue2(pos, OpLeq64,' "$mutation" > "$mutation/overlay.json"
go build -overlay="$mutation/overlay.json" -o "$mutation/compile" cmd/compile
cat > "$mutation/toolexec" <<'WRAPPER'
#!/usr/bin/env bash
set -euo pipefail
tool=$1
shift
if [[ "${tool##*/}" == compile && "${TOOLEXEC_IMPORTPATH-}" == command-line-arguments ]]; then
  exec "$AUTOVEC_MUTANT_COMPILER" "$@"
fi
exec "$tool" "$@"
WRAPPER
chmod +x "$mutation/toolexec"
AUTOVEC_MUTANT_COMPILER="$mutation/compile" GOCACHE="$mutation/cache" GOEXPERIMENT=simd GOAMD64=v3 go build -toolexec="$mutation/toolexec" -o "$mutation/memory" ../test/logos/run/autovec_memory.go
if "$mutation/memory" > "$RUNNER_TEMP/autovec-evidence/tail-mutation.txt" 2>&1; then
  cat "$RUNNER_TEMP/autovec-evidence/tail-mutation.txt"
  printf 'tail mutation unexpectedly passed\n' >&2
  exit 1
fi
grep -F 'memoryMap' "$RUNNER_TEMP/autovec-evidence/tail-mutation.txt"
grep -E 'SIGSEGV|unexpected fault address' "$RUNNER_TEMP/autovec-evidence/tail-mutation.txt"
"$RUNNER_TEMP/memory"
mutation="$RUNNER_TEMP/autovec-fault-mutation"
mkdir "$mutation"
sha256sum runtime/autovec_logos.go runtime/autovec_logos_test.go | tee "$RUNNER_TEMP/autovec-evidence/fault-mutation-inputs.sha256"
"$RUNNER_TEMP/autovec-overlay" runtime/autovec_logos.go 'return getg().paniconfault' 'return false' "$mutation" > "$mutation/overlay.json"
if GOCACHE="$mutation/cache" go test -count=1 -short -overlay="$mutation/overlay.json" runtime -run '^TestLogosAutovecPanicOnFault$' > "$RUNNER_TEMP/autovec-evidence/fault-mutation.txt" 2>&1; then
  cat "$RUNNER_TEMP/autovec-evidence/fault-mutation.txt"
  printf 'fault-policy mutation unexpectedly passed\n' >&2
  exit 1
fi
cat "$RUNNER_TEMP/autovec-evidence/fault-mutation.txt"
grep -F 'child enabled: got false want true' "$RUNNER_TEMP/autovec-evidence/fault-mutation.txt"
go test -count=1 -short runtime -run '^TestLogosAutovecPanicOnFault$'
GOEXPERIMENT=simd GOAMD64=v3 go run ../test/logos/run/autovec_fault.go
mutation="$RUNNER_TEMP/autovec-shift-mutation"
mkdir "$mutation"
"$RUNNER_TEMP/autovec-overlay" cmd/compile/internal/ssa/autovec_logos.go 'op, scalarCount = p.lane.signedRight, true' 'op, scalarCount = p.lane.right, true' "$mutation" > "$mutation/overlay.json"
if go test -count=1 -short -overlay="$mutation/overlay.json" cmd/compile/internal/ssa -run '^TestLogosAutovecExpressions$' > "$RUNNER_TEMP/autovec-evidence/shift-mutation.txt" 2>&1; then
  cat "$RUNNER_TEMP/autovec-evidence/shift-mutation.txt"
  printf 'signed-shift mutation unexpectedly passed\n' >&2
  exit 1
fi
cat "$RUNNER_TEMP/autovec-evidence/shift-mutation.txt"
grep -E 'missing vector operation ShiftAllRightInt(16x8|32x4)' "$RUNNER_TEMP/autovec-evidence/shift-mutation.txt"
go test -count=1 -short cmd/compile/internal/ssa -run '^TestLogosAutovecExpressions$'
mutation="$RUNNER_TEMP/autovec-start-mutation"
mkdir "$mutation"
"$RUNNER_TEMP/autovec-overlay" cmd/compile/internal/ssa/autovec_logos.go 'OpLeq64U, typ.Bool, count, distance' 'OpLeq64U, typ.Bool, end, distance' "$mutation" > "$mutation/overlay.json"
if go test -count=1 -short -overlay="$mutation/overlay.json" cmd/compile/internal/ssa -run '^TestLogosAutovecInvariantStartAndAliasGuard$' > "$RUNNER_TEMP/autovec-evidence/start-mutation.txt" 2>&1; then
  cat "$RUNNER_TEMP/autovec-evidence/start-mutation.txt"
  printf 'absolute-bound mutation unexpectedly passed\n' >&2
  exit 1
fi
cat "$RUNNER_TEMP/autovec-evidence/start-mutation.txt"
grep -F 'bound=' "$RUNNER_TEMP/autovec-evidence/start-mutation.txt"
go test -count=1 -short cmd/compile/internal/ssa -run '^TestLogosAutovecInvariantStartAndAliasGuard$'
fork="$GITHUB_WORKSPACE"
upgo="$UPROOT1274/bin/go"
upver=$(env -u GOROOT GOENV=off GOTOOLCHAIN=local "$upgo" version)
[[ "$upver" == 'go version go1.27.4 '* ]]
export GOENV=off GOFLAGS='' GOWORK=off GOPROXY=off GOTOOLCHAIN=local
if [[ $(env -u GOROOT "$fork/bin/go" env GOARCH) == amd64 ]]; then export GOAMD64=v3; fi
test -f "$fork/test/logos/run/autovec.lgo"
smoke=$(mktemp -d "$RUNNER_TEMP/autovec-eject.XXXXXX")
for name in autovec autovec_expr autovec_start; do
  fixture="$fork/test/logos/run/$name.lgo"
  if [[ ! -f "$fixture" ]]; then continue; fi
  dir="$smoke/$name"
  mkdir "$dir"
  printf 'module example.com/autovec-eject\n\ngo 1.27\n' > "$dir/go.mod"
  cp "$fixture" "$dir/main.lgo"
  (
    cd "$dir"
    env -u GOROOT GOEXPERIMENT=simd "$fork/bin/logos" run . > native.out
    env -u GOROOT GOEXPERIMENT=simd "$fork/bin/logos" eject -o out
    test -f out/main.go
    cd out
    env -u GOROOT GOEXPERIMENT=none "$upgo" run . > ../upstream.out
    cmp ../native.out ../upstream.out
  )
  printf 'Native and upstream eject passed: %s\n' "$name"
done
}
av3() {
ev=$RUNNER_TEMP/autovec-evidence
test ! -f "$ev/preflight.failed"
test -f "$ev/gofmt.diff" && test -f "$ev/builtin.diff"
test ! -s "$ev/gofmt.diff" && test ! -s "$ev/builtin.diff"
}
"$@"

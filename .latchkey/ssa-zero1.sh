#!/usr/bin/env bash
# SIMD zero guards: frozen attacks, vet and mutation proof. Step of the ci.yml `ssa` job (`ssa.sh zero1`).
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh; cd src
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

#!/usr/bin/env bash
# Autovec native: mutation proofs and ejection. Step of the ci.yml `ssa` job (`ssa.sh av2`).
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .github/scripts/env.sh; cd src
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

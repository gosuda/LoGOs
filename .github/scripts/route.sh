#!/usr/bin/env bash
# Path routing. Latchkey has no on.paths filter, so derive one true/false output per suite from the
# changed files (pull request: vs its base, push: the last commit, dispatch: the "suites" input).
# A change under .github/scripts/ or .github/workflows/ runs every suite, as each old workflow ran when its own file changed.
set -euo pipefail
ev=${LATCHKEY_CI_EVENT_CLASS:-}
files=; base=; all=false
case $ev in
  pr_update)
  git fetch -q --no-tags --depth=300 origin "$LATCHKEY_CI_BASE_REF"
  target=$(git rev-parse FETCH_HEAD)
  git fetch -q --no-tags --depth=300 origin "$LATCHKEY_CI_SHA" || true
  # The second fetch replaces FETCH_HEAD with the PR commit, so a failed
  # merge-base must fall back to the saved target tip, never FETCH_HEAD:
  # diffing the tip against itself would route nothing.
  base=$(git merge-base "$target" HEAD 2>/dev/null) || base=$target
  files=$(git diff --name-only "$base" HEAD) ;;
  branch_push)
    git fetch -q --no-tags --depth=2 origin "$LATCHKEY_CI_SHA"
    files=$(git diff --name-only HEAD~1 HEAD) || files= ;;
esac
if grep -Eq '^\.github/scripts/|^\.github/workflows/' <<< "$files"; then all=true; fi
want() { [[ " ${LATCHKEY_INPUT_SUITES:-all} " == *" all "* || " ${LATCHKEY_INPUT_SUITES:-all} " == *" $1 "* ]]; }
hit() { [ $all = true ] || grep -Eq "$1" <<< "$files"; }
any=false
# Optional .github/scripts/only: space-separated suites to run, to re-validate one suite cheaply (delete it when done)
only=; if [ -f .github/scripts/only ]; then only=$(cat .github/scripts/only); fi
in_only() { [ -z "$only" ] || [[ " $only " == *" $1 "* ]]; }
# flag NAME REGEX [push]: set on pull requests and dispatch; also on pushes when "push" is given.
flag() {
  local v=false
  if [ "$ev" = workflow_dispatch ]; then if [ "${3:-}" != pr ] && want "$1"; then v=true; fi
  elif [ "$ev" = pr_update ] || { [ "$ev" = branch_push ] && [ "${3:-}" = push ]; }; then if hit "$2"; then v=true; fi
  fi
  if ! in_only "$1"; then v=false; fi
  if [ $v = true ]; then any=true; fi
  echo "$1=$v" >> "$LATCHKEY_CI_OUTPUT"
}
# core: every change except docs-only ones (the old logos.yml paths-ignore)
core=false
if [ "$ev" = workflow_dispatch ]; then if want core; then core=true; fi
elif grep -Evq '^(AGENTS|CONTRIBUTING|README|SECURITY)\.md$' <<< "$files"; then core=true
fi
if ! in_only core; then core=false; fi
if [ $core = true ]; then any=true; fi
echo "core=$core" >> "$LATCHKEY_CI_OUTPUT"
flag ssa '^src/cmd/compile/internal/(ssa/|ssagen/(simd[^/]*|intrinsics[^/]*|ssa)\.go$|(amd64|arm64)/ssa\.go$|typecheck/(_builtin/runtime|builtin)\.go$|logos/wasm[^/]*_test\.go$)|^src/simd/archsimd/|^src/runtime/(autovec|export_autovec_logos_test)|^src/cmd/go/(scriptconds_test\.go$|testdata/script/logos_(simd_|eject_simd_slice))|^src/cmd/go/internal/logos/config/targetcpu|^test/logos/(run/simd_zero_guard|.*autovec)'
flag gc '^src/(runtime/|internal/abi/|internal/goexperiment/|logos/arena/)|^\.github/scripts/install-ci-tool\.sh$'
flag compiler '^src/(cmd/compile/|cmd/go/|internal/buildcfg/)|^test/logos/|^\.github/scripts/install-(ci-tool|upstream-oracles)\.sh$'
flag cgo '^misc/cgo/|^src/(cmd/cgo/|runtime/|internal/goexperiment/|internal/buildcfg/|cmd/go/internal/logos/settings/)' push
flag dogfood '^misc/logos-dogfood/|^src/'
flag bench '^src/cmd/compile/internal/ssa/(autovec_logos\.go|testdata/autovec_bench_test\.go)$|^test/logos/bench/autovec_measure\.sh$' pr
if [ "$ev" = workflow_dispatch ] && want bench; then printf 'warning: bench runs on pull requests only; this dispatch runs no benchmark\n' >&2; fi
gates=
if [ "$ev" = workflow_dispatch ] || [ $all = true ]; then
  if [ "$ev" != workflow_dispatch ] || want gates; then gates=$(bash .github/scripts/gate-changes.sh "" | tr '\n' ' '); fi
elif [ "$ev" = pr_update ]; then
  gates=$(GITHUB_SHA=$LATCHKEY_CI_SHA bash .github/scripts/gate-changes.sh "$base" | tr '\n' ' ')
fi
if ! in_only gates; then gates=; fi
if [ -n "$gates" ]; then any=true; fi
echo "gates=${gates:--}" >> "$LATCHKEY_CI_OUTPUT"
echo "gate_any=$([ -n "$gates" ] && echo true || echo false)" >> "$LATCHKEY_CI_OUTPUT"
# regression: the full run.bash gate the old compiler and SIMD workflows ran on
# manual dispatch too. On pull requests and pushes it follows core; on dispatch
# a targeted compiler or ssa selection brings it along.
regression=$core
if [ "$ev" = workflow_dispatch ] && { want compiler || want ssa; }; then regression=true; fi
if ! in_only regression; then regression=false; fi
echo "regression=$regression" >> "$LATCHKEY_CI_OUTPUT"
echo "build=$any" >> "$LATCHKEY_CI_OUTPUT"
printf 'changed files:\n%s\n--\n' "$files"; cat "$LATCHKEY_CI_OUTPUT"

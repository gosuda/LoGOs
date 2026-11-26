#!/usr/bin/env bash
# Path routing. The workflow has no on.paths filter, so derive one true/false output per suite from the
# changed files (pull request: vs its base, push: the complete event range,
# dispatch: the "suites" input).
# A change under .github/scripts/, .github/workflows/, or .depot/workflows/ runs every suite, as each
# old workflow ran when its own file changed.
set -euo pipefail
ev=${GITHUB_EVENT_NAME:-}
files=; base=; all=false
case $ev in
  pull_request)
  git fetch -q --no-tags --depth=300 origin "$GITHUB_BASE_REF"
  target=$(git rev-parse FETCH_HEAD)
  git fetch -q --no-tags --depth=300 origin "$GITHUB_SHA" || true
  # The second fetch replaces FETCH_HEAD with the PR commit, so a failed
  # merge-base must fall back to the saved target tip, never FETCH_HEAD:
  # diffing the tip against itself would route nothing.
  base=$(git merge-base "$target" HEAD 2>/dev/null) || base=$target
  files=$(git diff --name-only "$base" HEAD) ;;
  push)
    before=${PUSH_BEFORE_SHA:-}
    after=${PUSH_AFTER_SHA:-${GITHUB_SHA:-}}
    if [[ "$before" =~ ^[0-9a-f]{40}$ && "$after" =~ ^[0-9a-f]{40}$ &&
          "$before" != 0000000000000000000000000000000000000000 ]]; then
      for revision in "$before" "$after"; do
        if ! git cat-file -e "$revision^{commit}" 2>/dev/null &&
           ! git fetch -q --no-tags --depth=1 origin "$revision"; then
          all=true
        fi
      done
      if [ "$all" = false ]; then
        files=$(git diff --name-only "$before" "$after") || all=true
      fi
    else
      # Initial pushes and unavailable endpoints must not silently skip code.
      all=true
    fi ;;
esac
if grep -Eq '^\.github/scripts/|^\.github/workflows/|^\.depot/workflows/' <<< "$files"; then all=true; fi
want() { [[ " ${INPUT_SUITES:-all} " == *" all "* || " ${INPUT_SUITES:-all} " == *" $1 "* ]]; }
hit() { [ $all = true ] || grep -Eq "$1" <<< "$files"; }
any=false
# Optional .github/scripts/only: space-separated suites to run, to re-validate one suite cheaply (delete it when done)
only=; if [ -f .github/scripts/only ]; then only=$(cat .github/scripts/only); fi
in_only() { [ -z "$only" ] || [[ " $only " == *" $1 "* ]]; }
# flag NAME REGEX [push]: set on pull requests and dispatch; also on pushes when "push" is given.
flag() {
  local v=false
  if [ "$ev" = workflow_dispatch ]; then if [ "${3:-}" != pr ] && want "$1"; then v=true; fi
  elif [ "$ev" = pull_request ] || { [ "$ev" = push ] && [ "${3:-}" = push ]; }; then if hit "$2"; then v=true; fi
  fi
  if ! in_only "$1"; then v=false; fi
  if [ $v = true ]; then any=true; fi
  echo "$1=$v" >> "$GITHUB_OUTPUT"
}
# core: every change except docs-only ones (the old logos.yml paths-ignore)
core=false
if [ "$ev" = workflow_dispatch ]; then if want core || want gc; then core=true; fi
elif [ "$all" = true ] || { [ -n "$files" ] && grep -Evq '^(AGENTS|CONTRIBUTING|README|SECURITY)\.md$' <<< "$files"; }; then core=true
fi
if ! in_only core; then core=false; fi
if [ $core = true ]; then any=true; fi
echo "core=$core" >> "$GITHUB_OUTPUT"
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
elif [ "$ev" = pull_request ]; then
  gates=$(bash .github/scripts/gate-changes.sh "$base" | tr '\n' ' ')
fi
if ! in_only gates; then gates=; fi
if [ -n "$gates" ]; then any=true; fi
echo "gates=${gates:--}" >> "$GITHUB_OUTPUT"
echo "gate_any=$([ -n "$gates" ] && echo true || echo false)" >> "$GITHUB_OUTPUT"
# regression: the full run.bash gate the old compiler and SIMD workflows ran on
# manual dispatch too. On pull requests and pushes it follows core; on dispatch
# a targeted compiler or ssa selection brings it along.
regression=$core
if [ "$ev" = workflow_dispatch ] && { want compiler || want ssa; }; then regression=true; fi
if ! in_only regression; then regression=false; fi
echo "regression=$regression" >> "$GITHUB_OUTPUT"
echo "build=$any" >> "$GITHUB_OUTPUT"
printf 'changed files:\n%s\n--\n' "$files"; cat "$GITHUB_OUTPUT"

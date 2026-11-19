#!/usr/bin/env bash
# Path routing. Latchkey has no on.paths filter, so derive one true/false output per suite from the
# changed files (pull request: vs its base, push: the last commit, dispatch: the "suites" input).
set -euo pipefail
ev=${LATCHKEY_CI_EVENT_CLASS:-}
files=; base=
case $ev in
  pr_update)
    git fetch -q --no-tags --depth=1 origin "$LATCHKEY_CI_BASE_REF"
    base=$(git rev-parse FETCH_HEAD)
    files=$(git diff --name-only "$base" HEAD) ;;
  branch_push)
    git fetch -q --no-tags --depth=2 origin "$LATCHKEY_CI_SHA"
    files=$(git diff --name-only HEAD~1 HEAD) || files= ;;
esac
want() { [[ " ${LATCHKEY_INPUT_SUITES:-all} " == *" all "* || " ${LATCHKEY_INPUT_SUITES:-all} " == *" $1 "* ]]; }
any=false
# flag NAME REGEX [push]: set on pull requests and dispatch; also on pushes when "push" is given.
flag() {
  local v=false
  case $ev in
    workflow_dispatch) ! want "$1" || v=true ;;
    pr_update) ! grep -Eq "$2" <<< "$files" || v=true ;;
    branch_push) [ "${3:-}" != push ] || ! grep -Eq "$2" <<< "$files" || v=true ;;
  esac
  [ $v = false ] || any=true
  echo "$1=$v" >> "$LATCHKEY_CI_OUTPUT"
}
# core: every change except docs-only ones (the old logos.yml paths-ignore)
core=false
case $ev in
  workflow_dispatch) ! want core || core=true ;;
  *) ! grep -Evq '^(AGENTS|CONTRIBUTING|README|SECURITY)\.md$' <<< "$files" || core=true ;;
esac
[ $core = false ] || any=true
echo "core=$core" >> "$LATCHKEY_CI_OUTPUT"
flag ssa '^src/cmd/compile/internal/(ssa/|ssagen/(simd[^/]*|intrinsics[^/]*|ssa)\.go$|(amd64|arm64)/ssa\.go$|typecheck/(_builtin/runtime|builtin)\.go$|logos/wasm[^/]*_test\.go$)|^src/simd/archsimd/|^src/runtime/(autovec|export_autovec_logos_test)|^src/cmd/go/(scriptconds_test\.go$|testdata/script/logos_(simd_|eject_simd_slice))|^src/cmd/go/internal/logos/config/targetcpu|^test/logos/(run/simd_zero_guard|.*autovec)'
flag gc '^src/(runtime/|internal/abi/|internal/goexperiment/|logos/arena/)|^\.github/scripts/install-ci-tool\.sh$'
flag compiler '^src/(cmd/compile/|cmd/go/|internal/buildcfg/)|^test/logos/|^\.github/scripts/install-(ci-tool|upstream-oracles)\.sh$'
flag cgo '^misc/cgo/|^src/(cmd/cgo/|runtime/|internal/goexperiment/|internal/buildcfg/|cmd/go/internal/logos/settings/)' push
flag dogfood '^misc/logos-dogfood/|^src/'
flag bench '^src/cmd/compile/internal/ssa/(autovec_logos\.go|testdata/autovec_bench_test\.go)$|^test/logos/bench/autovec_measure\.sh$'
gates=
case $ev in
  workflow_dispatch) ! want gates || gates=$(bash .github/scripts/gate-changes.sh "" | tr '\n' ' ') ;;
  pr_update) gates=$(GITHUB_SHA=$LATCHKEY_CI_SHA bash .github/scripts/gate-changes.sh "$base" | tr '\n' ' ') ;;
esac
[ -z "$gates" ] || any=true
echo "gates=${gates:--}" >> "$LATCHKEY_CI_OUTPUT"
echo "gate_any=$([ -n "$gates" ] && echo true || echo false)" >> "$LATCHKEY_CI_OUTPUT"
echo "build=$any" >> "$LATCHKEY_CI_OUTPUT"
printf 'changed files:\n%s\n--\n' "$files"; cat "$LATCHKEY_CI_OUTPUT"

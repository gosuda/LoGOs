#!/usr/bin/env bash
# Exercise push routing against real Git histories without fetching the network.
set -euo pipefail
router=$(realpath "$(dirname "$0")/route.sh")
scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
repo="$scratch/repo"
mkdir "$repo"
cd "$repo"
git init -q
git config user.name "CI routing test"
git config user.email "ci-routing@example.invalid"
git remote add origin "$repo"
mkdir -p .github/scripts src/runtime
printf '#!/usr/bin/env bash\nprintf "fixture-gate\\n"\n' > .github/scripts/gate-changes.sh
printf 'initial\n' > README.md
git add .
git commit -qm initial
initial=$(git rev-parse HEAD)
# A cgo file the later move case deletes: it must exist in the base
# commit so the push range reports its deletion.
mkdir -p misc/cgo
printf 'package cgo_probe\n' > misc/cgo/cgo_probe
git add .
git commit -qm cgo-file
printf 'package runtime\n' > src/runtime/routing_probe.go
git add .
git commit -qm code
code=$(git rev-parse HEAD)
printf 'documentation\n' >> README.md
git add .
git commit -qm documentation
tip=$(git rev-parse HEAD)

run_push() {
  : > "$scratch/output"
  GITHUB_EVENT_NAME=push GITHUB_SHA="$2" PUSH_BEFORE_SHA="$1" PUSH_AFTER_SHA="$2" \
    GITHUB_OUTPUT="$scratch/output" bash "$router" > "$scratch/stdout" 2> "$scratch/stderr"
}

expect() {
  if ! grep -Fxq "$1" "$scratch/output"; then
    cat "$scratch/stdout" "$scratch/stderr" >&2
    printf 'missing route output: %s\n' "$1" >&2
    exit 1
  fi
}
# A docs-only final commit cannot hide the earlier code change in one push.
run_push "$initial" "$tip"
expect core=true
expect regression=true
expect cgo=true

# A push that moves a cgo file out of its watched path must still run
# the cgo suite: the deleted path is reported, not hidden by rename
# detection.
mkdir -p docs/moved
git mv misc/cgo/cgo_probe docs/moved/cgo_probe
git commit -qm move-cgo-file
move_tip=$(git rev-parse HEAD)
run_push "$code" "$move_tip"
expect cgo=true

# A push containing only the documentation edit can still skip core work.
run_push "$code" "$tip"
expect core=false
expect build=false

# A genuinely empty range should not treat the here-string's newline as a file.
run_push "$tip" "$tip"
expect core=false
expect build=false

# New branches and unavailable old revisions conservatively select push suites.
for before in 0000000000000000000000000000000000000000 1111111111111111111111111111111111111111 ""; do
  run_push "$before" "$tip"
  expect core=true
  expect cgo=true
  expect gate_any=true
  expect build=true
done

# A forced update need not have a common ancestor with the previous tip.
git checkout -q --orphan replacement
git rm -qrf .
mkdir -p .github/scripts
printf '#!/usr/bin/env bash\nprintf "fixture-gate\\n"\n' > .github/scripts/gate-changes.sh
printf 'replacement\n' > README.md
git add .
git commit -qm replacement
replacement=$(git rev-parse HEAD)
run_push "$tip" "$replacement"
expect core=true
expect regression=true

# A manual GC campaign also needs the core/upstream regression.
: > "$scratch/output"
GITHUB_EVENT_NAME=workflow_dispatch INPUT_SUITES=gc \
  GITHUB_OUTPUT="$scratch/output" bash "$router" > "$scratch/stdout"
expect core=true
expect gc=true
expect regression=true
printf 'Routing regression cases passed.\n'

#!/usr/bin/env bash
# Dogfood corpus replay for the ci.yml `dogfood` job step
# "Corpus: fixtures, backends, FFI, language, lint".
# Each check pipes through tee and trips the fail accumulator, so this file
# sets pipefail itself and stays free of -e, exactly like the inline block.
. .github/scripts/env.sh
set -o pipefail
fail=0
uv run --no-project python misc/logos-dogfood/run.py \
  --go-oracle "$RUNNER_TEMP/go1.27.4-ci/bin/go" \
  --go-candidate "$GITHUB_WORKSPACE/bin/go" --logos "$GITHUB_WORKSPACE/bin/logos" \
  --source-root "$INPUTS/sources" --module-cache "$INPUTS/modules" \
  --cache-root "$RUNNER_TEMP/corpus-cache" --operation test \
  --only fixture-semantics --only fixture-cgo --stage oracle --stage go --stage logos \
  --jobs 2 --timeout 600 --out "$EVIDENCE/fixtures" \
  2>&1 | tee "$EVIDENCE/fixtures-console.txt" || fail=1
uv run --no-project python misc/logos-dogfood/replay_backends.py \
  --go-oracle "$RUNNER_TEMP/go1.27.4-ci/bin/go" \
  --go-candidate "$GITHUB_WORKSPACE/bin/go" --logos "$GITHUB_WORKSPACE/bin/logos" \
  --source-root "$INPUTS/sources" --module-cache "$INPUTS/modules" \
  --cache-root "$RUNNER_TEMP/corpus-cache" --tmpdir "$RUNNER_TEMP/dogfood-tmp" \
  --timeout 900 --out "$EVIDENCE/backends" \
  2>&1 | tee "$EVIDENCE/backends-console.txt" || fail=1
uv run --no-project python misc/logos-dogfood/replay_ffi.py \
  --go-oracle "$RUNNER_TEMP/go1.27.4-ci/bin/go" \
  --go-candidate "$GITHUB_WORKSPACE/bin/go" --logos "$GITHUB_WORKSPACE/bin/logos" \
  --source-root "$INPUTS/sources" --module-cache "$INPUTS/modules" \
  --cache-root "$RUNNER_TEMP/corpus-cache" --tmpdir "$RUNNER_TEMP/dogfood-tmp" \
  --out "$EVIDENCE/ffi" 2>&1 | tee "$EVIDENCE/ffi-console.txt" || fail=1
uv run --no-project python misc/logos-dogfood/evidence/2026-11-16/runtime/replay.py \
  --oracle "$RUNNER_TEMP/go1.27.4-ci/bin/go" \
  --candidate "$GITHUB_WORKSPACE/bin/go" --logos "$GITHUB_WORKSPACE/bin/logos" \
  --source-root "$INPUTS/sources" --module-cache "$INPUTS/modules" \
  --cache-root "$RUNNER_TEMP/corpus-cache" --tmpdir "$RUNNER_TEMP/dogfood-tmp" \
  --part language --out "$EVIDENCE/language" 2>&1 | tee "$EVIDENCE/language-console.txt" || fail=1
uv run --no-project python misc/logos-dogfood/evidence/2026-11-16/runtime/replay.py \
  --oracle "$RUNNER_TEMP/go1.27.4-ci/bin/go" \
  --candidate "$GITHUB_WORKSPACE/bin/go" --logos "$GITHUB_WORKSPACE/bin/logos" \
  --source-root "$INPUTS/sources" --module-cache "$INPUTS/modules" \
  --cache-root "$RUNNER_TEMP/corpus-cache" --tmpdir "$RUNNER_TEMP/dogfood-tmp" \
  --part lint --out "$EVIDENCE/lint" 2>&1 | tee "$EVIDENCE/lint-console.txt" || fail=1
exit "$fail"

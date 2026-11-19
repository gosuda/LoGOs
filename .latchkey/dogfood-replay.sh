#!/usr/bin/env bash
# Full corpus replay for the ci.yml `dogfood` job step
# "Corpus: replay and assess all 196 cells".
# This step toggles -e itself around the piped replay, so this file stays
# free of set -e at the top, exactly like the inline block.
. .latchkey/env.sh
set +e
uv run --no-project python misc/logos-dogfood/run.py \
  --manifest misc/logos-dogfood/corpus-ci.json \
  --go-oracle "$RUNNER_TEMP/go1.27.4-ci/bin/go" \
  --go-candidate "$GITHUB_WORKSPACE/bin/go" --logos "$GITHUB_WORKSPACE/bin/logos" \
  --gglint "$RUNNER_TEMP/gglint" \
  --source-root "$INPUTS/sources" --module-cache "$INPUTS/modules" \
  --cache-root "$RUNNER_TEMP/corpus-cache" --operation build \
  --jobs 2 --timeout 600 --out "$EVIDENCE/corpus" \
  2>&1 | tee "$EVIDENCE/corpus-console.txt"
statuses=("${PIPESTATUS[@]}")
set -e
printf 'runner=%s\nconsole_capture=%s\n' "${statuses[0]}" "${statuses[1]}" | tee "$EVIDENCE/corpus-exit.txt"
test "${statuses[0]}" -eq 1
test "${statuses[1]}" -eq 0
uv run --no-project python misc/logos-dogfood/accept_corpus.py \
  --manifest misc/logos-dogfood/corpus-ci.json --report "$EVIDENCE/corpus/report.json" \
  2>&1 | tee "$EVIDENCE/corpus-acceptance.txt"

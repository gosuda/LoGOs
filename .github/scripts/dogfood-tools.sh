#!/usr/bin/env bash
# Tools-diagnostics capture for the ci.yml `dogfood` job step
# "Corpus: tools diagnostics".
# This step toggles -e itself around the piped probe, so this file stays
# free of set -e at the top, exactly like the inline block.
. .github/scripts/env.sh
set +e
uv run --no-project python misc/logos-dogfood/run.py \
  --manifest misc/logos-dogfood/corpus-permissions.json --only tools \
  --go-oracle "$RUNNER_TEMP/go1.27.4-ci/bin/go" \
  --go-candidate "$GITHUB_WORKSPACE/bin/go" --logos "$GITHUB_WORKSPACE/bin/logos" \
  --source-root "$INPUTS/sources" --module-cache "$INPUTS/modules" \
  --cache-root "$RUNNER_TEMP/corpus-cache" --operation build \
  --stage oracle --stage go --stage logos \
  --jobs 2 --timeout 600 --out "$EVIDENCE/tools-probe" \
  2>&1 | tee "$EVIDENCE/tools-probe-console.txt"
statuses=("${PIPESTATUS[@]}")
set -e
printf 'runner=%s\nconsole_capture=%s\n' "${statuses[0]}" "${statuses[1]}" | tee "$EVIDENCE/tools-probe-exit.txt"
test "${statuses[0]}" -eq 0 || test "${statuses[0]}" -eq 1
test "${statuses[1]}" -eq 0
test -f "$EVIDENCE/tools-probe/report.json"
# This diagnostic capture does not accept the corpus. The complete
# replay and its unchanged acceptance checker below remain required.
uv run --no-project python misc/logos-dogfood/probe_tools.py \
  --logos "$GITHUB_WORKSPACE/bin/logos" \
  --source-root "$INPUTS/sources" --module-cache "$INPUTS/modules" \
  --cache-root "$RUNNER_TEMP/corpus-cache" \
  --baseline "$EVIDENCE/tools-probe/report.json" \
  --jobs 2 --timeout 600 --out "$EVIDENCE/tools-unlimited"

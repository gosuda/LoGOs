#!/usr/bin/env bash
# Contracts gate for the ci.yml `dogfood` job step
# "Corpus: contracts, exact sources".
# Latchkey runs each step as `bash -e -o pipefail`; this file sets the same,
# so the moved body behaves exactly like the inline block. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh
printf 'INPUTS=%s\nEVIDENCE=%s\n' "$RUNNER_TEMP/logos-corpus-inputs" "$RUNNER_TEMP/logos-corpus-evidence" >> "$GITHUB_ENV"
. .latchkey/env.sh
mkdir -p "$EVIDENCE" "$RUNNER_TEMP/dogfood-tmp"
git rev-parse HEAD HEAD:src | tee "$EVIDENCE/logos-source.txt"
uv --version | tee "$EVIDENCE/uv-version.txt"
uv run --no-project --with-requirements misc/logos-dogfood/test-requirements.txt python -m pytest -q misc/logos-dogfood/test_run.py misc/logos-dogfood/test_prepare.py misc/logos-dogfood/test_accept_corpus.py misc/logos-dogfood/test_replay_backends.py misc/logos-dogfood/test_replay_backend_events.py misc/logos-dogfood/test_probe_tools.py 2>&1 | tee "$EVIDENCE/contracts.txt"
checked=(misc/logos-dogfood/prepare.py misc/logos-dogfood/test_prepare.py misc/logos-dogfood/accept_corpus.py misc/logos-dogfood/test_accept_corpus.py misc/logos-dogfood/replay_backends.py misc/logos-dogfood/test_replay_backends.py misc/logos-dogfood/test_replay_backend_events.py misc/logos-dogfood/probe_tools.py misc/logos-dogfood/test_probe_tools.py)
uv run --no-project --with-requirements misc/logos-dogfood/test-requirements.txt pyright "${checked[@]}" 2>&1 | tee "$EVIDENCE/helper-types.txt"
uv run --no-project --with-requirements misc/logos-dogfood/test-requirements.txt ruff check "${checked[@]}" 2>&1 | tee "$EVIDENCE/helper-lint.txt"
uv run --no-project --with-requirements misc/logos-dogfood/test-requirements.txt ruff format --check "${checked[@]}" 2>&1 | tee "$EVIDENCE/helper-format.txt"
uv run --no-project python misc/logos-dogfood/prepare.py --manifest misc/logos-dogfood/corpus-ci.json --go-oracle "$RUNNER_TEMP/go1.27.4-ci/bin/go" --out "$INPUTS" 2>&1 | tee "$EVIDENCE/preparation.txt"

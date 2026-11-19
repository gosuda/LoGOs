#!/usr/bin/env bash
# Evidence checker step for the ci.yml `unit` job.
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh
v=logos-doc/validation e=$RUNNER_TEMP/logos-regression-evidence
py() { uv run --no-project --no-config --python 3.12 --no-python-downloads python "$@"; }
fail=0
bash .github/scripts/verify-evidence-checkers.sh || fail=1
py $v/upstream-host/check_wasm.py --repo "$PWD" --evidence $e/wasm || fail=1
py $v/tagged-package-facts/check_ci.py verify --root "$PWD" --out $e/tagged-package --expected-head "$(git rev-parse HEAD)" || fail=1
py $v/tagged-package-facts/check_ci.py run --root "$PWD" --out $e/tagged-package --expected-head "$(git rev-parse HEAD)" || fail=1
py $v/tagged-method-facts/check_ci.py --repo "$PWD" --evidence $e/tagged-method || fail=1
py $v/field-offset-lint/field_offset_ci.py --repo "$PWD" --evidence $e/field-offset || fail=1
py $v/simd-transitive-imports/simd_imports_ci.py --repo "$PWD" --evidence $e/simd-imports || fail=1
py $v/zero-array-reference-free/zero_array_ci.py --repo "$PWD" --evidence $e/zero-array || fail=1
exit $fail

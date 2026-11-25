#!/usr/bin/env bash
# Early-evidence retain for the ci.yml `dogfood` job step
# "Retain early tools diagnostics".
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .github/scripts/env.sh
rm -rf dogfood-early; mkdir -p dogfood-early
if [ -n "${EVIDENCE:-}" ] && [ -d "$EVIDENCE" ]; then
  for p in tools-probe tools-probe-console.txt tools-probe-exit.txt tools-unlimited logos-source.txt uv-version.txt contracts.txt helper-types.txt helper-lint.txt helper-format.txt preparation.txt gglint-fetch.txt gglint-source.txt gglint-downloads.json gglint-build.txt gglint-build-info.txt gglint.sha256; do
    cp -r "$EVIDENCE/$p" dogfood-early/ 2>/dev/null || true
  done
  find dogfood-early -type d -name work -prune -exec rm -rf {} +
fi

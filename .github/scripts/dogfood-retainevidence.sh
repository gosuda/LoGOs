#!/usr/bin/env bash
# Evidence retain for the ci.yml `dogfood` job step
# "Retain source receipts, reports, and raw evidence".
# Folds dogfood-early/ into dogfood-evidence/early/: the separate early
# upload is gone, so the early files ride on the final upload instead.
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .github/scripts/env.sh
rm -rf dogfood-evidence; mkdir -p dogfood-evidence
if [ -d dogfood-early ]; then
  mkdir -p dogfood-evidence/early
  cp -r dogfood-early/. dogfood-evidence/early/ 2>/dev/null || true
fi
if [ -n "${EVIDENCE:-}" ] && [ -d "$EVIDENCE" ]; then
  cp -r "$EVIDENCE" dogfood-evidence/evidence
  find dogfood-evidence/evidence -type d -name work -prune -exec rm -rf {} +
fi
if [ -n "${INPUTS:-}" ] && [ -d "$INPUTS" ]; then
  mkdir -p dogfood-evidence/inputs
  for p in preparation.json logs inputs; do
    cp -r "$INPUTS/$p" dogfood-evidence/inputs/ 2>/dev/null || true
  done
fi
for p in misc/logos-dogfood/corpus-ci.json misc/logos-dogfood/policies misc/logos-dogfood/profiles; do
  cp -r "$p" dogfood-evidence/ 2>/dev/null || true
done

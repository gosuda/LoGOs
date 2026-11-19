#!/usr/bin/env bash
# Evidence retain for the ci.yml `dogfood` job step
# "Retain source receipts, reports, and raw evidence".
. .latchkey/env.sh
rm -rf dogfood-evidence; mkdir -p dogfood-evidence
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

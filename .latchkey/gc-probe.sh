#!/usr/bin/env bash
# Close-ownership probe for the ci.yml `gc` job.
# Latchkey runs each step as `bash -e -o pipefail`; this file sets the same,
# so the moved body behaves exactly like the inline block. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh; cd src; export GOEXPERIMENT=$EXP
probe="$GITHUB_WORKSPACE/.outline/verify/gc-close-$RUNNER_ARCH"
mkdir -p "$probe"
awk '
  /^func logosRegionReleaseChunks\(rec \*logosRegion\) \{$/ {
    in_release = 1
    seen++
  }
  in_release && /^\t\/\/ Detach the published root before any chunk becomes reusable\.$/ { next }
  in_release && /^\trec\.chunk = nil$/ { removed++; next }
  in_release && /^}$/ {
    print "\trec.chunk = nil"
    in_release = 0
  }
  { print }
  END { if (in_release || seen != 1 || removed != 1) exit 2 }
' runtime/region_logos.go > "$probe/region_logos.go"
test "$(git hash-object "$probe/region_logos.go")" = f1c09676d87d8d4bc82255eef5a9a8a7e8794c17
jq -n --arg from "$PWD/runtime/region_logos.go" --arg to "$probe/region_logos.go" \
  '{"Replace": {($from): $to}}' > "$probe/overlay.json"
set +e
go test -overlay="$probe/overlay.json" -count=10 -short -timeout=10m \
  runtime -run '^TestLogosRegionReleaseOwnership$' > "$probe/mutation.log" 2>&1
status=$?
set -e
cat "$probe/mutation.log"
test "$status" -eq 1
awk '
  /--- FAIL: TestLogosRegionReleaseOwnership/ { named = 1 }
  /published region root .* is also in a reusable chunk pool/ { diagnostic = 1 }
  /panic:|fatal error:|build failed|test timed out/ { unrelated = 1 }
  END { exit !(named && diagnostic && !unrelated) }
' "$probe/mutation.log"

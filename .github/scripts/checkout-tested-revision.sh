#!/usr/bin/env bash
# Fetches and checks out exactly one commit (the tested revision) in the
# current directory, then asserts HEAD matches it.
set -euo pipefail
sha=${1:?expected a 40-character commit SHA}
[[ $sha =~ ^[0-9a-f]{40}$ ]]
git init --quiet .
git remote add origin "$GITHUB_SERVER_URL/$GITHUB_REPOSITORY.git"
git -c protocol.version=2 fetch --no-tags --depth=1 origin "$sha"
git -c advice.detachedHead=false checkout --detach FETCH_HEAD
test "$(git rev-parse HEAD)" = "$sha"

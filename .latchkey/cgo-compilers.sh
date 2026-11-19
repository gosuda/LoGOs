#!/usr/bin/env bash
# C compiler setup shared by the cgo, cgofull and cgobench jobs.
# Latchkey runs each step as `bash -e -o pipefail`; this file sets the same,
# so the moved body behaves exactly like the inline block. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh
sudo apt-get update -qq && sudo apt-get install -y -qq gccgo
for c in gcc g++ gccgo clang; do command -v $c; done
echo "GCCGO=$(command -v gccgo)" >> "$GITHUB_ENV"

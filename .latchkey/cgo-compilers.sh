#!/usr/bin/env bash
# C compiler setup shared by the cgo, cgofull and cgobench jobs.
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh
sudo apt-get update -qq && sudo apt-get install -y -qq gccgo
for c in gcc g++ gccgo clang; do command -v $c; done
echo "GCCGO=$(command -v gccgo)" >> "$GITHUB_ENV"

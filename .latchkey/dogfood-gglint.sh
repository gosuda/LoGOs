#!/usr/bin/env bash
# Reviewed-gglint build for the ci.yml `dogfood` job step
# "Corpus: build reviewed gglint".
. .latchkey/env.sh
export GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org GOMODCACHE="$RUNNER_TEMP/gglint-modules"
source="$RUNNER_TEMP/gglint-source"
git init --quiet --object-format=sha1 "$source"
git -C "$source" remote add origin https://github.com/gosuda/gglint.git
git -C "$source" fetch --no-tags --depth=1 origin "$GGLINT_REVISION" 2>&1 | tee "$EVIDENCE/gglint-fetch.txt"
git -C "$source" checkout --detach --quiet "$GGLINT_REVISION"
test "$(git -C "$source" rev-parse HEAD)" = "$GGLINT_REVISION"
cd "$source"
git rev-parse HEAD 'HEAD^{tree}' | tee "$EVIDENCE/gglint-source.txt"
go mod download -json > "$EVIDENCE/gglint-downloads.json"
test -z "$(git status --porcelain --untracked-files=all)"
go build -mod=readonly -trimpath -o "$RUNNER_TEMP/gglint" ./cmd/gglint 2>&1 | tee "$EVIDENCE/gglint-build.txt"
test -z "$(git status --porcelain --untracked-files=all)"
go version -m "$RUNNER_TEMP/gglint" | tee "$EVIDENCE/gglint-build-info.txt"
sha256sum "$RUNNER_TEMP/gglint" | tee "$EVIDENCE/gglint.sha256"

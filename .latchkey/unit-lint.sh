#!/usr/bin/env bash
# Lint step for the ci.yml `unit` job.
# Latchkey runs each step as `bash -e -o pipefail`; this file sets the same,
# so the moved body behaves exactly like the inline block. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .latchkey/env.sh; cd src
read -r -a packages <<< "$LOGOS_PACKAGES"
fail=0
unformatted=$(gofmt -l logos internal/logosfile cmd/go/internal/logos cmd/compile/internal/logos cmd/internal/logos cmd/internal/logosowned cmd/cgo/internal/testlogos)
printf '%s\n' "$unformatted"; [ -z "$unformatted" ] || fail=1
go vet "${packages[@]}" || fail=1
archive="golangci-lint-${GOLANGCI_LINT_VERSION}-linux-amd64"
curl --fail --silent --show-error --location --output "$RUNNER_TEMP/$archive.tar.gz" "https://github.com/golangci/golangci-lint/releases/download/v${GOLANGCI_LINT_VERSION}/$archive.tar.gz"
printf '%s  %s\n' "$GOLANGCI_LINT_SHA256" "$RUNNER_TEMP/$archive.tar.gz" | sha256sum --check --strict -
tar -xzf "$RUNNER_TEMP/$archive.tar.gz" -C "$RUNNER_TEMP"
(cd cmd && "$RUNNER_TEMP/$archive/golangci-lint" run --config ../../logos-doc/golangci.yml ./go/internal/logos/...) || fail=1
exit $fail

#!/usr/bin/env bash
# Compiler checks for the ci.yml `compiler` job.
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .github/scripts/env.sh
sha256sum --check logos-doc/validation/compiler-stability/frozen-tests-v2.sha256
cd src
unformatted=$(gofmt -l cmd/compile/internal/inline/interleaved cmd/compile/internal/logos cmd/go/internal/logos/eject)
printf '%s\n' "$unformatted"; test -z "$unformatted"
pk="cmd/compile/internal/inline/... cmd/compile/internal/logos/... cmd/compile/internal/base cmd/compile/internal/noder cmd/compile/internal/syntax cmd/compile/internal/types2 cmd/go/internal/work cmd/go/internal/logos/eject internal/buildcfg"
go test -count=1 -short -timeout=20m $pk
go test -count=1 -short -race -timeout=15m cmd/go/internal/work cmd/go/internal/logos/eject internal/buildcfg
go test -count=1 -timeout=20m cmd/go -run 'Script/^logos_(ctfe_assignment_.*|tuple_(package_names|variant_names)|spread_defaults|inline_batches|eject_variant_tags|struct_default_validation)$'
go test -count=1 -race -timeout=30m cmd/go -run 'Script/^logos_(eject_cgo.*|cgo_lgo|eject|eject_target_(features|env_compat)|eject_inactive.*|eject_variant_package_names)$'
go test -count=1 -timeout=20m cmd/internal/testdir -run 'Test/logos/run/(ctfe_assignment_.*|const_func_map_field_store|spread_defaults|inline_batches)'
go vet $(printf '%s\n' $pk | grep -v '/noder$')
go vet -pgo="$PWD/cmd/compile/default.pgo" cmd/compile/internal/noder

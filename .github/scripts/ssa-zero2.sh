#!/usr/bin/env bash
# SIMD zero guards: native and emulated (qemu) CPUs. Step of the ci.yml `ssa` job (`ssa.sh zero2`).
# This file sets its own shell options, so the moved body behaves exactly
# like the inline block whatever the step shell provides. No -u: bodies
# expand variables the runner leaves empty.
set -eo pipefail
. .github/scripts/env.sh; cd src
qemu_dir="$RUNNER_TEMP/simd-zero-qemu"
mkdir "$qemu_dir"
curl --fail --silent --show-error --location --output "$qemu_dir/qemu-x86_64" https://github.com/multiarch/qemu-user-static/releases/download/v7.2.0-1/qemu-x86_64-static
printf '%s  %s\n' 7132ffd39aef71c26d3344cc0c7dffc530e10e3e720c58c8279a97ef6fdd7784 "$qemu_dir/qemu-x86_64" | sha256sum --check --strict -
chmod +x "$qemu_dir/qemu-x86_64"
"$qemu_dir/qemu-x86_64" --version | tee "$RUNNER_TEMP/simd-zero-evidence/qemu-version.txt"
printf '%s\n' "$qemu_dir" >> "$GITHUB_PATH"
expected=../test/logos/run/simd_zero_guard.out
binary="$RUNNER_TEMP/simd-zero-guard"
GOEXPERIMENT=simd GOAMD64=v1 go build -o "$binary" ../test/logos/run/simd_zero_guard.go
"$binary" > "$RUNNER_TEMP/simd-zero-evidence/native.out"
cmp "$expected" "$RUNNER_TEMP/simd-zero-evidence/native.out"
for cpu in Nehalem Haswell; do
  for width in 128 256 512; do
    output="$RUNNER_TEMP/simd-zero-evidence/qemu-$cpu-$width"
    "$qemu_dir/qemu-x86_64" -cpu "$cpu" "$binary" "$width" > "$output.out" 2> "$output.err"
    cmp "$expected" "$output.out"
    printf 'CPU=%s width=%s: exact output passed\n' "$cpu" "$width"
  done
done | tee "$RUNNER_TEMP/simd-zero-evidence/qemu-checks.txt"
fixture=cmd/go/testdata/script/logos_eject_simd_slice.txt
module="$RUNNER_TEMP/simd-zero-evidence/eject-simd-slice"
mkdir "$module"
test "$(grep -Fxc -- '-- m/go.mod --' "$fixture")" -eq 1
test "$(grep -Fxc -- '-- m/main.lgo --' "$fixture")" -eq 1
awk '$0 == "-- m/go.mod --" { copy = 1; next } copy && /^-- .* --$/ { exit } copy { print }' "$fixture" > "$module/go.mod"
awk '$0 == "-- m/main.lgo --" { copy = 1; next } copy && /^-- .* --$/ { exit } copy { print }' "$fixture" > "$module/main.lgo"
sha256sum "$module/go.mod" "$module/main.lgo" | tee "$module/sources.sha256"
printf '%s  %s\n' 6fe5f1a6cc7f1f3f5aa25b429e2b452a4aa088a3646587e5ba14a3e249cc35f0 "$module/main.lgo" | sha256sum --check --strict -
(
  cd "$module"
  export GOENV=off GOTOOLCHAIN=local GOWORK=off GOPROXY=off
  GOEXPERIMENT='' GOAMD64=v1 "$GITHUB_WORKSPACE/bin/logos" build -o "$RUNNER_TEMP/eject-scalar" . 2>&1 | tee "$module/scalar-build.txt"
  GOEXPERIMENT=simd GOAMD64=v1 "$GITHUB_WORKSPACE/bin/logos" build -o "$RUNNER_TEMP/eject-simd" . 2>&1 | tee "$module/simd-build.txt"
)
"$RUNNER_TEMP/eject-scalar" > "$module/scalar.out" 2> "$module/scalar.err"
test "$(wc -l < "$module/scalar.out")" -eq 72
"$RUNNER_TEMP/eject-simd" > "$module/native-simd.out" 2> "$module/native-simd.err"
cmp "$module/scalar.out" "$module/native-simd.out"
for cpu in Nehalem Haswell; do
  "$RUNNER_TEMP/simd-zero-qemu/qemu-x86_64" -cpu "$cpu" "$RUNNER_TEMP/eject-simd" > "$module/$cpu.out" 2> "$module/$cpu.err"
  cmp "$module/scalar.out" "$module/$cpu.out"
  printf 'Original slice-sum program on %s: exact output passed\n' "$cpu"
done | tee "$module/checks.txt"

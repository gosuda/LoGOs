#!/usr/bin/env bash
# Job setup: setup.sh build|restore [oracles] [py] [c23]
#   build    pinned tools, then build the toolchain once and pack it for the other jobs (~/tc)
#   restore  pinned tools, then unpack that toolchain
#   oracles  upstream Go 1.27.2/1.27.3 oracles    py  Python 3.12 for uv    c23  gcc-14 as cc
set -euo pipefail
mode=$1; shift
. .github/scripts/env.sh
bash .github/scripts/install-ci-tool.sh go
bash .github/scripts/install-ci-tool.sh node
. .github/scripts/env.sh
uv=$RUNNER_TEMP/uv.tar.gz
curl --fail --silent --show-error --location --output "$uv" https://github.com/astral-sh/uv/releases/download/0.13.8/uv-x86_64-unknown-linux-gnu.tar.gz
echo "5acfd9177cfecf0bf87578f059728215ccc884388988f55f7a65041b57387d91  $uv" | sha256sum --check --strict -
mkdir "$RUNNER_TEMP/uv" "$RUNNER_TEMP/logos-regression-evidence"
tar -xzf "$uv" -C "$RUNNER_TEMP/uv" --strip-components=1
printf '%s\n' "$RUNNER_TEMP/uv" >> "$GITHUB_PATH"
printf 'PYTHONDONTWRITEBYTECODE=1\nPYTEST_DISABLE_PLUGIN_AUTOLOAD=1\n' >> "$GITHUB_ENV"
. .github/scripts/env.sh
for f in "$@"; do
  case $f in
    oracles) bash .github/scripts/install-upstream-oracles.sh ;;
    py) uv python install 3.12 ;;
    c23)
      bin=$RUNNER_TEMP/c23-bin; mkdir "$bin"; ln -s "$(command -v gcc-14)" "$bin/cc"
      printf 'int main(void) { return 0; }\n' | "$bin/cc" -std=c23 -x c - -o "$bin/check"
      "$bin/check"; printf '%s\n' "$bin" >> "$GITHUB_PATH" ;;
  esac
done
if [ "$mode" = build ]; then
  (cd src && GOROOT_BOOTSTRAP=$(go env GOROOT) ./make.bash)
else
  tar -xzf ~/tc/tree.tgz
  tar -xzf ~/tc/cache.tgz -C ~
  rm -rf ~/tc
fi
[[ " $* " == *" nopath "* ]] || printf '%s\n' "$PWD/lib/wasm" "$PWD/bin" >> "$GITHUB_PATH"
. .github/scripts/env.sh
if [ "$mode" = build ]; then
  logos version | grep -F "logos version $(head -n 1 VERSION)"
  logos help 2>&1 | grep -F 'logos <command> [arguments]'
  mkdir ~/tc
  git ls-files -z -o -i --exclude-standard | tar --null -T - -czf ~/tc/tree.tgz
  cache=$(go env GOCACHE); tar -C ~ -czf ~/tc/cache.tgz "${cache#"$HOME"/}"
  du -sh ~/tc/*
fi

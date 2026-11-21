# Sourced first in every step. Steps run in their own shells, so this recreates
# GitHub Actions' env/path files and ported steps keep their ">> $GITHUB_ENV" / ">> $GITHUB_PATH" lines.
export GITHUB_WORKSPACE=$PWD RUNNER_ARCH=X64 GITHUB_ENV=$HOME/.gh_env GITHUB_PATH=$HOME/.gh_path
touch "$GITHUB_ENV" "$GITHUB_PATH"
set -a; . "$GITHUB_ENV"; set +a
[ ! -s "$GITHUB_PATH" ] || PATH=$(tac "$GITHUB_PATH" | paste -sd:):$PATH
export PATH

---
name: dogfooding-logos
description: Install the bootstrapped logos (bin/bootstrap/logos) as the everyday logos command, test it with the repository's logos suites, and compare it with stage 1. Use when the user says dogfood logos, install the bootstrapped logos, update the installed logos command, or test the bootstrapped binary. Needs a built stage 2.
license: BSD-3-Clause
compatibility: Needs bin/bootstrap/logos from a bootstrap build and bin/logos from make.bash, inside a LoGOs checkout, plus an upstream Go on PATH.
metadata:
  version: "0.22.0"
---

# Dogfood logos

Dogfooding means the logos you use every day was built by logos. Stage 1 is `bin/logos` (built from an upstream Go). Stage 2 is `bin/bootstrap/logos` (built by stage 1 at `-O=3`). This skill installs stage 2 on the user's `PATH` and checks that it behaves like stage 1.

It writes only one symlink (step 2), temporary directories, and test logs. It never copies binaries, never links a `go` command, and never changes `bin/logos`.

## Procedure

1. Check stage 2. From the checkout root run `bin/bootstrap/logos version`. Done when it prints `logos version go<VERSION>-X:logosarena <os>/<arch>`. When the file is missing, stop and tell the user to build it from the checkout root with `./bin/logos build -O=3 -o ./bin/bootstrap/logos cmd/go`.
2. Install by symlink. Set `bindir="${GOBIN:-$(go env GOPATH)/bin}"`, run `ls -l "$bindir/logos"` and keep the output, then run `mkdir -p "$bindir" && ln -sfn "$PWD/bin/bootstrap/logos" "$bindir/logos"`. When the target is a regular file and not a symlink, stop and ask first, because replacing it cannot be undone. A copy never works: the binary finds its GOROOT from its real path, and a copy fails with `go: cannot find GOROOT directory`. Done when, from a directory outside the checkout, `"$bindir/logos" version` and `"$bindir/logos" env GOROOT` print the stage 2 version and the checkout root, and `command -v logos` prints `$bindir/logos`. When another `logos` comes first on `PATH`, report its path.
3. Run the suites against stage 2. Put stage 2 first on `PATH` under the name `go`, so the tests find it with `exec.LookPath("go")`:
   `T=$(mktemp -d) && mkdir "$T/bin" && ln -s "$PWD/bin/bootstrap/logos" "$T/bin/go" && ln -s "$PWD/bin/gofmt" "$T/bin/gofmt" && export PATH="$T/bin:$PATH"`
   The script tests compare ejected output against upstream toolchains. Set `UPROOT1272` and `UPROOT1273` to upstream Go 1.27.2 and 1.27.3 roots and `UPROOT1274` to the upstream baseline root. An upstream Go prints a root with `GOTOOLCHAIN=go1.27.2 go env GOROOT`, which downloads that release on first use. Without these variables over 200 `logos_` script tests fail on `go version go1.27.2` assertions or on a system `go` that is too old. Done when `$UPROOT1272/bin/go version`, `$UPROOT1273/bin/go version`, and `$UPROOT1274/bin/go version` print their own releases.
   Then run the three groups from `src/`, each in its own subshell so they run in parallel, and `wait`:
   - `( go test -count=1 -short cmd/compile/internal/logos/... cmd/go/internal/logos/... cmd/internal/logos/... cmd/internal/logosowned internal/logosfile logos/... cmd/cgo/internal/testlogos > "$T/pkgs.log" 2>&1; echo "exit=$?" >> "$T/pkgs.log" ) &`
   - `( go test -count=1 cmd/go -run 'Script/^logos_' > "$T/script.log" 2>&1; echo "exit=$?" >> "$T/script.log" ) &`
   - `( go test -count=1 cmd/internal/testdir -run 'Test/logos/' > "$T/testdir.log" 2>&1; echo "exit=$?" >> "$T/testdir.log" ) &`
   The form `cmd > log; echo ... &` backgrounds only the `echo` and runs the groups one after another. Run the job in the background with no deadline; the script group took about eight minutes at a high load average. Done when each log ends with `exit=0`, or each failure is named with its test and reason.
4. Attribute every failure. Rerun the failing group with stage 1 first on `PATH` (the checkout's `bin` directory) and the same variables. Done when each failure is classed as environment (a missing `UPROOT` variable or toolchain), stage 2 only, or both stages. Call a failure pre-existing only with that baseline run.
5. Use it. Do day-to-day work with the installed `logos`. When a result looks wrong, rerun the same command with `bin/logos` and compare stdout, stderr, and exit code. Done when each difference is reported with the exact command, both outputs, and the stage that produced each.
6. Refresh. After any stage 1 rebuild or baseline change, rebuild stage 2 from the checkout root and rerun step 3. The symlink needs no change.

## Rules

- A symlink target outside the checkout, a copy, or a linked `go` is a failed install.
- Never rebuild stage 1 while a test job runs: `make.bash` rewrites `bin/` and `pkg/tool/`.
- Stage 2 is a different binary from stage 1 (`-O=3`, the `logosarena` experiment, larger file). A version suffix or a size difference is expected and is not a defect.

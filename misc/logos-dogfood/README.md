# Differential dogfood corpus

This replay compares the same pinned source and build settings through stock Go,
the fork's `go`, and `logos`. It also checks the report contracts of `logos lint`
and, when supplied, gglint in its basic, standard, and strict modes.

The manifest has **24 external sources and two local adversarial fixtures**.
Assembly/purego variants of xxhash and cgo/stub variants of go-sqlite3 bring the
total to **28 cases**. These are explicit samples, not exhaustive coverage of the
Go ecosystem or a claim that the third-party projects contain no defects.

## Inputs

`corpus.json` records each module, upstream URL, revision or module version,
checksum where applicable, selection cohort, backend, cgo setting, build tags,
package patterns, and operation. Inputs remain outside this repository except
for the small original fixtures in `fixtures/`.

| Cohort | Selection purpose | Examples |
|---|---|---|
| `popular` | Widely used implementation paths | chi, zap, protobuf, go-sqlite3, xxhash, UUID |
| `reference` | Small libraries or useful implementation practices | go-cmp, multierr, TOML, errorlint, scfg, exhaustive |
| `stress` | Generated code, unsafe/FFI, legacy control flow, or deliberately vulnerable code | modernc, purego FFI, YAML, x/sys, GoVWA |

The cohorts are sampling labels. They do not certify code quality. GoVWA is an
intentionally vulnerable application; the replay only builds and analyzes it.
It does not launch its server.

The upstream origins cover GitHub, GitLab, Codeberg, SourceHut, and
go.googlesource.com. A module cache entry represents its recorded origin, not an
additional independent source host.

### Source verification

- `git`: prepare a checkout under `--source-root/<source>`. The runner requires
  the exact HEAD, matching origin URL, and a clean worktree. It copies tracked
  files only. The manifest pins full commit IDs, never moving branch names.
- `module`: prepare the exact module version in `--module-cache`. The runner
  checks the cached `.info` version and origin commit when a revision is pinned,
  then recomputes Go's `h1` directory checksum from the actual extracted files.
  Reading a `.ziphash` marker alone does not verify the source being tested.
- `local`: resolve the fixture relative to the manifest and verify its pinned
  `h1` content checksum. Refresh that pin deliberately when changing a fixture.

The runner never fetches sources. Prepare Git checkouts at the manifest's full
commit IDs or populate the module cache with `go mod download -json
MODULE@VERSION`. Automatic module resolution and downloads are disabled for
replay commands through `GOPROXY=off` and `GOSUMDB=off`; these settings are not a
network sandbox. Missing dependencies remain visible as blocked cells. Module
inputs are verified against their pinned content sums before use.

Each command receives a fresh private copy of its source. The copy is checked
against the verified digest before execution. Tests cannot alter the corpus
checkout or influence a later command by leaving files in the previous copy.
Only one source copy remains on disk at a time; the copy is removed after its
case. Symlinks and newline-containing source filenames are rejected explicitly.

## Run

Use Python 3.11 or newer through `uv`, Git, a stock Go 1.27.4 toolchain, the built
fork, and a C compiler for cgo cases. Build the fork after changing its compiler,
runtime, or command packages. From this repository root:

```sh
uv run --no-project python misc/logos-dogfood/run.py \
  --go-oracle /opt/go1.27.4/bin/go \
  --go-candidate "$PWD/bin/go" \
  --logos "$PWD/bin/logos" \
  --gglint /opt/gglint/gglint \
  --source-root /opt/dogfood/git \
  --module-cache /opt/dogfood/modules \
  --cache-root /tmp/logos-dogfood-cache \
  --out /tmp/logos-dogfood-evidence
```

Omit `--gglint` to run the three toolchain stages and the built-in lint stage.
Use a new evidence directory for each invocation; an existing `report.json` is
never overwritten. Build caches can be reused through `--cache-root`, with
separate subdirectories for the oracle, fork, logos, and gglint.

Repeat `--only CASE` to select cases. Repeat `--stage oracle`, `--stage go`,
`--stage logos`, `--stage logos-lint`, or `--stage gglint` to select stages. For
example, this checks only the local semantic and ABI fixtures with both Go
drivers:

```sh
uv run --no-project python misc/logos-dogfood/run.py \
  --go-oracle /opt/go1.27.4/bin/go \
  --go-candidate "$PWD/bin/go" \
  --source-root /opt/dogfood/git \
  --module-cache /opt/dogfood/modules \
  --only fixture-semantics --only fixture-cgo \
  --stage oracle --stage go \
  --out /tmp/logos-fixture-evidence
```

`--operation build` overrides the per-case operation for a compile-only sweep.
The normal `test` operation uses **`go test -count=1 -short`**, with a test
deadline below the enclosing command deadline. This executes upstream short
tests; it is not a full upstream integration test suite. `--timeout` defaults to
180 seconds per command, and `--jobs` defaults to two. On POSIX systems, a
deadline kills the entire command process group and preserves partial logs.

Package patterns, cgo settings, and build tags are identical for all compiler
stages. The gglint stage uses stock Go on PATH and an explicit configuration with
the same build tags. Its report includes test-file diagnostics. `logos lint`
follows its own package scope and does not imply that the same test files were
analyzed.

## Evidence and interpretation

`report.json` is saved atomically after each cell. It starts with `complete:
false` and becomes complete only after every selected case has been attempted.
Complete means the replay finished, including any failed or blocked cells.

The report includes the runner and manifest hashes; selected cases and stages; source pins
and verified digests; driver and compiler/linker/assembler/cgo binary hashes;
tool versions and Go environment; compiler source digest and Git revision when
available; full commands and effective build environment overrides; exit codes;
deadlines and elapsed time; diagnostic counts; and log paths and hashes. Raw
stdout and stderr remain separate files beside the report.

| Status | Meaning |
|---|---|
| `pass` | Oracle succeeded, or candidate succeeded with a successful oracle in this invocation |
| `clean` | Lint returned a valid empty report and the corresponding success status |
| `findings` | Lint returned valid diagnostic records and the corresponding exit status |
| `upstream-failed` | The stock Go build or short-test baseline failed |
| `candidate-failed` | Candidate failed against a successful baseline, crashed, or returned an invalid driver exit status |
| `blocked` | A source, dependency, toolchain, native compiler, or disk prerequisite was unavailable |
| `load-failed` | A requested pattern was empty, or type errors prevented complete lint analysis |
| `timeout` | The command exceeded its deadline |
| `invalid-report` | Diagnostic JSON, fields, severity, count, or exit status violated the report contract |
| `nondeterministic` | Repeating lint changed the diagnostic multiset or exit status |
| `uncompared` | A compiler stage ran without a successful oracle in this invocation |

Every successful lint report is replayed on a fresh copy with the same cache.
Comparison sorts complete diagnostic records and preserves duplicate
multiplicity. Raw log hashes also expose ordering differences. gglint's promise
to emit each identical finding once is checked. A nonzero status alone is never
accepted as evidence that a lint completed: its parsed records must explain the
status. Package banners in logos stderr must be followed by diagnostic records;
other unstructured stderr is not silently discarded.

`findings` establishes report integrity and repeatability, not diagnostic
precision. Confirming a true positive or false positive requires source review
and a minimized paired regression. The runner does not set diagnostic budgets,
suppress difficult rules, or treat a large finding count as an upstream defect.

The command exits nonzero if any selected stage is blocked, failed, timed out,
invalid, nondeterministic, or uncompared. Selecting `--stage go` without
`--stage oracle` is useful for collecting evidence, but cannot produce a
differential pass. Such invocations remain visibly `uncompared`.

## Coverage boundaries

- Go-only code, the `purego` build tag, ebitengine's purego FFI implementation,
  native cgo, and modernc-generated Go are separate cases.
- The go-sqlite3 cgo-disabled case builds its four ordinary stub source files.
  Upstream's cgo-dependent test files do not compile in that configuration.
  This case does not claim SQLite functionality without cgo.
- modernc SQLite uses its own pinned `modernc.org/libc` dependency. The runner
  does not upgrade or replace that dependency to make tests pass.
- Architecture and OS variants need explicit separate invocations. The default
  replay is for the current host; it does not prove cross-platform execution.
- The external matrix contains Go source. The original `.lgo` language fixture
  under `fixtures/language` must be checked through native logos and the ejected
  Go result separately because stock Go cannot parse `.lgo` source.
- `fixtures/semantics` contains test code only. A clean `logos lint` result for
  that fixture does not establish that its test functions were analyzed. The
  deliberate safe/unsafe diagnostic pairs in `fixtures/lint` are outside this
  execution matrix and need a separate exact-diagnostic assertion.

## Test the replay contract

```sh
uv run --no-project python -m unittest discover \
  -s misc/logos-dogfood -p test_run.py
```

These tests cover false success on exit 1, partial-error masking, missing package
patterns, schema and severity mismatches, typecheck failures, duplicate findings,
repeat ordering and multiplicity, absent baselines, source hashes, and actual
subprocess timeout handling.

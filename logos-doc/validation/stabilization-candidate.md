# Integrated stabilization candidate

This record describes the reviewed source candidate in
[PR #58](https://github.com/gosuda/LoGOs/pull/58). It is an acceptance record,
not a completed release certificate. The shared Go and loGo/s implementation,
specification changes, frozen attacks, and remaining gates are published
together.

## Compiler-source evidence snapshot

These identities record the earlier integrated compiler source and its
published evidence. The subsequent CI merge is recorded separately below.

| Identity | Value |
|---|---|
| Published source commit | `4a77181e3f8a2973fb78e83caf4a934bd2089735` |
| Local source commit | `7b5e1598c3d81c1f628b35e41c4606ff56231298` |
| Complete Git tree | `7de05c6956f71e450e99b4aad1e41928ed6c7ec3` |
| `src` tree | `099367e1031585fa553ef7987b199288df36256a` |
| Included release branch | `bd318f1c558f3aa73ad434929fba7f237db9877f` |

Publication preserved each reviewed commit's complete tree and ordered
parents. At that snapshot the fetched public branch was byte-identical to
the local candidate. The release commit was an ancestor of that branch, and
`git merge-tree` returned the candidate's complete tree without conflicts. GitHub's PR API
nevertheless reported `mergeable: false` and `mergeable_state: dirty` at the
last check on 2026-11-16. The cause of that disagreement is unconfirmed;
no source conflict was found by the exact Git comparison.
A single close/reopen refresh left the PR open at the same head, but did not
clear that reported state or create an Actions run. No branch history was rewritten.

## CI release merge

The reconstructed merge starts at `e82c916ff4d8c2befc6f1891decb0f623401a94b`
and incorporates release `13a968f350564e739bf700df698a9a635dcdddcd`, including
the CI consolidation from `9a628f5b`. The compiler `src` tree remains
`099367e1031585fa553ef7987b199288df36256a`. No compiler repair or frozen
semantic assertion is changed by this merge.

M0-A-S18 retains the two-build workflow, independent suite results, native-shell
checkout, pinned installers, and the full Go regression with its Node, oracle,
C23 and ledger prerequisites. Actual actionlint 1.7.12/ShellCheck 0.11.0 and
the structural checks pass over eight YAML inputs, 67 run blocks and two
installer scripts. The isolated SC2155 mutation is caught, bootstrap exit 37
is preserved, SIMD selects the same 17 files, and all ten input hashes remain
unchanged. [CI merge evidence](ci-merge/README.md) records this scope. These
results do not establish installed compiler or hosted acceptance.

## Integrated scope

The candidate includes guarded SIMD optimization, indexed and generic
deferred calls, explicit generic type arguments, region chunk ownership,
CTFE assignment and aggregate-storage repairs, tuple names, spread defaults,
and the reviewed inliner change. Direct-C ejection preserves checked source
types and ownership; inactive packages and target-specific variants retain
their actual package and dependency identities. Both JSON profiles retain
their recorded method-dispatch and C-type repairs. Module-language boundaries,
context queries, LSP framing, metadata queries, and the WebAssembly test
environment have their recorded targeted repairs.

The last integrations add three reviewed scopes:

- Assigned `Close` results and builtin `panic` identity retain the original
  source, frozen regression tests, and recorded compiler acceptance from PR #66.
- Imported generic local types no longer force compiler-only lazy symbols
  through source-declaration fact traversal. The integrated source and both
  frozen tests match reviewed candidate `39c62004` exactly.
- The retained PR #63 declaration validator checks unused defaults in their
  lexical scope, assignment compatibility, recursive constant values and map
  keys, and repeated blank-field identity. Its production file and two frozen
  test files match retained PR #63 head `9a656c23` exactly. The new native
  eleven-package script exercises syntax-stored type information independently
  of default application.

SPEC and Table T preserve the matching contracts. M6-S3b's rejected binding
implementation remains rolled back, and its original attack patch remains
unchanged.

## Checks on the integrated tree

The following checks ran against the exact local source commit above:

| Check | Result |
|---|---|
| Compiler stability frozen-test manifest | All entries match |
| Inactive ejection and target-fact manifests | All entries match |
| Panic/Close production and frozen-test manifests | All entries match |
| Imported-local-type source, tests, and evidence | All 76 manifest entries match |
| Retained PR #63 source and two unit tests | Byte-identical to reviewed head |
| Native validator script | SHA-256 `4cd169dc2ab9b2fd0e5c751b75bdd4f68ad55ba72a4df63a446d214372b3aab0` |
| Blocked M6-S3b patch | SHA-256 `3d1bd84ac779294170db7d8e5e3d6403d005be31b94a634f5f9e4a16ae37d012` |
| Formatting of the six latest compiler/test files | No gofmt changes |
| Final integration whitespace check | Pass, except deliberately preserved raw author RED output |
| `go test -count=1 -short cmd/internal/goparity -run '^TestRedteamLedgerRows$'` | Pass, 0.024 seconds |

The ledger test used an existing installed driver to test the current ledger
and checker source. It does not prove that the combined compiler was rebuilt.
The imported-local-type author's raw RED log contains twelve whitespace-only
output lines; they are retained byte for byte and excluded only from the
whitespace check, never from a behavioral assertion.

With AUD-200, the embedded and standalone task ledgers contain 293 identical,
unique records. The CI merge adds no REDTEAM row.
The final attack ledger contains 342 unique rows:

| Status | Count | Remaining rows |
|---|---:|---|
| Tested | 328 | Individual proof boundaries remain as recorded |
| Satisfied upstream | 2 | As recorded |
| Planned | 5 | #386, #389, #391, #402, #418 |
| Blocked | 7 | #216, #217, #218, #226, #403, #404, #405 |

The five planned rows require final combined installed-toolchain acceptance
for generic deferred calls, explicit generic arguments, quoted JSON with C
types, native declaration validation, and imported local types respectively.
They are not promoted by integration-only checks.

## Behavioral evidence and its limits

The committed component reports retain semantic RED, unchanged assertions,
GREEN, caught mutations, and restored source. They distinguish their tested
source revisions from this final combination. Relevant reports include
[compiler stability](compiler-stability/README.md),
[inactive ejection](ejection-completeness/README.md),
[target facts](ejection-target-facts/README.md),
[panic and Close](dogfood-lint/README.md), and
[imported local types](imported-local-types/README.md).

The independent imported-local-type comparison contains eighteen command
records. Stock Go 1.27.4 and the existing repaired compiler pass all five
frozen programs and the exact upstream `digraph` build. The preceding S3
compiler fails the three captured-generic-local attacks and `digraph`, while
both nearby controls pass. This is concrete compatibility evidence for that
repair, not final merged-toolchain acceptance.

The [native declaration gate](../DEFAULT-VALIDATION-GATE.md) records one
accepted baseline control and ten semantic failures across eleven packages.
Its assertions are frozen. The retained validator still needs installed GREEN
on the combined compiler.

## Required release acceptance

A fresh `src/make.bash` must install both commands before the final native and
ejected script gates, affected suites, vet, and required race checks. The
remaining release requirements include both JSON profiles, native GC matrices,
`GO_TEST_TIMEOUT_SCALE=8 ./run.bash --no-rebuild`, both drivers over `std` and
`cmd` at O1/O2/O3, the full goparity release run and clean-tip stamp,
second-host reproducibility, native arm64 coverage, and actual execution of
the wasm and LSP prerequisites.

The [CI bootstrap repair](ci-bootstrap.md) and [CI consolidation](ci-merge/README.md)
preserve the repository's action
ownership policy and acceptance commands. It uses exact-revision shell
checkout and pinned, SHA-256-verified Go and Node archives. Its static checks
passed. At the earlier publication check, GitHub confirmed the branch but
returned no Actions runs for that source head. That observation is historical;
fresh hosted acceptance of the reconstructed merge remains pending.

## Local build preflight

### Historical resource-limited environment

The earlier fresh build was prepared with one worker, a 512 MiB soft Go heap limit,
compiler concurrency one in both bootstrap stages, and the normal final PGO
sequence. Recent resource-limited builds and changing host availability led
to a preflight reserve of 4 GiB workspace disk and 2.5 GiB available RAM.
These are conservative scheduling thresholds, not a measured compiler peak.

Two exact, hashed cleanup batches removed 3,116,597,248 allocated bytes of
reproducible task-owned workspace cache data. Four obsolete archive copies
and fourteen separately verified, exclusive cache-link/target pairs freed
1,328,570,368 allocated tmpfs bytes. Source, frozen tests, raw evidence,
installed reference tools, shared caches, and other workspaces were preserved.
The remaining 38,399 protected cache entries retained their recorded metadata.

Workspace availability continued to fall during that bounded cleanup. The
last snapshot at 2026-11-16 15:42:57 UTC was:

| Resource | Available | Preflight reserve |
|---|---:|---:|
| Workspace disk | 3,965,952,000 bytes, 3.69 GiB | 4 GiB |
| Available RAM | 3,213,959,168 bytes, 2.99 GiB | 2.5 GiB |

Disk remained short by 329,015,296 bytes, so that build was not started.
This 3.69 GiB snapshot describes the former environment, not the replacement
environment's current capacity. The compact
[first](build-preflight/cleanup-first.json),
[second](build-preflight/cleanup-second.json), and
[final](build-preflight/cleanup-final.json) cleanup records retain the measured
values and exact inventory hashes.

### Replacement environment

The fresh replacement environment and pinned Go 1.27.4 bootstrap, Node
26.13.1 runtime, and Go 1.27.2/1.27.3 oracle toolchains are ready. GCC 14.2.0
has passed the C23 compile/run prerequisite. Recovered CI input hashes match
the validated candidates, and fresh workflow checks pass as recorded above.
The combined `make.bash` and the five planned installed semantic gates remain
pending; neither restored tools nor workflow validation completes them.

Ordinary dist cleanup must remain intact. It removes `pkg/obj/go-build` at
exit, and `--no-clean` is deprecated and ineffective. After an eventual
successful build, use a newly empty dedicated cache for installed acceptance:
the unchanged `VERSION` string can mask changed compiler code in an older
external cache. An existing private bootstrap cache must not be assumed to
survive, and a prior source overlay does not substitute for the fresh build.

## Blocked default application

M6-S3b exhausted the correction budget in G1 item 7 of
[IMPLEMENTATION.md](../IMPLEMENTATION.md). Candidate three failed installed
native/export/ejection checks because default application wrote into nil
`Info.Types` under `StoreTypesInSyntax`; its capture logic also assumed the
map held syntax-stored constants. An empty-map allocation alone would hide
the syntax fallback and would not establish the invariant.

The failed implementation was removed, and its fourteen frozen attack files
remain in [M6-S3b.patch](../blocked/M6-S3b.patch). Constructor-local capture,
private producer defaults, generic declaration origins, blank-field
materialization, nested default composition, and exported anonymous-container
provenance remain unresolved. Rows #403 to #405 remain blocked, with their
mechanisms and gates explicit.

G1 requires an explicit exception before another implementation attempt.
The four older blocked rows (#216 to #218 and #226) are separate verification
exceptions permitted only with their documented release matrix. That
exception does not cover the unresolved default-application contract.

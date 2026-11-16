# Dogfood campaign: 16 November 2026

This directory preserves completed results and original failures at their exact
source revisions. An archive's status applies only to its recorded inputs and
executables. A completed capture is not necessarily an accepted run.

## Current revisions

| Component | Published revision | Compiler/source tree | Hosted result |
| --- | --- | --- | --- |
| gglint | `4456e61c4dbdf636fca2b5bd77787873e2d1c1e8` | `dfb46c72af5ce9695844c5d567cbb8aab1568183` | [All four gates passed](https://github.com/gosuda/gglint/actions/runs/43963396252) |
| Native LoGOs and lint | `f4242fed9cf0707a4103a0adbdb82e866bcabd69` | `src` = `ee5ae95824022485b74d4e3186a1540ef7124f76` | [Fresh validation in progress](https://github.com/gosuda/LoGOs/actions/runs/43967559240) |
| Pinned corpus | `3b2073eb98e488c411f52ba7cd723e8622c8cdfe` | `src` = `ee5ae95824022485b74d4e3186a1540ef7124f76` | [Fresh replay in progress](https://github.com/gosuda/LoGOs/actions/runs/43967842639) |

The gglint CI merge is `20294f56793727c9408f49bb854ed86f264e42af` and has the
exact reviewed tree shown above. The native and corpus workflows check out their
explicit PR heads. Later results will be recorded separately from the earlier
failures below.

## Retained archives

| Archive | Recorded result | Contents and limit |
| --- | --- | --- |
| [gglint-success-43963396252.tar.gz](gglint-success-43963396252.tar.gz) | All four jobs passed; regression report accepted | Four raw job logs, API metadata, both audits, all regression streams/receipts, two complete Chi source copies, and payload manifests. The generated CLI is omitted; its exact hash and the original ZIP identity are recorded. |
| [native-43951103189-evidence.tar.gz](native-43951103189-evidence.tar.gz) | Unit failed; scripts passed; upstream failed | All three raw logs and original artifacts, exact inventory/exits, available identity receipts, scheduling-source evidence, and the final audit. The earlier upstream job had no equivalent after-run installed-tool receipt. |
| [corpus-43950476822-evidence.tar.gz](corpus-43950476822-evidence.tar.gz) | Complete 196-cell replay; acceptance rejected `tools/logos`; independent runtime gates passed | Both original artifact ZIPs, the 764-stream checksum manifest, final audits, strict-policy captures, pinned-source review, provisional profile, and diagnostic-helper controls. |

Verify the exact archive bytes from this directory:

```sh
sha256sum --check --strict ARCHIVES.sha256
```

Each archive also carries its own payload manifest. The files were read back and
hashed after packaging. Repacking the recovered payloads with normalized order,
timestamps, ownership, modes, and gzip metadata produced byte-identical archives.
The original downloaded ZIP hashes remain recorded independently.

## Accepted gglint evidence

The full race job passed 39 packages; three packages without tests are listed
separately. The unchanged nine-worker vettool stress test passed in 51.046
seconds. Vet, golangci-lint with zero issues, and self-lint passed.

The regression proof requires the archived analyzer to fail exactly two named
tests with eight precise diagnostics. The candidate passes all seven normal and
seven race test identities, plus both selected LoGOs parity identities. All six
Chi comparisons pass: basic has three findings, standard has 52, and strict has
101. Strict removes only the frozen receive false positive at
`middleware/throttle.go:95:17`; the deferred send at line 97 remains.

Both audits verified 32 frozen inputs, 32 raw stream hashes, 642 source receipts,
six tool receipts, and both 111-file Chi copies. The parser follow-up recognizes
only the documented Go 1.27.4 `OutputType` values on output events. Its original
failed hosted run remains frozen in the gglint source PR.

## Preserved native failures

Run 43951103189 used head `9909feb26fe92b124f17c11fdf0022be7a643188` and source
tree `df267333b1d5c5727e2477215a6807cae52f8e18`. The complete `cmd/go` partition
passed in 1533.587 seconds. The complement reproduced the known SIMD import and
mutation-fact defects, then timed out in `cmd/internal/testdir` at 600.050 seconds.
Its four active fixtures were 1, 17, 37, and 76 seconds old, with 37 additional
parallel wait frames. The separate complete LoGOs fixture subset passed in
355.656 seconds.

The next candidate retains those failures and uses three complete, disjoint
partitions. Only the full testdir shard set receives a new 20-minute package
deadline; the 664 other entries keep scale 1, and `cmd/go` keeps its existing
scale 4. The 90-minute job bound remains. New before/after receipts cover HEAD,
the `src` and `test` trees, clean root-relative status, and six installed-tool
hashes. A focused frozen mutation proves why checking only `src` was insufficient.
These changes do not establish a pass until the fresh hosted run completes.

## Preserved corpus result and scope

The earlier corpus used head `7d5446cca8e5b65800732c27bee2354ce8ad1461` and
source tree `df267333b1d5c5727e2477215a6807cae52f8e18`. Its 28 cases produced 196
cells: 80 passes, 10 clean lint results, 98 findings results, one candidate
failure, and seven declared decorder failed-load/noncomparison cells. The sole
unexpected acceptance blocker was the strict tools capability policy.

All separate runtime gates passed on that recorded source. Per driver, the
assembly, purego-tag, plugin, cgo SQLite, modernc SQLite, and purego FFI profiles
had matching run/pass inventories of 159, 159, 223, 3, 15, and 8 respectively,
with no test-level skips or failures in those comparisons. Semantic/C ABI,
LoGo/ejected-Go, and exact native-lint runtime controls also passed. The plugin
profile explicitly aligns experiments and disables its LoGOs arena setting.

Both strict tools captures reached the linker's error limit. The newly reviewed
build profile remains provisional until its own hosted build passes. The fresh
replay preserves the original strict policy and adds a separate unlimited
diagnostic capture; capture completion does not accept the corpus.

This is a finite Linux/amd64 Go/LoGo and C-interoperability sample: 24 external
projects from GitHub, GitLab, Codeberg, SourceHut, and go.googlesource, plus two
synthetic inputs. Backend variants bring the total to 28 configurations. The
popular, reference-practice, and stress cohorts do not certify code quality.
Valid findings establish report integrity and repeatability, not that every
finding is precise or that a project is clean. The exact decorder failure is an
incomplete-analysis control, never a successful analysis.

See the [corpus acceptance contract](../../../misc/logos-dogfood/CI.md),
[tools policy review](../../../misc/logos-dogfood/TOOLS_POLICY.md),
[native source PR](https://github.com/gosuda/LoGOs/pull/71),
[corpus PR](https://github.com/gosuda/LoGOs/pull/73), and
[gglint PR](https://github.com/gosuda/gglint/pull/13).

# GC stability and optimization follow-up

Date: 2026-11-16

Base: PR #26, commit `968b8b436b08a0d8772d2159daa36e9c575d4b3d`.

**Status: draft; execution blocked.** The local execution environment is unavailable and Codexify's execution tunnel is disconnected. The new Go fixtures have not been compiled, formatted with gofmt, or executed. The CI workflow supplies an execution path if repository Actions runs it. A declared gate is not a passed gate. PR #26's earlier results apply to its recorded source, not to this follow-up.

## Ownership hypothesis

`logosRegionReleaseChunks` currently keeps `rec.chunk` published until its release loop finishes. Each `logosArenaRelease` makes a chunk reusable in a P pool before the loop clears that owner head. Implicit arena Drop and RegionPark detach their published owner before releasing chunks.

This is a source-supported hypothesis. Runtime code excludes asynchronous preemption, so a harmful observation requires a reachable synchronous safepoint or another proved interleaving. The generated call and inlining structure must be checked for both the original baseline and PR #26: the newly added accounting call may affect whether release is inlined.

`TestLogosRegionReleaseOwnership` runs ordinary production Close while a test-only observer stops the world. The observer compares each published explicit-region head with live P-pool entries and never follows the potentially stale head. The first snapshot is a positive visibility control. The bounded workload closes 128 chunks per round for 32 rounds, taking up to 16 snapshots per close. The worker is stopped and joined before any failure is reported or process-wide settings are restored.

This fixture checks published heads, not all interior owner-chain nodes. Pool lists are assumed to satisfy their existing acyclic-list invariant. A passing stress run cannot prove that every scheduling interval was exercised.

The dedicated no-inlining gate uses `-gcflags=runtime=-l` with `GODEBUG=asyncpreemptoff=1` to expose ordinary entry safepoints. This changes compiler configuration, not production release code. No ownership repair is included before a behavioral RED. If the failure is reproduced, detach `rec.chunk` before returning the saved chain to pools, keep the test fixed, and probe the repair by restoring the old publication order.

## Heap graph contract

`TestLogosGCHeapGraphLiveness` constructs roots in three different manual chunks. Each root reaches a small pointerful heap object and a pointerful heap object larger than 1 KiB; each intermediate owns a separate non-tiny leaf. A self pointer is present alongside the heap edges.

The builder returns before collection and retains only manual addresses and weak references in the caller. The fixture rejects heap fallback for manual roots and rejects manual placement for intermediates and leaves. Both intermediates and descendants must survive collections while their region is live, then disappear after release. Strong values obtained from weak pointers are confined to a separate non-inlined checker. `KeepAlive` precedes release. Both implicit arenas and explicit regions are covered.

## Scan boundary contract

`TestLogosGCScanLayoutBounds` computes expected scan extent from declared pointer offsets without consulting runtime GC masks. It scans every byte limit across repeated elements, scalar gaps, pointer-size boundaries, bitmap-word boundaries, and a full empty bitmap word.

A pointer slot contributes its full width when its start is below the requested byte limit. The backing allocation is rounded to a pointer word and initialized, so a partial final word remains readable. Raw interface, string, and slice headers are never interpreted as language values.

All pointer values are nil or target live manual allocations. This makes the existing scanner's nil-work-queue test contract valid. The fixture checks final scan extent; it is not a complete visited-slot oracle. A future bitmap traversal rewrite must add such an oracle and on-demand-mask coverage before implementation.

`BenchmarkLogosGCScanSmall` uses the same declared layouts of at most three pointer words. Each timed operation scans one already allocated object. Allocation, initialization, and collection are outside the timer. Self, Nil, and OtherArena remain separate controls. OtherArena does not measure heap marking.

## Configured execution gates

| Gate | Configuration |
| --- | --- |
| Toolchain build | Default experiment build from the changed source |
| Upstream regression | Native linux/amd64, default experiments, `./run.bash --no-rebuild` |
| Runtime regression | Native linux/amd64 and linux/arm64; `logosarena` and `logosarena,nogreenteagc`; runtime, metrics, pprof, and trace |
| Race | Region, arena, and GC tests under the race detector |
| Repetition | New ownership, heap-graph, and layout tests, count 10 at CPU counts 1, 2, and 4 |
| Synchronous safepoints | Ownership test, count 10, runtime inlining disabled, asynchronous preemption disabled |
| Mark verification | Existing external-pointer test and new heap graph, count 10, `gccheckmark=1` and `GOGC=1` |
| Hygiene | gofmt and vet |

These are pending requirements. This document records no passing result for them.

## Optimization queue

1. **Remove redundant initial mask loading.** The typed iterator's `fastForward(0, limit)` replaces the mask before any consumer can use the initializer. Omit only that initializer and retain clipping and lazy `getGCMask` resolution. Inspect emitted code because the compiler may already eliminate part of the work. Measure small allocations as well as the existing block workload.
2. **Defer scan-extent arithmetic until traversal completes.** Retain the last visited pointer address and convert it to an extent once. Preserve zero-slot behavior and partial final pointer words. Measure independently because a per-allocation exit branch can offset the saved per-slot arithmetic.
3. **Cache type bitmap traversal state.** This larger candidate needs a complete pointer-visitation oracle, repeated irregular layouts, and on-demand masks. It is not implemented in this follow-up. Direct access to `typ.GCData` cannot replace the lazy mask protocol.
4. **Share the heap-arena lookup across Green Tea fallback.** This needs actual heap-graph measurements and preservation of span bounds, mark state, checkmark behavior, linknamed interfaces, and nosplit contracts. An arena derived from an interior pointer cannot automatically supply page-mark ownership for a different span-base address. It is not implemented in this follow-up.

No persistent marking cache, extra duplicate-pointer branch, relaxed zeroing, or allocator-layout change is proposed.

## Promotion requirements

Keep each production candidate separate and retain the fixed behavioral fixtures. Run the applicable G2 gates and a mutation probe for any repaired correctness defect. A semantics-preserving performance candidate has a parity baseline, not an invented behavioral RED.

For G6, freeze source and workload; use identical fixed work long enough for each timed invocation to last at least one second, compiled binaries, `GOMAXPROCS=1`, hyperfine with three warmups and at least ten measurements, separate CPU profiles, and ten benchmem samples. Reject noisy samples and non-wins under the repository's existing thresholds. Validate the integrated candidate against its immediate parent.

PR #26 disclosed roughly 4-5% cost in its Nil and other-manual-allocation controls. Those controls remain part of acceptance; a focused Self result is not a whole-GC or application result. The first two queue items are reviewable draft candidates until measurement supports promotion.

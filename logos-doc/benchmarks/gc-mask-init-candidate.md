# Candidate: initialize the region scan bitmap once

Date: 2026-11-16

Status: draft; correctness and performance gates pending.

Related: #21, #26, and #35. This candidate targets the stability-test baseline and is independent of the deferred-extent candidate.

## Problem and change

`logosArenaScanObject` initializes `typePointers.mask` with `readUintptr(getGCMask(typ))` and immediately calls `fastForward(0, limit)`. For the typed path, `fastForward` replaces that mask before returning it to the scanner.

Remove only the initial `mask` field. Keep `fastForward`, the type descriptor, object addresses, byte limit, pointer dispatch, and extent accounting unchanged. This is a single-function candidate within the G6 exception; there is no new iterator or persistent cache.

## Contract and proof obligations

The existing scanner preconditions remain: a live typed manual allocation; a valid object range; initialized readable padding for any final partial pointer word; valid collector state for targets that require marking.

For a nonempty typed range, `fastForward` obtains the mask through `getGCMask` and applies the same clipping. Its old incoming mask is not read on this path. For an empty range it returns an empty iterator before either mask can be consumed. The proposed initializer therefore supplies the same iterator state to the scan loop.

The lazy mask-resolution protocol remains in `getGCMask`; direct access to `typ.GCData` is not substituted. A zero-length call no longer eagerly resolves a mask that it never scans. No contract requires that incidental initialization.

The postcondition remains the same pointer visits, marking effects, and returned scan extent. Nil slots, self pointers, scalar tails, bitmap-word boundaries, and partial pointer words retain their existing meaning.

## Required evidence

Run #35's native, race, Green Tea on/off, heap-graph, boundary, and upstream gates against this exact head. Keep the frozen tests unchanged. This semantics-preserving candidate starts from a parity baseline; it does not claim a behavioral RED.

Inspect emitted code before accepting a speed claim. The compiler may already eliminate some of the repeated source work. Use the small-object scanner benchmark to expose initialization cost and retain the 512-slot Self, Nil, and External controls. External is another manual allocation and does not measure heap marking.

Compare compiled base/head binaries with identical fixed work, `GOMAXPROCS=1`, separate CPU profiles, hyperfine warmup 3/min-runs 10, and ten benchmem samples. Require the repository's G6 admission, noise, and integrated speed-ratio gates. Record controls and reject a non-win. Do not infer application or full-collection speed from isolated scanner timing.

No new runtime result or speedup is asserted by this document.

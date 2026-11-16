# Candidate: compute region scan extent after traversal

Date: 2026-11-16

Status: draft; correctness and performance gates pending.

Related: #21, #26, and #35. This candidate targets the stability-test baseline and is independent of the bitmap-initialization candidate.

## Problem and change

The scanner currently calculates `addr - obj + PtrSize` for every typed pointer slot, although only the last value is returned. Retain the last visited address and compute the extent after traversal. Return zero if no slot was visited.

Assign `last = addr` before loading the slot and before every nil, self, stack, or heap dispatch branch. Keep the bitmap iterator, scan limit, target classification, marking calls, and work propagation unchanged.

This is a single-function candidate within the G6 exception. It adds no cache, object metadata, allocation, or new marking behavior.

## Contract and loop invariant

The existing scanner preconditions remain: a live typed manual allocation; a valid object range; initialized readable padding for a partial final pointer word; valid collector state for targets that require marking.

After zero visited slots, the old `scanned` and new `last` are both zero. After any positive number of visited slots, let A be the final visited address. The old value is `uintptr(A - obj + PtrSize)`; the new value is `last == A`. No intervening operation reads that accounting value. Computing the same expression at return preserves the result.

The iterator already uses address zero as its exhaustion sentinel, so testing `last == 0` adds no restriction to valid inputs. Keep subtraction and addition in `uintptr`, then convert the result to `int64`.

The postcondition includes nil and internally directed slots, excludes scalar tails, and counts a final pointer word in full when its start is below the byte limit. Do not clamp the result to the requested byte count. A leading scalar prefix or zero-size range with no visited pointer still returns zero.

## Required evidence

Run #35's independent byte-limit oracle, native/race/Green Tea on/off suites, real heap-graph checks, and upstream gate. Preserve the frozen tests. This semantics-preserving candidate has a parity baseline, not a claimed behavioral RED.

Inspect emitted code: the compiler may already simplify arithmetic, share offsets with heap dispatch, or change register allocation. The extra exit test can cost time on zero- and one-slot scans.

Measure independently against this candidate's immediate parent, using both the 512-slot Self/Nil/External workload and small typed allocations. External and OtherArena target manual allocations; neither measures heap-object marking. Use identical fixed work, `GOMAXPROCS=1`, separate profiles, hyperfine warmup 3/min-runs 10, and ten benchmem samples. Apply the repository's G6 admission, noise, and integrated speed-ratio requirements and record every control.

PR #26's roughly 4-5% Nil/other-arena control costs make those results material. Reject a non-win or an unacceptable distribution-dependent regression; a focused scanner gain cannot establish whole-GC or application speed.

No new runtime result or speedup is asserted by this document.

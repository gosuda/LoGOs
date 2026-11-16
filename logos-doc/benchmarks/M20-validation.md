# M20 validation follow-up

This follow-up preserves the four default-off cgo optimization candidates and strengthens their evidence. It changes no runtime or compiler behavior.

## Rebuilt test configuration

Commit `debf92a9ae8ab44c260cc1318fbfd1efabc3826d` made the shared `selfTestBin` call `cgoSpanToolFlags`. That helper deliberately disables `logosfastcgo`. A fast-enabled parent therefore disassembles a fast-disabled child, contradicting the existing trampoline and structure tests.

The test-first change adds `TestSelfBuildConfiguration`, which executes the rebuilt binary and compares its compiled experiment and instrumentation constants with the parent. The CI compatibility gate also runs the existing structural tests in all four independent cgo flag combinations. The both-enabled case repeats the configuration and inline checks with race, cgocheck2, ASan, and MSan. No test may silently skip.

The first gate is expected to fail on the inherited helper. A recorded CI failure must precede the helper repair. This record does not claim that expected failure has already executed.

## Span attack

The combined tree includes the external test-only commit `1c632c287c1eed95c4b0a08c6be7ee9bb509880c`. Its PrefixInterior case places the unpinned pointer before the passed address. Scanning only from that address would miss the pointer; scanning the complete containing object must reject it.

## Invalid benchmark evidence

[Run 43832392680, job 133190928032](https://github.com/gosuda/LoGOs/actions/runs/43832392680/job/133190928032) failed with `hyperfine must contain exactly one command`. The command logger wrote metadata to the same `hyperfine.json` path used for raw timing samples, overwriting all three exports for BenchmarkCgoOptNocallback. The later BenchmarkCgoOptLateContract row never ran.

This campaign is invalidated by an evidence-file collision. Corroborating Go benchmarks and profiles remain diagnostic evidence, but cannot replace the missing primary samples. No row is accepted and no prior baseline will be reused.

The repair must give command metadata a distinct path and start a new campaign for every declared target and reference row. Calibration, warmups, sample counts, phase order, noise limits, the 1.05 threshold, admission, and the same-contract floor requirement remain unchanged. Focused and full regression results are separate evidence and must not be discarded because the benchmark recorder failed.

## Status

Test-only follow-up is published first. Compatibility RED, the harness repairs, combined focused/full gates, and the repaired complete campaign remain pending. No speedup, G6 acceptance, default enablement, or complete stability is claimed.

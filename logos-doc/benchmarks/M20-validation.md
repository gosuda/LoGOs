# M20 validation follow-up

This follow-up keeps the four default-off cgo optimization candidates and repairs their validation. It changes no production compiler or runtime behavior. Issue #48 and PR #49 track this work.

## Rebuilt test configuration

The inherited `selfTestBin` called `cgoSpanToolFlags`, which deliberately disables fast-cgo for the span workload. A fast-enabled parent therefore inspected a fast-disabled child.

Test-only commit `aad4fbc7657b6ded06f72cd863e4b7f43401d091`, tree `e88ea33e85d14afd040135c1aad8c54270eccf1e`, adds an actual child-execution check and runs the existing structural tests in all four independent cgo flag combinations. A clean local toolchain build succeeded with stock Go 1.27.4 before the observed RED at 09:43:04 UTC on 2026-11-16.

The new test required `fast=true opt=true` and observed `fast=false opt=true`. OneTrampoline reported zero definitions instead of one; Structure and NoClockInFastBody found no fast trampoline disassembly. The inline control passed. The complete raw failure is in [M20-validation-red.jsonl](M20-validation-red.jsonl); its SHA-256 is `ecaa6ccd2300310d6b61d1e68ca70b84483d0f772dbc9c77ac2749a6a6fcb2bb`. HEAD, tree, tracked index, and frozen source hashes were unchanged throughout RED.

The repair rebuilds from the running binary's embedded GOEXPERIMENT and instrumentation settings. Explicit false race/ASan/MSan arguments prevent inherited GOFLAGS from adding another mode; recorded settings then restore the original mode. GOENV is disabled for the child build so an empty recorded experiment cannot pick up a different settings file. C compiler variables remain explicit in CI.

### Local repair results

| Configuration | Required top-level checks | Result |
|---|---:|---|
| Both experiments on | 5 | Passed |
| Cgo optimization only | 5 | Passed |
| Fast-cgo only | 5 | Passed |
| Both experiments off | 5 | Passed |
| Both on, race | 5 | Passed |
| Both on, cgocheck2 | 2 | Passed |
| Both on, ASan | 2 | Passed |

All tests ran with `-count=1`. The existing fast-body disassembly subtest skips when fast-cgo is off; its parent still checks calls and results. Every required top-level check passed.

A helper-only mutation changed the GOEXPERIMENT switch key to an unmatched key. The new test then observed `fast=false opt=false`, as predicted. Restoration ran in finally; the original helper hash was restored and the test passed again. Frozen attack tests and the CI compatibility script remained unchanged. The repair's Go source is the same source used by these checks.

Local clang and gccgo are absent. MSan and gccgo-dependent checks remain required in CI, which installs those compilers. The local compatibility result is not a completed full-regression claim.

## Stronger span attack

The combined tree includes test-only commit `1c632c287c1eed95c4b0a08c6be7ee9bb509880c`. Its PrefixInterior case places an unpinned pointer before the passed address. Starting a scan at the argument would miss that pointer; scanning the complete containing object must reject it.

## Benchmark recorder and format

The prior recorder exported to `hyperfine.json` and used `hyperfine` as the command-log prefix. The command logger consequently replaced each export with process metadata.

Both affected campaigns are invalidated:

| Run / job | Frozen work for the first row | Secondary Go medians, baseline / candidate / integration |
|---|---:|---|
| [43831729270 / 133188676400](https://github.com/gosuda/LoGOs/actions/runs/43831729270/job/133188676400) | 88,888,681 | 27.365 / 27.060 / 27.090 ns/op |
| [43832392680 / 133190928032](https://github.com/gosuda/LoGOs/actions/runs/43832392680/job/133190928032) | 89,398,006 | 27.095 / 26.795 / 26.815 ns/op |

These medians describe BenchmarkCgoOptNocallback only. The package-contract campaign never reached BenchmarkCgoOptLateContract. No primary timing sample survived, so no row receives a latency verdict. All secondary samples and artifact identities are retained in [M20-validation-evidence.jsonl](M20-validation-evidence.jsonl).

The original parent and package-contract full-dist jobs were later cancelled when their branches advanced. Their logs contained no test failure before cancellation, but none completed the full gate.

The follow-up preserves external repair `eb7aa44a0f804ffb993adc73a6ec7baa3d7966f4` unchanged. It separates `hyperfine-export.json` from command metadata and reads Hyperfine 2's per-measurement JSON. The exact schema is confirmed by the [official v2.0.0 exporter](https://github.com/sharkdp/hyperfine/blob/b33755fe1fca8bcc1f182c773ca3ab040bfe517d/src/export/json.rs) and its [integration snapshot](https://github.com/sharkdp/hyperfine/blob/b33755fe1fca8bcc1f182c773ca3ab040bfe517d/tests/snapshots/integration_tests__json_export_basic.snap).

Local verification used the actual Hyperfine 2.0.0 release archive with SHA-256 `38e59087bd65520930a13cf513d8a493b7043741e8ad62349a8a9929eb61bc5c`. A small serialization workload produced ten real samples. The new parser read them while command metadata remained separate; wrong units, a wrong primary metric, and the old schema were rejected. The 14 checker tests passed. This exercise verifies the recorder and format, not cgo performance.

Warmups, sample counts, frozen work, phase order, the one-second useful-work rule, the 20% noise limit, and both 1.05 ratios are unchanged. The statistics and acceptance functions are byte-identical to their earlier versions.

## Remaining admission

A fresh complete campaign must measure every target and reference row from a new baseline. Prior samples cannot be selected or reused to manufacture a pass. Full regression, required instrumented CI, profile admission, and a justified same-contract floor remain independent gates. No accepted speedup, G6 promotion, default enablement, or complete stability is claimed.

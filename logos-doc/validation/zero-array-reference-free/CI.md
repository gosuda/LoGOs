# Hosted zero-array proof

The frozen tests and evidence in `FROZEN.sha256` remain unchanged. This follow-up
adds the hosted gate; it does not change the predicate, the real SIMD fixture, or
any expected mutation mask.

`zero_array_ci.py` uses the shared `native_frozen_gate.py` introduced by commit
`e0c50c90984fce048a99d06efd200ec4db2aa5b7`. The shared runner retains the existing
tagged-method event parser and process capture. This wrapper only defines the
frozen inputs, exact test inventory, and four phases:

| Phase | Required result |
| --- | --- |
| Old type predicate | Exit 1; exactly the root, `function` parent, and `function/zero` leaf fail with the original predicate's false classification. |
| Old mutation predicate | Exit 1; exactly the root and `ReturnZero` leaf fail with `ParamMutated="1"` and an unchanged false receiver flag. |
| Candidate type classification | All 67 named tests pass, covering 57 leaves. |
| Candidate mutation facts | All 15 named tests pass, covering 14 leaves. |

Each negative phase overlays only `types2/const_logos.go` with its frozen original.
The candidate phases use the installed compiler and current source. The runner
captures every phase even if an earlier phase fails acceptance. It requires exact
test events, empty stderr, the expected process status, and unchanged input,
source, and tool identities before accepting the suite. The inherited parser
rejects skipped, missing, duplicate, unexpected, or crashing test results.

The unit workflow runs this gate as an independent step after a successful
build and smoke check. The 45-minute outer limit covers four phase limits of
660 seconds each. Each Go test process has a 10-minute test budget. Evidence is
retained with the other native regression artifacts.

## Local checks

Only Python and static checks ran locally. The 26 Python boundary controls pass,
including exact positive and negative transcripts, missing leaves, wrong exit
statuses, diagnostics attached to the wrong test, skipped tests, crashes, and
changed frozen inputs. Strict Pyright, Ruff check and format, actionlint with
ShellCheck, `git diff --check`, and all 21 frozen file hashes pass.

The shared runner was copied byte-for-byte into this checkout only as an
untracked dependency for those Python checks. Its implementation belongs to the
separate shared-runner commit and is not duplicated in this follow-up.

No Go build, test, compiler, analyzer, or vet command ran locally for this gate.
The new focused RED/GREEN runs, installed real SIMD regression replay, and full
native gates remain pending hosted execution. The previously observed hosted
failure is preserved separately in the frozen evidence.

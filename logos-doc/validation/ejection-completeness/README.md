# Complete ejection inventories

Issue: [#56](https://github.com/gosuda/LoGOs/issues/56).
Slice: M24-A-S1, REDTEAM #392, AUD-196.
Reviewed source base: `153705a0134c32a6a27344f63bdf3cdddd50216d`.
The target-context prerequisite is M24-A-S2 with its GO386 review correction.

## Frozen regressions and fixture correction

The files in `frozen-tests.sha256` cover unloaded OS/architecture/custom-tag
packages, an entirely host-inactive module, inactive-only tests, dependency
module discovery, ownership exclusions, real declaration provenance, and
unique formatting of repeated variant outputs.

The original baseline omitted the Windows-only package in the real command
script. The unit census returned no unloaded packages. The owned-provenance
test rejected a declaration selected by a real alternate load inventory.

The first production attempt passed the complete inactive-package script,
including its pristine-Go dependency, cross-target, test-only, and empty-host
checks. It exposed two test setup errors: the existing
`ownedTestMainPackage` convenience helper appends `/sub`, but two new fixtures
wrote sources at the supplied root. The production delta was saved and
reverted. Only those fixture directory fields were corrected; all behavioral
assertions stayed unchanged. A separate test commit records that correction.

The same attempt also exposed repeated gofmt paths from several actual load
inventories. Four unchanged command scripts failed when parallel formatter
workers raced on the same output. A deterministic formatter-argument attack
was added while production remained reverted.

On the corrected pre-repair tree, all five component tests fail for their
predicted semantic reasons, and both real command scripts omit the expected
inactive consumer output. The final hashes were frozen at that point.
The initial owned-script spelling used an ordinary imported function where
the language requires a `const func`; that was corrected before any issue #56
production edit and before the semantic missing-output RED was accepted.

## Validation boundary

Local complete logs and the saved first production attempt remain in
`tooling-evidence/ejection` in the stabilization workspace. The repair commit
records the unchanged-test GREEN, race/vet/format, and caught/restored mutation
results. Full toolchain rebuilding and the complete upstream regression suite
remain integration gates owned by the parent stabilization branch.

# CI entry points

GitHub owns the events and every job uses a Depot runner. The canonical
CI workflow builds the AMD64 toolchain once and shares it across its suites.
The specialist workflows retain native ARM coverage.

Pull requests select suites from their complete diff against the base.
Pushes select suites from the push event's complete before/after range.
An unavailable push endpoint selects all applicable push suites. Only newer
pull-request runs cancel older runs; push and manual runs keep separate groups.

## Manual validation

Use **Actions → CI → Run workflow**:

- **gc** runs both native architectures, both GC experiments, and the core/upstream
  regression. The ARM workflow is called by this entry point and has no separate
  manual dispatch.
- **ssa** runs focused SIMD/SSA checks, the complete SIMD standard-library sweeps
  at AMD64 v1 and v3, and the upstream/LoGOs regression.
- **core**, **compiler**, **cgo**, **gates**, or **dogfood** select those suites;
  **all** selects the complete manual campaign.

The complete SIMD standard-library sweeps are intentionally manual. Matching
pull requests still run the focused SSA/SIMD checks and the ordinary Go
regression. This cadence reduction is part of the CI cost reduction.

Autovec measurements remain PR-only. The canonical job uploads AMD64 evidence;
the specialist benchmark workflow uploads ARM evidence.

## Release evidence

Release acceptance uses independent Depot Ubuntu 24.04 two-CPU and four-CPU
runner configurations. Both carry the required compiler tools. The gate requires
distinct VM fingerprints and equal source, recipe, and output digests.
Its fresh build caches disable an inherited GOCACHEPROG.

Workflow and job-count reductions are configuration counts. Historical timings
from the deleted LoGOs/native-Depot pipelines do not measure this layout.
Runtime savings and complete acceptance require results from the current head.

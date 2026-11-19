---
name: logos-concurrency
description: Watch a running LoGOs program or test with logos conc run or logos conc test. Use when the user says logos conc, lock order inversion, stalled group child, goroutine leak, long lock hold, unbounded group, or shake seed. Not for data races; run logos test -race for those.
license: BSD-3-Clause
compatibility: Needs the logos command from github.com/gosuda/LoGOs on PATH.
metadata:
  version: "0.22.0"
  milestone: "M8"
---

# LoGOs concurrency

LoGOs provides structured concurrency through goroutine groups and panic-safe synchronization primitives. Goroutines in `.lgo` files must run inside a `go group` block. Shared state is coordinated through `logos/sync.Locked[T]` and `logos/sync.Hot[T]`. `logos conc` runs the program or its tests with a monitor and reports concurrency faults that `logos lint` and `logos test -race` miss.

## Procedure

1. Structure goroutines with `go group`: define child tasks inside `go group (ctx, limit = N) { ... }` blocks.
2. Propagate errors: use `?` on fallible calls inside child tasks or invoke an error-last child call to yield errors to the group (direct `return` inside a child is rejected with `correctness::return_in_group_child`). Ensure the function enclosing the `go group` returns an `error`.
3. Guard shared state: wrap mutable state with `logos/sync.Locked[T]` for mutex protection or `logos/sync.Hot[T]` for atomic read-mostly pointers.
4. Run the linter and the race detector first: they catch static violations and exercised data races.
5. Run the program under the monitor when the code is lint-clean and race-clean but still suspects a liveness or ordering fault: use `logos conc run` for a program and `logos conc test` for tests.
6. Read each finding on standard error while the child still runs: repair the named position, then rerun without `conc` to confirm the program still behaves.
7. Retry a shake failure with the printed seed: pass the same seed to shake the run again. The same seed can yield differently, because yields also depend on goroutine identity and scheduling, so treat a rerun as a retry, not an exact replay.

## Group syntax

Each row contrasts the LoGOs form with the plain Go pattern it replaces.

| LoGOs | Plain Go |
|---|---|
| `go group (ctx, limit = N) { go { work()? } }` | `g, gctx := rt.NewGroup(ctx, N)` followed by `g.Go(func(ctx context.Context) error { return work() })` and `g.Wait()` |
| `go group (ctx) { go { work()? } }` | `g, gctx := rt.NewGroup(ctx, 0)` followed by `g.Go(func(ctx context.Context) error { return work() })` and `g.Wait()` |

## Group semantics

The compiler lowers `go group` blocks to `logos/rt.Group`. The following invariants govern group execution:

- Cancellation: the group context cancels upon the first child error or panic.
- Joining: the group statement joins all child goroutines before proceeding. The enclosing function propagates any group error like `?`.
- Concurrency limit: when `limit = N` is specified, an internal semaphore bounds concurrent execution to at most N child goroutines.
- Child block contract: using `?` inside a child returns the error directly to the group.
- Disallowed return: a bare `return` inside a child block is rejected with `correctness::return_in_group_child`.
- Channel operations: direct channel send, receive, and range operations inside a child block automatically select on context cancellation.
- Netpoll integration: children blocked on netpoll-backed network I/O wake within 100 ms of group cancellation with a timeout error.
- Panic handling: a panicking child goroutine terminates the group and wraps the panic value and stack into `*logos/rt.PanicError`.

## Shared state

The `logos/sync` package provides concurrency containers:

- `Locked[T]`: pairs a value of type `T` with a mutex. Call `l.WithLock(func(v *T) { ... })` to access the value. `WithLock` acquires the lock, executes the callback, and guarantees unlock on return or panic. Callbacks must not return or leak the pointer `*T`.
- `Hot[T]`: holds a read-mostly pointer managed with atomic pointer swaps. `Load()` returns a read-only pointer `const *T`. `Store(*T)` publishes a new pointer. `Update(func(old const *T) *T)` applies updates inside a compare-and-swap loop without maintaining a retire list.

## Diagnostics

| Rule | Severity | Fix |
|---|---|---|
| `correctness::return_in_group_child` | forbid | Remove return statement; use ? or expression outcome to yield an error to the group. |
| `correctness::child_outside_group` | forbid | Move the child go block inside an enclosing go group statement. |
| `correctness::group_needs_error_result` | forbid | Add an error return type to the function enclosing the go group statement. |
| `concurrency::unsupervised_goroutine` | forbid | Replace bare go f() with a managed child block inside go group. |
| `concurrency::reentrant_withlock_deadlock` | forbid | Do not invoke WithLock on the same Locked receiver inside a WithLock callback. |
| `concurrency::concurrent_multiword_race` | forbid | Protect multi-word structures accessed across goroutines with Locked or Hot. |
| `conc::lock_order_inversion` | monitor | Take the two Locked values in one order everywhere, or keep both in one Locked value. |
| `conc::group_child_stalled` | monitor | Make the child return when its context ends. |
| `conc::goroutine_leak` | monitor | Give the wait an end: close the channel, release the lock, or wait on a context. |
| `conc::lock_held_long` | monitor | Move the slow call out of the WithLock callback, or keep the value in Hot. |
| `conc::group_unbounded` | monitor | Give the group a limit: `go group (ctx, limit = N)`. |

## Use conc run and conc test

Run `logos conc run` to watch a program. Run `logos conc test` to watch tests. Pick the verb by what executes the code. Keep every other flag unchanged: pass the same build and test flags used without `conc`. Place `conc` flags before the package for `run`. Place `conc` flags before `-args` or `--` for `test`. Treat everything after that boundary as program arguments. Expect each finding on standard error the moment the monitor sees it. Expect no output when the run is clean.

```sh
logos conc run ./cmd/app
logos conc test -count=1 ./...
logos conc run -hold 50ms ./slow serve --port 8080
logos conc test -stall=200ms -count=1 ./ignores
```

## Read each finding kind

Each finding names a source position written by the user, a rule id, and a fix hint. Never edit a file under GOROOT to answer one.

`conc::lock_order_inversion` means two goroutines took the same two `Locked` values in opposite orders. Treat it as a deadlock that this run escaped by timing. Run the two orders at once to deadlock. Repair by taking the two values in one order everywhere.

```lgo
checking.WithLock(a => {
  savings.WithLock(b => { move(a, b)? })
})
savings.WithLock(a => {
  checking.WithLock(b => { move(a, b)? })
})
```

`conc::group_child_stalled` means a child still runs after its group was cancelled. Treat it as a join the group cannot reach. Repair by receiving from the `Done` channel of the context in a `select`, or by giving the context to the call that blocks.

```lgo
go group (ctx) {
  go { slowFetch()? }
  go { fastFail()? }
}
```

`conc::goroutine_leak` means a goroutine waits on something no running goroutine reaches. Treat it as a wait that never ends. Repair by letting the owner close the channel, release the lock, or wait on a context in the same `select`. The leak source lives in an imported Go package: a bare child outside `go group` is rejected in `.lgo`, so the `.lgo` caller only starts it and sleeps.

```go
package blocks

func Subscribe() {
	updates := make(chan int)
	go func() {
		<-updates
	}()
}
```

`conc::lock_held_long` means one goroutine held a `Locked` value past the `-hold` duration while another goroutine waited. Treat it as a liveness fault, not a race. Repair by copying what the slow call needs, leaving the callback, then calling; or keep the value in `Hot` when readers must not wait.

```lgo
ledger.WithLock(a => {
  time.Sleep(400 * time.Millisecond)
  a.balance += 10
})
```

`conc::group_unbounded` means a group without a limit runs 10000 children at one time. Treat it as resource growth that follows the work given to the group. Repair by giving the group a limit.

```lgo
go group (ctx) {
  for range 10000 {
    go { work()? }
  }
}
```

A shake schedule fault has no rule id. Treat a test that fails only under `-shake` as an order dependence. Rerun with the printed seed to shake the run again; the same seed can yield differently, so it is a retry, not an exact replay.

## Set flags and replay seeds

Pass `-stall` with a Go duration to set how long a cancelled-group child may run before it is reported. Omit it to keep the 5s default. Pass `-hold` with a Go duration to set how long a contested hold may last. Omit it to keep the 100ms default. Use either the space form or the `=` form for both.

```sh
logos conc run -stall 200ms ./ignores
logos conc run -stall=200ms ./ignores
logos conc run -hold 50ms ./slow
logos conc run -hold=50ms ./slow
```

Pass `-shake` with a decimal seed to yield at group and lock hook points from that seed. Rerun the printed seed to shake the run again. Pass bare `-shake` to take the seed from the clock; the command prints the seed it used. Omit `-shake` to turn shaking off. A seed of 0 is a usage error, because seed 0 turns shaking off. Treat a non-numeric seed as a usage error.

```sh
logos conc test -shake=1 -count=1 ./ord
logos conc test -shake 1 -count=1 ./ord
logos conc test -shake -count=1 ./ord
```

Pass `--json-diagnostics` to print each finding as one JSON object per line in the `logos lint --json-diagnostics` schema. Expect the `note` field to hold the position lines and the fix line. Expect tab-separated text output otherwise. Apply every repair by hand: this revision offers no `--fix` flag.

```sh
logos conc run --json-diagnostics ./opposite
```

## Read exit codes

Expect the child status when the child exits nonzero. Expect 1 when the child exits zero but a finding was printed. Expect 0 when the child exits zero and no finding was printed. Treat a damaged finding line as a finding. Expect a usage error and no child run for a bad flag value. Run only on unix: the command transports findings over a named pipe.

## Know what conc does not find

Do not use `conc` for data races; use `logos test -race`. Do not use `conc` for static violations; `logos lint` rejects them before the program runs. Do not expect a finding for one lock order, a bounded group, or a goroutine the program can still wake. Do not expect a leak finding when every goroutine sleeps; the runtime ends that program itself with its own deadlock error. Do not expect findings from a plain `logos run`; only a `conc` build carries the monitor.

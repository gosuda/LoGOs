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

LoGOs runs goroutines inside `go group` blocks. Shared state lives in `logos/sync.Locked[T]`. `logos conc` runs a program or its tests in a build that carries a monitor. The monitor reports five fault kinds. `logos lint` and `logos test -race` do not report them. No memory is raced and nothing has failed yet.

## Procedure

1. Write group code: start children inside `go group` blocks.
2. Guard shared state with `logos/sync.Locked`.
3. Run `logos lint` and `logos test -race` first. `-race` reports only exercised races and gives no universal data-race freedom. The run works under `[build] cgo = "forbid"`: it links only the race runtime and needs a C compiler or `zig` in PATH.
4. Run the program under the monitor only when it is lint-clean and race-clean.
5. Use `logos conc run` for a program and `logos conc test` for tests.
6. Read each finding on standard output while the child still runs. Under `--json-diagnostics`, read the records on standard error.
7. Repair the named position, then rerun without `conc` to confirm the behavior.
8. Retry a shake failure with the printed seed to shake the run again. The seed fixes the yield counts, not the exact goroutine schedule, so treat a rerun as a retry, not an exact replay.

## Group and lock forms

Each row contrasts the LoGOs form with the plain Go pattern it replaces.

| LoGOs | Plain Go |
|---|---|
| `go group (ctx, limit = N) { go { work()? } }` | `rt.NewGroup(ctx, N)` followed by `Go` calls and `Wait` |
| `go group (ctx) { go { work()? } }` | `rt.NewGroup(ctx, 0)` followed by `Go` calls and `Wait` |

The group context cancels on the first child error or panic. The group joins every child before it returns. A child returns its error with `?`. A bare `return` inside a child is rejected. A `limit = N` bounds the children that run at one time. `Locked[T].WithLock` runs one callback at a time. Never return or leak the pointer from the callback. A plain channel send, receive, or range written directly in a child selects on the group context: on cancel the child returns instead of waiting. A child panic ends the group as `*logos/rt.PanicError` with the value and a stack.

## Run and test forms

`logos conc run` wraps `logos run`. `logos conc test` wraps `logos test`. Build and test flags pass through to the child. The monitor tag joins the build tags from `-tags` or from `GOFLAGS`. `logos conc test` adds `-count=1` unless a count is already set. Place conc flags before the package for `run`. Place conc flags before `-args` or `--` for `test`. Text after that boundary belongs to the program.

```sh
logos help conc
logos conc run ./opposite
logos conc test -count=1 ./opposite
logos conc run ./oneorder
```

Expect exit 0 for the help and the one-order run. Expect exit 1 for both opposite runs: the monitor prints one finding although the program exits zero.

Set up the example packages before running the commands: create one module with a `go.mod`, then write each section's snippet into its package as `main.lgo` in a `package main` with the shown imports and a `main` that runs it (`./opposite` for the lock-order snippet, `./ignores` for the stalled child, `./leaks` for the leak, `./slow` for the hold; `./oneorder` repeats `./opposite` with both locks in one order and exits 3 when passed an argument, `./ord` is the order program in `logos_conc_shake.txt`). The leak's `Subscribe` helper lives in an imported Go package: a module holds only `.go` packages or only `.lgo` packages, so put `blocks` in its own module with its own `go.mod` and wire it into `./leaks` with a `require` plus a `replace` pointing at its path — placing it beside the `.lgo` files in one module fails the module census before the monitor runs. A `logos conc test` run also needs a `main_test.lgo` in the package whose test calls the workload through `t.Context()` — testing a `main` package never runs `main` itself, so without the test file the command exits 0 with `[no test files]` and the promised finding never prints. The exact fixtures these commands were tested against are in `src/cmd/go/testdata/script/logos_conc_*.txt`. Without the files the command fails with a package lookup error and exit 1, the same status a finding gives, so a bare run proves nothing.

## Read conc::lock_order_inversion

Two goroutines took the same two `Locked` values in opposite orders. Treat it as a deadlock this run may have escaped by timing: the same run can still end with the runtime deadlock error after printing the finding, and the exit status is then the child's. One order everywhere gives no finding. Repair by taking the two values in one order everywhere, or by keeping both in one `Locked` value. Never edit a file under GOROOT to answer one.

```lgo
checking.WithLock(a => {
  savings.WithLock(b => { move(a, b)? })
})
savings.WithLock(a => {
  checking.WithLock(b => { move(a, b)? })
})
```

## Read conc::group_child_stalled

A child did not return within the `-stall` duration after its group was cancelled. The group cannot join before the child returns. Repair by receiving from the `Done` channel of the context in a `select`. Or give the context to the call that blocks.

```lgo
go group (ctx) {
  go { slowFetch()? }
  go { fastFail()? }
}
```

## Read conc::goroutine_leak

A goroutine waits, and no goroutine that can run reaches what it waits for. It never ends. The leak source lives in an imported Go package. A bare child outside `go group` is rejected in `.lgo`, so the `.lgo` caller only starts it and sleeps. Repair by letting the owner close the channel or release the lock. Or wait on a context in the same `select`. A goroutine the program can still wake is not a leak. A program where every goroutine sleeps ends with the runtime deadlock error and no finding.

```go
package blocks

func Subscribe() {
	updates := make(chan int)
	go func() {
		<-updates
	}()
}
```

## Read conc::lock_held_long

One goroutine held a `Locked` value past the `-hold` duration while another goroutine waited. Treat it as a liveness fault, not a race. The monitor keeps no timer for a hold. It prints the finding when a waiter comes after the limit, or when the hold ends. Repair by copying what the slow call needs, leaving the callback, then calling. Keep read-mostly state in `logos/sync.Hot[T]`: `Load` returns a read-only pointer, `Store` publishes a new pointer, and `Update` applies `func(old) *T` in a compare-and-swap loop that never exposes a partial value.

```lgo
go group (ctx) {
  go {
    ledger.WithLock(a => {
      time.Sleep(400 * time.Millisecond)
      a.balance += 10
    })
  }
  go {
    ledger.WithLock(a => {
      a.balance--
    })
  }
}
```

The example needs the second goroutine: with only the slow callback no
goroutine ever waits on the lock, so no finding prints even at
`-hold 50ms`.

## Read conc::group_unbounded

A group without a limit runs 10000 children at one time. The count follows the work the program gives the group. A group with a limit never gets this finding. Repair by giving the group a limit.

```lgo
go group (ctx) {
  for range 10000 {
    go { work()? }
  }
}
```

## Set flags and replay seeds

Pass `-stall` with a Go duration to set how long a cancelled-group child may run before it is reported. Omit it to keep the 5s default. Pass `-hold` with a Go duration to set how long a contested hold may last. Omit it to keep the 100ms default. Use either the space form or the `=` form for both.

```sh
logos conc run -stall 200ms ./ignores
logos conc run -hold 50ms ./slow
logos conc run -hold 2s ./slow
```

Expect exit 1 for the first two: each prints its finding. Expect exit 0 for the third: the 400ms hold stays below the 2s limit.

Pass `-shake` with a decimal seed to yield at group and lock hook points from that seed. Rerun the printed seed to shake the run again. The seed fixes the yield counts, not the exact goroutine schedule. Treat a rerun as a retry, not an exact replay. Pass bare `-shake` to take the seed from the clock; the command prints the seed it used. A bare `-shake` followed by a number takes that number as its seed. Omit `-shake` to turn shaking off. A seed of 0 is a usage error, because seed 0 turns shaking off. A non-numeric seed is a usage error.

Pin GOMAXPROCS=1 for the seeded runs: one thread stops cross-processor stealing from hiding the order fault. It does not fix the schedule: the run queue still picks among the children that can run, and the seed mixes in goroutine IDs and hook counts.

```sh
GOMAXPROCS=1 logos conc test -shake=1 -count=1 ./ord
GOMAXPROCS=1 logos conc run -shake=1 ./ord
logos conc test -shake -count=1 ./ord
```

Expect exit 1 for the test: the order flips and the test fails. Expect exit 1 for the run: the program prints the flipped order and `logos run` reports `exit status 2`. Expect exit 0 or 1 for the bare seed: the drawn order decides. Every one prints its `shake seed` line on standard error.

Pass `--json-diagnostics` to print each finding as one JSON object per line on standard error, in the `logos lint --json-diagnostics` schema. Expect the `note` field to hold the position lines and the fix line. Expect the tab-separated text form on standard output otherwise.

Pass `--fix` to rewrite a stalled child that waits on a plain receive in a function that takes a context: the line becomes a select on the receive and the context. Pass `--ai-patch` instead to print that edit as one JSON object per line on standard output without changing files. Repair every other finding by hand.

Expect review output on standard output: the text findings and the `--ai-patch` edits. Under `logos conc test -json`, standard output is the test2json stream, and each line of review output arrives as one event with `"Action":"output"`. The `logos conc: fixed` lines of `--fix` and the `shake seed` line stay on standard error.

```sh
logos conc run --json-diagnostics ./opposite
logos conc test -json --json-diagnostics ./opposite
logos conc test -json ./opposite
```

Expect exit 1 for each. Expect one JSON object on standard error for the first two. Expect the test2json stream on standard output for the last two; in the last one, the lines of the text finding arrive as its `output` events, and standard error stays empty.

## Read exit codes

Expect the status of the `logos run` or `logos test` child when that is not zero. A program that exits N makes `logos run` print `exit status N` and exit 1, so `conc run` exits 1 for it. Expect 1 when the child exits zero but a finding was printed. Expect 0 when the child exits zero and no finding was printed. Treat a damaged finding line as a finding. Expect a usage error and no child run for a bad flag value.

```sh
logos conc run ./oneorder fail
logos conc run ./leaks
logos conc test ./opposite
logos conc run -stall soon ./ignores
logos conc test -shake=nope -count=1 ./ord
```

Expect exit 1 for the first: `logos run` reports `exit status 3` and no finding prints. Expect exit 1 for the next two: each prints its finding. Expect exit 1 for both usage errors: each prints its message and runs no child. `logos conc test` without `-count` adds `-count=1` itself.

## Know what conc does not find

Do not use `conc` for data races; use `logos test -race`. Do not use `conc` for static violations; `logos lint` rejects them before the program runs. Do not expect a finding for one lock order, a bounded group, or a goroutine the program can still wake. Do not expect findings from a plain `logos run`; only a `conc` build carries the monitor. Run only on unix: the command transports findings over a named pipe.

## Diagnostics

`logos lint` rejects group and lock misuse before the program runs; the `conc` sections above cover what only a running build shows. These rows give the `.lgo` policy: in Go packages `logos lint` runs only language-neutral rules and reports `forbid` as suppressible `deny`. The concurrency lint rows:

| Rule | Severity | Fix |
|---|---|---|
| `concurrency::unsupervised_goroutine` | forbid | Move the bare `go f()` into a `go group` child block. |
| `concurrency::reentrant_withlock_deadlock` | forbid | Do not call `WithLock` on the same receiver inside its own callback. |
| `concurrency::concurrent_multiword_race` | forbid | Guard the shared multi-word value with `Locked` or `Hot`, or make it a const view. |
| `concurrency::unguarded_access` | forbid | Hold the `//logos:guardedby` lock at the access. |
| `concurrency::nonatomic_access` | forbid | Touch a `//logos:atomic` field only through `sync/atomic`. |
| `concurrency::lock_requirement_unmet` | forbid | Hold the `//logos:requires` set before the call. |

```sh
logos lint ./...
```

Expect exit 1 naming the rule id when a row is violated, and exit 0 when the tree is clean.

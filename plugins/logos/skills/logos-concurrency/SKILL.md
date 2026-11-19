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
3. Run `logos lint` and `logos test -race` first.
4. Run the program under the monitor only when it is lint-clean and race-clean.
5. Use `logos conc run` for a program and `logos conc test` for tests.
6. Read each finding on standard error while the child still runs.
7. Repair the named position, then rerun without `conc` to confirm the behavior.
8. Retry a shake failure with the printed seed to shake the run again.

## Group and lock forms

Each row contrasts the LoGOs form with the plain Go pattern it replaces.

| LoGOs | Plain Go |
|---|---|
| `go group (ctx, limit = N) { go { work()? } }` | `rt.NewGroup(ctx, N)` followed by `Go` calls and `Wait` |
| `go group (ctx) { go { work()? } }` | `rt.NewGroup(ctx, 0)` followed by `Go` calls and `Wait` |

The group context cancels on the first child error or panic. The group joins every child before it returns. A child returns its error with `?`. A bare `return` inside a child is rejected. A `limit = N` bounds the children that run at one time. `Locked[T].WithLock` runs one callback at a time. Never return or leak the pointer from the callback.

## Run and test forms

`logos conc run` wraps `logos run`. `logos conc test` wraps `logos test`. Build and test flags pass through to the child. The monitor tag joins tags passed with `-tags`; tags set only in `GOFLAGS` are replaced by the monitor tag, so pass them with `-tags` instead. `logos conc test` adds `-count=1` unless a count is already set. Place conc flags before the package for `run`. Place conc flags before `-args` or `--` for `test`. Text after that boundary belongs to the program.

```sh
logos help conc
logos conc run ./opposite
logos conc test -count=1 ./opposite
logos conc run ./oneorder
```

Expect exit 0 for the help and the one-order run. Expect exit 1 for both opposite runs: the monitor prints one finding although the program exits zero.

Set up the example packages before running the commands: create one module with a `go.mod`, then write each section's snippet into its package as `main.lgo` in a `package main` with the shown imports and a `main` that runs it (`./opposite` for the lock-order snippet, `./ignores` for the stalled child, `./leaks` for the leak with its `Subscribe` in an imported Go package, `./slow` for the hold; `./oneorder` repeats `./opposite` with both locks in one order, `./ord` is the order program in `logos_conc_shake.txt`). The exact fixtures these commands were tested against are in `src/cmd/go/testdata/script/logos_conc_*.txt`. Without the files the command fails with a package lookup error and exit 1, the same status a finding gives, so a bare run proves nothing.

## Read conc::lock_order_inversion

Two goroutines took the same two `Locked` values in opposite orders. Treat it as a deadlock that this run escaped by timing. One order everywhere gives no finding. Repair by taking the two values in one order everywhere, or by keeping both in one `Locked` value. Never edit a file under GOROOT to answer one.

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

One goroutine held a `Locked` value past the `-hold` duration while another goroutine waited. Treat it as a liveness fault, not a race. The monitor keeps no timer for a hold. It prints the finding when a waiter comes after the limit, or when the hold ends. Repair by copying what the slow call needs, leaving the callback, then calling. Keep the value in `logos/sync.Hot` when readers must not wait.

```lgo
ledger.WithLock(a => {
  time.Sleep(400 * time.Millisecond)
  a.balance += 10
})
```

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

Pin GOMAXPROCS=1 for the seeded runs: one thread keeps the spawn order.

```sh
GOMAXPROCS=1 logos conc test -shake=1 -count=1 ./ord
GOMAXPROCS=1 logos conc run -shake=1 ./ord
logos conc test -shake -count=1 ./ord
```

Expect exit 1 for the test: the order flips and the test fails. Expect exit 1 for the run: the program prints the flipped order and `logos run` reports `exit status 2`. Expect exit 0 or 1 for the bare seed: the drawn order decides. Every one prints its `shake seed` line on standard error.

Pass `--json-diagnostics` to print each finding as one JSON object per line in the `logos lint --json-diagnostics` schema. Expect the `note` field to hold the position lines and the fix line. Expect tab-separated text output otherwise. Apply every repair by hand. This revision offers no `--fix` flag.

```sh
logos conc run --json-diagnostics ./opposite
```

Expect exit 1 with one JSON object on standard error.

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

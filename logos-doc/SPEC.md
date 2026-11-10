# loGo/s specification

- Status: Tailored spec, 2026-10-19; amended 2026-11-08 (withdrew GC modes, synthetic PGO, ICF, clone specialization, the fast-cgo default, vet-duplicate lints, and numeric targets; replaced the upstream arena with placement regions; moved nogo into logos lint)
- Base: Go 1.27.2
- Original: `logos-docs/PLAN.md` at commit 57475ed (`git show 57475ed:logos-docs/PLAN.md`)
- Source files: `.lgo` (canonical); aliases U+1F4DC (scroll), U+26A1 (lightning), and U+0295 U+25D4 U+03D6 U+25D4 U+0294 (gopher); `.go` files stay Go source files with Go language semantics.

The words MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY follow RFC 2119. The text follows ASD-STE100. Bracketed tags trace each rule to rows in IMPLEMENTATION.md.

## 0. Architectural axioms and transpilation contract

### 0.1 Upstream compatibility
Every improvement to code generation, runtime, stack, SIMD, wasm, cgo, optimization level, and tooling MUST apply to Go packages and to loGo/s packages [T-0.1][R-U1].
That rule MUST hold under `logos` and under plain `go` [T-0.1][R-U1].
The following are by design outside those two sentences: fixed-array operators and reduction methods, which Sec 2.4 withholds from Go packages; the `make` length overflow lowering, which Sec 3.3 and Sec 13.2 define for `.lgo` files; the slice-sum vector form, which needs the `.sum()` chain call of Sec 1.4 and has no Go spelling; the wasm loop-stride default, which Sec 13 applies under `logos` only while Sec 12.1 needs the explicit `--wasm-features` flag under plain `go`; `target_cpu`, which Sec 1.5 applies to `logos` builds while plain `go` keeps the upstream `GOAMD64` and `GOARM64` defaults; the capabilities auditor, which Sec 11.1 runs in `logos` builds only; the `//logos:zeroalloc` directive, which `go build` MUST ignore in Go packages per Sec 17.2 while `logos lint` and `logos build` apply it; tooling delivered as `logos` subcommands (`lint`, `eject`, `lsp`, `context`, `next`), which have no plain-`go` counterpart, while plain-`go` tooling breadth is the shared flags and diagnostics of Sec 0.1, Sec 12, and Sec 14; alternate GC modes and synthetic PGO, which carry no rules after the withdrawal; loGo/s syntax-gated lowering (options, unions, tuples, const views and composites, comptime, `go group`, static JSON and binary forms), which has no Go spelling while Go importers see the Go-facing forms of Sec 0.3 and Sec 5; and eject, which converts loGo/s to Go one way per Sec 0.3.
An improvement that this spec puts behind a named `GOEXPERIMENT` flag MUST stay behind that flag as this spec states. The `logos_settings_env.txt` script gates the arena and preemptibleloops experiment defaults [T-0.1][R-U1].
Note: the `go` command can differ from upstream Go 1.27.2 in performance, code generation, runtime, and tooling. No test asserts a difference.
The `go` command MUST keep Go language semantics, so every valid Go program MUST keep its Go meaning [T-0.1][R-U1].
The `go version` output and the `go help` output, including every help topic, MUST stay the upstream text [T-0.1].
Plain `go` MUST select improvements through flags and `GOEXPERIMENT` and MUST read no `logos.toml` [T-0.1][R-U1].
A flag that plain `go` gains MUST stay absent from the help text, so that text stays byte-identical to upstream [T-0.1][R-U1].
The `logos` command MUST build Go modules with Go language semantics [T-0.1].
Under `logos`, a Go-module build MUST set the `logos` build tag and, with the default arena enablement, the `goexperiment.logosarena` build tag [T-0.1].
An author `//logos:zeroalloc` directive in a Go file MUST fail `logos build` per Sec 17.2, while `go build` MUST ignore it [T-0.1].
Under `logos` with the default arena enablement, `runtime.Version()` MUST carry the `X:logosarena` experiment suffix [T-0.1].
A Go call into a loGo/s function MUST get the `logos/rt.MustNonNil` check per Sec 5 [T-0.1].
These four MUST be the only differences from Go language semantics for Go modules under `logos` [T-0.1].
The `logos` command MUST apply no loGo/s type rules to Go modules [T-0.1].
The `logos build` command MUST run no default lint on Go modules [T-0.1][R-U1].
The `logos lint` command MUST lint Go packages with the language-neutral rules only [T-0.1][T-102][R-U1].
A module MUST hold one kind only [T-0.1][R-U3].
A loGo/s module MUST hold only `.lgo` packages [T-0.1][R-U3].
A Go module MUST hold only `.go` packages [T-0.1][R-U3].
A workspace MAY hold Go modules and loGo/s modules side by side [T-0.1][R-U3].
A package MUST NOT mix user-authored `.go` files and `.lgo` files. Compiler-generated cgo `.go` outputs are the only exception. [T-0.1][A-CODEGEN]
The tool MUST reject a mixed package with `correctness::mixed_package` [T-0.1][R-U3].
The `correctness::mixed_package` text MUST name both file classes [T-0.1][R-U3].
The tool MUST reject a module with both kinds with `correctness::mixed_module` [T-0.1][R-U3].
The `correctness::mixed_module` text MUST name the Go packages and the loGo/s packages [T-0.1][R-U3].
A loGo/s package MAY import any Go package [T-0.1][R-U2].
A Go package MAY import any loGo/s package, except as the next sentence states [T-0.1][R-U2].
An import across kinds MUST NOT be an error [T-0.1][R-U2].
Interop MUST work per import and MUST NOT need a package or a module that holds both kinds [T-0.1][R-U3].
A Go declaration that a `.lgo` file imports MUST be a platform declaration per Sec 2.2 [T-0.1][T-202][A-STD][R-U2].
See Sec 0.3 for the Go-facing signatures that a Go importer sees.
A workspace root `logos build ./...` MUST build every Go module and every loGo/s module of the workspace [T-0.1][R-U2].
The `logos` command alone MUST select the `.lgo` language. This covers file recognition, loGo/s syntax, loGo/s type rules, and loGo/s lints [T-0.1][A-LGOSEL].
Plain `go` MUST ignore `.lgo` files [T-0.1][A-LGOSEL].
Plain `go` MUST report the upstream `no Go files` result for a loGo/s-only package [T-0.1][A-LGOSEL].
A plain `go` build of a Go package that imports a loGo/s package MUST fail with a load error that names the imported package, because plain `go` ignores `.lgo` files [T-0.1][A-LGOSEL].
Packages from `golang.org/x` and other third-party Go modules MUST count as Go packages that `.lgo` code MAY import [A-XMOD][R-U2].
A loGo/s package MUST NOT hold a generated `.go` file except cgo outputs [A-CODEGEN].
A generator MAY write `.go` files into a Go package that a loGo/s package imports [A-CODEGEN][R-U2].
Project tools SHOULD use `tool` directives in `go.mod`, never a Go file admitted to the build in a loGo/s module [A-MODKIND][R-U3].

### 0.2 No new global keywords
The language MUST add no new global keywords [T-0.2].
See Sec 4.1 for contextual words and `const` positions.
Identifiers that start with `_logos_` or `Logos_` MUST be rejected in `.lgo` [T-203.2].
The compiler MUST report such identifiers with `correctness::reserved_identifier` [T-203.2].

### 0.3 Pure Go output through eject
The command `logos eject -o DIR` MUST write a complete pure-Go copy of the main module to DIR [T-0.3].
The command `logos build --emit-go=DIR` MUST be an alias of `logos eject -o DIR` [T-0.3].
DIR MUST NOT exist, or DIR MUST be empty [T-0.3].
The tool MUST refuse a DIR that exists and holds files [T-0.3].
Each `.lgo` file MUST become a `.go` file [T-0.3].
The output MUST keep all comments from the source [T-0.3].
Each non-standard loGo/s dependency module MUST be ejected too [T-0.3].
Each ejected dependency MUST land in `DIR/_logos_deps/<module path>@<version>/` [T-0.3].
The output `go.mod` MUST carry a `replace` line for each ejected module [T-0.3].
A Go dependency module MUST stay a `require` entry of the output `go.mod` [T-0.3][R-U2].
The support sources MUST become one shared module `github.com/gosuda/loGos/rt` [T-0.3].
The shared module MUST land in `DIR/_logos_deps/rt` [T-0.3].
The output MUST carry a `replace` line for the shared module [T-0.3].
The tool MUST remove `import "comptime"` during eject [T-0.3].
The output MUST NOT include a `//go:build !logos` line [T-0.3][R-U3].
The output MUST NOT emit `.gen.go` file names [T-0.3].
The output MUST carry the reserved `_logos_` import aliases that lowering writes, such as `_logos_unsafe "unsafe"` [T-0.3].
The output MUST NOT emit `_logos_errors` imports [T-0.3].
Among support imports, lowered files before eject MUST import only `_logos_rt "logos/rt"`, `_logos_json "logos/rt/json"`, plus `_logos_unsafe "unsafe"` where needed, in addition to standard library imports under reserved `_logos_` aliases. Ejected files MUST spell those support imports as `_logos_rt "github.com/gosuda/loGos/rt"` and `_logos_json "github.com/gosuda/loGos/rt/json"` (the eject rewrite in `src/cmd/go/internal/logos/eject/eject.go:supportPathEdit`).
The tool MUST NOT write ejected files into the source tree [T-0.3].
Ejected code MUST build with an upstream Go 1.27 toolchain [T-0.3].
A Go package that imports a loGo/s package MUST see each exported declaration with the Go-facing signature that `logos eject` emits for it [T-0.3][R-U2].
The output MUST NOT emit colliding helper imports. [T-0.3]

### 0.4 Design axiom for explicit code
Explicitness guides the rules below. Signatures and call sites show side effects, mutability contracts, nilability states, and error paths [T-0.4]. Each later section states its own testable rules [T-0.4].
The lint registry enumerates the enforced lint rules [T-0.4].

## 1. Tier 1 specification (toolchain, linter, desugaring)

### 1.1 RFC-101: File loader, Unicode normalization, and cgo policy
The loader MUST recognize `.lgo` and the three alias extensions only in `logos` mode [T-101.1].
The loader MUST strip trailing U+FE0E and U+FE0F from the extension before matching [T-101.1].
Two files in one package whose names differ only by alias extension MUST fail the build [T-101.1].
The tool MUST report that failure with `correctness::duplicate_stem_filename` [T-101.1].
Module zips MUST reject alias extensions as non-letter symbols for publication [T-101.1].
The tool MUST report warning `portability::alias_extension` for each alias file when the main module path first element holds a dot [T-101.1].
When the main module is a loGo/s module, `CGO_ENABLED` MUST default to 0 [T-101.2].
The key `logos.toml [build] cgo` MUST accept `forbid`, `restricted`, or `allow` in a loGo/s main module [T-101.2].
The default for `[build] cgo` MUST be `forbid` in a loGo/s main module [T-101.2].
The value `restricted` MUST enable cgo only for packages that match `cgo_allowlist` globs [T-101.2].
In a loGo/s main module, a package with `import "C"` outside the allowlist MUST fail with `restriction::cgo_usage` [T-101.2].
In a loGo/s main module with no `logos.toml`, a package with `import "C"` MUST fail with `restriction::cgo_usage` and text `cgo is disabled` [T-101.2].
In a loGo/s main module, an explicit `CGO_ENABLED` in the environment MUST win over the default only when the `cgo` key is absent [T-101.2]. An explicit `[build] cgo = "forbid"` MUST stay enforced against the environment [T-101.2]. An explicit `[build] cgo = "restricted"` allowlist refusal MUST stay enforced against the environment [T-101.2].
When cgo is enabled, `CC` is unset, and the default C compiler is missing, `logos` and plain `go` MUST use `zig cc -target <zig-arch>-<zig-os>` for every cgo package, Go or loGo/s, when `zig` is in PATH [T-101.2][R-U1].
The `-musl` suffix MUST apply only for Linux targets [T-101.2].
The `[build] cgo` policy (`forbid`, `restricted`, `allow`) and its `forbid` default MUST apply to loGo/s main modules only [T-101.2][R-U1].
A Go main module MUST keep the upstream default for `CGO_ENABLED` under `logos`, and a `cgo` key in its `logos.toml` MUST have no effect [T-101.2][R-U1].
Plain `go` MUST keep the upstream default for `CGO_ENABLED` [T-101.2].
With cgo off, `net` and `os/user` in the standard library MUST use their pure-Go paths [T-101.2].
When the main module path's first element contains a dot, each alias-extension file MUST produce warning `portability::alias_extension`. [T-101.1][T-102]

### 1.2 RFC-102: Compiler linter and rule set
The `logos lint` check MUST run inside the compiler on `.lgo` packages for syntax and type rules [T-102].
The `logos lint` check MUST run the language-neutral syntax and type rules on Go packages at the same point [T-102][R-U1].
The `logos lint` check MUST run inside escape analysis for escape rules, on Go packages and on `.lgo` packages [T-102][R-U1].
`logos lint` MUST lint Go packages with the language-neutral rules only [T-102][R-U1].
A rule MUST count as language-neutral when a Go package can trigger it with Go syntax and types alone and the code it guards exists in Go packages. A loGo/s form inside its detection, such as a group child, a const view, or `?`, MUST stay inactive in Go [T-102][R-U1]. The only exception is a `go` statement inside a `WithLock` callback that spawns a relock, which MUST NOT report under `concurrency::reentrant_withlock_deadlock` [T-102][R-U1].
The language-neutral rules MUST be `correctness::typed_nil_return` (in Go, the `(*T)(nil)` form only), `correctness::arena_pointer_escape`, `correctness::infinite_recursion`, `correctness::unclosed_io_closer`, `correctness::unbounded_channel_op`, `concurrency::concurrent_multiword_race`, `concurrency::reentrant_withlock_deadlock`, `correctness::withlock_leak`, `slop::err_chain_reset`, `slop::discarded_error`, `complexity::cognitive_overflow`, `perf::large_backing_array_pin`, `perf::time_after_in_loop`, `concurrency::unguarded_access`, `concurrency::nonatomic_access`, `concurrency::lock_requirement_unmet`, `correctness::layout_constant_mismatch`, `portability::atomic_plain_int64`, `correctness::linkname_mismatch`, `perf::arena_fallback`, and `pedantic::unsafe_outside_unsafe_file` [T-102][R-U1].
The compiler MUST enforce `concurrency::unguarded_access` as `forbid` against a read or write of a field that `//logos:guardedby` guards without its lock held [T-102].
The compiler MUST enforce `concurrency::nonatomic_access` as `forbid` against a non-`sync/atomic` access of a field that `//logos:atomic` marks [T-102].
The compiler MUST enforce `concurrency::lock_requirement_unmet` as `forbid` against a call whose `//logos:requires` set the caller does not hold [T-102].
The compiler MUST enforce `correctness::layout_constant_mismatch` as `forbid` against a constant declared with `//logos:sizeof`, `//logos:offsetof`, or `//logos:alignof` whose value differs from the computed layout [T-102].
The compiler MUST enforce `portability::atomic_plain_int64` as `warn` against a 64-bit `sync/atomic` call on the address of a plain `int64` or `uint64`, with an autofix to `atomic.Int64` or `atomic.Uint64` [T-102].
The compiler MUST enforce `correctness::linkname_mismatch` as `forbid` against a `//go:linkname local pkg.name` whose imported signature differs from the local one, when `pkg` is in the import graph [T-102].
The compiler MUST enforce `pedantic::unsafe_outside_unsafe_file` as `allow` against `import "unsafe"` in a file whose name does not end in `_unsafe.go` or `_test.go` [T-102].
The compiler MUST enforce `correctness::region_escape` as `forbid` against a `logos/arena.Region` value that leaves its local scope in a `.lgo` package [T-102].
The compiler MUST report `perf::arena_fallback` when a `logos/arena` call escapes to the heap [T-102].
The directive grammar for these checks is `//logos:guardedby <mu>`, `//logos:atomic`, `//logos:requires <p.mu>`, `//logos:acquires <p.mu>`, `//logos:releases <p.mu>`, `//logos:zeroalloc [local]`, `//logos:sizeof T`, `//logos:offsetof T.f`, and `//logos:alignof T` [T-102]
The rules `correctness::nil_deref_unproven`, `correctness::defer_erases_errdefer_state`, `concurrency::unsupervised_goroutine`, `suspicious::unapplied_struct_default`, `suspicious::side_effect_before_sort`, `portability::serialized_unsized_int`, `portability::alias_extension`, and `pedantic::exhaustive_union_switch` MUST NOT apply to Go packages, because each depends on loGo/s syntax, types, or generated code [T-102][R-U1].
The rule `portability::serialized_unsized_int` guards the generated binary layout of Sec 10.1, which Go packages do not get [T-102][R-U1].
A Go package MUST get the default group severities, the `logos.toml [lints]` table, and the `//logos:allow(<rule>)` directive of this section, with the cap that the next line states [T-102][R-U1].
A language-neutral rule whose default severity is `forbid` MUST run at `deny` for a Go package [T-102][R-U1].
`//logos:allow(<rule>)` MUST silence such a rule in a Go package [T-102][R-U1].
A loGo/s package MUST keep the `forbid` severity of such a rule [T-102].
`logos build` MUST run no default lint on a Go package [T-102][R-U1].
Third-party modules of either kind MUST get no code lints [T-102][R-U1].
Capability checks MUST apply to every module in the build [T-102].
`forbid` MUST NOT be lowered by configuration [T-102].
In a loGo/s package, `forbid` MUST ignore `//logos:allow(<rule>)` [T-102].
`deny` MUST be an error unless `//logos:allow(<rule>)` precedes the statement or declaration [T-102].
`warn` MUST print a warning and MUST NOT fail the build [T-102].
`allow` MUST stay silent [T-102].
In a loGo/s package, a `//logos:allow` directive on a `forbid` rule MUST itself be an error with `correctness::allow_forbidden_rule` [T-102].
In a Go package, a `//logos:allow` directive on a rule that the Go cap lowers from `forbid` to `deny` MUST silence that rule [T-102][R-U1].
The default group severities MUST be `correctness` forbid, `concurrency` forbid, `slop` deny, `complexity` deny, `perf` warn, `style` warn, `suspicious` warn, `portability` warn, `pedantic` allow [T-102].
The `logos.toml [lints]` table MUST control group severities [T-102].
The `logos.toml [lints.thresholds]` defaults MUST be 3 for nesting depth, 15 for cognitive complexity, 60 for function lines, 4 for function parameters, 4096 for comptime nodes, and 100000 for CTFE steps [T-102].
Configuration MAY raise `max_comptime_ast_nodes` and `max_ctfe_steps` above their defaults [T-102].
`logos build` and `logos lint` MUST accept `--pedantic` with default false [T-102].
The `--pedantic` flag MUST NOT lower a stricter `logos.toml` severity [T-102]. `--pedantic` MUST raise the pedantic group from `allow` to `warn` and MUST keep a stricter configured severity, and `--pedantic=false` MUST set the pedantic group to `allow` [T-102].
A `logos.toml` value that lowers a `forbid` group MUST fail with `configuration::forbid_lowered` [T-102].
A Go package MUST NOT run any rule at `forbid`: a group configured `forbid` runs at `deny` there [T-102][R-U1].
A default build MUST stay silent for the pedantic group [T-102].
`logos lint ./...` MUST report findings without building binaries [T-102].
`--json-diagnostics` MUST print one JSON object per line on stderr for each compiler and lint diagnostic in `.lgo` files and in Go files with schema version `1.0` [T-102][R-U1].
The compiler MUST enforce `correctness::nil_deref_unproven` as `forbid` against use of a nullable value before its guard [T-102].
The compiler MUST enforce `correctness::typed_nil_return` as `forbid` against return of a concrete nil pointer as an interface or `error` result without a dominating nil check [T-102].
The compiler MUST enforce `correctness::arena_pointer_escape` as `forbid` against storage of a stack or arena pointer outside its frame through an unsafe address [T-102].
The compiler MUST enforce `correctness::defer_erases_errdefer_state` as `forbid` against assignment of nil to the error result inside a plain deferred literal in a function with `errdefer` [T-102].
The compiler MUST enforce `correctness::infinite_recursion` as `forbid` against a function whose every path from entry calls itself with identical arguments before any return [T-102].
The compiler MUST enforce `concurrency::concurrent_multiword_race` as `forbid` against sharing of a mutable multi-word value with a child block plus a write on either side, unless the value is a const view or a `logos/sync` type [T-102].
The compiler MUST enforce `concurrency::reentrant_withlock_deadlock` as `forbid` against a `WithLock` call on the same receiver inside its own synchronous closure call graph. A relock that a `go` statement inside the callback spawns MUST NOT report. [T-102].
The compiler MUST enforce `correctness::withlock_leak` as `deny` against a `WithLock` lambda that returns or stores its argument, sends it on a channel, or passes it to a goroutine [T-102].
The compiler MUST enforce `concurrency::unsupervised_goroutine` as `forbid` against a `go` statement in `.lgo` outside a `go group` block [T-102].
The compiler MUST enforce `slop::err_chain_reset` as `deny` against formatting of an `error` operand with a verb other than `%w` or against passing of `err.Error()` [T-102].
The compiler MUST enforce `slop::discarded_error` as `deny` against dropping of an `error` result, except callees with an always-nil-error fact, `fmt.Print*` calls, `fmt.Fprint*` calls to `os.Stdout` or `os.Stderr`, and `bytes.Buffer.Write*` and `strings.Builder.Write*` calls [T-102].
The compiler MUST enforce `correctness::unclosed_io_closer` as `deny` against a local `io.Closer` with no deferred close and no transfer out of the function [T-102].
The compiler MUST enforce `correctness::unbounded_channel_op` as `deny` against a channel operation outside a select with a done case, unless the channel is locally made with constant capacity and send count stays within capacity [T-102].
The compiler MUST enforce `complexity::cognitive_overflow` as `deny` against nesting depth above 3, parameter count above 4, line count above 60, or cognitive score above 15 [T-102].
The cognitive score MUST be the sum over branch, loop, switch, select, conditioned case, goto, and labeled jump points of one plus nesting depth, plus one per change of boolean operator in a chain [T-102].
The compiler MUST enforce `suspicious::unapplied_struct_default` as `warn` against zero declaration of a type with field defaults [T-102].
The compiler MUST enforce `suspicious::side_effect_before_sort` as `warn` against a chain lambda with a call of unknown purity before `.sortBy()` [T-102].
The compiler MUST enforce `perf::large_backing_array_pin` as `warn` against escape of a subslice or substring of a backing array above 64 KB [T-102].
The compiler MUST enforce `portability::serialized_unsized_int` as `warn` against an `int` or `uint` field in a struct with JSON tags [T-102].
The compiler MUST enforce `portability::alias_extension` as `warn` for each alias-extension file per the module-path rule [T-101.1][T-102].
The warnings of `suspicious::unapplied_struct_default`, `suspicious::side_effect_before_sort`, and `portability::alias_extension` MUST take their severity from the `logos.toml [lints]` group of the rule, and `//logos:allow(<rule>)` MUST silence each of them under `warn` and `deny` [T-102].
A `//logos:allow(portability::alias_extension)` directive MUST silence the rule for an alias-extension file only when it sits in the comment block directly above the package clause of that file [T-102].
The compiler MUST enforce `perf::time_after_in_loop` as `warn` against `time.After` inside a loop body [T-102].
The compiler MUST enforce `pedantic::exhaustive_union_switch` as `allow` against a `default` clause in a switch over a closed union [T-102].
JSON output MUST offer autofix candidates for optional chaining with default, early-return guard, split of a misplaced `?`, verb fix to `%w`, and addition of `?` in an error-returning function [T-102].
JSON output for a Go package MUST offer only the verb fix to `%w` among the previous sentence's candidates, because the other candidates name loGo/s syntax; it MAY also offer the `portability::atomic_plain_int64` wrapper fix. [T-102][R-U1]
A default over a closed union MUST report pedantic::exhaustive_union_switch when the pedantic group is enabled; that rule defaults to allow and is silent otherwise. [T-102]
Exhaustive switches MUST still list every member or default. [T-102]
The compiler MUST allow always-nil-error callees and benign writers to standard output, standard error, byte buffers, and string builders. [T-102]
It MUST NOT hoist timers across iterations. [T-102]

### 1.3 RFC-103: Context and next tools
The command `logos context <pkg>#<Symbol>` MUST print the declaration plus one level of mentioned signatures with no function bodies [T-103].
`logos context` output MUST hold at most 100 lines [T-103].
`logos context` MUST serve `.lgo` and `.go` packages [T-103].
`logos context` MUST report an unknown symbol with text that names the package and the missing symbol [T-103].
The command `logos next --fix` MUST convert a whole Go module to a loGo/s module [T-103][R-U3].
`logos next` MUST use byte-span splicing and MUST keep every comment and build line byte-identical outside rewrites [T-103].
`logos next` MUST keep each non-standard Go import of the module as an import of a Go package [T-103][R-U2].
`logos next` MUST change nothing while it only prints a diff [T-103].
`logos next --fix` MUST rename converted files and MUST rewrite matched patterns [T-103].
`logos next` MUST leave complex refactors unchanged and MUST list them in `next_tasks.json` [T-103].
`logos next` MUST rewrite an index-range append loop with a pure body to a map chain [T-103].
`logos next` MUST leave a loop with jump control or side effects unchanged and MUST list it in `next_tasks.json` [T-103].
No `logos fmt` command exists [T-103].
No command except `logos next` MUST rewrite `.lgo` files [T-103].
Conversion MUST use byte-span splicing. [T-103]
`logos context` MUST print the declaration and one level of referenced signatures without function bodies, capped at 100 lines. [T-103]
`logos next` MUST preserve comments and build lines outside its edits. [T-103]
The toolchain MUST NOT add a `logos fmt` rewriting command. [T-103]

### 1.4 RFC-104: Syntactic sugar and desugaring
Error propagation with `?` MUST be a postfix operator [T-104.1].
`?` MUST appear only as the outermost operator of a single right-hand call in `=` or `:=`, an expression statement, a `defer` call, an `errdefer` call, or the body of an expression lambda [T-104.1].
Any other use of `?` MUST fail with `correctness::misplaced_try` and text `? must end an assignment, expression statement, defer, or errdefer` [T-104.1].
The operand MUST be a call whose last result type is identical to `error` [T-104.1].
A named type whose underlying type is `error` MUST NOT satisfy the operand rule [T-104.1].
The enclosing function MUST have a last result type identical to `error` [T-104.1].
`x := f()?` MUST bind the call results minus the error [T-104.1].
`x := f()?` where the call returns only `error` MUST fail [T-104.1].
Zero remaining results MUST be allowed only in an expression statement, a defer, or an errdefer [T-104.1].
Normal `:=` scope rules MUST apply to `x := f()?` with no new variable [T-104.1].
On error, every non-error result MUST take its zero value [T-104.1].
The returned error MUST be the call error value unchanged [T-104.1].
Assignments in the statement MUST run before the error check [T-104.1].
The error path MUST record the site through `logos/rt` when built by `logos` [T-104.1].
The error path MUST be a no-op for traces in ejected code [T-104.1].
`logos/rt.ErrorTrace()` MUST return recorded sites in propagation order for the calling goroutine [T-104.1].
A different error value MUST reset the trace [T-104.1].
The trace MUST keep the last 8 sites [T-104.1].
```go
func double(s string) (int, error) {
	n := strconv.Atoi(s)?
	return 2 * n, nil
}
```
`defer f()?` MUST join a non-nil result into the error result [T-104.2].
The join MUST keep the existing error when nil and MUST use errors join semantics otherwise [T-104.2].
`errdefer` MUST be allowed only in functions whose last result is `error` [T-104.2].
Other uses MUST fail with `correctness::errdefer_needs_error_result` [T-104.2].
`errdefer` MUST run when the frame panics, exits through Goexit, or holds a non-nil error result at pop time [T-104.2].
A typed-nil error MUST count as non-nil for `errdefer` [T-104.2].
The function, receiver, and arguments of a deferred call MUST be evaluated at the statement [T-104.2].
A panic while evaluating return values MUST count as failure for `errdefer` [T-104.2].
Deferred calls MUST run in LIFO order across `defer`, `defer?`, and `errdefer` [T-104.2].
```go
func copyFile(dst string, src string) (error) {
	f := os.Open(src)?
	defer f.Close()?
	return nil
}
```
`?.` and `??` MUST operate on option types `T?` [T-104.3].
A map index in `.lgo` MUST yield `V?` [T-104.3].
The form `v, ok := m[k]` MUST keep Go meaning [T-104.3].
`??` MUST bind less tightly than `||` [T-104.3].
`??` MUST group to the right [T-104.3].
See Sec 2.2 for `?.` evaluation and option operand checks.
```go
func greet(u *User?) (string, error) {
	return u?.Name ?? "Guest", nil
}
```
`$"..."` MUST accept operands of type string, any integer, bool, float32, float64, `[]byte`, `error`, or `fmt.Stringer` [T-104.4].
Any other operand type MUST fail with `correctness::interp_type` [T-104.4].
`{{` and `}}` MUST be literal braces [T-104.4].
Operands MUST be evaluated once from left to right [T-104.4].
The lowering MUST use one allocation with a bound covering the literal bytes and the converted operands [T-104.4].
The lowering MUST never regrow the buffer [T-104.4].
Operand text MUST NOT be read as a format string [T-104.4].
An unterminated `{` MUST be a syntax error [T-104.4].
```go
func msg(name string, n int) string {
	return $"User {name} has {n} items"
}
```
`&expr` for a non-addressable operand MUST type as a pointer [T-104.5].
Each `&expr` evaluation MUST yield a fresh variable [T-104.5].
A struct field default MUST have form `F T = constExpr` before the tag [T-104.5].
Defaults MUST apply in composite literals of the type, including address forms and partial literals [T-104.5].
A Go struct literal of an exported loGo/s struct MUST get zero values and MUST NOT get the field defaults; this is a documented hole [T-104.5][R-U2][A-GOBOUNDARY].
`var x T` and `new(T)` MUST trigger `suspicious::unapplied_struct_default` [T-104.5].
A non-constant default MUST fail with `correctness::default_not_constant` [T-104.5].
Spread `T{..src}` MUST copy fields with identical names and identical types [T-104.5].
The same name with a different type MUST fail with `correctness::spread_type_mismatch` [T-104.5].
Explicit fields MUST win over spread fields [T-104.5].
The source MUST be evaluated once [T-104.5].
Self-update `x = T{f: e, ..x}` MUST lower to one tuple assignment of the explicit fields [T-104.5].
Lambdas MUST have expression form `x => expr`, block form `x => { stmts }`, or tuple-parameter form `(a, b) => ...` [T-104.6].
A lambda MUST appear only where a function value of known parameter types is expected [T-104.6].
Parameter types MUST come from the target type [T-104.6].
A lambda without a target function type MUST fail with `correctness::lambda_needs_target` [T-104.6].
Slices MUST offer `filter`, `map`, `find`, `any`, `all`, `sortBy`, `first`, `last`, `get`, `take`, `drop`, `sum`, and `count` chain methods [T-104.6].
`find`, `first`, `last`, and `get` MUST return `T?` [T-104.6].
`take`, `drop`, and `get` MUST clamp indices without panic [T-104.6].
`sortBy` MUST be a stable sort by `cmp.Compare` of keys over ordered key types [T-104.6].
Float `.sum()` MUST use fixed pairwise order with no FMA contraction [T-104.6].
Float `.sum()` MUST be bit-identical across x86_64 and ARM64 builds [T-104.6].
Chained calls MUST fuse into one loop with the receiver evaluated once [T-104.6].
Lambda parameters in chains MUST be const views [T-104.6].
Mutation of the receiver inside a chain lambda MUST fail compilation [T-104.6].
`?` in an expression-lambda body MUST propagate from the enclosing function [T-104.6].
A `?`-chain in a `for` condition or post statement, with the `?` inside a chain's expression lambda body, MUST run on every iteration: the loop lowers to an infinite loop evaluating the hoisted computation at the top and the narrowed post at the bottom of each pass, with `continue` reaching the post computation [T-104.6].
A `?`-chain in a `switch` tag, with the `?` inside a chain's expression lambda body, MUST run once before any guard [T-104.6].
A `?`-chain in a case guard, with the `?` inside a chain's expression lambda body, MUST run only when earlier guards miss, in order: the switch lowers to sequential guarded ifs over one tag temporary, with multi-guard cases joined by temporaries, a done flag keeping order, and `fallthrough` setting a flag tested by later conditions [T-104.6].
A `?`-chain in a `go { }` child MUST return to the group with only the error result, never the enclosing function's results [T-206].
A `?`-chain nested in a lambda body, in a `??` fallback, or in a `?.` call argument MUST fail with `correctness::nested_try_capture`: hoisting it to the enclosing function would unbind its captured parameters or run it when the source skips it [T-104.6].
When produced elements implement `io.Closer`, partial failure MUST close already produced elements in reverse order [T-104.6].
A newline followed by spaces or tabs then `.` plus a letter or `_` MUST insert no semicolon in `.lgo` [T-104.6].
```go
func big(xs []int) []int {
	return xs.filter(x => x > 0).map(x => 2 * x).take(5)
}
```
A function whose single result is `iter.Seq[T]` or `iter.Seq2[K, V]` MUST be a generator function [T-104.8].
`yield e` MUST be a statement, allowed only directly inside a generator function [T-104.8][T-0.2].
A `yield` elsewhere MUST fail with `correctness::yield_outside_generator` [T-104.8].
A generator MUST supply one value per `yield` for `iter.Seq[T]` and two values per `yield` for `iter.Seq2[K, V]` [T-104.8].
Each yielded value MUST be assignable to its element type [T-104.8].
A generator body MUST lower to returning an iterator closure with each `yield e` becoming `if !_logos_yield(e) { return }`, so an early `break` in the consumer stops the generator with no further body running [T-104.8].
A generator body MUST need no `return`: falling off the end ends the sequence [T-104.8].
A `return` with values inside a generator MUST act as a bare `return`: the values are discarded [T-104.8].
```go
func Count(n int) iter.Seq[int] {
	for i := range n {
		yield i
	}
}
```
Lambdas MUST allow expression and block forms with known target types. [T-104.6]
A ? in an expression lambda MUST propagate from the enclosing function. [T-104.6]
A bare return that a shadowed named error result would change MUST fail as upstream Go reports it. errdefer MUST run on panic, Goexit, or non-nil error. [T-104.2]
Fused take(5) loops MUST run map functions at most 5 times. [T-104.6][T-102]
Side effects before sortBy MUST warn suspicious::side_effect_before_sort. [T-104.6][T-102]
Interpolation MUST evaluate each operand once, left to right, and MUST convert error and fmt.Stringer operands to strings before sizing. [T-104.4]
The bound MUST count those strings, so the lowering uses one allocation and the buffer MUST never regrow. [T-104.4]
Chains MUST treat lambda parameters as const views. [T-104.6]
`defer f()?` with nil result MUST leave the error result nil with no allocation. [T-104.2]
With a prior error it MUST join both errors. [T-104.2]
Interpolation operands MUST evaluate once from left to right. [T-104.4]
Errdefer MUST work in functions with unnamed error results. [T-104.2]
Float sums MUST use fixed pairwise order with no implicit contraction. [T-104.6]
Chains MUST fuse into one loop. [T-104.6]
The receiver MUST evaluate once before the loop. [T-104.6]
Errdefer MUST run on panic and Goexit exits. [T-104.2]
A panic during return evaluation MUST count as failure. [T-104.2]
Errdefer MUST stay skipped on success returns. [T-104.2]
`errdefer` MUST run when the frame holds a non-nil error, panics, or exits through Goexit. [T-104.2]
For `errdefer f()?`, a rollback error MUST join the primary error with errors join semantics, as `defer f()?` does. [T-104.2]
A nil primary error MUST take the rollback error as is. [T-104.2]
Each `{expr}` MUST bind to a local variable before sizing. [T-104.4]
`defer f()?` MUST run in place on the standard LIFO stack. [T-104.2]
Partial chain failure MUST close already produced `io.Closer` elements in reverse order. [T-104.6]
The fused loop MUST run that rollback before returning. [T-104.6]
It MUST perform no full struct copy. [T-104.5]
User braces MUST print literally. [T-104.4]
NaN keys MUST order deterministically. [T-104.6]

### 1.5 RFC-105: Optimization tiers
`logos build` and `go build` MUST accept `-O=0`, `-O=1`, `-O=2`, `-O=3`, and `-O=s` [T-105][R-U1].
A level MUST apply to every package of the build, Go or loGo/s [T-105][R-U1].
Under `logos`, the default level MUST come from `logos.toml [build] opt_level` with default `O2` [T-105].
Under plain `go`, the default level MUST be `O1` [T-105][R-U1].
`[build] opt_level` MUST accept `O0`, `O1`, `O2`, `O3`, and `Os` only [T-105].
A bare spelling such as `2` MUST fail validation like an unknown level [T-105].
An invalid level MUST fail with text that lists the five accepted levels [T-105].
O0 MUST pass `-N -l` for no inline and no debug-info loss protection [T-105].
O1 MUST be the upstream baseline [T-105].
O2 MUST set inline budget 120 and variable-make stack buffer 256 bytes for non-standard packages [T-105].
O3 MUST set inline budget 400 with 80 inside recursive cycles, and variable-make stack buffer 4096 bytes for non-standard packages [T-105].
A function in a recursive call-graph cycle MUST keep the upstream variable-make buffer of 32 bytes at every level, inlined bodies included [T-105][T-302].
A standard-library-provenance package (a standard library package or its external test package) MUST keep the default variable-make buffer of 32 bytes at every opt level; non-standard packages MUST use 256 bytes at O2 and 4096 bytes at O3 [T-105][T-302].
Os MUST set inline budget 15 [T-105].
O3 MUST add no escape-analysis or prove-pass work budget [T-105].
`target_cpu` MUST accept `deterministic`, `baseline`, or `native` [T-105].
`target_cpu` MUST apply to every package that `logos` builds, Go or loGo/s [T-105][R-U1].
Plain `go` MUST read no `target_cpu` and MUST keep the upstream `GOAMD64` and `GOARM64` defaults [T-105][R-U1].
`deterministic` MUST set GOAMD64 v3 and GOARM64 v8.2 unless the environment already sets them [T-105].
`baseline` MUST set GOAMD64 v1 and GOARM64 v8.0 [T-105].
`native` MUST pick the host level [T-105].
The default binary MUST NOT run on x86 CPUs older than v3 [T-105].
Self-aliased calls such as arr = Reverse(arr) MUST return correct results. [T-105]
The deterministic target MUST select GOAMD64=v3 and GOARM64=v8.2 unless the environment overrides them. [T-105]
Host-specific selection MUST require `target_cpu = "native"`. [T-105]
O3 MUST set inline budget 400 with budget 80 inside recursive cycles. [T-105][T-302]
Recursive functions MUST NOT inline above that budget. [T-105][T-302]
A function in a recursive cycle MUST keep the upstream 32-byte variable-make buffer, so a helper inlined into it adds no tier buffer to its frame. [T-105][T-302]

## 2. Tier 2 specification

This section defines the loGo/s type system: compile-time constants, pointers and options, unions and property interfaces, tuples and fixed-array operators, comptime expansion, and group concurrency.

### 2.1 RFC-201: Const composites, CTFE, const views, and const maps

A `const` declaration MUST accept a composite literal (a struct, an array, a slice, or a map) and a call to a `const func`. [T-201]

```lgo
const primes = []int{2, 3, 5}
const httpPort = 8080
```

A `const func` MUST NOT perform I/O, spawn a goroutine, read a mutable package variable, call `recover`, convert a pointer to `uintptr`, range over a dynamic map, or use a pointer. [T-201]

The compiler MUST evaluate `const` composites and `const func` calls at compile time with target integer widths, a step limit from `max_ctfe_steps` with default 100,000, and a value cap of 1 MB. [T-201][T-102]

A run that exceeds the selected `max_ctfe_steps` limit MUST fail with `[comptime::ctfe_step_limit]`. [T-201][T-102]

Configuration MAY raise `max_ctfe_steps` above its default. [T-102]


A constant value above 1 MB MUST fail with `[comptime::const_too_large]`. [T-201]

A `const func` that breaks a purity rule MUST fail with `[correctness::const_func_impure]` naming the reason. [T-201]

A compile-time evaluation that overflows the target width, inserts a NaN or duplicate map key, executes a panic, or reaches a form the interpreter does not support MUST fail with `[correctness::const_eval]` naming the cause. [T-201]

Pointer-free constant data MUST live in read-only memory. [T-201]

Constant data with pointers MUST live in read-only memory, so a write to it faults. [T-201]

A write to a constant composite through `unsafe` MUST fault at run time. [T-201]

`reflect.DeepEqual` and `fmt.Println` MUST accept constant composites. [T-201]

A package with a 1,000-key constant map MUST add no heap bytes at init, with its table symbol in read-only memory. [T-201]

See Sec 7 for const map lowering, iteration, and float keys.

`const []T`, `const *T`, and `const map[K]V` MUST form a qualifier checked at compile time and erased before code generation. [T-201]

A Go importer MUST see a constant view as its plain type: `const []T` as `[]T`, `const *T` as `*T`, and `const map[K]V` as `map[K]V`. [T-201][T-Sec5][R-U2]

A Go importer MUST see an exported `const` composite as a package-level variable of its plain type and a `const func` as a plain function. [T-201][T-Sec5][R-U2]

A write from Go into such a variable lies outside the loGo/s contract. The Go compiler does not report it, and the write MAY fault at run time because the data can sit in read-only memory. This is a documented hole. [T-201][T-Sec5][R-U2][A-GOBOUNDARY]

An index assignment through a constant view MUST fail with `[correctness::const_view_mutation]` naming the operation and the expression. [T-201]

A field assignment through `const *T` MUST fail with the same diagnostic. [T-201]

`append` to, `copy` into, `delete` from, and `clear` of a constant view MUST fail with the same diagnostic. [T-201]

A slice expression with a bound above `len` (such as `b[:cap(b)]`) MUST fail with the same diagnostic, with a run-time check when the bound is not constant. [T-201]

A call to a method with a pointer receiver that lacks `const` MUST fail with the same diagnostic. [T-201]

A method with a `const` pointer receiver (such as `func (r const *T) M()`) MUST be allowed. [T-201]

An interface method written `const M() T` (such as `const Name() string`) MUST be a readonly method obligation, distinct from a property element `const F T` (such as `const Name string`). [T-201]

The `const` on an interface method MUST NOT declare an accessor property and MUST NOT lower to `Logos_Get` or `Logos_Set` methods. [T-201]

A concrete type MUST satisfy an interface readonly method only with a method proven readonly; an unknown or mutating effect MUST fail satisfaction with `[correctness::const_view_mutation]` naming the method. [T-201]

A trusted imported `FnFact.Readonly` fact for the concrete method MUST count as the proof; an absent or old fact MUST NOT prove readonly. [T-201][T-Sec5]

Satisfaction MUST NOT depend on declaration order; a local proof finalized after method bodies MUST give the same legality, and the compiler MUST emit no code before the final satisfaction errors. [T-201]

Interface identity MUST distinguish the obligation: an ordinary interface with the same method name and signature MUST NOT acquire the readonly obligation, and an ordinary method set MUST NOT satisfy a readonly requirement without the proof above. [T-201]

A value of an interface with a readonly method MUST be assignable to the same method set without the obligation; the reverse direction MUST require the proof above. [T-201]

A dynamic call through a constant view to a method the interface declares `const` MUST be allowed. [T-201]

A dynamic call through a constant view to a method the interface does not declare `const` MUST fail with `[correctness::const_view_mutation]`. [T-201]

The explicit `const` receiver rule and the inference for ordinary concrete value receivers MUST stay unchanged. [T-201]

Receiver readonlyness MUST NOT imply parameter purity: a `FnFact.Readonly` fact proves only the receiver obligation, and a readonly-only or assertion-only fact MUST NOT prove that arguments are unmodified. An absent `ParamMutated` summary MUST be treated as unknown, distinct from an explicit proven-zero `ParamMutated=0` summary. [T-201][T-Sec5]

Ordinary generic assertion definitions, including `func As[T any](x any) T { return x.(T) }` and assertion methods on generic receivers, MUST remain legal. The compiler MUST record their assertion and type-switch effects and validate relevant function and receiver-type instantiations, including transitive wrappers and cross-package uses, independently of declaration order. An instantiation MUST NOT manufacture a readonly obligation erased from runtime interface identity; an unsafe instantiation MUST fail with `[correctness::const_view_mutation]`. [T-201][T-Sec5]

Qualified type arguments in assertion-relevant positions MUST require verified effects. Unknown effects MUST be conservative, not proven zero; rejection MUST be limited to arguments whose assertion could manufacture an erased readonly obligation. Ordinary concrete named types and nominal nesting MUST remain legal. Known-zero generics such as `Identity[Qualified]` and storage-only containers such as `StoreBox[Qualified]` MUST remain legal; those names alone MUST NOT exempt an imported generic whose effects are unknown. [T-201][T-Sec5]

Assertion-effect masks for function and receiver type parameters and mutation masks for value parameters MUST support arbitrary arity; positions at or above 64 MUST NOT be truncated into a false proof of safety. [T-201][T-Sec5]

A `const []I`, `const *I`, or `const map[K]I` where `I` is an interface MUST remain allowed as a container qualifier; container covariance MUST NOT imply method readonlyness and method readonlyness MUST NOT ban interface containers. [T-201]

Passing a constant view to a non-constant parameter of an `.lgo` function MUST fail with the same diagnostic. [T-201]

A constant view that reaches a Go-mode parameter (the standard library or any Go package) is checked by the caller-side `correctness::const_view_mutation` diagnostic; the compiler stores no per-parameter `ParamMutated` gate for Go-mode parameters outside the standard-library table of Sec 7. [T-201][R-U2]

Assigning `const []*T` to `[]*T` MUST fail with `[correctness::const_view_escape]`. [T-201]

A reference-free constant view MUST be assignable to its plain type. [T-201]

A plain value MUST be assignable to its constant view. [T-201]

A `for _, v := range` loop over a constant slice MUST yield constant elements. [T-201]

Converting `string` to `const []byte` MUST be zero-copy. [T-201]

Converting `const []byte` to `string` MUST be zero-copy when provenance is a string or a constant composite. [T-201]

The same conversion MUST copy for any other provenance. [T-201]

`logos/rt.Freeze` MUST deep-copy its argument and return a `const` view. [T-201]

A later write to the source (or to a child object it points to) MUST NOT change the frozen view. [T-201]

`Freeze` MUST panic on a reachable non-nil `func`, `chan`, or `unsafe.Pointer`. It cannot isolate their referenced state. [T-201]

A nil `func`, `chan`, or `unsafe.Pointer` MUST remain valid and copy as nil. [T-201]

Reachability MUST include map keys and values, interface payloads, and slice capacity tails. [T-201]
String to const byte views MUST allocate nothing. [T-201]
Views of other provenance to string MUST copy. [T-201]
Ordinary reslicing such as s = s[:0] MUST stay allowed. [T-201]
Only const views MUST enforce the len bound. [T-201]
Dynamic map iteration in const funcs MUST fail compilation. [T-201][T-Sec7]
Const map range MUST visit keys in sorted order. [T-201][T-Sec7]
A violation MUST fail with correctness::const_func_impure naming the reason. [T-201]
reflect.DeepEqual and fmt.Println MUST accept constant composites. [T-201]
Compile-time evaluation MUST stop after the selected `max_ctfe_steps` limit with default 100,000 steps with `comptime::ctfe_step_limit`. [T-201][T-102]
A value above 1 MB MUST fail with `comptime::const_too_large`. [T-201][T-102]
A call through a const view to a pointer-receiver method that lacks const MUST fail with correctness::const_view_mutation. [T-201]
A method with a const pointer receiver MUST allow the call. [T-201]
Zero-copy to string MUST apply only with string or const composite provenance. [T-201]
All other provenances MUST copy. [T-201]
A bound above len MUST fail with `correctness::const_view_mutation`. [T-201]
Non-constant bounds MUST trap at run time. [T-201]
`reflect.DeepEqual` MUST accept constant composites. [T-201]
The compiler MUST evaluate CTFE integers with target widths. [T-201]
A later write to the source or its child MUST NOT change the view. [T-201]
Nil leaves MUST copy as nil. [T-201]
`const []byte` to `string` MUST be zero-copy only from string or const-composite provenance. [T-201]
Any other provenance MUST copy to the Go heap. [T-201]
Pointer-free data MUST live in read-only memory before any init runs. [T-201]
Ordinary generic assertion definitions MUST remain legal. [T-201][T-Sec5]
Function and receiver-type instantiations, including wrappers and cross-package uses, MUST require verified effects before manufacturing qualified targets; unsafe instantiations MUST fail with `correctness::const_view_mutation`. [T-201][T-Sec5]
Unknown effects MUST NOT count as proven zero. [T-201][T-Sec5]
Concrete named arguments and known-zero identity/storage containers MUST remain legal. [T-201][T-Sec5]
Assertion and mutation masks MUST NOT truncate parameter positions at or above 64, and receiver-readonly MUST NOT prove parameter purity. [T-201][T-Sec5]

### 2.2 RFC-202: Non-nil pointers, option types, and nil flow

`*T` in `.lgo` MUST be non-nil. [T-202]

`*T?` MUST denote an optional pointer. [T-202]

Assigning `nil` to `*T` MUST fail with `[correctness::nil_to_nonnil]`. [T-202]

`var p *T` MUST fail compilation. [T-202]

An array type with non-nil element pointers (such as `[3]*T`) MUST fail without initializers. [T-202]

`make([]*T, n)` with a non-constant `n` or a constant above 0 MUST fail compilation. [T-202]

`make([]*T, 0, n)` MUST be allowed. [T-202]

`new(S)`, `var s S`, and `S{}` MUST fail when `S` has a non-nil pointer field without an initializer, with `[correctness::nonnil_uninitialized]` naming the field and its type. [T-202]

Zero-clearing through `*p = S{}` MUST fail with the same diagnostic. [T-202]

`var t T` for a type parameter MUST be allowed. [T-202]

Pointers from Go-mode declarations (the standard library, every other Go package, and cgo-generated declarations) MUST be platform pointers. [T-202][A-STD][R-U2]

A `.lgo` file MAY import any Go package, and each declaration that it imports MUST be a platform declaration. [R-U2]

A type-parameter value instantiated with a non-nil pointer MUST also be a platform pointer. [T-202]

Dereferencing a platform pointer MUST be allowed. [T-202]

Comparing a platform pointer with `nil` MUST be allowed. [T-202]

A store of a platform value into a declared non-nil location MUST wrap the stored expression in `logos/rt.MustNonNil` with the source position. [T-202]

A nil value at such a store MUST panic with `logos: nil from Go value stored into non-nil *T at <file>:<line>`. [T-202]

`if x != nil` MUST narrow `x` from `T?` to `T` in the guarded block. [T-202]

An early exit of the form `if x == nil { return }` (also with `panic`, `break`, or `continue`) MUST narrow `x` after the statement. [T-202]

An `&&` chain with a nil test MUST narrow `x` in the rest of the chain. [T-202]

A later assignment MUST end narrowing. [T-202]

Taking the address of `x` MUST disable narrowing for `x`. [T-202]

Assigning `x` in a closure MUST disable narrowing for `x`. [T-202]

A nil test on a struct field MUST NOT narrow the field. [T-202]

Code MUST copy the field to a local first. [T-202]

For a callee declared in `.lgo`, reading a result before the `err == nil` proof MUST fail with `[correctness::result_before_error_check]`. [T-202]

A Go-mode callee (the standard library or any Go package, such as `io.Reader.Read`) MUST keep its own contract. [T-202][R-U2]

`nil` MUST be assignable to every `T?`. [T-202]

`T` MUST be assignable to `T?`. [T-202]

`T?` MUST NOT be assignable to `T`. [T-202]

`*T?` where `*T` is non-nil MUST lower to `*T` with nil as none and no extra storage. [T-202]

When nil is a valid payload of `T`, `T?` MUST carry explicit presence separate from payload nil. [T-202]

An interface-backed union option with no nil-capable member MUST use the interface nil value for absence. [T-202]

For every other `T` outside those two cases, `T?` MUST lower to `logos/rt.Option[T]`. [T-202]

A present nil payload MUST read as some with nil payload, distinct from none. [T-202]

`(T?)?` MUST lower to `logos/rt.Option[T?]`. [T-202]

The spelling `T??` MUST be a syntax error reporting `T?? is not allowed; use (T?)?`. [T-202]

`?.` on a non-option value MUST fail with `[correctness::not_an_option]`. [T-104.3]

`??` with a non-option left side MUST fail with the same diagnostic. [T-104.3]

A method call on an unnarrowed `T?` MUST fail with `[correctness::nil_deref_unproven]`. [T-202]

`??` MUST evaluate its right side only when the left side is none. [T-104.3]

`?.` MUST evaluate its receiver once. [T-104.3]

`?.` over a field that is already an option MUST flatten the result. [T-104.3]

See Sec 1.4 for `??` grouping, precedence, and single-value map reads.

`m[k] ?? d` MUST type as `V`. [T-104.3]

`v, ok := m[k]` MUST keep Go types. [T-104.3]

An absent key MUST read as none. [T-104.3]

A present zero value MUST read as some. [T-104.3]

A present nil slice payload MUST read as some with nil payload, distinct from none. [T-104.3][T-202]

Ejected and Go-facing signatures MUST use plain pointers and `rt.Option[T]` from the support module. [T-202][T-Sec5][R-U2]

A Go importer MUST see a non-nil `*T` and a `*T?` as `*T`, and MUST see every other `T?` as the lowered type of the rules above. [T-202][T-Sec5][R-U2]

```lgo
func greet(u *User?) string {
    return u?.Name ?? "anon"
}
```
The pointer type `*T` in `.lgo` code MUST mean non-nil [T-202].
The type `*T?` MUST mean an optional pointer [T-202].
A value of type `T` MUST convert to `T?`, and `nil` MUST convert to every `T?` [T-202].
A value of type `T?` MUST NOT convert to `T` without a check [T-202].
For `*T?` where `*T` is non-nil, the ejected Go type MUST be `*T` with `nil` as none [T-202][T-0.3].
An interface-backed union option with no nil-capable member MUST use the interface nil value for absence in ejected code [T-202][T-0.3].
For every other `T?` outside those two cases, the ejected Go type MUST be `logos/rt.Option[T]` [T-202][T-0.3].
When nil is a valid payload, the ejected form MUST carry explicit presence separate from payload nil [T-202][T-0.3].
A present nil payload MUST stay distinguishable from none in ejected code [T-202][T-0.3].
Ejected signatures MUST use plain pointers and `Option[T]` from the shared support module [T-202][T-0.3].
A Go importer MUST see the same Go types [T-202][T-Sec5][R-U2].
Zero values of non-nil pointers MUST fail compilation, including `var p *T`, arrays of non-nil pointers, `make` of a non-nil pointer slice with length above zero, and composite literals that leave a non-nil field empty [T-202].
Stores of `nil` into non-nil locations MUST fail compilation [T-202].
Narrowing after `x != nil`, after an early exit on `x == nil`, and through `&&` chains MUST apply to locals and parameters until their next assignment [T-202].
Narrowing MUST NOT apply to fields, to variables whose address escapes, or to variables that a closure assigns [T-202].
The error-first guard MUST apply only to callees declared in `.lgo` files, so standard-library and Go-package calls such as `io.Reader.Read` MUST keep their own contracts [T-202][A-STD][R-U2].
Pointers from Go-mode declarations and from type parameters instantiated with non-nil pointers MUST act as platform pointers: dereference and nil comparison MUST compile, and a store into a non-nil location MUST check at run time and panic with the `.lgo` file and line on nil [T-202][A-STD].
The operators `?.` and `??` MUST operate only on option types [T-104.3].
A single-value map read `m[k]` MUST type as `V?`, while `v, ok := m[k]` MUST keep Go meaning [T-104.3].
Zero values of non-nil pointers MUST fail with correctness::nonnil_uninitialized. [T-202][T-Sec12]
Absent map and JSON values MUST check through V? and MissingFieldError. [T-202][T-Sec12]
An absent key MUST read as none while a present nil payload MUST read as some. [T-202][T-Sec12]
Pointers from Go declarations, in the standard library and in every other Go package, MUST act as platform pointers. [T-202][R-U2]
Stores into non-nil locations MUST panic with logos: nil from Go value at the source line. [T-202][R-U2]
Block scoping MUST stay standard Go. [T-202]
Outer variables MUST narrow after if x == nil return guards. [T-202]
Reads of .lgo results before err == nil proof MUST fail with correctness::result_before_error_check. [T-202]
Standard library callees MUST keep their own contracts. [T-202]
An option whose payload can be nil MUST carry explicit presence as specified in Sec 2.2. [T-202]
Non-nilable value options and nested options MUST use `logos/rt.Option[T]` as specified in Sec 2.2. [T-202]
Zero-clearing through `*p = S{}` MUST fail with `correctness::nonnil_uninitialized`. [T-202]
A literal MUST initialize every non-nil field. [T-202]
The spelling `T??` MUST fail as a syntax error. [T-202]
`(T?)?` MUST lower to `Option[T?]`. [T-202]
An ordinary method call on an option without a non-nil proof MUST fail with `[correctness::nil_deref_unproven]`. [T-202]
For callees declared in .lgo the compiler MUST fail reads before the err proof with [correctness::result_before_error_check]. [T-202]
Standard-library callees MUST keep their own contract, so use of n first stays allowed. [T-202]

### 2.3 RFC-203: Closed unions, property interfaces, and inline assertions

A closed union MUST be an interface whose type set is a finite union of non-`~` terms. [T-203.1]

It MAY also declare methods that every member implements. [T-203.1]

A closed union MUST be allowed in value positions (variables, parameters, results, fields, elements, keys, and values). [T-203.1]

A union with `~` terms or an unbounded set MUST fail with `[correctness::open_union_value]`. [T-203.1]

A conversion MUST be allowed when the source type is a member or a closed union of members. [T-203.1]

Any other conversion MUST fail with `[correctness::union_nonmember]` naming the type. [T-203.1]

A comparison between a union and a constant MUST convert the constant to its default type and require membership. [T-203.1]

An assertion or case type outside the member set MUST fail with `[correctness::union_nonmember]`. [T-203.1]

A comma-ok assertion on a non-member MUST yield `ok == false`. [T-203.1]

A single-value assertion on a non-member MUST panic with `interface conversion: <T> is not a member of <pkg>.<U>`. [T-203.1]

`case U:` MUST match only members. [T-203.1]

`nil` MUST go to `case nil`. [T-203.1]

An exhaustive `switch x.(type)` MUST cover every member and `nil`, or carry `default`. [T-203.1]

A missing member MUST fail with `[correctness::nonexhaustive_switch]` listing misses in declaration order. [T-203.1]

A case naming a sub-union MUST cover its members. [T-203.1]

A switch expression MUST lower to an immediately invoked function literal. [T-203.1]

Its result type MUST be the common type of the arms (identical types, the default type of untyped constants, or `T` when other arms are `nil`). [T-203.1]

Mismatched arms MUST fail with `[correctness::switch_arm_mismatch]`. [T-203.1]

A type switch expression without full coverage MUST fail with `nonexhaustive_switch`. [T-203.1]

A value switch expression without `default` MUST fail with `[correctness::switch_expr_needs_default]`. [T-203.1]

The switched value MUST evaluate once. [T-203.1]

Only the chosen arm MUST evaluate. [T-203.1]

The run-time representation MUST be the upstream interface. [T-203.1]

A two-tier tag layout MUST NOT be used. [T-203.1]

Reflection MAY store non-members. [T-203.1]

The compiler MUST NOT check this case. [T-203.1]

A closed-union value MUST marshal through its member static helper. [T-203.1]

Unmarshalling MUST try members in declaration order into fresh scratch values. [T-203.1]

It MUST assign the target only on a single success. [T-203.1]

Otherwise it MUST return an ambiguity error naming the union and the members. [T-203.1]

An ejected value-used union MUST keep its methods and carry the comment `// loGo/s union: A | B`. [T-203.1]

A Go importer MUST see a value-used union as that method-only interface. [T-203.1][T-Sec5][R-U2]

A union used both as a constraint and as a value type MUST fail eject with `logos eject: union U is used as a type constraint and as a value type; Go cannot express both`. [T-203.1]

Such a union has no Go-facing form, and a Go import of it MUST fail with `logos build: union U is used as a type constraint and as a value type; Go cannot express both`. [T-203.1][R-U2]

```lgo
type U interface {
    int | string
}
func f(u U) int {
    return switch u.(type) {
    case int => 1
    case string => 2
    case nil => 0
    }
}
```

A property interface is an interface with at least one `const F T` or `F T` element.

A struct MUST satisfy a property interface only through an explicit `: I` declaration in any package. [T-203.2][R-U4]

For each property the declaring package MUST generate `Logos_GetF() T` from an exported field or from a method `F() T`. [T-203.2]

For each read-write property it MUST also generate `Logos_SetF(T)` from the field or from `SetF(T)`. [T-203.2]

A field-sourced getter MUST use a value receiver. [T-203.2]

A field-sourced setter MUST use a pointer receiver. [T-203.2]

A method-sourced accessor MUST keep the receiver kind of the forwarded method. [T-203.2]

Property elements MUST lower to these interface methods. [T-203.2]

A Go importer MUST see a property interface as the interface of its `Logos_GetF` and `Logos_SetF` accessor methods. [T-203.2][T-Sec5][R-U2]

A dynamic assertion such as `any(x).(I)` MUST work. [T-203.2]

Identifiers starting with `Logos_` or `_logos_` MUST be reserved in `.lgo`. [T-203.2]

A use of such an identifier MUST fail with `[correctness::reserved_identifier]`. [T-203.2]

A read-write property MUST require a pointer type. [T-203.2]

Using a value type for one MUST fail with `[correctness::property_needs_pointer]`. [T-203.2]

Taking the address of a property (`&c.HP`) MUST fail with `[correctness::property_address]`. [T-203.2]

A `: I` declaration without a field or method source for a property MUST fail with `[correctness::property_unsatisfied]`. [T-203.2]

The right fields without the declaration MUST fail with `[correctness::property_requires_conformance]` telling the writer to add `: I`. [T-203.2]

Conflicting property types across listed interfaces MUST fail with `[correctness::property_conflict]`. [T-203.2]

A non-struct type MUST NOT satisfy a property interface. [T-203.2]

A compound update through an interface MUST evaluate the receiver once and write the value back. [T-203.2]

A method call on a property value MUST use read-call-write-back around the call. [T-203.2]

Only a type with a `: I` list MUST gain accessor methods. [T-203.2]

`reflect` MUST count the generated accessors. [T-203.2]

A type without the declaration MUST see no method change. [T-203.2]

Embedded fields MUST satisfy properties by Go selector depth rules. [T-203.2]

An ambiguous equal-depth selector MUST fail as an ambiguous selector. [T-203.2]

Read-only and read-write declarations of one name in one interface MUST merge into read-write. [T-203.2]

```lgo
type Creature interface {
    const Name string
    HP int
    const Start() error
}
type Monster struct : Creature {
    Name string
    HP int
}
func (m Monster) Start() error {
    return nil
}
```

The `Monster.Start` value receiver above MUST satisfy the `const Start` obligation by the unchanged value-receiver inference, without an explicit `const` receiver. [T-201]

The `const Start` method obligation MUST stay distinct from the `const Name` property element: the method MUST NOT gain accessor methods and MUST NOT count as a property. [T-201][T-203.2]

`type S struct : I, J { ... }` MUST assert that `*S` implements each listed interface at the declaration. [T-203.3]

A missing method MUST fail with `[correctness::assertion_failed]` naming the method. [T-203.3]

Each assertion MUST emit a compile-time check that `*S` implements each listed interface. [T-203.3]

An assertion list on a non-struct type MUST be a syntax error. [T-203.3]
A closed union MUST be an interface whose type set is a finite union of non-`~` terms, and it MAY declare methods that every member implements [T-203.1].
Closed unions MUST be allowed in value positions in `.lgo` code [T-203.1].
Membership MUST be checked for assignments, conversions, comparisons with constants, assertions, and case types [T-203.1].
The forms `x.(U)` and `case U:` MUST check membership at run time [T-203.1].
An exhaustive `switch x.(type)` MUST cover every member and `nil` or hold `default` [T-203.1].
A switch expression MUST evaluate its operand once and only its chosen arm [T-203.1].
The result type of a switch expression MUST be the common type of its arms [T-203.1].
Ejected code for a value-used union MUST be a plain method-only interface plus a comment that names the member set [T-203.1][T-0.3].
A Go importer MUST see the same interface [T-203.1][T-Sec5][R-U2].
Reflection MAY store a non-member in a closed union, and the compiler MUST NOT check that path [T-203.1].
A readonly method `const M() T` MUST NOT count as a property element and MUST NOT satisfy a property requirement [T-201][T-203.2].
Satisfaction MUST be explicit: only a struct whose declaration lists `: I` MUST satisfy property interface `I`, in any package [T-203.2][R-U4].
For each property `F`, the declaring package MUST generate `Logos_GetF() T` and, for read-write properties, `Logos_SetF(T)` [T-203.2].
An exported field MUST source its accessors with a value-receiver getter and a pointer-receiver setter, and `F() T` or `SetF(T)` methods MUST source them by forwarding with the same receiver kind [T-203.2].
Property elements MUST lower to those interface methods, so dynamic assertions such as `any(x).(I)` MUST work [T-203.2].
A Go importer MUST see a property interface as the interface of those accessor methods [T-203.2][T-Sec5][R-U2].
Identifiers that start with `Logos_` or `_logos_` MUST remain reserved in `.lgo` code [T-203.2].
Read-write properties MUST need a pointer type [T-203.2].
The form `&c.HP` MUST fail compilation [T-203.2].
Compound updates through a property MUST evaluate the receiver once and write the result back [T-203.2].
Only types with a `: I` list MUST gain accessor methods, and `reflect` MUST count exactly those methods; types without the list MUST gain none [T-203.2][R-U4].
Non-struct types MUST NOT satisfy a property interface [T-203.2][R-U4].
The assertion `type S struct : I, J` MUST require `*S` to implement `I` and `J` at the declaration [T-203.3].
Unions MUST use the upstream interface representation. [T-203.1]
Ejected value unions MUST keep methods with a member comment. [T-203.1]
A union with `~` terms MUST fail with `correctness::open_union_value`. [T-203.1]
A closed union MUST use the upstream interface representation. [T-203.1]
A closed union MUST use the upstream interface representation, including upstream handling of zero-sized members. [T-203.1]
Switch results MUST take the common type of the arms. [T-203.1]
Union values MUST use the upstream interface representation. [T-203.1]
A `nil` union value MUST go to `case nil`. [T-203.1]
Exhaustive switches MUST cover every member and `nil`. [T-203.1]
Method bodies inside interfaces MUST fail compilation. [T-203.2]
Structs MUST satisfy property interfaces only through an explicit `: I` declaration. [T-203.2]
The package MUST generate both getter and setter for it. [T-203.2]
The write MUST reach the struct. [T-203.2]
An equal-depth match MUST fail as an ambiguous selector. [T-203.2]

### 2.4 RFC-204: Tuples and fixed-array SIMD

Tuple types `(a A, b B)` and `(A, B)` in non-result positions MUST lower to structs with exported fields (`T_a`, `T_b` / `T_0`, `T_1`). [T-204.1]

Exported field names MUST keep tuple identity across packages. [T-204.1]

A Go importer MUST see a tuple as that struct. [T-204.1][T-Sec5][R-U2]

`var t (int, error)`, `chan (int, error)`, slices of tuples, and map values with tuple types MUST be allowed. [T-204.1]

Named access such as `t.x` MUST work. [T-204.1]

Index-style access such as `t.0` MUST NOT be allowed. [T-204.1]

Code MUST use names or destructuring instead. [T-204.1]

Destructuring (`a, b := t`) MUST work. [T-204.1]

A multi-value call MUST unpack in calls and assignments. [T-204.1]

A tuple-typed variable MUST pack such values. [T-204.1]

`fmt.Println(pair())` MUST unpack. [T-204.1]

A tuple with non-comparable elements MUST NOT be comparable. [T-204.1]

```lgo
var t (id int64, err error)
a, b := t
_ = a
_ = b
```

Element-wise `+ - * / & | ^` MUST be allowed on `[N]T` with `N` a power of two, `T` an integer or floating-point kind other than complex (`int`, `uint`, and `uintptr` ride the 64-bit lane), and size at most 64 bytes. [T-204.2]

`.sum()`, `.min()`, `.max()`, and `.dot()` MUST be allowed on the same shapes; `a.dot(b)` takes one argument of `a`'s shape. [T-204.2]

A larger array MUST fail with `[correctness::simd_array_too_large]`. [T-204.2]

A non-power-of-two length MUST fail with `[correctness::simd_array_shape]`. [T-204.2]

With `GOEXPERIMENT=simd` on amd64 or arm64 the lowering MUST call `simd/archsimd` for element types that are a fixed-width numeric kind or an alias of one (`byte`, `rune`, `float32`, or `type f32 = float32`); `int`, `uint`, `uintptr`, and defined (non-alias) element types keep the scalar form, because their pointers do not match the archsimd loaders. [T-204.2]

Otherwise it MUST emit unrolled scalar code. [T-204.2]

On amd64 each array-operator vector form MUST run only when the `simd/archsimd` CPU feature check its methods need is true (`X86.AVX()` for 128-bit shapes and 256-bit float shapes, `X86.AVX2()` for 256-bit integer shapes, `X86.AVX512()` for 512-bit shapes and for 64-bit integer `*`) and MUST take the scalar form when it is false; the check MUST be omitted when the build's `GOAMD64` level guarantees the feature (`v3` for AVX and AVX2, `v4` for AVX512), and arm64 needs no check because its forms are baseline NEON. [T-204.2]

Direct ISA lowering is replaced by this two-form rule. [T-204.2]

A Go package MUST get no array operator and no reduction method from this rule. It MAY use `simd/archsimd` under `GOEXPERIMENT=simd`. [T-204.2][R-U1]

Reductions MUST use one fixed pairwise order in both forms, giving bit-identical results. [T-204.2]

Every operator and reduction MUST match a scalar loop oracle. [T-204.2]

```lgo
var a, b [4]float64
c := a + b
_ = c.sum()
```
Direct calls in multi-assignments MUST unpack. [T-204.1]
Single-variable bindings MUST pack into tuple values. [T-204.1]
A direct multi-value call in call position MUST unpack. [T-204.1]
A tuple-typed variable MUST pack the values. [T-204.1]
A tuple with slice, map, or func elements MUST fail as non-comparable. [T-204.1][T-203.1]
A closed union MUST use upstream interface equality. [T-204.1][T-203.1]
Element-wise operators MUST allow at most 64 bytes. [T-204.2]

### 2.5 RFC-205: Comptime

`const if` MUST take a constant condition. [T-205]

A non-constant condition MUST fail with `[comptime::not_constant]`. [T-205]

Both branches MUST type-check. [T-205]

Only the live branch MUST reach lowering. [T-205]

The condition MUST end at the `{` that opens the branch, so a name or `true` before `{` is the condition and not a composite literal. [T-205]

`comptime.IsStruct[T]()` MUST be a constant `const if` condition, true when the underlying type of `T` is a struct. [T-205]

Inside a comptime generic its value MUST be taken per instantiation, and a condition built from it with `!`, `&&`, `||`, `==`, `!=` and constants MUST NOT fail `[comptime::not_constant]`. [T-205]

`comptime.SizeOf[T]()`, `comptime.AlignOf[T]()`, and `comptime.HasPointers[T]()` are compile-time constants describing the size, alignment, and pointer-containing status of `T`.

A `const if` inside a comptime generic or a `const for` body MUST evaluate per instantiation or per field over `true`, `false`, constants, `comptime.IsStruct[X]()` with `X` a type argument, a type parameter or `f.Type`, and the connectives `!`, `&&`, `||`, `==`, `!=`. [T-205]

Any other non-constant condition MUST fail with `[comptime::not_constant]`, including a condition on `f.Name`, `f.Index`, `f.Exported` or the index variable. [T-205]

A `const for` in a dead `const if` branch MUST NOT fail with `[comptime::const_for_fields]`. [T-205]

`comptime.Fields[T]()`, `f.Type` used as a value, `comptime.IsStruct[T]()`, `comptime.TypeOf[X]()`, and `comptime.Error(msg)` MUST be compile-time only. `val.[f]`, `f.Name`, and `f.Tag.Get(k)` inside a `const for` body expand to per-field code; outside the body they are scope or projection errors, not run-time uses. [T-205]

Any run-time use of those compile-time-only forms MUST fail with `[comptime::runtime_use]`. A use of `val.[f]`, `f.Name`, or `f.Tag` outside a `const for` body fails with the scope or projection diagnostic. [T-205]

A generic function that uses them MUST become a comptime generic. [T-205]

Each instantiation MUST expand into a non-generic copy `<f>_logosct<N>` in the instantiating package. [T-205]

Copies MUST be memoized per type-argument list. [T-205]

An exported comptime generic MUST travel as source text in `__.LOGOS`. [T-205]

A `const for` range MUST be `comptime.Fields[T]()` over a struct type. [T-205]

Any other range operand, and a non-struct `T`, MUST fail with `[comptime::const_for_fields]`. [T-205]

Expansion MUST visit fields in declaration order. [T-205]

It MUST include exported fields and unexported fields of a current-package `T`. [T-205]

It MUST exclude unexported fields of other packages. [T-205]

`val.[f]` MUST read and write the named field. [T-205]

`val.[f]` MUST name the field variable of an enclosing `const for` and project a struct value of that loop's type. [T-205]

Any other use MUST fail with `[comptime::const_for_projection]`. [T-205]

A `const for` index variable MUST NOT be assigned. [T-205]

An assignment to it MUST fail with `[comptime::const_for_assign]`. [T-205]

`f.Tag.Get(k)` MUST constant-fold when `k` is a compile-time constant: a literal, a named constant, an imported constant, or a constant expression. A run-time `k` cannot fold and stays an ordinary call. [T-205]

A live `comptime.Error(msg)` MUST fail compilation with `msg`. [T-205]

`comptime.Error(msg)` MUST take a constant string. [T-205]

A non-constant `msg` MUST fail with `[comptime::error_not_constant]`. [T-205]

`Resolve[f.Type](c)` MUST resolve per field. [T-205]

A panic in unrolled code MUST name the `.lgo` `const for` line in its trace. [T-205]

A circular instantiation MUST fail with `[comptime::circular_instantiation]` naming the chain. [T-205]

A comptime generic MUST be instantiated with concrete type arguments. [T-205]

An instantiation with a type parameter or a call inside an expansion without explicit type arguments MUST fail with `[comptime::generic_targ]`. [T-205]

A method of a generic type MUST NOT be a comptime generic. [T-205]

A method of a generic type that uses `const for` over a type parameter MUST fail with `[comptime::generic_method]`. [T-205]

An expansion above the selected `max_comptime_ast_nodes` limit with default 4,096 nodes MUST fail with `[comptime::node_limit]`. [T-205][T-102]

Configuration MAY raise `max_comptime_ast_nodes` above its default. [T-102]

Validation and boundary tests MUST use the selected limit. [T-102]

A stack trace MUST print the base name without the `_logosct<N>` suffix. [T-205]

A shadowed `const switch` case MUST fail with `[comptime::unreachable_case]`. [T-205]

An exact case MUST rebind `T` and dependent variables in its block. [T-205]

An interface case MUST keep the concrete layout and gain the interface method set. [T-205]

`case int` MUST match `int` only. [T-205]

`case ~int` MUST match the underlying `int` (such as `UserID`). [T-205]

A `~` case whose type is not a basic type MUST fail with `[comptime::tilde_not_basic]`. [T-205]

A `const switch` tag that is not a type MUST fail with `[comptime::switch_not_type]`. [T-205]

A `const switch` case that is not a type MUST fail with `[comptime::case_not_type]`. [T-205]

A `const switch` case MUST name exactly one type. A `default` arm and a list of types MUST be syntax errors. [T-205]

An unlabeled `break` or `continue` directly in a `const switch` case MUST fail compilation, because the cases lower into generated switches. [T-205]

`[]*os.File` MUST never rebind as `[]io.Writer`. [T-205]

Cases MUST evaluate top-down in first-match order. [T-205]

Outside comptime generics a `const switch` MUST lower to a run-time type switch on `any((*T)(nil))` with `unsafe` rebinding for exact cases. [T-205]

Interface cases in that form MUST use assertion on the value. [T-205]

No match MUST return the input unchanged. [T-205]

Native and ejected output of the run-time form MUST agree. [T-205]

`comptime.ViewAs[T](b)` MUST require a POD struct (no padding, no `bool`, no pointers, interfaces, slices, maps, or strings). [T-205]

Any other shape MUST fail with `[comptime::viewas_not_pod]` naming the offset. [T-205]

A short input MUST panic with a message of the form `logos: ViewAs: need <N> bytes, have <M>` naming the sizes. [T-205]

The view MUST keep `b` alive. [T-205]

Targets without unaligned loads MUST copy to an aligned temporary. [T-205]

```lgo
const if comptime.IsStruct[T]() {
    const for _, f := range comptime.Fields[T]() {
        _ = val.[f]
    }
}
```
ViewAs MUST accept only POD structs with no padding, no bool, no pointers, interfaces, slices, maps, or strings, and MUST keep the source alive. [T-205]
Short inputs MUST panic with logos: ViewAs need-have sizes. [T-205]
An exact `const switch` case MUST lower to a run-time type switch with rebinding. [T-205]
An interface case MUST assert on the value. [T-205]
An exact case MUST rebind the value to the case type. [T-205]
A shadowed case MUST fail with `comptime::unreachable_case`. [T-205]
Expansion MUST include unexported fields only for the current package. [T-205]
The view MUST keep the backing bytes alive. [T-205]
A short input MUST panic with need and have byte counts in `logos: ViewAs` text. [T-205]
Interface cases MUST keep the concrete layout. [T-205]
They MUST gain the case interface method set. [T-205]
Exact cases MUST match only the named type. [T-205]
Tilde cases MUST match underlying types. [T-205]
Interface cases in run-time `const switch` MUST use assertion on the value. [T-205]
Native and ejected output MUST agree. [T-205]
Comptime generics MUST expand into non-generic copies in the instantiating package. [T-205]
`Resolve[f.Type](c)` MUST resolve per field in the copy. [T-205]
The check MUST track the active instantiation stack. [T-205]
Field copies MUST compare compile-time types through `f.Type`; `Resolve[f.Type](c)` resolves per field. [T-205]
A copy whose field types differ MUST fail compilation as an ordinary type mismatch. [T-205]
An expansion at or below the selected limit MUST compile. [T-205][T-102]
Both const-if branches MUST type-check. [T-205]

### 2.6 RFC-206: Go group and logos/sync

`go group (ctx) { ... }` and `go group (ctx, limit = N) { ... }` MUST lower to `logos/rt.Group`. [T-206]

The group MUST cancel on the first child error or panic. [T-206]

It MUST join all children before scope exit. [T-206]

It MUST bound concurrency with a semaphore when `limit` is present. [T-206]

`go group(x)` without a block MUST keep Go meaning as a call to a function named `group`. [T-206]

A child block MUST have type `func() error`. [T-206]

`?` inside a child MUST return to the group. [T-206]

`return` inside a child MUST fail with `[correctness::return_in_group_child]`. [T-206]

`go f(args)` inside a group body MUST form a child, sending an error-last result to the group. [T-206]

The function value and arguments MUST be evaluated in the calling goroutine before the child is submitted to the group, including when the concurrency limit would block submission. [T-206]

`go f()` outside any group body MUST fail with `[concurrency::unsupervised_goroutine]`. [T-206]

A child outside a group body MUST fail with `[correctness::child_outside_group]`. [T-206]

A `go group` statement in a function without an error result MUST fail with `[correctness::group_needs_error_result]`. [T-206]

The group error MUST propagate like `?`. [T-206]

The first failure in completion order MUST win. [T-206]

`Wait` MUST return only after all children exit. [T-206]

The context MUST be cancelled at join. [T-206]

A child panic MUST become `*logos/rt.PanicError` with the value and a stack. [T-206]

`NewGroup`, `Go`, `Wait`, and `PanicError.Error` MUST form the `Group` API (`NewGroup(ctx, limit)` returns the group and a child context; `Go` takes `func(context.Context) error`). [T-206]

A plain channel send, receive, or range directly in a child MUST lower to a `select` with `<-gctx.Done()`. [T-206]

A user `select` with `default` MUST stay unchanged. [T-206]

Cancellation MUST reach siblings blocked on channels. [T-206]

A child blocked in netpoll-backed network I/O MUST wake within 100 ms with a timeout error satisfying `os.IsTimeout`. [T-206]

A goroutine outside the group MUST stay blocked. [T-206]

Cgo calls MUST NOT wake this way. [T-206]

```lgo
func f(ctx context.Context) error {
    go group (ctx, limit = 8) {
        go { work()? }
    }
    return nil
}
```

`logos/sync.Locked[T]` MUST expose `WithLock(f func(*T))`. [T-206]

`WithLock` MUST hold `Lock`, defer `Unlock`, then call the function. [T-206]

It MUST unlock after a panic. [T-206]

A `WithLock` lambda MUST NOT return or store its argument. [T-206]

`logos/sync.Hot[T]` MUST expose `Load() const *T`, `Store(*T)`, and `Update(func(old const *T) *T)` with a CAS loop. [T-206]

Concurrent updates MUST never expose a partial value. [T-206]





No retire list MUST exist. [T-206]

Values MUST stay GC-managed. [T-206]

```lgo
var c Locked[Config]
c.WithLock(p => { p.Port = 8080 })
```
`Hot[T]` MUST hold its value through `atomic.Pointer[T]` only. [T-206]

## 3. Tier 3 specification

A rule that sits behind a `GOEXPERIMENT` flag MUST stay behind it as this section states [T-0.1][R-U1].

### 3.1 RFC-301: Tier-0 arena and placement regions

Substrate [T-301.1]:
Region memory MUST come from 64 KB chunks leased by the goroutine that holds region objects; each open region holds at most its own chain. [T-301.1]
A chunk MUST hold a noscan region that grows up and a scan region that grows down. [T-301.1]
An allocation that would cross the two cursors MUST link a fresh chunk and MUST still succeed. [T-301.1]
Scan-region objects MUST carry a type word, and the collector MUST scan them with the goroutine stack. [T-301.1]
Popped scan memory MUST be zeroed before reuse. [T-301.1]
After a release, a collection MUST find no stale pointers. [T-301.1]
Pointer-containing region objects MUST keep their heap referents alive across `runtime.GC()`. [T-301.1]
A goroutine parked with empty regions MUST hold no chunk. [T-301.1]
`runtime.SetFinalizer` and `runtime.AddCleanup` on a region object MUST panic with `runtime: <API> on loGo/s arena object`, naming the called function. [T-301.1]
Cgo pointer checks MUST NOT panic on region addresses. [T-301.1]
Region placement MUST NOT change program meaning: every program MUST print the same output when every region allocation is a heap allocation. [T-301.1]

Implicit placement [T-301.1][R-U1]:
With implicit placement on, an object that escape analysis proves does not outlive its frame, but that cannot live on the stack, MUST allocate in the frame's region. [T-301.1]
Frame exit MUST release that region. [T-301.1]
A returned value that the caller keeps MUST allocate in the caller's region only when escape analysis proves no pointer to it outlives the caller. [T-301.1]
A loop back-edge MUST release iteration-local objects. [T-301.1]
Implicit placement MUST stay off unless the build sets `GOEXPERIMENT=logosarena`. [T-301.1]
Under `logos`, `[build] arena` MUST set that experiment, and its default MUST be true. [T-301.1][R-U1]
Under plain `go`, only `GOEXPERIMENT=logosarena` MUST enable it. [T-301.1][R-U1]

Placement regions [T-301.2]:
The std package `logos/arena` MUST provide `Open() Region`, `New[T any](r Region) *T`, `Make[T any](r Region, n int) []T`, and `Append[T any](r Region, s []T, v ...T) []T`. [T-301.2]
Without `GOEXPERIMENT=logosarena`, these MUST behave exactly as `Region{}`, `new(T)`, `make([]T, n)`, and `append(s, v...)`. [T-301.2]
Under `logos` with `[build] arena` true, each call MUST resolve at compile time, per type argument, to the noscan region when `comptime.HasPointers[T]()` would be false, and to the scan region otherwise. [T-301.2]
Constant-size noscan objects of at most 512 bytes MUST use the inline bump sequence on amd64 in non-race builds; other targets call the region slow path. [T-301.2]
`Open` MUST make a region owned by the innermost enclosing block. [T-301.2]
Exit from that block MUST release the region's objects, and inside a loop body each iteration MUST release them. [T-301.2]
An `Open` in an `if`, `switch`, or `for` init clause MUST belong to the whole statement, and its close MUST run after the statement. [T-301.2]
The compiler MUST place an object in `r` only when escape analysis proves that no pointer to it outlives the owner block of `r`. [T-301.2]
Otherwise the object MUST go on the heap, with `perf::arena_fallback` naming the call and the escaping flow. [T-301.2]
A store of a region pointer into a region object whose owner block is the same as, or nested inside, the owner block of the stored pointer's region MUST NOT count as an escape. A store into an object of an enclosing region MUST count as an escape. [T-301.2]
In a `.lgo` package, a `Region` that is stored outside a local variable, returned, sent on a channel, captured by a `go` statement, or captured by an escaping closure MUST fail with `correctness::region_escape`. [T-301.2]
In a Go package, passing an `r` to a callee is allowed. With `GOEXPERIMENT=logosarena` placement through that region follows the same escape rule as in a `.lgo` package: the compiler places the object in the region only when escape analysis proves no pointer to it outlives the owner block of `r`, otherwise the object goes on the heap; a use that would fail with `correctness::region_escape` in a `.lgo` package falls back to the heap instead. Under the `logos` driver that fallback is reported as `perf::arena_fallback`, naming the call and the escaping flow; plain `go` reports no rule ID. Without the experiment every `logos/arena` call behaves as its heap equivalent. [T-301.2][R-U1]
A `go group` child MAY read a parent-region object through a const view. [T-301.2]
A non-const capture MUST count as an escape of that object. [T-301.2]
`logos eject` MUST lower `arena.Open()` to `_logos_rt.Region{}`, `arena.New[T](r)` to `new(T)`, `arena.Make[T](r, n)` to `make([]T, n)`, and `arena.Append[T](r, s, v...)` to `append(s, v...)`. [T-301.2]
`logos/rt` MUST provide `type Region = arena.Region`. [T-301.2]
The eject support module MUST carry the `arena` package. [T-301.2]
The eject MUST append `_ = r` after each local Region declaration. [T-301.2]
The toolchain MUST NOT ship the upstream `arena` package, `reflect.ArenaNew`, or `GOEXPERIMENT=arenas`. [T-301.2]

Comptime and escape analysis:
Escape analysis MUST record a parameter that leaks only into the pointee of another parameter. [T-301.2][R-U1]
This precision MUST apply to `.lgo` packages, and to Go packages under `GOEXPERIMENT=logosescape`, which is off by default. [T-301.2][R-U1]


[T-301.1] Recursion MUST return arena-allocated results to the caller intact.
[T-301.1] When a goroutine parks, it MUST release every chunk whose noscan and scan regions are both empty, including empty chunks behind a live chunk.
[T-301.1] Idle parked goroutines MUST consume 0 bytes of arena memory.
It MUST retain every live chunk and preserve all live-object addresses. [T-301.1]
With no live chunks, it MUST retain no arena capacity; its ordinary stack and runtime metadata still exist. [T-301.1]
Frame exit, including exit by panic, MUST restore the watermark. [T-301.1]
A loop with only iteration-local allocation MUST NOT grow arena use across iterations. [T-301.1]

### 3.2 RFC-302: Smart stack engine

The variable-size `make` stack buffer is 32 bytes at O0 and O1, and under plain `go` without `-O`. At O2 it is 256 bytes and at O3 it is 4096 bytes for non-standard packages; a `logos build` with no `-O` flag uses `logos.toml [build] opt_level`, which defaults to O2. Standard-library-provenance packages and functions in a recursive call-graph cycle keep the 32-byte buffer at every opt level. [T-302]
A slice `make` with variable effective capacity that flows into a cgo call argument or an `unsafe.Pointer` conversion MUST NOT use the stack buffer and MUST allocate its backing storage on the heap. Effective capacity is the explicit capacity when present, otherwise the length; it is fixed only when the compiler proves it constant with `ir.IsSmallIntConst` after literal rewriting. Fixed-effective-capacity makes MUST retain ordinary escape and size behavior, not a guarantee of stack allocation. [T-302]
Under `GOEXPERIMENT=logosstack`, the runtime MUST choose the initial goroutine stack size from the per-PC exponential moving average of final stack sizes. [T-302]
Without history for a PC, it MUST keep the current stack size. [T-302]

### 3.3 RFC-303: Hardware backend optimizations

Fault-suppressing masked tails MUST appear only in loops that the slice reductions of Sec 13 produce. [T-303]
Other code MUST carry no tail padding. [T-303]
BMI2 and `CMOV` code MUST come from existing `GOAMD64=v3` rules. [T-303]
Constant-count rotates MUST use BMI2 rotates at `GOAMD64=v3`. [T-303]
Variable-count rotates MUST stay on the existing shift and rotate path. [T-303]
In `.lgo`, non-constant `*` inside a `make` length or capacity MUST lower to `logos/rt.MulLen`. [T-303]
On overflow, `MulLen` MUST panic with `logos: make length overflow: w * h`. [T-303]
LSE atomics MUST come from the `GOARM64=v8.2` default. [T-303]
Cache-line spacing MUST keep the upstream per-GOARCH `CacheLinePadSize` values (64 on amd64, 128 on arm64). [T-303]
The runtime structs MUST carry no new padding. [T-303]
Preemption checks in long loops MUST fire every 4096 iterations, with the first check at iteration 4096, on GOARCH=wasm builds with the checks enabled. An explicit GOEXPERIMENT=preemptibleloops elsewhere yields the upstream per-backedge checks. [T-303]
They MUST run only where async preemption is absent (`GOARCH=wasm`). [T-303]
A call-free loop MUST NOT starve peers. [T-303]
A peer goroutine MUST observe one quantum at 4096 iterations and two at 8192 iterations. [T-303]
`clear(b)` MUST reach a runtime memory clear. [T-303]
Dead-store elimination MUST NOT remove that clear. [T-303]
The rest of the WASM profile is defined in SPEC Sec 12. [T-Sec12]
Go 1.27 lowering already keeps `clear(b)` as a runtime memory clear; the test MUST pin its survival before return. [T-303]

### 3.4 RFC-304: Fast-CGo engine

A loGo/s package MUST accept cgo files beside other `.lgo` files. [T-304][A-CODEGEN]
A file that imports `C` MUST use Go syntax only. [T-304][A-CODEGEN]
LoGo/s syntax in such a file MUST fail with `[correctness::cgo_file_logos_syntax] files that import "C" must use Go syntax only`. [T-304][A-CODEGEN]
Pointers that C returns MUST count as platform pointers. [T-202]
Storing one into a non-nil location MUST check it. [T-202]
When the fast path is on, a C function that carries both `#cgo noescape` and `#cgo nocallback` MUST take the fast call path. [T-304][R-U1]
This rule MUST hold in Go packages under `logos` and under plain `go`, and in loGo/s packages under `logos`. [T-304][R-U1]
Go code MUST need no new directive and no new syntax for the fast path. [T-304][R-U1]
A C function that carries only one of the two directives, or none, MUST keep the upstream call path. [T-304]
`//logos:cgo_fastcall` MUST be the `.lgo` spelling of the same pair and MUST mark a cgo call `noescape` and `nocallback`. [T-304]
One fast path MUST serve Go packages and loGo/s packages: the single `runtime.logosCgocallFast` trampoline and the one cgo pipeline of this section. [T-304][R-U1]
The fast path MUST use the syscall-style `reentersyscall`/`asmcgocall`/`exitsyscall` sequence, so it may release the P like a syscall; fast-call diagnostics stay on the original M; no deterministic timing gate is provided. [T-304]
It MUST NOT read a clock in the fast body: no per-call monotonic read takes part, because that read dominated the measured fast path and no consumer read the value. [T-304]
The fast path MUST stay behind `GOEXPERIMENT=logosfastcgo`, which is off by default under both commands. [T-304][R-U1]
Profiling with `SIGPROF` during fast calls MUST NOT crash. [T-304]
Fast calls MUST carry noescape and nocallback marks. [T-304]
A C function marked `nocallback` that calls into Go MUST still panic on the fast path. It MUST NOT hang and MUST NOT corrupt state. [T-304][R-U1]
A CPU profile MUST attribute time in a fast-called C function to its Go call site. [T-304][R-U1]
Go pointer arguments MUST stay valid during a fast call. [T-304]
The original M holds diagnostics only: `m.fastCgoFrame[0]` records the DI/arg address copy; record also zero-stores slots 1 to 3 so a reused M carries no stale addresses, and clear zeroes all four. The slots are plain `uintptr`, not pins or roots. [T-304]
The fast-call state MUST read zero after return without clearing the caller's argument values. [T-304]
POD and primitive-slice arguments MUST skip `_cgoCheckPointer`. [T-304]

Implementation note (informative): Fast calls preserve `runtime.NumCgoCall` accounting and reset the cgo traceback buffer.
The original M owns the diagnostic slots, which read zero after return, as the fast-call state rule above states, even when sysmon retakes its P.
Normal return and rejected callbacks clear this record before leaving syscall state.
The slots contain address copies, not GC roots or pins.
The ordinary cgo pointer-lifetime rules still apply.
With `GOEXPERIMENT=logosfastcgo`, eligible result wrappers omit stack-top lookups and result-frame relocation.
The trusted `nocallback` contract and the runtime's syscall stack rules keep that frame stable.
Disabled, unmarked, and single-directive calls retain ordinary relocation.
Result stores, sanitizer operations, and callback guards are unchanged.
These implementation changes do not establish the speed or blocking gates above.

#### 3.4.1 Shared-library export usage (informative)

This subsection is informative and adds no requirements.
The [shared-library fixture](../src/cmd/go/testdata/script/logos_cgo_shared.txt) demonstrates C and Python calls on Linux.
Its `main` package contains a Logos core and a separate Go-syntax `.lgo` bridge.
The bridge imports `C` and declares the `//export` functions.

Enable cgo in `logos.toml`:

```toml
[build]
cgo = "allow"
```

Build the library with `logos build -buildmode=c-shared -o liblogos.so ./lib`.
The build also produces `liblogos.h`.
C callers include that generated header.
Python callers use the same ABI through `ctypes`, with explicit `argtypes` and `restype`.
The library includes the managed Go runtime; it is not runtime-free.

The fixture uses fixed-width integers, example status codes, and borrowed pointer-plus-length buffers copied before use.
Opaque integer handles backed by `runtime/cgo.Handle` retain Go objects without exposing Go pointers.
The caller supplies valid buffer ranges and aligned, writable output slots.
Selected exports recover Go panics and return status codes; this does not provide recovery from every fault.
Concurrent reads use an immutable object.
The caller joins all reader threads before deleting its handle.
The fixture's `-buildvcs=false` isolates the test from VCS metadata; it is not an FFI requirement.

## 4. Lexer, parser, and syntax

### 4.1 Tokens and contextual words

The language MUST add no new global keywords [T-0.2]. The words `errdefer`, `yield`, `scope`, and `group` MUST act as contextual words valid only in `.lgo` files [T-0.2]. The word `errdefer` MUST start a statement only when an identifier or `func` follows it [T-0.2][T-104.2]. Inside a function whose results are `iter.Seq` or `iter.Seq2`, `yield` MUST start a statement; elsewhere `yield e` still parses as a statement and the type checker rejects it with `correctness::yield_outside_generator` [T-0.2]. The word `scope` MUST appear only directly before `defer` [T-0.2]. The word `group` MUST appear only directly after `go` [T-0.2]. The word `const` MUST gain new positions before `for`, `if`, `switch`, and `func`, and those positions MUST remain syntax errors in plain Go [T-0.2]. The word `const` MUST also appear before an interface method declaration to mark a readonly obligation, and only there outside the listed positions; that method position MUST remain a syntax error in plain Go [T-0.2][T-201]. Code MUST NOT declare `errdefer`, `yield`, `scope`, or `group` as ordinary names in those positions [T-0.2]. Outside those positions and outside `yield e` statements, each word MUST keep its ordinary identifier meaning, so `errdefer := 1` MUST parse as an assignment [T-0.2][T-104.2]. The form `?` MUST scan as a postfix operator only in `.lgo` files [T-104.1]. The form `?.` MUST scan as one unit only when `?` joins `.` with no gap [T-104.3]. The form `??` MUST scan as one binary operator only when two `?` marks join with no gap [T-104.3]. The form `=>` MUST scan as one unit only when `=` joins `>` with no gap in `.lgo` files [T-104.6]. The form `$"..."` MUST scan as an interpolated string only when `$` joins an opening quote with no gap in `.lgo` files [T-104.4].

### 4.2 Grammar

New forms MUST follow this grammar [T-0.2][T-104.1][T-104.3][T-104.4][T-104.6][T-201][T-202][T-203.1][T-203.2][T-203.3]:

```
PostfixTry   ::= Operand "?"
OptionType   ::= Type "?"
Coalesce     ::= OrExpr ["??" Coalesce]
OptChain     ::= Operand ("?." IdentOrCall)*
Interp       ::= "$" QuotedStringWithBraces
Lambda       ::= ParamList "=>" (Expr | Block)
ChainCall    ::= SliceExpr ("." ChainMethod parens)*
TupleType    ::= "(" [Name] Type ("," [Name] Type)+ ")"
TupleLit     ::= "(" Expr ("," Expr)+ ")"
SwitchExpr   ::= "switch" [Init ";"] [Tag] "{" Arm+ "}"
Arm          ::= "case" ExprList "=>" Expr | "default" "=>" Expr
UnionDecl    ::= "type" Name "interface" "{" UnionTerms [";"] Methods [";"] "}"
UnionTerms   ::= Term ("|" Term)*
AssertDecl   ::= "type" Name "struct" ":" InterfaceList "{" Fields "}"
PropertyElem ::= ["const"] Name Type
ReadonlyMethod ::= "const" Name Signature
ConstView    ::= "const" ("[]" Type | "*" Type | "map" "[" Type "]" Type)
ConstDecl    ::= "const" Name "=" (CompositeLit | ConstFuncCall)
ConstIf      ::= "const" "if" ConstExpr Block ["else" (Block | ConstIf)]
ConstFor     ::= "const" "for" [Index ","] Field ":=" "range" ComptimeFieldsCall Block
FieldProj    ::= Value ".[" Field "]"
ConstSwitch  ::= "const" "switch" TypeParam "{" ConstCase+ "}"
DeferTry     ::= "defer" Call "?"
ErrDefer     ::= "errdefer" (Call | Call "?" | FuncLitCall)
ScopeDefer   ::= "scope" "defer" Call
GoGroup      ::= "go" "group" "(" Expr ["," "limit" "=" Expr] ")" Block
GroupChild   ::= "go" "{" Stmts "}"
YieldStmt    ::= "yield" Expr
SpreadElem   ::= ".." Expr
DefaultField ::= Field Name Type "=" ConstExpr [Tag]
```

The type `T??` MUST remain a syntax error [T-202]. The type `(T?)?` MUST nest as an option of an option [T-202]. The union in `UnionDecl` MUST list a finite set of terms with no `~` term when used as a value type; with a `~` term it is a constraint only [T-203.1]. A struct MUST satisfy a property interface only through an explicit `: I` list [T-203.2][R-U4]. A `ReadonlyMethod` MUST appear only as an interface method; `const Name() string` MUST be a readonly method obligation and MUST stay distinct from property `const Name string` [T-201][T-203.2]. Outside the listed positions, `const` keeps its ordinary Go meaning, and other positions remain syntax errors as in plain Go [T-201]. Operands inside `$"..."` are checked by `LogosInterpKindOf`; values outside its accepted kinds fail with `correctness::interp_type` [T-104.4]. The texts `{{` and `}}` MUST mean literal braces [T-104.4].

```lgo
v := parse(s)?
f()?
defer closeAll()?
errdefer tx.Rollback()?
type S struct : Creature {
    Name string
    HP   int
}
```

### 4.3 Disambiguation and semicolon insertion

An opening `(` MUST branch on the next token after its element list [T-104.6]. A `:` after the first element MUST mean a named tuple type [T-204.1]. A `,` list followed by `=>` MUST mean a lambda, and each element MUST be a plain name [T-104.6]. A lone name followed by `=>` MUST mean a one-parameter lambda [T-104.6]. A lone `(e)` with no `=>` after it MUST mean a parenthesized expression [T-104.6]. The token `??` MUST bind less tightly than `||`, so `a || b ?? c` MUST parse as `(a || b) ?? c` [T-104.3]. Chained `??` MUST group to the right, so `a ?? b ?? c` MUST parse as `a ?? (b ?? c)` [T-104.3]. A newline after `?` MUST end the statement [T-104.1]. A newline after `?.` MUST NOT end the statement [T-104.3]. A newline followed by spaces or tabs and then `.` plus a letter or `_` MUST NOT insert a semicolon [T-104.6]. The form `x` newline `.5` MUST still insert a semicolon [T-104.6].

## 5. Cross-package facts

Cross-package facts MUST travel in the archive member `__.LOGOS` [T-Sec5]. Exported declaration signatures remain in `__.PKGDEF`; `__.LOGOS` carries fingerprint-bound typed fact records and no declaration header. [T-Sec5][T-201] The member MUST hold one fact line per function for `ReceiverMutated`, the `ParamMutated` mask, `FnFact.Readonly`, `MayYield`, `AlwaysNilError`, and generic assertion effects `AssertTP` and `AssertRTP` [T-Sec5][T-201]. A function carrying the `AlwaysNilError` flag MUST return a nil error on every return path [T-Sec5][T-102]. Facts MUST use format version 3; older members MUST be rejected rather than read through a compatibility path [T-Sec5][T-201]. Presence of assertion-effect flags MUST distinguish analyzed effects from unknown effects; explicit `AssertTP=0,AssertRTP=0` MUST denote proven-zero effects [T-Sec5][T-201]. Assertion and mutation masks MUST use arbitrary-arity hexadecimal masks without a 64-parameter limit [T-Sec5][T-201]. Encode and decode MUST preserve `FnFact.Readonly` [T-201][T-Sec5]. The facts MUST bind to the `__.PKGDEF` fingerprint, and a stale fingerprint MUST fail the dependent build [T-Sec5]. A Go importer of a loGo/s package MUST see the signatures that `logos eject` emits: a closed union value type as its methods-only interface with the terms dropped, `*T?` as the bare pointer, a non-nil `*T` as a plain `*T`, a property interface as its `Logos_GetF` and `Logos_SetF` accessor methods, a readonly interface method as its plain method, and a const view as its plain type [T-Sec5][R-U2][T-201]. Lowering and eject MUST erase only the `const` keyword of a readonly method for emitted Go, keeping source positions and qualification facts for loGo/s importers [T-201][T-0.3][T-Sec5]. A Go package's member MUST carry one `lang go` record and no declaration header [T-Sec5][R-U1][T-201]. An importer MUST NOT read a package that carries the `lang go` record as a loGo/s package: no non-nil pointer check, no const-view mutation fact, and no overlay applies to it [T-Sec5][R-U1]. Assertion effects are emitted for Go and `.lgo` packages without an opt-level or GOARCH gate in the facts writer; command coverage stays as tested [T-Sec5][T-201]. A Go package with assertion effects writes `__.LOGOS` under the emission condition its writer tests pin for Go-package facts [T-Sec5][T-201]. Importers MUST consume assertion effects from `lang go` members [T-Sec5][R-U1]. Any written Go-package member carries one `lang go` record; whether a member is written follows the writer gate [T-Sec5][T-Sec12]. When this toolchain compiles a Go package that calls a loGo/s function or method, the compiler MUST insert a `logos/rt.MustNonNil` check for each non-nil pointer parameter of the callee in a direct call, in a `go` statement, and in a `defer` statement, using the callee facts from `__.LOGOS` [T-Sec5][R-U2]. A call through a function value, a method value, or an interface has no such check; this is a documented hole [T-Sec5][R-U2][A-GOBOUNDARY]. A nil receiver that a Go caller passes to a method with a non-nil pointer receiver has no such check either; this is a documented hole [T-Sec5][R-U2][A-GOBOUNDARY]. Code that an upstream toolchain builds from eject output has no such check either; this is a documented hole, like reflection and `unsafe` [T-Sec5][R-U2][A-GOBOUNDARY]. A Go mutation through a const view and a Go store of a non-member into a closed union are documented holes, and the compiler does not check them [T-Sec5][R-U2][A-GOBOUNDARY].
Cross-package facts MUST travel in `__.LOGOS` with a fingerprint. [T-Sec5]
When this toolchain compiles the Go caller, it MUST insert `logos/rt.MustNonNil` and MUST panic with the call position. [T-Sec5]

## 6. Eject and error-handling semantics


See Sec 1.4 for error propagation, cleanup, and traces.
See Sec 0.3 for eject output, dependencies, and toolchain requirements.
Ejected output MUST be gofmt-clean. [T-0.3]

```lgo
func Transfer(db *DB, from int64, to int64, amt int64) (int64, error) {
    tx := db.Begin()?
    errdefer tx.Rollback()?
    defer tx.Close()?
    bal := tx.AddBalance(from, -amt)?
    tx.AddBalance(to, amt)?
    tx.Commit()?
    return bal, nil
}
```

## 7. Const maps

### 7 Const map semantics

A const map with up to 8 keys MUST lower to a `switch` lookup [T-Sec7]. A const map with more keys MUST lower to a static perfect-hash table in read-only data plus a generated lookup function [T-Sec7]. A lookup MUST perform constant-time auxiliary metadata reads plus exactly one final key/value-slot probe with full-key validation [T-Sec7]. Construction MUST use at most 1,000,000 seed-candidate trials in total per const map [T-Sec7]. Exhaustion MUST fail with `comptime::constmap_seed_not_found` [T-Sec7]. A logarithmic fallback MUST NOT satisfy the contract [T-Sec7]. The 1, 8, 9, and 10,000-key cases in `test/logos/run/const_map.lgo` MUST be the acceptance check [T-Sec7]. That check MUST NOT promise success for every key set [T-Sec7]. A lookup of an absent key MUST return none [T-Sec7]. Iteration with `range` MUST visit keys in sorted order [T-Sec7]. The function `len` on a const map MUST be constant [T-Sec7]. Use of a const map as a `map[K]V` value MUST materialize a regular map once [T-Sec7]. The materialized map is a copy; writes to it do not change the const map's reads [T-Sec7]. A const map key MUST be a string, number, bool, or a struct or array of those; any other key type fails with `comptime::constmap_key_type` [T-Sec7]. A const map passes to a standard-library parameter only when that parameter is read-only: the compiler reads the ParamMutated mask from a fixed table of standard-library functions, because standard-library packages carry no facts, and any other imported function rejects the argument with `correctness::const_view_mutation` [T-201][T-Sec7]. A const map passes to a parameter of a function of the same package only when that function is a const func, which evaluates on copies of its arguments; any other function of the package rejects the argument with `correctness::const_view_mutation` [T-201][T-Sec7]. A `-0.0` float key MUST canonicalize, and a `NaN` key MUST fail compilation [T-201][T-Sec7]. The table symbol MUST sit in read-only data and package init MUST add no heap bytes [T-Sec7].

```lgo
const mimeMap = map[string]string{
    "txt": "text/plain",
    "html": "text/html",
}
v := mimeMap[k] ?? "unknown"
```

## 8. Configuration schema

Earlier drafts named this file bettergo.toml. [T-Sec8]

### 8.1 File location and parsing

The file `logos.toml` MUST sit next to `go.mod` in the main module. [T-Sec8]
Plain `go` MUST read no `logos.toml`. [T-Sec8][R-U1]
The `[build]`, `[lints]`, and `[capabilities]` tables MUST apply to modules of both kinds under `logos`. [T-Sec8][R-U1]
When the file is absent, the tool MUST use the default for every key. [T-Sec8]
The parser MUST accept tables. [T-Sec8]
The parser MUST accept key and value pairs, basic strings, literal strings, integers, booleans, string arrays, and hash comments. [T-Sec8]
Inputs larger than 1 MB MUST fail parsing. [T-Sec8]
The parser MUST reject unknown tables and unknown keys. [T-Sec8]
Each error MUST report file, line, and column as logos.toml:<line>:<col>: <message>. [T-Sec8]

### 8.2 Project table

The project table holds strict_files. [T-Sec8]
strict_files is a string array. [T-Sec8]
strict_files lists the loGo/s extensions that the module enables, and its default is all four. [T-Sec8][T-101.1]
A source file whose loGo/s extension is not listed MUST fail with `[correctness::extension_not_enabled] <file>: extension <ext> is not enabled by logos.toml strict_files`. [T-Sec8][T-101.1]
Each entry MUST name one of the four source extensions: .lgo, U+1F4DC, U+26A1, or the gopher extension U+0295 U+25D4 U+03D6 U+25D4 U+0294. [T-Sec8][T-101.1]

### 8.3 Build table

cgo is a string with default "forbid" for a loGo/s main module. [T-Sec8][T-101.2]
It MUST have no effect in a Go main module. [T-101.2][R-U1]
It MUST be one of "forbid", "restricted", or "allow". [T-101.2]
With "forbid", a package that imports C fails with restriction::cgo_usage. [T-101.2]
With "restricted", logos enables cgo. [T-101.2]
With "restricted", each package that imports C MUST match cgo_allowlist. [T-101.2]
A package outside the list fails with restriction::cgo_usage. [T-101.2]
cgo_allowlist is a string array of path globs with default empty. [T-Sec8][T-101.2]
A double star matches any suffix. [T-Sec8][T-101.2]
An example entry is "example.com/c-sqlite/**". [A-EXAMPLES][T-101.2]
An explicit CGO_ENABLED value in the environment MUST win over the default only when the `cgo` key is absent [T-101.2]. It MUST NOT override an explicit `forbid` or a `restricted` allowlist refusal [T-101.2].
When cgo is on, CC is unset, and no C compiler is present, logos and plain go MUST use zig cc with the build target for every cgo package, Go or loGo/s, if zig is on PATH. [T-101.2][R-U1] The build target is the GOOS/GOARCH pair of the build, not the host. [T-101.2][R-U1]
With cgo off, net and os/user follow the upstream CGO_ENABLED=0 behavior. [T-101.2]
opt_level is a string with default "O2". [T-Sec8][T-105]
See Sec 1.5 for accepted `opt_level` values.
A `-O=<level>` flag on `logos build` MUST override the key. [T-105]
target_cpu is a string with default "deterministic". [T-Sec8][T-105]
It MUST be one of "deterministic", "baseline", or "native". [T-Sec8][T-105]
With "deterministic", logos sets GOAMD64 to v3 and GOARM64 to v8.2 unless the environment already sets them. [T-105]
With "native", logos uses the host level. [T-105]
See Sec 1.5 for the default binary CPU requirement.
arena is a boolean with default true. [T-Sec8][T-301.1]
A true value sets `GOEXPERIMENT=logosarena`, which enables implicit placement and the placement regions for Go packages and loGo/s packages under `logos`. [T-Sec8][T-301.1][R-U1]

### 8.4 Capabilities table

The capabilities table holds four severity keys and four allowlist keys. [T-Sec8]
transitive_reflect is a string with default "warn". [T-Sec8]
transitive_exec, transitive_net, and transitive_unsafe are strings with default "deny". [T-Sec8]
Each severity key MUST be one of "deny", "warn", or "allow". [T-Sec8]
reflect_allowlist, transitive_exec_allowlist, transitive_net_allowlist, and transitive_unsafe_allowlist are string arrays of path globs with default empty. [T-Sec8]
An entry MAY name a Go module or a loGo/s module. [T-Sec8][R-U2]
Example entries use module paths such as "example.com/proto-runtime/**". [A-EXAMPLES][T-Sec8]
logos enforces the policy for every module in the build. [T-Sec8][T-Sec11]

### 8.5 Lint tables

The lints table holds one severity key per group: correctness, concurrency, slop, complexity, perf, style, suspicious, portability, and pedantic. [T-Sec8][T-102]
Each value MUST be one of "forbid", "deny", "warn", or "allow". [T-Sec8][T-102]
correctness and concurrency default to forbid. [T-Sec8][T-102]
slop and complexity default to deny. [T-Sec8][T-102]
perf, style, suspicious, and portability default to warn. [T-Sec8][T-102]
pedantic defaults to allow. [T-Sec8][T-102]
forbid cannot be lowered and ignores allow directives in a loGo/s package. [T-102]
deny fails the build unless a logos allow directive for the rule precedes the statement or declaration. [T-102]
warn prints a diagnostic. [T-102]
allow stays silent. [T-102]
The --pedantic flag overrides the pedantic key for logos build and logos lint, but it MUST NOT lower a stricter configured severity. [T-Sec8]
The lints.thresholds table holds six integer keys. [T-Sec8]
Defaults are 3 for max_nesting_depth, 15 for max_cognitive_complexity, 60 for max_function_lines, 4 for max_function_params, 4096 for max_comptime_ast_nodes, and 100000 for max_ctfe_steps. [T-Sec8]
Configuration MAY raise max_comptime_ast_nodes and max_ctfe_steps above their defaults. [T-Sec8][T-102]
Validation and boundary tests MUST use the selected limits. [T-Sec8][T-102]
`logos lint` lints Go packages with the language-neutral rules of Sec 1.2 under these severities, with the cap that the next line states. [T-102][R-U1]
A language-neutral rule whose default severity is forbid runs at deny for Go packages, and `//logos:allow(<rule>)` silences it there. [T-102][R-U1]
`logos build` runs no default lints on Go packages; an author `//logos:zeroalloc` directive is the Sec 17.2 exception. [T-102][R-U1]
Third-party modules of either kind get no code lints. [T-102][R-U1]

## 9. End-to-end example

The normative example is test/logos/example/order_service.lgo. [T-Sec9]
It runs ProcessBatch against a fake database and ships an expected-output file. [T-Sec9]
The file uses only valid syntax, including leading-dot chains, block lambdas, and pointer assertion targets. [T-Sec9]
logos eject reproduces the same output from the ejected module. [T-Sec9]
The build steps are in IMPLEMENTATION.md, milestone M22. [T-Sec9]

## 10. Static serializer

### 10.1 Static marshaling

For each .lgo struct type with json tags, defaults, or non-nil pointer fields and no embedded fields, the lowering MUST generate a value-receiver MarshalJSON method. [T-Sec10]
Field kinds without static support skip generation for that type. [T-Sec10]
These kinds are interfaces, maps with non-string keys, and embedded structs. [T-Sec10]
Generated output MUST be byte-identical to encoding/json for the same value under both `goexperiment.jsonv2` and `!goexperiment.jsonv2`. [T-Sec10]
The default build MUST use the fork baseline with `goexperiment.jsonv2` on. [T-Sec10]
In both configurations, json.Marshal on an eligible value MUST return the same bytes as the generated method. [T-Sec10]
No call site is rewritten. [T-Sec10]
The generator emits fixed key literals first, computes a size bound, then fills one buffer from a single make. [T-Sec10]
The buffer is returned to the caller, so it escapes to the heap. [T-Sec10]
No size threshold and no caller buffer apply. [T-Sec10]
The fast path calls no reflect. [T-Sec10]
Only unmodeled fields fall back to reflective json.Marshal per field. [T-Sec10]
Concurrent mutation during marshal is a data race under Go's memory model; the generated body adds no synchronization. [T-Sec10]
Escaping and float formatting MUST follow encoding/json under the active `goexperiment.jsonv2` setting with portable Go and no SIMD requirement. [T-Sec10]
Supported tag options are name, omitempty, omitzero, string, and -. [T-Sec10]
HTML characters, U+2028, U+2029, invalid UTF-8, and float exponents encode per encoding/json. [T-Sec10]
NaN and infinities report UnsupportedValueError. [T-Sec10]
A closed-union value marshals to its dynamic member static bytes. [T-Sec10][T-203.1]
A comptime generic that marshals over comptime fields expands once per instantiation. [T-205]
Each instantiation carries its own static helpers. [T-205]

### 10.2 Static unmarshaling

For each eligible struct, the lowering generates pointer-receiver UnmarshalJSON. [T-Sec10]
Decoding fills defaults first, then parses. [T-Sec10]
A _logos_seen mask sized [(K+63)/64]uint64 tracks matched keys against a required-mask constant, where K is the number of required non-nil fields of the struct. [T-Sec10]
At the closing brace, a missing required non-nil field returns MissingFieldError naming type and field. [T-Sec10]
JSON null for a non-nil field returns the same error. [T-Sec10]
Keys MUST dispatch by switch up to 8 keys and by the Sec 7 const-map table above that, with constant-time auxiliary metadata reads plus exactly one final key/value-slot probe with full-key validation. [T-Sec10][T-Sec7]
Unknown fields are ignored. [T-Sec10]
Duplicate keys keep the last value. [T-Sec10]
Arrays grow incrementally with no pre-allocation cap. [T-Sec10]
Decoding through json.Decoder uses the same method. [T-Sec10]
No validate tags are needed. [T-Sec10]
Unmarshal of a closed union tries members in declaration order into fresh scratch values and counts successes. [T-Sec10][T-203.1]
A keyed member trial MUST count a success only when at least one key in the object is modeled by that member and its decode then succeeds. A key modeled by any sibling member but not this one disqualifies this member trial, while keys no member models are ignored. Keys MUST decide every keyed member trial, including a member that declares its own UnmarshalJSON. Such a decoder MUST run only after its keys match. A member with no static key set MUST be tried only when no keyed member key occurs in the input. Exactly one such member MUST succeed for the union to select it. A closed union member without an UnmarshalJSON method MUST fail with [correctness::union_member_no_json]. [T-Sec10][T-203.1]
It assigns the target only on a single success. [T-Sec10][T-203.1]
Ambiguity returns an error naming the union and the members. [T-Sec10][T-203.1]
Failed trials leave the target untouched. [T-Sec10][T-203.1]

### 10.3 Binary encoding

For each JSON-eligible struct whose every JSON-visible field has a binary representation, the lowering generates MarshalBinary, UnmarshalBinary, and AppendBinary. [T-Sec10.1]
They implement encoding.BinaryMarshaler, encoding.BinaryUnmarshaler, and encoding.BinaryAppender. [T-Sec10.1]
Output starts with version byte 0x01, then fields in declaration order. [T-Sec10.1]
All integer kinds encode as 8 bytes little-endian of the 64-bit value. [T-Sec10.1]
float32 encodes as 4 bytes little-endian of Float32bits. [T-Sec10.1]
float64 encodes as 8 bytes little-endian of Float64bits. [T-Sec10.1]
bool MUST encode as 0x00 or 0x01. [T-Sec10.1]
string and []byte encode as uvarint length plus raw bytes. [T-Sec10.1]
Slice and array encode as uvarint count plus elements. [T-Sec10.1]
Maps with string keys encode as uvarint count plus entries sorted by key. [T-Sec10.1]
Nested structs encode inline. [T-Sec10.1]
Optional values MUST encode as 0x00 when absent or 0x01 plus payload when present. [T-Sec10.1]
A field of type int or uint in a struct with a JSON-tagged field warns portability::serialized_unsized_int in .lgo packages. [T-Sec10.1]
Sized kinds are preferred. [T-Sec10.1]
A tag `-` excludes the field from binary output; other tag options change nothing. [T-Sec10.1]
UnmarshalBinary rejects truncation, bad presence bytes, version mismatch, and trailing bytes with errors naming field and offset. [T-Sec10.1]
For elements with a nonzero wire payload, a count header MUST pre-allocate at most min(len(rest)/2, 1 MB / sizeof(elem)) elements and MUST grow past that only as input bytes arrive. [T-Sec10.1]
Charge nothing against a total budget on the count header; a count whose element bytes are all present in the input is lawful and MUST decode, even when the total exceeds 1 MiB. [T-Sec10.1]
For slices whose elements have zero wire payload, the count itself MUST NOT exceed 1,048,576 (the 1 MiB prealloc bound at unit 1); larger counts MUST be rejected with a count-out-of-range error, encoder symmetric, checked before allocation or element iteration. One count MUST NOT allocate more than 64 MiB of element storage in a single allocation. [T-Sec10.1]
Zero-wire slices MUST be handled in constant time with respect to count, without per-element iteration. [T-Sec10.1]
These resource bounds MUST NOT change the wire bytes of accepted values. [T-Sec10.1]

## 11. Capabilities auditor

### 11.1 Sink symbols

After deadcode, the linker MUST run a reachability audit from main.main and init functions. [T-Sec11]
The audit MUST run only in `logos` builds and MUST cover modules of both kinds. [T-Sec11][R-U1]
The audit follows relocations. [T-Sec11]
It adds synthetic edges for interface calls and reflective method lookups. [T-Sec11]
Protected sinks are reflect.ValueOf, reflect.TypeOf, reflect.New, syscall.Exec, syscall.ForkExec, syscall.StartProcess, os.StartProcess, os.startProcess, net.sysSocket, syscall.Socket, syscall.Connect, runtime.cgocall, and runtime.logosCgocallFast. [T-Sec11]
Packages that import unsafe are found by walking the cmd/go load graph. [T-Sec11]
The standard library, first-party packages under a main module, and the program's own packages are exempt. [T-Sec11]
A dependency is not the standard library because its module path has no dot. [T-Sec11]
A dependency that imports a sink package but never calls it passes as dead code. [T-Sec11]
Reflection through reflect.Value.Method counts as reachable. [T-Sec11]
On diamond graphs the report keeps the shortest chain. [T-Sec11]
The audit reports one chain per sink and dependency, so a chain through the standard library alone never hides another dependency's chain to the same sink. [T-Sec11]
A chain blames the dependency frame nearest the sink, and chains of standard-library or first-party frames blame nobody. [T-Sec11]
Policy comes from logos.toml capabilities. [T-Sec8][T-Sec11]

### 11.2 Reachability report

A violation at severity `deny` aborts linking with ERROR capabilities::unauthorized_exec naming the dependency and the sink. A `warn` violation prints a warning. An `allow` violation stays silent. [T-Sec11]
The report lists the shortest call chain with file and line per frame. [T-Sec11]
For the allowlist keys that authorize each sink, see Sec 8.4. [T-Sec8][T-Sec11]
Unsafe violations use capabilities::unauthorized_unsafe with the shortest import chain. [T-Sec11]
cgo reachability reports capabilities::unauthorized_cgo unless the cgo policy allows the package. [T-Sec11][T-101.2]
Only a dependency that owns the cgo wrapper is blamed: the standard library's own cgo never blames the dependencies that call it. [T-Sec11]
The tailored cgo sink is the per-call-site `pkg._Cfunc_*` wrapper, reported as `runtime.cgocall`, because an external cgo call leaves the loader graph at the host object and `runtime.cgocall` is reachable only from `runtime.cgoContextPCs`. [T-Sec11]

## 12. Wasm profile

### 12.1 Selective resume points

The WASM profile MUST activate automatically for GOOS=js GOARCH=wasm and GOOS=wasip1 GOARCH=wasm under `logos`. [T-Sec12]
It MUST apply to Go packages and to loGo/s packages; under plain `go` it needs the explicit `--wasm-features` flag. [T-Sec12][R-U1]
It uses the installed toolchain with no external tool. [T-Sec12]
A MayYield=0 function with straight-line code compiles without resume-point dispatch; one with inter-block jumps keeps the entry-loop dispatch that addresses those jumps. [T-Sec12]
A function keeps MayYield=0 only with no defer, no indirect call, no call to MayYield=1 or unknown callees, no transitive runtime.gopark, no channel, select, time.Sleep, Gosched, or blocking wasmimport call, no nil check, and no prologue stack check, because stack growth and preemption unwind through runtime.morestack. [T-Sec12]
Pure imports keep MayYield=0. [T-Sec12]
Unknown wasmimport targets default to MayYield=1. [T-Sec12]
A wrong MayYield=0 MUST trap instead of hanging. [T-Sec12]
A function with a call that keeps a resume point keeps resume-point dispatch. Without such a call it keeps dispatch only for inter-block jumps. MayYield=1 alone, through a prologue stack check or a nil check, needs none. Under --wasm-features=stack-switching, continuation calls replace dispatch, kept only for deferreturn, runtime and mcall-caller frames besides the entry-loop dispatch. [T-Sec12]
A caller of runtime.mcall MUST keep resume-point dispatch even when it is not in the runtime: the goroutine switch back re-enters that frame, which inlining (Gosched) can place in user code. [T-Sec12]
With `--wasm-features=stack-switching` on `logos build` or `go build` they MUST use stack switching instead. [T-Sec12][R-U1]
Pointers are 8 bytes and slice headers are 24 bytes. [T-Sec12]
alignof of int64, uint64, and float64 equals 8, so 64-bit atomics stay naturally aligned with no forcing code. [T-Sec12]
Deadcode removes unreachable functions, but type and method tables remain so reflect and encoding/json keep resolving. [T-Sec12]

### 12.2 Host interface and loader

`logos build` MUST emit <name>.loader.mjs beside the wasm file when the output is a `.wasm` file, for a Go main package and for a loGo/s main package. [T-Sec12][R-U1]
Plain `go` MUST emit the same loader by default for such an output, and `--wasm-loader=false` MUST turn it off. The flag MUST stay absent from the `go help build` text, as Sec 0.1 states for flags that plain `go` gains. [T-Sec12][R-U1]
The loader MUST have identical bytes under both commands. [T-Sec12][R-U1]
Both commands MUST add the implicit `logos/rt` import for the loader helpers when they emit the loader for a `-o` output that names a `.wasm` file, unless the module graph already provides the helpers. [T-Sec12][R-U1]
A build with no `-o` writes no loader, because the default output name of a wasm main package carries no `.wasm` suffix. [T-Sec12]
The loader MUST work under `GOOS=js` and, in a reactor module that the host initializes through WASI, under `GOOS=wasip1`. [T-Sec12]
`logos/rt` MUST export the wasm helpers `_logos_scratch_alloc`, `_logos_scratch_reset`, `_logos_errmsg_ptr`, and `_logos_errmsg_len` for the loader. [T-Sec12]
The loader exports instantiateLogos(wasmBytes, imports). [T-Sec12]
The loader re-reads memory.buffer after every call into wasm. [T-Sec12]
The loader MUST accept an errCode of int32 or int64 width; the compiler accepts int32 and int64 results. The host wrapper throws on any nonzero (truthy) return. [T-Sec12]
String results are invalid. [T-Sec12]
On nonzero errCode the wrapper throws the message read from wasm memory. [T-Sec12]
`logos/rt.WasmError(err)` MUST return 0 for a nil error, and for a non-nil error it MUST record `err.Error()` as the last error message and return 1; the loader reads that message through `_logos_errmsg_ptr` and `_logos_errmsg_len`. [T-Sec12]
//logos:wasmexport f MUST behave as //go:wasmexport f. [T-Sec12]
//logos:wasmimport m f MUST behave as //go:wasmimport m f. [T-Sec12]
Decoding MUST fill defaults first, then parse. [T-Sec12]
JSON decoders MUST grow arrays incrementally with no pre-allocation cap. [T-Sec12][T-Sec10.1]

## 13. Backend peephole and SIMD passes

- Every pass in this section MUST apply to Go packages under `logos` and under plain `go`, and to loGo/s packages under `logos`, except a rule about a loGo/s language construct (array operators, reduction methods, and `make` length checks) and except the wasm loop-stride default, which applies under `logos` only. [T-303][R-U1]

### 13.1 Arm64 load and store pairing

- Adjacent loads and stores that meet the safety predicates MAY fuse into one paired instruction. [T-303]
- Fusion MUST NOT change program meaning, fault behavior, or atomic order. [T-303]
- Candidates split by a call or an atomic MUST stay separate; loads MAY fuse across a block edge only when the source block has one successor and the target block has one predecessor. [T-303]
- A pair MUST NOT write back its base register. [T-303]
- The two destinations of a pair MUST differ. [T-303]
- A 16-byte pair offset outside the encodable range MUST stay separate loads or stores; 4-byte and 8-byte pairs MAY use the assembler fixup. [T-303]
- The 4-byte and 8-byte pairing above is upstream behavior. [T-303]
- Adjacent 16-byte vector loads MUST pair into FLDPQ when they meet the safety predicates with an encodable pair offset. [T-303]
- Adjacent 16-byte vector stores MUST pair into FSTPQ under the same conditions. [T-303]
- A 16-byte pair offset outside -1024..1008 or not a multiple of 16 MUST stay separate loads or stores. [T-303]
- With the default target_cpu the ARM64 target MUST use GOARM64=v8.2, so LSE atomics stay available. [T-303][T-105]
- The `arm64_pair` fixtures MUST pin the pairing rules of this list. [T-303]

### 13.2 Amd64 masked tails, rotates, and checked lengths

- In a native build, reductions over slices MUST use fault-suppressing masked loads for tail elements. [T-303]
- Masked tails MUST NOT read past the end of the slice. [T-303]
- The build MUST NOT add guard padding for vector tails. [T-303]
- In a native build with `GOEXPERIMENT=simd` on amd64 or arm64, `.sum()` on a bare slice of a fixed-width numeric element kind or an alias of one MUST load full vectors for the body and load the tail once through a masked load that enables only the remaining elements. [T-303]
- On amd64 that masked load MUST be VPMASKMOVD or VPMASKMOVQ for 32 and 64-bit elements, which need AVX2, and the zeroing K-masked VMOVDQU8 or VMOVDQU16 for 8 and 16-bit elements, which need AVX512. [T-303]
- The amd64 vector form MUST run only when the archsimd CPU feature check its instructions need is true and MUST take the scalar loop when it is false, and the check MUST be omitted when the build's `GOAMD64` level guarantees the feature (`v3` for AVX2, `v4` for AVX512). [T-303]
- On arm64, which has no masked load, the tail MUST load only the elements present. [T-303]
- An integer slice sum MUST accumulate in vector lanes. [T-303]
- A float slice sum MUST run the fixed pairwise halving tree of Sec 1.4 over the loaded elements, so its bits equal the scalar form with and without `GOEXPERIMENT=simd`. [T-303][T-104.6]
- A slice sum with a fused operation, or over an `int`, `uint`, or `uintptr` element, MUST take the scalar form. [T-303]
- Eject output of slice reductions MUST use the scalar form, so it builds with an upstream toolchain. [T-303][T-0.3]
- At GOAMD64=v3/v4 an immediate 32/64-bit constant-count rotate MUST use RORXQ or RORXL besides SARX, SHLX, and SHRX. [T-303]
- Variable-count rotates MUST stay on the existing path. [T-303]
- A ternary-shaped branch at GOAMD64=v3 MAY compile to a conditional move. [T-303]
- In `.lgo` files a non-constant integer product in a `make` length or capacity MUST lower to `logos/rt.MulLen`; constant products stay constant. [T-303]
- An overflowing `make` length MUST panic with `logos: make length overflow: <expr>`. [T-303]
- A negative `make` length MUST panic. [T-303]
- The call below panics with `logos: make length overflow: w * h` on overflow. [T-303]

```lgo
buf := make([]Pixel, w * h)
```

- An explicit `clear` of a slice MUST persist before return. [T-303]

### 13.3 Long-loop preemption on wasm

- Back-edge checks MUST fire every 4096 iterations in `logos` builds only where async preemption is absent (GOARCH=wasm). [T-303]
- A call-free loop MUST NOT starve its peers. [T-303]

## 14. JSON diagnostics schema

### 14.1 Emission

- `logos lint --json-diagnostics` and `logos build --json-diagnostics` MUST write one JSON object per line to stderr for every compiler and lint diagnostic in `.lgo` files and in Go files. [T-Sec14][R-U1]
- Each line MUST be a complete object with schema version `1.0`. [T-Sec14]
Wasm MUST align 64-bit types on 8 bytes. [T-Sec14]

### 14.2 Top-level fields

- Field `schema_version` is a string and its value is `1.0`. [T-Sec14]
- Field `severity` is a string with one of `forbid`, `deny`, or `warn`. [T-Sec14]
- Level `allow` emits no diagnostic. [T-Sec14]
- Field `rule_id` is a string of the form `group::name` for a lint diagnostic, and the empty string for a compiler diagnostic that carries no rule ID, including each Go compiler diagnostic. [T-Sec14][R-U1]
- Field `file` is a string with the path of the source file. [T-Sec14]
- Field `message` is a string with the human-readable diagnostic text. [T-Sec14]
- Field `span` is an object with the line, column, and byte range. [T-Sec14]
- Field `provenance` is an object with the source of the inferred type. [T-Sec14]
- Field `autofix_candidates` is an array of fix objects. [T-Sec14]
The loader MUST re-read memory.buffer after every wasm call. [T-Sec14]
Calls after growth MUST see current memory. [T-Sec14]
`logos build` MUST emit the loader for a Go main package with a `.wasm` output, and plain `go build` MUST emit the same loader by default. [T-Sec14]

### 14.3 Span object

- Object `span` holds six integers called `start_line`, `start_col`, `end_line`, `end_col`, `byte_offset`, and `byte_len`. [T-Sec14]

### 14.4 Provenance object

- Object `provenance` holds `origin_expr` as a string, `origin_line` as an integer, and `inferred_type` as a string. [T-Sec14]

### 14.5 Autofix objects

- Each candidate holds `description` as a string and `replacement` as a string. [T-Sec14]
- Candidates that insert text also hold `insert_before_line` as an integer. [T-Sec14]

## 15. Unwind tables for scope defer and Locked

### 15.1 Scope defer

For the `scope` token rule, see Sec 4.1. [T-0.2]
`scope defer f()` MUST run `f` at block exit. [T-104.7]
Scope defers in one block MUST run last-in-first-out. [T-104.7]
The function, receiver, and arguments MUST evaluate at the statement. [T-104.7]
`return`, `break`, `continue`, labeled `break`, and `goto` out of the block MUST run scope defers first. [T-104.7]
A panic MUST run scope defers during unwinding. [T-104.7]
A scope defer in a loop body MUST run every iteration. [T-104.7]
Cleanup on panic MUST use open-coded defers. [T-206][T-104.7]

```lgo
{
    mu.Lock()
    scope defer mu.Unlock()
    work()
}
```

### 15.2 Locked WithLock

For `WithLock` locking and unlock-after-panic, see Sec 2.6. [T-206]
A `WithLock` lambda MUST NOT return or store its argument. [T-206]

This example imports `logos/sync` with the package name `sync`.

```lgo
var m sync.Locked[Config]

m.WithLock(func(c *Config) {
    c.Port = 8080
})
```

## 16. IDE protocol

### 16.1 Proxy model and position mapping

- `logos lsp` MUST keep one document store keyed by the file path derived from the source URI that holds the text, the client URI, the client version, and at most one lowered snapshot per package. [T-Sec16]
- The proxy MUST answer `logos/snapshotCounts` with the number of retained lowered snapshots per package. [T-Sec16]
- A request on a `.lgo` document MUST use a snapshot whose versions equal the current client versions of every document of the package; the proxy MUST compute it on demand, once per version set, and concurrent requests MUST share one computation. [T-Sec16]
- The store MUST keep the newest client version of each open document; a change that carries an older version MUST be dropped and MUST NOT reach `gopls`. [T-Sec16]
- A frame that carries a duplicate Content-Length header MUST fail with a descriptive error. [T-Sec16]
- The proxy MUST NOT recompile without a request, a `didSave`, or the `didClose` of an unsaved `.lgo` document. [T-Sec16]
- The lowering MUST come from one `logos list` run with compiled JSON output and the overlay that returns, per `.lgo` file, the lowered text and the edit map; per package, the hints JSON and the compiler diagnostics. [T-Sec16]
- Position translation MUST rest on the compiler-written origin segments of the eject map: a lowered offset inside a copied segment maps to its exact original offset, and an offset inside a compiler-generated run maps to the anchor position of that run; no heuristic or size-capped textual matching takes part. [T-Sec16]
- Closing an unsaved `.lgo` document MUST discard its unsaved overlay and MUST refresh the retained package's compiler results. [T-Sec16]
- A `textDocument/formatting` request for a `.lgo` document MUST be refused instead of returning edits to lowered Go. Formatting requests for Go documents MUST pass through to `gopls`. [T-Sec16][R-U1]
- It MUST merge compiler JSON diagnostics into `publishDiagnostics`. [T-Sec16]
- It MUST report `logos lsp: gopls not found on PATH` when `gopls` is missing. [T-Sec16]
- The proxy MUST speak standard LSP over stdio. [T-Sec16]
- Stock LSP clients reach `.lgo` files through the proxy with no editor-specific code. [T-Sec16]

### 16.2 Inlay hints for lowering

- Inlay hints MUST show narrowed types such as `/* narrowed: *User */`. [T-Sec16]
- Inlay hints MUST show union members at switch statements. [T-Sec16]
- Inlay hints MUST show allocation badges `[stack]`, `[arena]`, `[heap]`, and `[rodata]`. [T-Sec16]
- Allocation badges MUST appear for Go packages too. [T-Sec16][R-U1]

### 16.3 Hover previews

- Hover over `?`, `const for`, `const switch T`, or `$"..."` MUST show the lowered code. [T-Sec16]
- Hover over an interpolation MUST render `Fixed: <n>B + dynamic` with the fixed byte count. [T-Sec16]

### 16.4 Code actions and cross-kind workspaces

- Code actions MUST apply the compiler autofix candidates. [T-Sec16]
- In a mixed workspace the overlay MUST hold the lowered `.lgo` files only, and Go packages MUST stay on disk for `gopls`. [T-Sec16][R-U3]
- A definition request across kinds MUST resolve to the declaration in its own file with the positions of that file. It MUST NOT write files to disk. [T-Sec16][R-U2]
- A Go package that imports a loGo/s package MUST resolve that import through the lowered overlay. [T-Sec16][R-U2]

## 17. Verification suite

### 17.1 Red-team completeness

### 17.2 Zero-alloc and codegen checks

- A function marked `//logos:zeroalloc` that performs a heap allocation MUST fail with `[perf::zeroalloc_violation]` naming the function and the escape-analysis reason. [T-Sec17]
- The function below MUST fail with `[perf::zeroalloc_violation]`. [T-Sec17]

```lgo
//logos:zeroalloc
func pack(v int) []int {
    return []int{v}
}
```

- The diagnostic MUST NOT be lowered by a `logos.toml` severity and MUST NOT be silenced by a `//logos:allow` directive. [T-Sec17]
- A `//logos:zeroalloc` directive that is not on its own line directly above a function declaration MUST fail with `[perf::zeroalloc_violation]`. [T-Sec17]
- The check MUST cover the function literals inside the marked function and the bodies inlined into it. It MUST report the allocation sites that escape analysis puts on the heap and `make(chan T)`. It MUST NOT report a constant converted to an interface, a function literal that captures nothing, or a zero-size object. [T-Sec17]
- The check MUST NOT report an operation that falls back to the heap at run time: `append` past the capacity of its operand, a string concatenation or conversion longer than the stack buffer, or an insert that grows a map. The directive is an assertion about the allocation sites that escape analysis decides, not a proof that the function never calls the allocator. [T-Sec17]
- In a Go package, `logos lint` MUST apply the check, and `logos build` MUST apply it as well when the author wrote the directive; `go build` MUST ignore the directive. [T-Sec17][R-U1]
- Codegen checks MUST pin the SIMD and fast-call emission. [T-Sec17]

### 17.3 Reproducibility and sizes

- Two builds from clean caches MUST yield identical SHA-256 digests for linux/amd64, linux/arm64, and js/wasm. [T-Sec17]
- The stripped js/wasm size of the `repro` module built with `-trimpath -buildvcs=false -ldflags='-s -w'` MUST be tracked in `test/logos/size/wasm_size.txt`. [T-Sec17]
- Growth above 1% against the recorded value MUST fail the gate. [T-Sec17]

### 17.4 End-to-end example exercise

- The suite MUST exercise `test/logos/example/order_service.lgo`. [T-Sec17]
- Ejected output MUST match native output for the example. [T-Sec17]

### 17.5 Interop suite

- Case 1: a loGo/s package that imports a Go package from another workspace module MUST build and run. [T-Sec17][R-U2]
- Case 2: a Go package that imports a loGo/s package from another workspace module MUST build and run under `logos`. [T-Sec17][R-U2][A-LGOSEL]
- In case 2, a plain `go build ./...` of that Go module MUST fail with a load error that names the imported package. [T-Sec17][A-LGOSEL]
- Case 3: a loGo/s package that imports `fmt` and `context` MUST build and run. [T-Sec17][R-U2]
- Case 4: `correctness::mixed_module` MUST name the Go packages and the loGo/s packages of the module. [T-Sec17][R-U3]
- Case 5: `logos build ./...` at a workspace root MUST build both modules, and each binary MUST run. [T-Sec17][R-U2]

## 18. Spec areas

### 18.1 Objective

loGo/s is a complete toolchain for Go and for the loGo/s language, which adds checked error propagation to Go. The command `logos eject` writes the pure-Go form of a loGo/s module.
Writers write `.lgo` files, the `logos` command checks and lowers them, and the Go 1.27 backend builds them.
The toolchain is one complete toolchain for Go and loGo/s: Go packages and loGo/s packages share one backend and one runtime, and each improvement serves both.

### 18.2 Compiler-made names

Compiler-made names use the `_logos_` prefix; comptime copies use the `_logosct` infix.
For example, the lowering of `v := parse(s)?` reads as follows (temporary names and site indices vary by position).

```go
v, _logos_e0 := parse(s)
if _logos_e0 != nil {
    _logos_rt.TraceErr(_logos_e0, &_logos_site0)
    return 0, _logos_e0
}
```

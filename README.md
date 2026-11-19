# LoGOs

**Go with checked errors, nil-safe option types, closed unions, and a way back to standard Go.**

[![CI](https://github.com/gosuda/LoGOs/workflows/logos/badge.svg)](https://github.com/gosuda/LoGOs/blob/release-branch.go1.27/.github/workflows/logos.yml)
[![License: BSD-3-Clause](https://img.shields.io/badge/license-BSD--3--Clause-blue.svg)](LICENSE)

LoGOs is a fork of the Go 1.27.4 toolchain that adds the `.lgo` language and the `logos` command. It is for Go developers who want compiler-checked error paths, nilability, concurrency boundaries, and generated serialization while staying inside Go's module and package model.

The `logos` command builds `.lgo` and `.go` packages. The `go` command keeps upstream Go language semantics and command text. When you need to leave, `logos eject` writes a standard Go copy of the module.

```go
package main

import (
	"fmt"
	"strconv"
)

func double(s string) (int, error) {
	n := strconv.Atoi(s)?
	return 2 * n, nil
}

func main() {
	fmt.Println(double("21"))
}
```

Output:

```text
42 <nil>
```

Why use it:

- `?` makes an error path explicit at the call site and checked at compile time.
- `*T` is non-nil in `.lgo`; `T?` marks absence and supports `?.` and `??`.
- A closed union declares its variants, and the compiler checks exhaustive switches.
- `go group` joins child goroutines and cancels siblings on the first failure.
- `logos eject` emits standard Go with source comments and support modules preserved.

Contents: [Quick start](#quick-start) · [Installation](#installation) · [Usage](#usage) · [Language tour](#language-tour) · [Documentation](#documentation) · [Development](#development) · [License](#license)

## Quick start

The repository ships a source build. Build it with Bash and an upstream Go 1.27.4 installation:

```sh
git clone https://github.com/gosuda/LoGOs.git
cd LoGOs
unset GOROOT GOTOOLCHAIN
upstream_go=$(command -v go)
bootstrap_root=$(env -u GOROOT GOTOOLCHAIN=local "$upstream_go" env GOROOT)
(cd src && GOROOT_BOOTSTRAP="$bootstrap_root" ./make.bash)
export GOROOT="$PWD"
export PATH="$GOROOT/bin:$PATH"
export GOTOOLCHAIN=local
logos version
```

`logos version` reports the fork's Go baseline, for example `logos version go1.27.4 linux/amd64`.

Create a module outside the toolchain tree:

```sh
hello=$(mktemp -d) && cd "$hello"
logos mod init example.com/hello
cat > main.lgo <<'EOF'
package main

import (
	"fmt"
	"strconv"
)

func double(s string) (int, error) {
	n := strconv.Atoi(s)?
	return 2 * n, nil
}

func main() {
	fmt.Println(double("21"))
}
EOF
logos run .
```

Output:

```text
42 <nil>
```

## Installation

The source build in the quick start is the installation path shipped by this repository.

- CI bootstraps the fork with upstream Go 1.27.4.
- If `go` is not on `PATH`, or `command -v go` finds a LoGOs build instead of upstream Go, pass the upstream root directly: `GOROOT_BOOTSTRAP=/path/to/go`.
- The build installs `bin/go` and `bin/logos` in the repository. Export `GOROOT`, `PATH`, and `GOTOOLCHAIN` in each shell that uses them.
- `README.go.md` is the upstream Go README retained for the base project; it does not document a LoGOs binary installer.

## Usage

Use `logos` on a LoGOs module the way you use `go` on a Go module: `logos mod init`, `logos build`, `logos run`, `logos test`, `logos list`, and `logos get` follow their Go counterparts.

Five commands are specific to LoGOs:

- `logos context <pkg>#<symbol>` prints one declaration and one level of signatures without function bodies.
- `logos eject -o DIR` writes a standard Go copy of the main module.
- `logos lint [build flags] [--pedantic] [--json-diagnostics] [packages]` runs compiler lints without writing binaries.
- `logos lsp` serves the language server protocol over standard input and output through `gopls`.
- `logos next [--fix | --ai-patch]` converts a Go module toward `.lgo` source; without a flag it prints only a diff.

Module rules:

- A module holds only `.lgo` packages or only `.go` packages; a package holds one kind.
- A `.lgo` package can import a Go package, and a `.go` package can import a LoGOs package under `logos`.
- Plain `go` ignores `.lgo` files. Build a Go importer after ejecting, or build the workspace with `logos`.

### Eject to standard Go

`logos eject` writes every `.lgo` file as a `.go` file, copies LoGOs dependency modules under `_logos_deps`, and places the shared support module under `_logos_deps/rt` as `github.com/gosuda/loGos/rt`. The output builds with upstream Go 1.27.4 or newer.

```sh
logos eject -o /tmp/logos-hello-go
cd /tmp/logos-hello-go
env -u GOROOT -u GOTOOLCHAIN "$upstream_go" run .
```

Output:

```text
42 <nil>
```

`DIR` must not exist, or it must be empty. Ejection never replaces files in the source module; keep `DIR` outside the module, because a nested `DIR` such as `./out` still lands inside the source tree.

### Lint

```sh
logos lint ./...
logos lint --pedantic --json-diagnostics ./...
```

`logos lint` checks packages in the main module only. It applies every rule to `.lgo` packages and language-neutral rules to `.go` packages, then stops before code generation. `--pedantic` raises the pedantic group from `allow` to `warn`; `--json-diagnostics` prints one JSON object per diagnostic.

A `//logos:allow(<rule>)` comment before a statement or declaration silences a `deny` or `warn` rule. It cannot silence a `forbid` rule in `.lgo` source. A `//logos:zeroalloc` comment above a function makes allocation sites selected by escape analysis a diagnostic.

## Language tour

### Error propagation and cleanup

`?` unwraps every non-error result from a call whose last result is `error`. On a non-nil error it returns the zero values plus the original error from a function whose last result is `error`.

```go
package main

import (
	"fmt"
	"strconv"
)

func parsePair(raw string) (sum int, err error) {
	n := strconv.Atoi(raw)?
	defer cleanup()?
	return n + n, nil
}

func cleanup() error { return nil }

func main() {
	fmt.Println(parsePair("21"))
}
```

Output:

```text
42 <nil>
```

`defer f()?` joins a cleanup error into the result. `errdefer f()` runs only when the function returns an error, panics, or exits through `runtime.Goexit`. Deferred calls still run last in, first out.

### Structured concurrency

`go group` keeps child goroutines inside a block, joins them before the block exits, cancels siblings after the first error or panic, and caps concurrency with `limit`.

```go
package main

import (
	"context"
	"fmt"

	"logos/sync"
)

func run(ctx context.Context) error {
	var c sync.Locked[int]
	go group (ctx, limit = 8) {
		for _, x := range []int{10, 20, 30} {
			go {
				c.WithLock(s => { *s += x })
			}
		}
	}
	var res int
	c.WithLock(s => { res = *s })
	fmt.Println("sum:", res)
	return nil
}

func main() {
	if err := run(context.Background()); err != nil {
		panic(err)
	}
}
```

Output:

```text
sum: 60
```

`logos/sync.Locked[T]` pairs a value with its mutex and exposes it through `WithLock`. `Hot[T]` atomically publishes read-mostly values. A bare `go` statement in `.lgo` outside a group is a compile error.

### Closed unions and property interfaces

A closed union is an interface with a finite member list. A type switch expression without `default` must cover every member and `nil`.

```go
package main

import "fmt"

type Value interface {
	int | string
}

func tag(v Value) string {
	return switch v.(type) {
	case int => "int"
	case string => "str"
	case nil => "nil"
	}
}

func main() {
	var v Value = 7
	fmt.Println(tag(v))
	var z Value
	fmt.Println(tag(z))
}
```

Output:

```text
int
nil
```

A property interface declares field-style contracts. A struct satisfies it through an explicit `: I` declaration:

```go
package main

import "fmt"

type Creature interface {
	const Name string
	HP int
}

type Monster struct : Creature {
	Name string
	HP   int
}

func main() {
	m := &Monster{Name: "orc", HP: 7}
	m.HP += 5
	fmt.Println(m.Name, m.HP)
}
```

Output:

```text
orc 12
```

A read-write property requires a pointer. Go importers see generated getter and setter methods, while `.lgo` code reads and writes `m.HP` directly.

### Comptime

`const if`, `const for`, and `const switch T` move fixed decisions into compilation. `const for` unrolls over a struct's fields in declaration order.

```go
package main

import (
	"comptime"
	"fmt"
)

type Src struct{ X int }
type Dst struct{ X int }

func copyFields[D, S any](dst *D, src *S) {
	const for _, f := range comptime.Fields[D]() {
		dst.[f] = src.[f]
	}
}

func main() {
	var d Dst
	copyFields(&d, &Src{X: 7})
	fmt.Println(d.X)
}
```

Output:

```text
7
```

`const switch T` picks a case by the type argument: exact cases match one named type, `~` cases match an underlying basic type, and interface cases expose that interface's method set. Unmatched types continue after the switch. A default arm and multiple types in one case are syntax errors.

### Tuples, lambdas, and chains

A function's multiple results can become a named tuple. Tuples can be stored, passed, destructured, and read by field name.

```go
package main

import "fmt"

func resolve(service string) (host string, port int) {
	return "api.example.com", 443
}

func main() {
	api := resolve("api")
	fmt.Println(api.host, api.port)
	host, port := api
	fmt.Println(host, port)
}
```

Output:

```text
api.example.com 443
api.example.com 443
```

The language also has target-typed lambdas (`x => x + 1`) and slice chains such as `xs.filter(x => x > 2).map(x => 2 * x).take(5)`. Consecutive `filter`, `map`, `take`, and `drop` stages fuse into one loop.

### Read-only data and fixed-array math

A `const` view reads an existing slice, pointer, map, or composite without allowing writes through that name. `logos/rt.Freeze` deep-copies a value and returns a protected view, so later writes to the source cannot change it.

Fixed-size numeric arrays support element-wise `+`, `-`, `*`, `/`, `&`, `|`, and `^`, plus `.sum()`, `.min()`, `.max()`, and `.dot()`:

```go
package main

import "fmt"

func main() {
	a := [4]float64{1, 2, 3, 4}
	b := [4]float64{5, 6, 7, 8}
	fmt.Println(a + b, a.dot(b))
}
```

Output:

```text
[6 8 10 12] 70
```

Array operators work on power-of-two arrays of at most 64 bytes. With `GOEXPERIMENT=simd`, supported shapes lower through `simd/archsimd` behind a CPU feature check; otherwise the compiler emits unrolled scalar code with the same numeric result.

### More shipped forms

| Form | What it does |
|---|---|
| `$"user {name}: {count}"` | Interpolates supported values once, left to right. |
| `&expr` | Gives a non-addressable value a fresh pointer each evaluation. |
| `type Cfg struct { Port int = 8080 }` | Fills omitted fields from constant defaults in `.lgo` literals. |
| `S{A: 1, ..src()}` | Copies same-name, same-type fields from a struct source. |
| `yield` | Writes `iter.Seq` and `iter.Seq2` generators without returning a closure literal. |
| `scope defer` | Runs cleanup at block exit, including each loop iteration. |
| `const` composites and maps | Places fixed data in read-only storage and lowers map lookup without a run-time map table. |
| Static JSON and binary methods | Generates marshalers for eligible `.lgo` structs without a call-site rewrite. |
| `logos.toml` | Configures cgo policy, optimization, target CPU, arena behavior, lint severities, and capability audits. |
| `-O=0` to `-O=3` and `-O=s` | Selects optimization levels for Go and LoGOs packages; `O3` adds bounded scalar analysis. |
| Automatic vectorization | Vectorizes eligible integer loops with `GOEXPERIMENT=simd` on supported targets while retaining scalar semantics. |
| WASM profile | Activates for `GOOS=js GOARCH=wasm` and `GOOS=wasip1 GOARCH=wasm` under `logos`, including a host loader. |
| `//logos:zeroalloc` | Rejects escape-analysis heap sites in the marked function without claiming every run-time allocation is impossible. |

## Documentation

- [Language specification](logos-doc/SPEC.md): the normative contract for every language form.
- [Implementation plan and milestone map](logos-doc/IMPLEMENTATION.md): shipped slices, gates, and build-plan context.
- [AGENTS.md](AGENTS.md): repository commands, test entry points, and contribution workflow.
- [Agent skills](plugins/logos/): packaged usage, review, migration, performance, concurrency, and toolchain skills.
- [Upstream Go README](README.go.md): the retained README for the base Go project.

## Development

Build the toolchain with the source-build commands in the quick start. Then run focused checks from the repository root:

```sh
export GOROOT="$PWD"
export PATH="$GOROOT/bin:$PATH"
export GOTOOLCHAIN=local
(cd src && go test -count=1 cmd/internal/testdir -run 'Test/logos/')
(cd src && go test -count=1 -short cmd/compile/internal/types2 -run TestLogos)
(cd src && go test -count=1 cmd/go -run 'Script/^logos_')
```

For contributions, read [AGENTS.md](AGENTS.md) first. Add language behavior with a failing test under `test/logos/` or `src/internal/types/testdata/logos/`, run the focused gate, and keep plain `go` behavior compatible unless a documented exception applies. Commit subjects use the Go convention `<area>: <lowercase summary>`.

[CONTRIBUTING.md](CONTRIBUTING.md) is the inherited upstream Go contribution document. The fork-specific workflow is in `AGENTS.md`.

## License

LoGOs additions and toolchain modifications use the BSD-3-Clause license, the same license as the standard Go source. See [LICENSE](LICENSE) for the terms.

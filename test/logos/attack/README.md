# Comptime instantiation attacks

Each `.lgo` file is one behavior and a complete program. A `// run` file pairs with a `.out` file of the same stem. A `// errorcheck` file carries `// ERROR` comments. A `// compile` file must build with no diagnostic. Expected results follow three invariants that SPEC 2.5 states [T-205]. I1: a comptime generic reached with the same type arguments, directly or through plain generics, is one instance, and different type arguments are different instances. I2: `f.Name`, `f.Index`, `f.Exported`, `f.Tag.Get` of a constant, and `comptime.TypeOf` identity are constants inside an instance. I3: an untaken `const if` arm or `const switch` case is const-dead. Other diagnostics follow SPEC 2.5, including the chain spelling in `test/logos/compile/comptime_cycle.lgo`.

An untaken branch is const-dead: it is not type-checked, is not counted for termination or break, and does not report `comptime.Error`. Code after a `return` or `panic` in the taken branch is termination-dead, not untaken: it is type-checked and reports `comptime.Error` (owner decision of 2026-11-23, the #186 pin `test/logos/compile/comptime_error_unreached_case.lgo`). `f.Name`, `f.Index`, `f.Exported`, `f.Tag.Get` of a constant, and `comptime.TypeOf` identity are constants inside an instance. A comptime generic may name an unexported helper and an unexported type of its defining package, because each instance is checked there (owner decision of 2026-11-23).

Two packages are not a fixture. The logos testdir harness writes one source file into a throwaway module and builds that file. A second package has no import path. `identity_two_callsites.lgo` is the single-file form of I1 (one instance per type-argument list). An importing caller of an exported comptime generic is the same gap. `cross_unexported_helper.lgo` and `cross_unexported_type.lgo` attack the defining-package half of that rule. `src/cmd/go/testdata/script/logos_comptime_unexported.txt` covers the importer half: the instance is compiled for the defining package, so an instantiation in another package builds and runs. `logos eject` refuses with `[comptime::unexported_in_exported]` only an instance that it prints outside its defining package and that names such an identifier (`src/cmd/go/testdata/script/logos_comptime_instance_identity.txt`).

`eval.zig:413`, `inline_switch.zig`, and `for.zig:396` are not here. They are `const for` over arrays and `const switch` type lists, which SPEC 2.5 still rejects and which later slices own.

| File | Invariant | Zig |
| --- | --- | --- |
| compose_plain_param.lgo | I1. A plain generic passes its type parameter into a comptime generic. | generics.zig:127-134 |
| compose_three_levels.lgo | I1. The type parameter passes through two plain generics into a comptime generic. | generics.zig:127-134 |
| compose_named_type.lgo | I1. The plain generic passes a named type built from its type parameter. | generics.zig:127-134 |
| compose_pointer_arg.lgo | I1. The plain generic passes a pointer to its type parameter. Size matches `unsafe.Sizeof`. | generics.zig:127-134 |
| compose_slice_arg.lgo | I1. The plain generic passes a slice of its type parameter. | generics.zig:127-134 |
| compose_map_arg.lgo | I1. The plain generic passes a map whose element type is its type parameter. | generics.zig:127-134 |
| compose_three_through_pointer.lgo | I1. Three levels, and the last call receives a pointer. | generics.zig:127-134 |
| dead_arm_type_error.lgo | I3. An untaken `const if` arm with a type error for that instance compiles. | |
| dead_error_silent.lgo | I3. `comptime.Error` in an untaken arm is silent. | basic.zig:353 |
| dead_break_ignored.lgo | I3. `break` in an untaken arm does not affect the loop. | |
| dead_false_error.lgo | I3. `comptime.Error` in a `const if false` arm is silent. | basic.zig:353 |
| switch_tail_type_error.lgo | I3 and SPEC 2.5. Code after a returning taken case is termination-dead: its type error reports while its `comptime.Error` is silent. | |
| switch_dead_case_type_error.lgo | I3. A type error in an untaken `const switch` case is not checked. | |
| switch_untaken_error_silent.lgo | I3. `comptime.Error` in an untaken case is silent. | |
| dead_error_taken.lgo | I3 and SPEC 2.5. A taken `comptime.Error` fails with its message. | |
| switch_no_match_error.lgo | I3 and SPEC 2.5. No matching case leaves the later `comptime.Error` live. | |
| taken_arm_type_error.lgo | I3. A type error in the taken `const if` arm still fails. | |
| taken_switch_type_error.lgo | I3. A type error in the taken `const switch` case still fails. | |
| fact_name_constif.lgo | I2. `f.Name` is a `const if` condition. | |
| fact_index_constif.lgo | I2. `f.Index` is a `const if` condition. | |
| fact_exported_constif.lgo | I2. `f.Exported` is a `const if` condition. | |
| fact_tag_get_constif.lgo | I2. `f.Tag.Get` of a string literal is a `const if` condition. | |
| fact_tag_named_const.lgo | I2 and SPEC 2.5. `f.Tag.Get` of a named constant folds. | |
| fact_typeof_eq_int.lgo | I2. `comptime.TypeOf[T]() == comptime.TypeOf[int]()` is a `const if` condition. | |
| fact_typeof_named_not_int.lgo | I2 and SPEC 2.5. Type identity is exact: a named `int` is not `int`. | |
| fact_print_index_exported.lgo | I2. `f.Index` and `f.Exported` lower to ordinary values, including an unexported field. | |
| identity_two_callsites.lgo | I1. The same type arguments from two call sites are one function, for `int` and for a named struct. | |
| identity_distinct_args.lgo | I1. Two named structs with the same shape are two instances. | |
| cycle_ab.lgo | I1 and SPEC 2.5. Cycle A to B to A fails with `[comptime::circular_instantiation]` and the chain `A -> B -> A`. | |
| cycle_self.lgo | I1 and SPEC 2.5. A self-cycle names the chain `Box -> Box`. | |
| cross_unexported_helper.lgo | Owner scope. An exported comptime generic calls an unexported helper. Expected result is a successful build. | |
| cross_unexported_type.lgo | Owner scope. An exported comptime generic uses an unexported type. Expected result is a successful build. | |
| hostile_tag_bracket.lgo | A `]` inside a struct tag survives instantiation. | |
| hostile_tag_brace.lgo | A `}` inside a struct tag survives instantiation. | |
| hostile_tag_quote.lgo | A quote inside a struct tag survives instantiation. `Tag.Get` returns the unquoted value. | |
| hostile_string_bracket.lgo | A `]` inside a string in the generic body survives instantiation. | |
| hostile_string_brace.lgo | A `}` inside a string in the generic body survives instantiation. | |
| hostile_string_quote.lgo | A quote inside a string in the generic body survives instantiation. | |
| hostile_all.lgo | One instantiation carries `]`, `}`, and a quote in a body string and in three tags. | |

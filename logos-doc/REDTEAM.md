Rules: logos-doc/SPEC.md. Status values: planned, tested, satisfied upstream, blocked.

| ID | Attack | Milestone | Test | Status |
| --- | --- | --- | --- | --- |
| #1 | Split-union pointer scalar overlap confusion | M2 | test/logos/redteam/attack_001_split_union_ptr_scalar | tested |
| #2 | String constBytes mutated via surviving alias | M5 | test/logos/run/zerocopy.lgo | tested |
| #3 | Rodata map breaks from runtime hash seed | M5 | test/logos/run/constmap_switch.lgo; src/cmd/compile/internal/logos/constmap_test.go (TestConstmapSwitchMax) | tested |
| #4 | Side-stack arena growth across parked goroutines | M18 | src/cmd/go/testdata/script/logos_redteam_4.txt (executed 2026-10-28: `go test cmd/go -run 'Script/^logos_redteam_4$'` passes both legs, the five arena park tests plus the `-race` leg SPEC 17 requires) | tested |
| #5 | Cgo fastcall blocks OS thread forever | M8, M20 | src/cmd/cgo/internal/testlogos/fastcall_test.go | tested |
| #6 | Block lambda return ambiguity inside map | M6 | test/logos/run/lambda.lgo; test/logos/run/chains.lgo (dblBlock, count with ?) | tested |
| #7 | Non-nil pointer bypass via arrays maps JSON | M3, M11 | test/logos/run/json_unmarshal_test.lgo; src/internal/types/testdata/logos/nonnil_zero.lgo; src/internal/types/testdata/logos/map_index.lgo | tested |
| #8 | Go functions returning nil into lgo | M3 | test/logos/run/platform.lgo | tested |
| #9 | Text cold RVO corrupting debugger output | M7 | test/logos/redteam/attack_009_cold_last_trace_names.lgo (executed 2026-10-28: the fixpoint strip in src/cmd/link/internal/ld/pcln_logos.go:50-79 lands it; the fixture asserts the `·logosct<N>` suffix absent and the base name present, and passes) | tested |
| #10 | Exhaustive union switch bypassed via default | M9 | test/logos/lint/exhaustive_union_switch.lgo; src/cmd/go/testdata/script/logos_pedantic_lint.txt | tested |
| #11 | Async preemption migrating G mid-arena use | M18 | test/logos/redteam/attack_011_preempt_arena_reachable.lgo | tested |
| #12 | Inline bump allocator corrupting mark bits | M18 | test/logos/codegen/redteam12_bump_scan_guard.lgo | tested |
| #13 | Concurrent multiword union write causing confusion | M8, M9 | test/logos/lint/concurrent_multiword_race.lgo; test/logos/lint/concurrent_multiword_race_go.go | tested |
| #14 | If x nil promotion breaking redeclarations | M3 | src/internal/types/testdata/logos/narrowing.lgo | tested |
| #16 | Auto-clip subslicing breaking buffer reuse loop | M5 | src/internal/types/testdata/logos/const_views.lgo (cvPlainReslice, cvPlainReuseLoop); test/logos/run/reslice_reuse.lgo | tested |
| #17 | Tuple colliding with multi-return unpacking | M6 | test/logos/run/tuples.lgo (direct) | tested |
| #18 | Named return shadowing corrupting defer close | M1 | test/logos/redteam/attack_018_shadowed_named_return | tested |
| #19 | Comptime ViewAs unaligned trap padding garbage | M7 | src/internal/types/testdata/logos/viewas.lgo; test/logos/run/viewas.lgo | tested |
| #20 | Side-Stack object pointing to Call-Stack var | M18 | test/logos/run/redteam20_arena_stack_ref.lgo | tested |
| #21 | Slowloris exhausting pinned iouring buffers | M8 | src/logos/sync/netidle_logos_test.go (576.8 B per idle connection) | tested |
| #22 | AVX vector tail read crossing protection | M15 | test/logos/run/simd_slice_tail.lgo (page-end mmap); test/logos/codegen/simd_slice.lgo; src/simd/archsimd/logos_maskload_linux_amd64_test.go; src/cmd/compile/internal/logos/simdslice_test.go (no padding) | tested |
| #23 | Accessing valid res when defer close fails | M3 | src/internal/types/testdata/logos/error_first.lgo | tested |
| #24 | Non-deterministic map iteration inside const func | M5 | src/internal/types/testdata/logos/ctfe.lgo (BadMapRange); test/logos/run/const_map.lgo (sorted range) | tested |
| #25 | Transitive dependencies bypassing reflect ban unsafely | M12 | src/cmd/go/testdata/script/logos_cap_unsafe.txt | tested |
| #26 | ARM64 weak ordering exposing Hot fields | M8 | src/logos/sync/sync_test.go (TestHotUpdateConcurrent, TestHotLayout) | tested |
| #27 | WASM memory grow detaching JS views | M16 | src/cmd/go/testdata/script/logos_wasm_loader.txt | tested |
| #28 | RVO destination aliasing corrupting input reverse | M13 | test/logos/redteam/attack_028_self_aliased_rvo.lgo | tested |
| #31 | Stream fusion over-executing side effects take | M6 | src/internal/types/testdata/logos/chains.lgo; test/logos/run/sort_side_effect.lgo | tested |
| #32 | Execution order chaos between defers | M1 | test/logos/redteam/attack_032_defer_lifo_order | tested |
| #33 | Stack-first allocation exceeding its frame buffer | M17, M18 | test/logos/redteam/attack_033_stack_buffer_overflow.lgo | tested |
| #34 | String interpolation allocating heap intermediate strings | M6 | test/logos/run/interp.lgo (bigBox AllocsPerRun) | tested |
| #35 | Go test race detector incompatibility with custom passes | M18 | test/logos/run/redteam35_arena_race_reuse.lgo | tested |
| #36 | Overlapping union tags and infinite complement sets | M2 | test/logos/redteam/attack_036_open_union_value | tested |
| #37 | Emit-go failing on exported const switch functions | M7 | src/cmd/go/testdata/script/logos_eject_const_switch.txt | tested |
| #38 | Parser backtracking explosion on lambdas versus tuples | M6 | src/cmd/compile/internal/syntax/parser_paren_logos_test.go | tested |
| #39 | Variadic ambiguity between one tuple and two args | M6 | test/logos/run/tuples.lgo (pair call unpack, tuple var pack) | tested |
| #40 | Comparing unions and tuples containing slices | M2 | test/logos/redteam/attack_040_union_equality | tested |
| #41 | Mutating source slice inside filter and map | M5, M6 | src/internal/types/testdata/logos/chains.lgo (chainMutateRecv, chainMutateParam) | tested |
| #42 | Defer close with error allocating join slice | M1 | test/logos/redteam/attack_042_defer_close_join | tested |
| #43 | Zero-sized union member representation | M2 | test/logos/redteam/attack_043_zero_sized_member | tested |
| #44 | Cross-package nil and const inference slowing compilation | M3 | src/cmd/go/testdata/script/logos_facts.txt | tested |
| #45 | Const switch rebinding slice of files as writers | M7 | src/internal/types/testdata/logos/const_switch.lgo; test/logos/run/const_switch.lgo | tested |
| #46 | Side-stack arena hiding heap pointers from GC | M18 | test/logos/run/redteam46_arena_gc_scan.lgo | tested |
| #47 | Const func purity broken by recover and addresses | M5 | src/internal/types/testdata/logos/ctfe.lgo | tested |
| #49 | Optional union representation | M3 | test/logos/run/option_union.lgo | tested |
| #50 | Const rodata slices rejected by reflection and print | M5 | test/logos/run/rodata_reflect.lgo | tested |
| #51 | Infinite compile-time loop freezing the IDE | M5 | src/internal/types/testdata/logos/ctfe.lgo | tested |
| #52 | Inlined defer inside loop exhausting stack slots | M6 | src/cmd/compile/internal/logos/m6gaps_test.go (TestScopeDeferOpenCoded) | tested |
| #53 | Recursive AST escaping local arena on return | M18 | test/logos/run/redteam53_recursive_arena_return.lgo | tested |
| #54 | Rodata constants crashing under address relocation | M5 | test/logos/run/relro_write.lgo | tested |
| #55 | Ignored error leaving stale trace frames | M1 | test/logos/redteam/attack_055_errtrace_reset_cap | tested |
| #56 | Inlined scope defer evaluating args too late | M6 | test/logos/run/scope_defer.lgo (funcVarDefer, recvDefer) | tested |
| #57 | Overlapping const switch cases shadowing each other | M2, M7 | test/logos/redteam/attack_057_case_first_match | tested |
| #59 | Cgo pointer check panicking on arena addresses | M18 | src/cmd/go/testdata/script/logos_redteam_59.txt (executed 2026-10-28: `go test cmd/go -run 'Script/^logos_redteam_59$'` passes, race leg included) | tested |
| #60 | Value struct with non-nil pointer zero-cleared | M3 | src/internal/types/testdata/logos/nonnil_zero.lgo | tested |
| #61 | Nested option none versus some nil collision | M3 | test/logos/run/option_nested.lgo | tested |
| #62 | Mutating method called on const receiver | M5 | src/internal/types/testdata/logos/const_views.lgo (Rename, Peek) | tested |
| #63 | panic leaking arena watermarks on unwind | M18 | src/cmd/go/testdata/script/logos_redteam_63.txt (executed 2026-10-28: `go test cmd/go -run 'Script/^logos_redteam_63$'` passes, race leg included) | tested |
| #64 | concurrent mutation between interpolation sizing passes | M6 | test/logos/run/interp.lgo (nextID once, tag order) | tested |
| #65 | comptime fields exposing private fields across packages | M7 | test/logos/run/const_for.lgo; src/cmd/go/testdata/script/logos_comptime_cross.txt | tested |
| #66 | errdefer with unnamed returns failing in eject | M1 | test/logos/redteam/attack_066_errdefer_unnamed_results | tested |
| #67 | FMA contraction causing x86 ARM64 sum drift | M6 | M6 half: src/cmd/compile/internal/logos/m6gaps_test.go (TestChainSumPairwise); test/logos/run/chains.lgo (sum order). The arm64 leg stays blocked on this host, which cannot execute arm64; a cross-architecture FMA sum-drift attack needs both architectures to mean anything | blocked |
| #68 | group returning early while sibling reads stack | M8 | src/logos/rt/group_test.go (FirstErrorWins, CancelReachesSibling); test/logos/run/group_exit.lgo | tested |
| #69 | next stripping comments and build tags | M10 | src/cmd/go/testdata/script/logos_next.txt; src/cmd/compile/internal/logos/next_test.go | tested |
| #70 | constant slice literal allocating on heap | M5 | test/logos/codegen/rodata.lgo | tested |
| #71 | scan region dead pointers breaking barrier reuse | M18 | src/cmd/go/testdata/script/logos_redteam_71.txt (executed 2026-10-28: `go test cmd/go -run 'Script/^logos_redteam_71$'` passes, race leg included) | tested |
| #72 | ViewAs interior pointer confusing collector scan | M7 | test/logos/run/viewas.lgo (GC keep-alive, need-have panic); src/logos/rt/viewas_test.go | tested |
| #73 | Const-map seed search fails to terminate | M5 | test/logos/run/const_map.lgo; src/cmd/compile/internal/logos/constmap_test.go | tested |
| #74 | - | M7 | test/logos/redteam/attack_074_funcforpc_spec_name.lgo | tested |
| #75 | order dependent inference in nil switch arms | M2 | test/logos/redteam/attack_075_nil_arm_common_type | tested |
| #76 | const for skipping checks on unrolled bodies | M7 | test/logos/compile/const_for_perfield.lgo; test/logos/compile/const_for_error.lgo; test/logos/run/const_for_lines.lgo | tested |
| #77 | plain defer clearing error after errdefer | M9 | test/logos/lint/defer_erases_errdefer_state.lgo | tested |
| #78 | wasm32 alignment trapping 64-bit atomics | M16 | test/logos/codegen/wasm_align.lgo | tested |
| #79 | eject helper imports colliding with user names | M1 | test/logos/redteam/attack_079_import_alias_collision | tested |
| #80 | cross layer regression matrix verification | M22 | src/cmd/go/testdata/script/logos_redteam_80.txt (planned: script exists, no executed test/logos attack run) | planned |
| #81 | concurrent collector racing scan zeroing | M18 | src/cmd/go/testdata/script/logos_redteam_81.txt (executed 2026-10-28: `go test cmd/go -run 'Script/^logos_redteam_81$'` passes, race leg included) | tested |
| #82 | split union needing custom GC bitmap | M2 | test/logos/redteam/attack_082_union_gc_shape | tested |
| #83 | escaping const byte to string zero copy mutation | M5 | test/logos/run/zerocopy.lgo | tested |
| #84 | eject evaluating chain receiver twice | M6 | src/cmd/compile/internal/logos/m6gaps_test.go (TestChainFuseOneLoop) | tested |
| #85 | typed nil error triggering errdefer | M1 | test/logos/redteam/attack_085_typed_nil_errdefer | tested |
| #86 | const switch losing outer constraint methods | M7 | src/internal/types/testdata/logos/const_switch.lgo; test/logos/run/const_switch.lgo | tested |
| #87 | errdefer skipped on panic or Goexit exit | M1 | test/logos/redteam/attack_087_errdefer_panic_goexit | tested |
| #88 | fastcall corrupting callee saved registers | M20 | src/cmd/cgo/internal/testlogos/fastcall_test.go | tested |
| #89 | overlapped scalar union leaving tail garbage | M2 | test/logos/redteam/attack_089_scalar_roundtrip | tested |
| #90 | recover inside emitted errdefer corrupting frames | M1 | test/logos/redteam/attack_090_errdefer_recover_success_skip | tested |
| #91 | const switch ambiguity on defined int types | M7 | test/logos/run/const_switch.lgo; src/internal/types/testdata/logos/const_switch.lgo | tested |
| #93 | malicious read beyond len on const bytes | M5 | test/logos/run/constview_bound.lgo | tested |
| #94 | Slow context extraction on large repositories | M10 | src/cmd/go/testdata/script/logos_context.txt; src/cmd/compile/internal/logos/context_test.go (TestContextLineCap); src/cmd/compile/internal/logos/context_bench_test.go (BenchmarkContext) | tested |
| #95 | Negative or huge take and drop indices | M6 | test/logos/run/chains.lgo (take, drop, get clamps) | tested |
| #96 | Early return skipping errdefer armed flag | M1 | test/logos/redteam/attack_096_errdefer_armed_flag | tested |
| #97 | Stale bytes in union struct variant slot | M2 | test/logos/redteam/attack_097_struct_variant_slots | tested |
| #98 | DeepEqual on read-only composite constants | M5 | test/logos/run/rodata_reflect.lgo | tested |
| #99 | Method call on an unchecked option | M3 | src/internal/types/testdata/logos/map_index.lgo | tested |
| #100 | Oversize fixed-array SIMD operator use | M15 | src/internal/types/testdata/logos/simd_types.lgo (simdTooLarge); test/logos/run/simd.lgo | tested |
| #101 | Race false positives on reused arena chunks | M18 | test/logos/run/redteam101_arena_race_reuse.lgo | tested |
| #102 | CTFE integer width across compile targets | M5 | src/internal/types/testdata/logos/ctfe.lgo | tested |
| #103 | Negative zero and NaN float map keys | M5 | src/internal/types/testdata/logos/ctfe.lgo | tested |
| #106 | Finalizer registered on arena allocated object | M18 | src/cmd/go/testdata/script/logos_redteam_106.txt (executed 2026-10-28: `go test cmd/go -run 'Script/^logos_redteam_106$'` passes, race leg included) | tested |
| #107 | Native CPU settings break reproducibility | M13 | src/cmd/go/testdata/script/logos_opt_levels.txt | tested |
| #108 | Emit-go fallback for interface switch cases | M7 | src/cmd/go/testdata/script/logos_eject_const_switch.txt; src/cmd/compile/internal/logos (TestEjectMatchesLowering) | tested |
| #109 | Conversion changes unrelated source text | M10 | src/cmd/go/testdata/script/logos_next.txt (goldens, ! logos fmt) | tested |
| #110 | Frozen view sharing mutable heap child | M5 | test/logos/run/freeze.lgo | tested |
| #111 | Unsafe ARM64 load store pair fusion | M15 | test/logos/codegen/arm64_pair.lgo; test/logos/codegen/arm64_pair.go; src/cmd/compile/internal/ssa/pair_logos_test.go (TestLogosPairARM64) | tested |
| #112 | Generated files clashing with source files | M1 | src/cmd/go/testdata/script/logos_eject.txt | tested |
| #113 | Inlined defer leaking resources in loops | M6 | src/cmd/compile/internal/logos/m6gaps_test.go (TestScopeDeferOpenCoded) | tested |
| #114 | Field reordering breaking serialized field order | M7 | test/logos/run/const_for.lgo | tested |
| #115 | Typed nil inside union interface value | M3 | src/internal/types/testdata/logos/union_exhaustive.lgo | tested |
| #116 | Mutating value-boxed struct through interface | M4 | src/internal/types/testdata/logos/property_iface.lgo | tested |
| #117 | Taking address of interface property value | M4 | src/internal/types/testdata/logos/property_iface.lgo | tested |
| #118 | Embedded fields satisfying property contract depth | M4 | src/internal/types/testdata/logos/property_iface.lgo | tested |
| #119 | Spread dropping mismatched same-name fields | M6 | test/logos/run/spread.lgo; src/internal/types/testdata/logos/spread.lgo | tested |
| #120 | Synthetic getters polluting reflection method count | M4 | test/logos/run/properties.lgo | tested |
| #122 | Emoji file extensions with variation selectors | M0 | src/cmd/go/testdata/script/logos_alias.txt | tested |
| #123 | Default value overriding explicit JSON false | M6, M11 | test/logos/run/json_unmarshal_test.lgo (TestDefaultsAppliedFirst) | tested |
| #125 | errdefer rollback overwriting primary return error | M1 | test/logos/redteam/attack_125_errdefer_join | tested |
| #126 | interpolation calling nextID twice in sizing | M6 | src/cmd/compile/internal/logos/m6gaps_test.go (TestInterpBindBeforeSizing) | tested |
| #127 | Interface default methods causing diamond ambiguity | M4 | src/internal/types/testdata/logos/property_iface.lgo | tested |
| #128 | Resolve failing before const for unrolls | M7 | test/logos/run/comptime_generic.lgo; test/logos/run/comptime_generic_inner_lowering.lgo; src/cmd/go/testdata/script/logos_comptime_cross.txt; src/cmd/go/testdata/script/logos_comptime_importcfg.txt; src/cmd/go/testdata/script/logos_comptime_foreign_types.txt | tested |
| #129 | Circular DI ServiceA ServiceB crashing compiler | M7 | test/logos/compile/comptime_cycle.lgo | tested |
| #130 | MapTo failing on same-named distinct field types | M7 | test/logos/compile/comptime_mapto.lgo; test/logos/run/comptime_mapto.lgo | tested |
| #131 | Embedding read-only and read-write same property | M4 | src/internal/types/testdata/logos/property_iface.lgo | tested |
| #132 | Compound property update evaluating receiver twice | M4 | test/logos/run/properties.lgo | tested |
| #133 | Non-ASCII module paths rejected by proxy | M0 | test/logos/lint/alias_extension.lgo; src/cmd/go/testdata/script/logos_lint_alias_extension.txt | tested |
| #134 | O3 inlining inflating recursive stack frames | M13 | test/logos/codegen/recursive_stack.lgo; test/logos/codegen/recursive_stack.go; test/logos/codegen/inline_levels.lgo; src/cmd/compile/internal/inline/stackbuf_logos_test.go (TestLogosVariableMakeThreshold) | tested |
| #135 | defer Close violating LIFO order | M1 | test/logos/redteam/attack_135_lifo_order | tested |
| #136 | go group sibling hanging on channel receive | M8 | test/logos/run/group_chan.lgo | tested |
| #137 | Call-free loop starving scheduler blocking GC | M15 | test/logos/run/wasm_loop_preempt.lgo | tested |
| #138 | Arena pointer stored into sync Pool | M18 | test/logos/run/redteam138_sync_pool.lgo; test/logos/codegen/redteam138_pool_heap.lgo | tested |
| #139 | Malicious JSON array triggering huge preallocation | M11 | test/logos/run/json_unmarshal_test.lgo (TestHostileArrayCostsOnArrival, TestLargeArrayDecodesIncrementally) | tested |
| #140 | Known hash seed enabling hash-flooding DoS | M5 | src/cmd/compile/internal/logos/constmap_test.go (TestConstmapBudgetIsOneMillion, TestConstmapProbeIsSingleProbe); src/logos/rt/constmap_proof_test.go (TestConstTableGetSingleProbe) | tested |
| #141 | Method call on property value losing mutation | M4 | test/logos/run/properties.lgo | tested |
| #142 | Chain map leaking file descriptors on failure | M6 | test/logos/run/chains.lgo (at-return closes) | tested |
| #143 | Buffered channel retaining dead pointer after receive | M8 | src/logos/sync/chanslot_logos_test.go | satisfied upstream |
| #144 | Stack slice passed to CGo invalidated | M17, M20 | src/cmd/go/testdata/script/logos_redteam_144.txt (executed 2026-10-28: `go test cmd/go -run 'Script/^logos_redteam_144$'` passes) | tested |
| #145 | Field versus embedded method selector ambiguity | M4 | src/internal/types/testdata/logos/property_iface.lgo | tested |
| #146 | Self spread copying large struct each tick | M6 | src/cmd/compile/internal/logos/spread_test.go; test/logos/codegen/spread_nomemmove.lgo | tested |
| #147 | Background goroutine panic crashing server process | M8 | src/logos/rt/group_test.go; test/logos/run/group_panic.lgo | tested |
| #148 | Concurrent multiword interface slice tearing | M8, M9 | test/logos/lint/concurrent_multiword_race.lgo; test/logos/lint/concurrent_multiword_race_go.go | tested |
| #149 | Dead-store elimination deleting clear before return | M15 | test/logos/codegen/clear_kept.lgo | satisfied upstream |
| #150 | Triple nested const for exploding compiler AST | M7 | src/cmd/go/testdata/script/logos_comptime_nodes.txt | tested |
| #151 | Group sibling stuck in blocking socket read | M8 | test/logos/run/group_netpoll.lgo; src/runtime/logos_group_netpoll_test.go | tested |
| #152 | Zero-copy string crashing after munmap | M5 | test/logos/run/zerocopy.lgo (other-provenance AllocsPerRun, survival) | tested |
| #153 | Hot Store leaking retire-list memory | M8 | src/logos/sync/sync_test.go (TestHotNoMemoryGrowth) | tested |
| #154 | Global 16-byte alignment inflating struct padding | M8 | src/logos/sync/sync_test.go (TestHotLayout) | tested |
| #155 | SIGURG arriving mid-bump double allocating address | M18 | test/logos/codegen/redteam155_bump_unsafe.lgo | tested |
| #156 | Caller reading hash before Close error check | M3 | src/internal/types/testdata/logos/error_first.lgo | tested |
| #157 | Unconditional self-recursion in const switch branch | M7, M9 | test/logos/lint/infinite_recursion.lgo; test/logos/lint/infinite_recursion_go.go | tested |
| #158 | Substring pinning large backing array in heap | M9 | test/logos/lint/large_backing_array_pin.lgo; test/logos/lint/large_backing_array_pin_go.go | tested |
| #160 | Collection moves live cgo arguments | M20 | src/cmd/go/testdata/script/logos_redteam_160.txt (blocked: fastcall foreign hold verification; no executed run) | blocked |
| #162 | Strict lint breaking third-party module libraries | M9 | src/cmd/go/testdata/script/logos_lint_go.txt | tested |
| #163 | Buffered channel send flagged as unbounded operation | M9 | test/logos/lint/unbounded_channel_op.lgo; test/logos/lint/unbounded_channel_op_go.go | tested |
| #164 | Benign writer flagged as discarded error | M9 | test/logos/lint/discarded_error.lgo; test/logos/lint/discarded_error_go.go | tested |
| #165 | Fastcall overflowing goroutine stack or handler crash | M20 | src/cmd/cgo/internal/testlogos/fastcall_test.go | tested |
| #167 | Nested WithLock causing self-deadlock | M8, M9 | test/logos/lint/reentrant_withlock_deadlock.lgo; test/logos/lint/reentrant_withlock_deadlock_go.go; src/logos/sync/sync_test.go (TestLockedWithLockMutates, TestLockedWithLockUnlocksAfterPanic) | tested |
| #169 | Make length integer overflow allocating tiny buffer | M15 | test/logos/run/make_overflow.lgo (length and capacity); test/logos/run/make_overflow_go.go; test/logos/run/make_const.lgo; src/logos/rt/mullen_test.go | tested |
| #170 | Time.After inside loop leaking timers | M9 | test/logos/lint/time_after_in_loop.lgo; test/logos/lint/time_after_in_loop_go.go | tested |
| #172 | Hoisted timer firing from undrained channel | M9 | test/logos/lint/time_after_in_loop.lgo; test/logos/lint/time_after_in_loop_go.go | tested |
| #173 | SIGPROF during fastcall causing stack panic | M20 | src/cmd/cgo/internal/testlogos/fastcall_test.go | tested |
| #174 | Const-if dead branch triggering unused lint | M7 | src/internal/types/testdata/logos/const_if.lgo; test/logos/run/const_if.lgo; test/logos/codegen/const_if_dead.lgo | tested |
| #176 | Panic inside WithLock leaving mutex locked | M8 | src/logos/sync/sync_test.go (TestLockedWithLockUnlocksAfterPanic) | tested |
| #177 | User input causing format injection in interpolation | M6 | test/logos/run/interp.lgo (format verbs printed literally) | tested |
| #178 | Spawning many goroutines exhausting memory in group | M8 | src/logos/rt/group_test.go; test/logos/run/group.lgo; src/internal/types/testdata/logos/group.lgo | tested |
| #179 | NaN breaking float sortBy ordering | M6 | test/logos/run/chains.lgo (64-item stability, NaN order) | tested |
| #180 | Init order affecting const composite tables | M5 | src/cmd/compile/internal/logos/ctfe/ctfe_test.go (TestWidthsComposite); test/logos/run/const_init_read.lgo | tested |
| #181 | Long loop exhausting arena before return | M18 | src/cmd/go/testdata/script/logos_redteam_181.txt (executed 2026-10-28: `go test cmd/go -run 'Script/^logos_redteam_181$'` passes, race leg included) | tested |
| #182 | Unsized int overflowing in serialized struct | M9 | test/logos/lint/serialized_unsized_int.lgo | tested |
| #183 | loGo/s package unable to use a Go package from another module | M0 | src/cmd/go/testdata/script/logos_interop.txt (case 1) | tested |
| #184 | Go package unable to use a loGo/s package from another module | M0 | src/cmd/go/testdata/script/logos_interop.txt (case 2) | tested |
| #185 | Union used as constraint and value type crossing into Go | M2 | test/logos/redteam/attack_185_dual_use_union; src/cmd/go/testdata/script/logos_eject_union.txt; src/cmd/go/testdata/script/logos_interop_union.txt | tested |
| #186 | Go caller passing nil to a non-nil pointer parameter | M3 | src/cmd/go/testdata/script/logos_interop_nil.txt | tested |
| #187 | Wasm loader missing for a Go main package | M16 | src/cmd/go/testdata/script/logos_wasm_loader.txt | tested |
| #188 | nocallback C function calling into Go on the fast path | M20 | src/cmd/cgo/internal/testlogos/fastcall_test.go | tested |
| #189 | CPU profile losing fast-called C time | M20 | src/cmd/cgo/internal/testlogos/fastcall_test.go | tested |
| #191 | region handle smuggled through a closure | M18 | (planned; region handle smuggled through a closure) | planned |
| #192 | group child writes a parent-region object | M18 | (planned; group child writes a parent-region object) | planned |
| #193 | region pointer stored into an older region object across a call | M18 | (planned; region pointer stored into an older region object across a call) | planned |
| #194 | ejected region program changes output | M18 | (planned; ejected region program changes output) | planned |
| #195 | unguarded field read after deferred unlock moved | M9 | (planned; unguarded field read after deferred unlock moved) | planned |
| #196 | facts object name with junk tag accepted | M3 | src/cmd/compile/internal/logos/facts/facts_test.go (TestValidObjectTag, TestTaggedObjectRoundTrip) | tested |
| #197 | stale LSP snapshot served after a newer edit | M21 | (planned; stale LSP snapshot served after a newer edit) | planned |
| #190 | Generic assertions manufacturing erased readonly obligations | M5 | test/logos/compile/const_interface_generic_assert.lgo; test/logos/run/const_interface_generic_assert.lgo; src/cmd/go/testdata/script/logos_const_interface_generic_assert.txt (local and imported function/receiver instantiations, wrappers, closures, type switches, composite/nested targets, and parameter indexes above 64; executed 2026-10-28: both testdir rows pass and `go test cmd/go -run 'Script/^logos_const_interface_generic_assert$'` passes) | tested |

Generic assertion effects have source changes, not a completed parent verification gate. Ordinary assertion definitions remain legal; the compile fixture targets unsafe qualified instantiations, while the run fixture retains identity, ordinary named arguments, and known-zero storage-container controls. The script covers both Go-to-loGo/s and loGo/s-to-Go generic imports. Receiver-readonly facts must not stand in for parameter-mutation summaries, and neither assertion nor mutation masks may truncate positions at or above 64.

The parent reported that the corrected baseline unsafe fixture compiled unexpectedly and that the safe run controls passed. Those are baseline observations, not evidence that the new source closes the attacks. Pending parent gates include a rebuilt compiler, the compile-negative and safe-run fixtures, the cross-package script, facts-v3/unknown-versus-known-zero and arbitrary-arity assertion/mutation cases, and the parent-owned broader regression gates. No generic assertion attack is marked tested or closed here.

The source owner's disclosed limits remain open for parent review: CTFE-driven instantiations after checker cleanup are not validated by the recorded cleanup pass; exported per-parameter masks collapse direct and nested assertion targets, and imported declarations retain masks rather than local site evidence. Reflection and `unsafe` remain outside this model.

## Cgo benchmark input validity

The old `BenchmarkFastcall` recurrence caused signed C overflow.
A 64-call UBSan run failed at `fastcall_c.c:12` on `2147483647 + 1`.
Both cgo benchmarks now bound the first argument to 1073741823 and assign the C result.
The maximum C sum is 1073741824, within the signed 32-bit range.
Both variants pass the same UBSan check with `logosfastcgo` enabled and disabled.
The C operation and the performance thresholds are unchanged.
Historical timings from the old recurrence are observations of a different workload.
They do not prove acceptance of the corrected benchmark.

Execution boundary (M20): the fastcall/M-owned, occupancy/dirty, and foreign-contract source changes in this worktree are authored/implemented but UNVERIFIED. Historical `tested` rows are preserved only where their executed evidence supports them; retained failures, skips, and blocked/noise-limited checks are not passes, and no row is flipped to `tested` by code reading. The M20 acceptance gate (`-race`, blocking parity, `./run.bash --no-rebuild`) stays closed. Row #160 stays `planned` (unmet); no unmet attack is closed here and no speedup is claimed.

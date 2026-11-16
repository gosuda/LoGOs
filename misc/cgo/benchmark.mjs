// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

import { mkdirSync, readFileSync, statSync } from "node:fs";
import { join, resolve } from "node:path";
import { assessRow, benchmarkRows, manifest, verdict } from "./check.mjs";
import { root, go, experiments, goEnv, run, saveJSON, sha256, snapshot } from "./common.mjs";

if (process.argv.length !== 3) throw new Error("usage: node benchmark.mjs EVIDENCE_DIRECTORY");
if (!process.env.HYPERFINE) throw new Error("HYPERFINE must name the verified binary");
const directory = resolve(process.argv[2]);
const config = manifest(JSON.parse(readFileSync(join(root, "misc/cgo/manifest.json"), "utf8")));
const fingerprint = snapshot(join(directory, "environment"));
const phases = [
  { name: "baseline", experiment: experiments.off },
  { name: "candidate", experiment: experiments.on },
  { name: "integration", experiment: experiments.on },
];
const binaries = {};
const output = [];
const shellQuote = (value) => "'" + value.replaceAll("'", "'\\''") + "'";
const benchmarkArgs = (name, duration) => ["-test.run=^$", "-test.bench=^" + name + "$",
  "-test.benchtime=" + duration, "-test.benchmem"];
try {
  run(process.env.HYPERFINE, ["--version"], { env: goEnv(experiments.off),
    log: join(directory, "hyperfine-version") });
  for (const phase of phases) {
    const env = goEnv(phase.experiment, { GOMAXPROCS: "1", GOFLAGS: "-p=1" });
    const binary = join(directory, phase.name, "testlogos.test");
    mkdirSync(join(directory, phase.name), { recursive: true });
    const args = ["test", "-p=1", "-c", "-o", binary];
    if (phase.name === "integration") args.push("-a");
    args.push("cmd/cgo/internal/testlogos");
    run(go, args, { env, cwd: join(root, "src"), log: join(directory, phase.name, "build") });
    const listed = run(binary, ["-test.list=^BenchmarkCgoOpt"], { env,
      log: join(directory, phase.name, "list") }).stdout.trim().split("\n");
    const names = listed.filter((name) => /^BenchmarkCgoOpt[A-Za-z0-9_]*$/.test(name)).sort();
    const expected = config.benchmarks.map((row) => row.name).sort();
    if (JSON.stringify(names) !== JSON.stringify(expected)) {
      throw new Error(phase.name + ": benchmark list differs from the explicit manifest");
    }
    binaries[phase.name] = { binary, env, sha256: sha256(binary), bytes: statSync(binary).size };
  }
  saveJSON(join(directory, "builds.json"), { fingerprint, builds: binaries,
    integration: "fresh -a build of the same checked-out source with the experiment enabled" });

  for (const target of config.benchmarks) {
    const name = target.name;
    const out = join(directory, name);
    let iterations = 0;
    for (const phase of phases) {
      const build = binaries[phase.name];
      const result = run(build.binary, benchmarkArgs(name, "2s"), { env: build.env,
        log: join(out, phase.name, "adaptive-pilot") });
      iterations = Math.max(iterations, benchmarkRows(result.stdout, name, null, 1)[0].iterations);
    }
    let calibrated = false;
    for (let round = 0; round < 4; round++) {
      let minimumWork = Infinity;
      for (const phase of phases) {
        const build = binaries[phase.name];
        const result = run(build.binary, benchmarkArgs(name, iterations + "x"), { env: build.env,
          log: join(out, phase.name, "fixed-pilot-" + round) });
        minimumWork = Math.min(minimumWork,
          benchmarkRows(result.stdout, name, iterations, 1)[0].usefulSeconds);
      }
      if (minimumWork >= 1) {
        calibrated = true;
        break;
      }
      iterations = Math.ceil(iterations * 2 / minimumWork);
      if (!Number.isSafeInteger(iterations) || iterations <= 0) throw new Error(name + ": invalid calibration");
    }
    if (!calibrated) throw new Error(name + ": useful-work calibration failed before scoring");
    saveJSON(join(out, "frozen-work.json"), { name, iterations, role: target.role,
      protocol: "one scored campaign; no automatic measurement retries" });
    const measurements = {};
    for (const phase of phases) {
      const build = binaries[phase.name];
      const log = join(out, phase.name);
      const timingPath = join(log, "hyperfine.json");
      const command = [build.binary, ...benchmarkArgs(name, iterations + "x")].map(shellQuote).join(" ");
      run(process.env.HYPERFINE, ["--warmup", "3", "--min-runs", "10",
        "--export-json", timingPath, command], { env: build.env, log: join(log, "hyperfine") });
      const corroboration = run(go, ["test", "-p=1", "-run=^$", "-bench=^" + name + "$",
        "-benchtime=" + iterations + "x", "-count=10", "-benchmem", "cmd/cgo/internal/testlogos"],
        { env: build.env, cwd: join(root, "src"), log: join(log, "corroboration") });
      const profile = join(log, "cpu.pprof");
      run(build.binary, [...benchmarkArgs(name, iterations + "x"), "-test.cpuprofile=" + profile],
        { env: build.env, log: join(log, "profile") });
      run(go, ["tool", "pprof", "-top", "-nodecount=30", build.binary, profile],
        { env: build.env, log: join(log, "profile-top") });
      measurements[phase.name] = { hyperfine: JSON.parse(readFileSync(timingPath, "utf8")),
        corroboration: corroboration.stdout };
    }
    const row = { ...assessRow(name, iterations, measurements), role: target.role,
      binaryBytes: Object.fromEntries(phases.map((phase) => [phase.name, binaries[phase.name].bytes])) };
    if (target.role === "reference" && row.status !== "measurement-blocked") row.status = "reference-only";
    output.push(row);
    saveJSON(join(out, "assessment.json"), row);
    saveJSON(join(directory, "rows.json"), output);
  }
  const targets = output.filter((row) => row.role === "target");
  const status = verdict(targets);
  const blockedReference = output.some((row) => row.role === "reference" && row.status === "measurement-blocked");
  saveJSON(join(directory, "summary.json"), { status, blockedReference, rows: output,
    promotion: "not established: this workflow does not manufacture or prove a same-contract floor" });
  const final = run("git", ["status", "--porcelain"], { env: goEnv(experiments.off),
    log: join(directory, "final-status") });
  if (final.stdout.trim()) throw new Error("benchmarking changed the source checkout");
  if (status !== "latency-pass" || blockedReference) {
    throw new Error("benchmark acceptance blocked or rejected; inspect every exact row");
  }
} catch (error) {
  saveJSON(join(directory, "failure.json"), { message: error.message });
  throw error;
}

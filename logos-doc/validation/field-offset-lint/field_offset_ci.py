# pyright: strict
"""Run frozen field-offset controls with the installed native compiler."""

from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path
from typing import cast

# Reuse the separately tested exact event parser and subprocess capture.
sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "tagged-method-facts"))
import check_ci as method_gate

DIRECTORY = "logos-doc/validation/field-offset-lint"
PRODUCTION = "src/cmd/compile/internal/logos/lint/rule_arena_pointer_escape.go"
PACKAGE = "cmd/compile/internal/logos/lint"
MANIFEST_SHA256 = "40fa23ddbefabafa4a55f8a58132f2eb311c95a8a49fbb46d424677396cba499"


def frozen(repo: Path) -> tuple[tuple[str, ...], frozenset[str], str, str]:
    manifest_path = f"{DIRECTORY}/frozen.json"
    path = method_gate.rooted(repo, manifest_path)
    if method_gate.digest(path) != MANIFEST_SHA256:
        raise ValueError("field-offset manifest changed")
    data = method_gate.json_object(path.read_text(encoding="utf-8"))
    entries, names = data["files"], data["required_names"]
    if not isinstance(entries, list) or not isinstance(names, list):
        raise TypeError("manifest files and required_names must be lists")
    paths = [manifest_path, f"{DIRECTORY}/README.md"]
    for value in cast(list[object], entries):
        entry = method_gate.mapping(value)
        relative = method_gate.field(entry, "path")
        if method_gate.digest(method_gate.rooted(repo, relative)) != method_gate.field(
            entry, "sha256"
        ):
            raise ValueError(f"frozen field-offset input changed: {relative}")
        paths.append(relative)
    recorded_names = cast(list[object], names)
    required = frozenset(method_gate.string(name) for name in recorded_names)
    if len(required) != 50 or len(recorded_names) != 50:
        raise ValueError("expected exactly 50 distinct named tests")
    return (
        tuple(paths),
        required,
        method_gate.field(data, "negative_name"),
        method_gate.field(data, "negative_diagnostic"),
    )


def run(repo: Path, out: Path) -> bool:
    phases: list[dict[str, object]] = []
    report: dict[str, object] = {"accepted": False, "complete": False, "phases": phases}
    before: dict[str, object] | None = None
    inputs: tuple[str, ...] = ()
    try:
        paths, names, negative_name, diagnostic = frozen(repo)
        inputs = (*paths, PRODUCTION, f"{DIRECTORY}/field_offset_ci.py")
        before = method_gate.identity(repo, inputs)
        method_gate.write_json(out / "before.json", before)
        if before["tracked_status"]:
            raise ValueError("tracked checkout files are modified")
        checks = (
            method_gate.Check(
                "old-rule",
                PACKAGE,
                frozenset(("TestArenaFieldOffset", negative_name)),
                (negative_name, diagnostic),
                (PRODUCTION, f"{DIRECTORY}/old-rule.go.txt"),
            ),
            method_gate.Check("candidate", PACKAGE, names),
        )
        env = method_gate.environment(repo)
        for check in checks:
            overlay_flags: tuple[str, ...] = ()
            if check.overlay is not None:
                source, old = check.overlay
                overlay = out / "old-rule.overlay.json"
                method_gate.write_json(
                    overlay,
                    {
                        "Replace": {
                            str(method_gate.rooted(repo, source)): str(
                                method_gate.rooted(repo, old)
                            )
                        }
                    },
                )
                overlay_flags = (f"-overlay={overlay}",)
            command = (
                str(repo / "bin/go"),
                "test",
                "-json",
                "-count=1",
                "-p=1",
                "-parallel=1",
                "-vet=off",
                "-timeout=10m",
                *overlay_flags,
                check.package,
                "-run",
                check.pattern(),
            )
            phases.append(method_gate.capture(command, repo, out, check, env, 660))
        report["complete"] = len(phases) == len(checks)
        report["accepted"] = all(phase["accepted"] is True for phase in phases)
    except (OSError, TypeError, ValueError) as error:
        report["rejection"] = str(error)
    finally:
        if before is not None:
            try:
                frozen(repo)
                after = method_gate.identity(repo, inputs)
                method_gate.write_json(out / "after.json", after)
                if after != before:
                    raise ValueError("source, frozen input, or installed tool changed")
            except (OSError, TypeError, ValueError) as error:
                report["accepted"] = False
                report["identity_rejection"] = str(error)
        method_gate.write_json(out / "report.json", report)
        print(json.dumps(report, indent=2), flush=True)
    return report["accepted"] is True and report["complete"] is True


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    repo: Path = args.repo.resolve(strict=True)
    out: Path = args.evidence.resolve()
    out.mkdir(parents=True, exist_ok=False)
    raise SystemExit(0 if run(repo, out) else 1)


if __name__ == "__main__":
    main()

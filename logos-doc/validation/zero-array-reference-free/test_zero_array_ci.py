# pyright: strict
"""Check the zero-array gate boundaries without executing Go."""

from __future__ import annotations

import json
from pathlib import Path

import pytest
import zero_array_ci as gate


def transcript(check: gate.method_gate.Check) -> list[dict[str, object]]:
    rows: list[dict[str, object]] = [{"Action": "start", "Package": check.package}]
    result = "fail" if check.negative is not None else "pass"

    def emit(name: str) -> None:
        rows.append({"Action": "run", "Package": check.package, "Test": name})
        for child in sorted(
            candidate
            for candidate in check.names
            if candidate.rpartition("/")[0] == name
        ):
            emit(child)
        if check.negative is not None and name == check.negative[0]:
            rows.append(
                {
                    "Action": "output",
                    "Package": check.package,
                    "Test": name,
                    "Output": f"    frozen_test.go:1: {check.negative[1]}\n",
                }
            )
        rows.append({"Action": result, "Package": check.package, "Test": name})

    for root in sorted(name for name in check.names if "/" not in name):
        emit(root)
    rows.append({"Action": result, "Package": check.package})
    return rows


def accept(
    rows: list[dict[str, object]], check: gate.method_gate.Check, code: int
) -> None:
    text = "".join(json.dumps(row) + "\n" for row in rows)
    gate.method_gate.accept(gate.method_gate.parse_events(text, check), check, code)


def test_frozen_inventory() -> None:
    repo = Path(__file__).resolve().parents[3]
    assert len(gate.frozen(repo)) == 22
    assert len(gate.type_names()) == 67
    assert len(gate.fact_names()) == 15
    assert (
        sum(len(check.names) for check in gate.CHECKS if check.negative is None) == 82
    )


@pytest.mark.parametrize(
    "check", gate.CHECKS, ids=[check.label for check in gate.CHECKS]
)
def test_exact_event_transcript(check: gate.method_gate.Check) -> None:
    accept(transcript(check), check, 1 if check.negative is not None else 0)


@pytest.mark.parametrize(
    "check", gate.CHECKS[2:], ids=[check.label for check in gate.CHECKS[2:]]
)
def test_missing_leaf_is_rejected(check: gate.method_gate.Check) -> None:
    leaf = max(check.names, key=lambda name: (name.count("/"), name))
    rows = [row for row in transcript(check) if row.get("Test") != leaf]
    with pytest.raises(ValueError, match="test runs"):
        accept(rows, check, 0)


@pytest.mark.parametrize(
    "check", gate.CHECKS[:2], ids=[check.label for check in gate.CHECKS[:2]]
)
@pytest.mark.parametrize("code", (0, 2, 124, -9))
def test_negative_exit_must_be_one(check: gate.method_gate.Check, code: int) -> None:
    with pytest.raises(ValueError, match="exit/package"):
        accept(transcript(check), check, code)


@pytest.mark.parametrize(
    "check", gate.CHECKS[:2], ids=[check.label for check in gate.CHECKS[:2]]
)
def test_negative_diagnostic_must_belong_to_leaf(check: gate.method_gate.Check) -> None:
    rows = transcript(check)
    root = next(name for name in check.names if "/" not in name)
    for row in rows:
        if row["Action"] == "output":
            row["Test"] = root
    with pytest.raises(ValueError, match="exact diagnostic"):
        accept(rows, check, 1)


def test_candidate_wrong_package_is_rejected() -> None:
    check = gate.CHECKS[2]
    rows = transcript(check)
    rows[0]["Package"] = gate.LOGOS
    with pytest.raises(ValueError, match="unexpected package"):
        accept(rows, check, 0)


def test_candidate_skip_is_rejected() -> None:
    check = gate.CHECKS[3]
    rows = transcript(check)
    for row in rows:
        if row.get("Test") == f"{gate.FACTS}/ReturnZero" and row["Action"] == "pass":
            row["Action"] = "skip"
    with pytest.raises(ValueError):
        accept(rows, check, 0)


def test_negative_crash_is_rejected() -> None:
    check = gate.CHECKS[0]
    rows = transcript(check)
    rows.insert(
        -1, {"Action": "output", "Package": check.package, "Output": "panic: crash\n"}
    )
    with pytest.raises(ValueError, match="crash"):
        accept(rows, check, 1)


def test_manifest_change_is_rejected(tmp_path: Path) -> None:
    directory = tmp_path / gate.DIRECTORY
    directory.mkdir(parents=True)
    (directory / "FROZEN.sha256").write_text("", encoding="utf-8")
    with pytest.raises(ValueError, match="manifest changed"):
        gate.frozen(tmp_path)


@pytest.mark.parametrize(
    "relative",
    (
        "src/cmd/compile/internal/types2/referencefree_zero_array_logos_test.go",
        "src/cmd/compile/internal/logos/constfacts_zero_array_test.go",
        "src/cmd/compile/internal/logos/compile_method_tagged_facts_test.go",
        "src/simd/testdata/iface/iface.go",
        f"{gate.DIRECTORY}/old-const_logos.go.txt",
    ),
)
def test_frozen_input_change_is_rejected(tmp_path: Path, relative: str) -> None:
    repo = Path(__file__).resolve().parents[3]
    for path in gate.frozen(repo):
        destination = tmp_path / path
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_bytes((repo / path).read_bytes())
    destination = tmp_path / relative
    destination.write_bytes(destination.read_bytes() + b"\nchanged\n")
    with pytest.raises(ValueError, match="frozen zero-array input changed"):
        gate.frozen(tmp_path)

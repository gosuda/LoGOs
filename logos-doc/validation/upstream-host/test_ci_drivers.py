# pyright: strict
"""Exercise CI baseline selection and transcript retention with real shell drivers."""

from __future__ import annotations

import os
import shutil
import subprocess
from pathlib import Path

import pytest

REPO = Path(__file__).resolve().parents[3]


def write(path: Path, text: str, *, executable: bool = False) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    if executable:
        path.chmod(0o755)


def git(root: Path, *args: str) -> str:
    return subprocess.check_output(
        ("git", "-C", str(root), *args), text=True, stderr=subprocess.STDOUT
    ).strip()


def commit(root: Path, message: str) -> str:
    git(root, "add", ".")
    git(
        root,
        "-c",
        "user.name=Evidence fixture",
        "-c",
        "user.email=evidence@example.invalid",
        "commit",
        "-qm",
        message,
    )
    return git(root, "rev-parse", "HEAD")


def environment(root: Path, tmp: Path) -> dict[str, str]:
    home, runner, commands = tmp / "home", tmp / "runner", tmp / "commands"
    for directory in (home, runner, commands):
        directory.mkdir(parents=True, exist_ok=True)
    return {
        **os.environ,
        "HOME": str(home),
        "RUNNER_TEMP": str(runner),
        "PATH": f"{commands}:{os.environ['PATH']}",
        "GITHUB_WORKSPACE": str(root),
        "GITHUB_RUN_ID": "fixture",
        "GITHUB_RUN_ATTEMPT": "1",
    }


def copy_driver(root: Path, name: str) -> Path:
    destination = root / ".github/scripts"
    destination.mkdir(parents=True, exist_ok=True)
    for filename in ("env.sh", name):
        shutil.copyfile(REPO / ".github/scripts" / filename, destination / filename)
    return destination / name


def test_benchmark_uses_event_base_after_the_branch_moves(tmp_path: Path) -> None:
    upstream, root = tmp_path / "upstream", tmp_path / "candidate"
    upstream.mkdir()
    git(upstream, "init", "-q", "-b", "main")
    write(upstream / "src/make.bash", "#!/bin/bash\nexit 0\n", executable=True)
    base = commit(upstream, "event base")
    write(upstream / "later.txt", "base branch moved\n")
    moved = commit(upstream, "new base tip")
    subprocess.run(("git", "clone", "-q", str(upstream), str(root)), check=True)
    driver = copy_driver(root, "bench-autovec.sh")
    write(
        root / "test/logos/bench/autovec_measure.sh",
        '#!/bin/bash\nset -eu\nprintf "%s %s\\n" "$6" "$7" >> "$RUNNER_TEMP/measured-shas"\n',
    )
    candidate = commit(root, "candidate")
    env = environment(root, tmp_path)
    env.update(
        AUTOVEC_BASE_SHA=base,
        AUTOVEC_CAND_SHA=candidate,
        GITHUB_BASE_REF="main",
        GITHUB_SHA=candidate,
    )
    for command, body in {
        "curl": "exit 0",
        "sha256sum": "cat >/dev/null",
        "tar": "exit 0",
        "go": "printf 'amd64\\n'",
        "jq": "printf '1\\n'",
    }.items():
        write(
            tmp_path / "commands" / command, f"#!/bin/bash\n{body}\n", executable=True
        )
    result = subprocess.run(
        ("bash", str(driver)),
        check=False,
        cwd=root,
        env=env,
        text=True,
        capture_output=True,
    )
    assert result.returncode == 0, result.stdout + result.stderr
    checked_out = git(tmp_path / "runner/autovec-base", "rev-parse", "HEAD")
    assert checked_out == base, (checked_out, base, moved)
    assert (tmp_path / "runner/measured-shas").read_text().splitlines() == [
        f"{base} {candidate}"
    ] * 6


def test_benchmark_fetches_missing_base_in_shallow_checkout(tmp_path: Path) -> None:
    # The CI checkout is shallow and may lack the event base; the driver
    # must retrieve a missing commit via fetch, not rely on a full clone.
    upstream, root = tmp_path / "upstream", tmp_path / "candidate"
    upstream.mkdir()
    git(upstream, "init", "-q", "-b", "main")
    write(upstream / "src/make.bash", "#!/bin/bash\nexit 0\n", executable=True)
    base = commit(upstream, "event base")
    write(upstream / "later.txt", "base branch moved\n")
    moved = commit(upstream, "new base tip")
    subprocess.run(
        ("git", "clone", "-q", "--depth=1", str(upstream), str(root)), check=True
    )
    assert base not in git(root, "rev-list", "HEAD"), "fixture must lack base"
    driver = copy_driver(root, "bench-autovec.sh")
    write(
        root / "test/logos/bench/autovec_measure.sh",
        '#!/bin/bash\nset -eu\nprintf "%s %s\\n" "$6" "$7" >> "$RUNNER_TEMP/measured-shas"\n',
    )
    candidate = commit(root, "candidate")
    env = environment(root, tmp_path)
    env.update(
        AUTOVEC_BASE_SHA=base,
        AUTOVEC_CAND_SHA=candidate,
        GITHUB_BASE_REF="main",
        GITHUB_SHA=candidate,
    )
    for command, body in {
        "curl": "exit 0",
        "sha256sum": "cat >/dev/null",
        "tar": "exit 0",
        "go": "printf 'amd64\\n'",
        "jq": "printf '1\\n'",
    }.items():
        write(
            tmp_path / "commands" / command, f"#!/bin/bash\n{body}\n", executable=True
        )
    result = subprocess.run(
        ("bash", str(driver)),
        check=False,
        cwd=root,
        env=env,
        text=True,
        capture_output=True,
    )
    assert result.returncode == 0, result.stdout + result.stderr
    checked_out = git(tmp_path / "runner/autovec-base", "rev-parse", "HEAD")
    assert checked_out == base, (checked_out, base, moved)
    assert (tmp_path / "runner/measured-shas").read_text().splitlines() == [
        f"{base} {candidate}"
    ] * 6


@pytest.mark.parametrize("status", (0, 37))
def test_cgo_transcript_keeps_both_streams_and_exit_status(
    tmp_path: Path, status: int
) -> None:
    root = tmp_path / "source"
    driver = copy_driver(root, "cgofull-dist.sh")
    env = environment(root, tmp_path)
    env["CGO_EXPERIMENT"] = "logoscgoopt,nologosfastcgo"
    write(
        tmp_path / "commands/node",
        '#!/bin/bash\nmkdir -p "$RUNNER_TEMP/cgo-evidence/full/environment"\n',
        executable=True,
    )
    write(
        root / "src/run.bash",
        "#!/bin/bash\nset -eu\n"
        'test "$*" = "--no-rebuild -k"\n'
        'test "$GOEXPERIMENT" = "logoscgoopt,nologosfastcgo"\n'
        'test "$CGO_ENABLED" = 1\n'
        'printf "dist stdout\\n"\nprintf "dist stderr\\n" >&2\n'
        f"exit {status}\n",
    )
    result = subprocess.run(
        ("bash", str(driver)),
        check=False,
        cwd=root,
        env=env,
        text=True,
        capture_output=True,
    )
    assert result.returncode == status, result.stdout + result.stderr
    assert (tmp_path / "runner/cgo-evidence/full/dist.log").read_text() == (
        "dist stdout\ndist stderr\n"
    )


def test_workflow_supplies_the_immutable_pr_base() -> None:
    workflow = (REPO / ".github/workflows/ci.yml").read_text()
    bench = workflow.split("  bench:\n", 1)[1]
    assert "AUTOVEC_BASE_SHA: ${{ github.event.pull_request.base.sha }}" in bench

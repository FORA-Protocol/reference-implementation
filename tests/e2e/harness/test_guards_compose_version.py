"""Meta-guard — the compose-version gate must refuse Docker tools too old for the stack.

``scripts/check-compose-version.sh`` is a prerequisite of ``make e2e-up``. Every
health check in the stack sets ``start_interval``: often while a container starts,
rarely once it is healthy. Docker Engine before 25 has no start interval, and
Compose releases before 2.24 are reported to drop the setting without a word, so
every check would run at the slow interval from the start and the stack would come
up minutes late, with no error to say why.

On a machine with current tools the refusing branch never runs, so this suite
drives the real script against a ``docker`` stub at the front of PATH that reports
whichever versions the case needs.

Pure subprocess work: no stack, no Docker, no database. The marks come from
``guard_harness.guard_marks``, whose docstring says why the isolation one is
there.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from .conftest import REPO_ROOT
from .guard_harness import guard_marks, run_gate, stub_commands_env

GATE = "check-compose-version.sh"

pytestmark = guard_marks(skip_when=not (REPO_ROOT / "scripts" / GATE).is_file())


def _run_with(tmp_path: Path, *, api: str, compose: str):
    """Run the gate with a ``docker`` that reports ``api`` and ``compose``.

    The stub answers only the two version queries the gate makes and fails
    anything else, so a gate that drifted to another call fails this suite.
    """
    env = stub_commands_env(
        tmp_path,
        {
            "docker": f'if [ "$1" = "version" ]; then echo "{api}"; exit 0; fi\n'
            f'if [ "$1 $2" = "compose version" ]; then echo "{compose}"; exit 0; fi\n'
            "exit 1\n"
        },
    )
    return run_gate(REPO_ROOT, GATE, reads_root=False, env=env)


@pytest.mark.parametrize(
    ("api", "compose"),
    [("1.44", "2.24.0"), ("1.47", "v2.30.3"), ("1.56", "5.5.1")],
)
def test_gate_passes_tools_that_pace_health_checks(tmp_path: Path, api: str, compose: str) -> None:
    """Without these the refusals below prove nothing: a gate that failed for an
    unrelated reason — a missing file, a syntax error — would satisfy them."""
    proc = _run_with(tmp_path, api=api, compose=compose)

    assert proc.returncode == 0, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"


@pytest.mark.parametrize(
    ("api", "compose", "named"),
    [
        ("1.43", "2.29.1", "Docker Engine API 1.43"),
        ("1.47", "2.23.0", "Docker Compose 2.23.0"),
        ("1.47", "v2.20.2", "Docker Compose 2.20.2"),
        ("", "2.29.1", "Docker Engine API unknown"),
        ("1.47", "", "Docker Compose unknown"),
    ],
)
def test_gate_refuses_tools_that_cannot(tmp_path: Path, api: str, compose: str, named: str) -> None:
    """The branch a current machine never runs, asserted on the message as well as
    the exit code: the condition it catches produces no error of its own, so the
    refusal must say which tool is too old."""
    proc = _run_with(tmp_path, api=api, compose=compose)

    assert proc.returncode == 1, f"STDOUT:\n{proc.stdout}\nSTDERR:\n{proc.stderr}"
    assert named in proc.stderr, proc.stderr

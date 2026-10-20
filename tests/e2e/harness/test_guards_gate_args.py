"""Meta-guard — the argument seam the structural gates share.

``scripts/gate-args.sh`` is how four gates decide which tree they read. That
decision is a security property rather than a convenience: these gates say whether
a tree is fit to publish or to build from, and a gate pointed at the wrong tree
reports PASS about a tree nobody asked about.

The seam holds three rules, and each is here because breaking it produces a PASS
rather than an error:

    an unknown argument      -> refuse    (a misspelled flag must not run as no flag)
    --root without a value   -> refuse    (never fall through to the default)
    --root at a missing dir  -> refuse    (never fall through to the default)

and one more the rules above exist to protect: the tree comes from the argument
and from nothing else. No environment variable selects it.

The seam is sourced rather than executed, so every case drives it through a real
gate. The first three run against all four gates, because the property has to hold
for each of them and a seam sourced by four callers can be broken for one. The
last two run against the image-version gate alone: it reads a handful of documents
with no network and no toolchain, so it answers in milliseconds, and the question
being asked is about the seam rather than about that gate.
"""

from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

from .conftest import REPO_ROOT
from .guard_harness import GATE_SEAM, fixture_env, guard_marks

_SCRIPTS = REPO_ROOT / "scripts"
_SEAM = _SCRIPTS / GATE_SEAM

# Every gate that sources the seam. Written out rather than derived by grepping
# for the source line: a gate that stopped sourcing it would silently leave this
# list, and losing the seam is exactly what these cases are about.
_GATES = (
    "check-published-refs.sh",
    "check-published-secrets.sh",
    "check-image-version.sh",
    "check-sdk-pin-consistency.sh",
)

# The one used where a case needs a gate to actually run. Offline, no toolchain,
# no container: it reads three tracked documents and compares one string.
_FAST_GATE = "check-image-version.sh"

pytestmark = guard_marks(skip_when=not _SEAM.is_file())


def _run(
    gate: str, *args: str, env: dict[str, str] | None = None
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["bash", str(_SCRIPTS / gate), *args],
        capture_output=True,
        text=True,
        env=fixture_env(env),
        check=False,
    )


def _output(result: subprocess.CompletedProcess[str]) -> str:
    return f"{result.stdout}\n{result.stderr}"


def test_every_gate_sources_the_seam() -> None:
    """The list above is the premise of every case below it."""
    for gate in _GATES:
        body = (_SCRIPTS / gate).read_text(encoding="utf-8")
        assert GATE_SEAM in body, f"{gate} no longer sources {GATE_SEAM}"
        assert 'parse_gate_args "$@"' in body, f"{gate} sources the seam but does not call it"


@pytest.mark.parametrize("gate", _GATES)
def test_an_unknown_argument_is_refused(gate: str) -> None:
    """A misspelled flag must not run as though no flag had been given.

    That is the shape that turns a redirected check into one that looks clean: the
    caller believes it named a tree, the gate read this checkout, and the PASS
    describes neither.
    """
    result = _run(gate, "--rooot", "/tmp")

    assert result.returncode == 1, _output(result)
    assert "unknown argument: --rooot" in _output(result), _output(result)
    assert "PASS" not in result.stdout, result.stdout


@pytest.mark.parametrize("gate", _GATES)
def test_root_without_a_directory_is_refused(gate: str) -> None:
    """A flag with its value left off must not fall through to the default tree."""
    result = _run(gate, "--root")

    assert result.returncode == 1, _output(result)
    assert "--root requires a directory" in _output(result), _output(result)
    assert "PASS" not in result.stdout, result.stdout


@pytest.mark.parametrize("gate", _GATES)
def test_root_at_a_missing_directory_is_refused(gate: str, tmp_path: Path) -> None:
    """A directory that is not there is an error, never the default tree."""
    result = _run(gate, "--root", str(tmp_path / "absent"))

    assert result.returncode == 1, _output(result)
    assert "no such directory" in _output(result), _output(result)
    assert "PASS" not in result.stdout, result.stdout


def test_root_selects_the_tree_that_is_read(tmp_path: Path) -> None:
    """Without this the three refusals above are satisfied by a gate that ignores --root.

    An empty directory holds none of the documents the gate compares, so a gate
    that honours the argument fails and a gate that quietly read this checkout
    instead would pass.
    """
    result = _run(_FAST_GATE, "--root", str(tmp_path))

    assert result.returncode == 1, _output(result)
    assert "agree on one image version" not in result.stdout, result.stdout


def test_no_environment_variable_selects_the_tree(tmp_path: Path) -> None:
    """The tree comes from the argument and from nothing else.

    Each name below is one a seam like this could plausibly have been given, and
    the first two are the seam's own internal variable names. An ambient value that
    selected the tree could redirect a `make quality` run at a different one while
    it reported PASS, which is the whole reason this is an argument.
    """
    hostile = {
        "PATH": "/usr/bin:/bin",
        "HOME": "/tmp",
        "repo_root": str(tmp_path),
        "script_dir": str(tmp_path),
        "ROOT": str(tmp_path),
        "REPO_ROOT": str(tmp_path),
        "GATE_ROOT": str(tmp_path),
    }
    result = _run(_FAST_GATE, env=hostile)

    assert result.returncode == 0, _output(result)
    assert "agree on one image version" in result.stdout, _output(result)

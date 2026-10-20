"""Companion tests for the dead-code gate of ADR-028 D8 H8 (``make deadcode``).

A misconfigured vulture passes silently: a confidence set one step too high, an
ignore pattern broad enough to cover real helpers, or a recipe that stopped
running it all leave ``make quality`` clean. So vulture is run here with the real
``[tool.vulture]`` table on sample modules, and must report each kind of dead code
and nothing pytest reaches by name.

Each case writes its samples under ``tests/e2e/harness/`` inside a temporary
directory. Vulture treats every file under a ``tests`` directory as a test file,
so the samples are judged the way the real harness modules are. The paths given
on the command line replace the table's own ``paths``; everything else in the
table applies.

No stack, no Docker: vulture is run in a subprocess. The runner image carries
neither the Makefile nor vulture, so the module skips there and runs in the
host's ``test-fast`` tier.
"""

from __future__ import annotations

import subprocess
import sys
import tomllib
from pathlib import Path

import pytest

from ._compose import REPO_ROOT
from .guard_harness import guard_marks, make_recipe

_E2E_DIR = Path(__file__).resolve().parents[1]
_PYPROJECT = _E2E_DIR / "pyproject.toml"
_E2E_MAKEFILE = _E2E_DIR / "Makefile"
_ROOT_MAKEFILE = REPO_ROOT / "Makefile"

pytestmark = guard_marks(
    skip_when=not _E2E_MAKEFILE.is_file(),
    reason="the Makefile is not in the runner image; host test-fast runs this",
)

#: One sample per kind of dead code, keyed by what vulture calls it.
_DEAD = {
    "function": "def helper():\n    return 1\n",
    "class": "class Helper:\n    pass\n",
    "method": "class Used:\n    def helper(self):\n        return 1\n\n\nUsed()\n",
    "variable": "HELPER = 1\n",
    "import": "import json\n",
}

#: What pytest reaches by name, none of which vulture may report.
_PYTEST_NAMES = """\
import pytest

pytest_plugins = ["pytester"]
pytestmark = pytest.mark.stack_isolation("isolated")


def pytest_configure(config):
    return config


@pytest.fixture(autouse=True)
def _requested_by_no_one():
    return None


@pytest.fixture
def requested():
    return 1


def test_one(requested):
    assert requested


class TestGroup:
    def test_two(self):
        pass
"""


def _reported(tmp_path: Path, modules: dict[str, str]) -> list[str]:
    """vulture's report on ``modules``, written under ``tests/e2e/harness/``, one line each."""
    pytest.importorskip("vulture", reason="vulture is a dev dependency; the runner image has none")
    harness = tmp_path / "tests" / "e2e" / "harness"
    harness.mkdir(parents=True)
    for name, source in modules.items():
        (harness / name).write_text(source)
    result = subprocess.run(  # noqa: S603 -- fixed argv, this interpreter's own vulture
        [sys.executable, "-m", "vulture", "--config", str(_PYPROJECT), str(harness)],
        capture_output=True,
        text=True,
        check=False,
    )
    assert result.returncode in (0, 3), result.stderr
    return result.stdout.splitlines()


@pytest.mark.parametrize("kind", sorted(_DEAD))
@pytest.mark.parametrize("module", ["helper.py", "test_sample.py"])
def test_each_kind_of_dead_code_is_reported(kind: str, module: str, tmp_path: Path) -> None:
    """A helper module and a test module alike: the pytest patterns excuse no other name."""
    reported = _reported(tmp_path, {module: _DEAD[kind]})
    assert [line for line in reported if f"unused {kind}" in line], (
        f"an unused {kind} in {module} is not reported: {reported}"
    )


def test_the_names_pytest_reaches_are_not_reported(tmp_path: Path) -> None:
    """Tests, fixtures, hooks and the module-level pytest names are used by pytest."""
    assert _reported(tmp_path, {"test_sample.py": _PYTEST_NAMES}) == []


def test_the_scope_is_the_typecheck_scope() -> None:
    """A driver left out of the scope would make the helpers only it calls look unused.

    The typecheck recipe also checks its own probe module, which is not harness code.
    """
    command = " ".join(make_recipe(_E2E_MAKEFILE.read_text(), "typecheck")[:2])
    checked = set(command.replace("\\", " ").split()[len("uv run mypy".split()) :])
    checked.discard("typecheck_probes/check.py")
    paths = tomllib.loads(_PYPROJECT.read_text())["tool"]["vulture"]["paths"]
    assert set(paths) == checked, {"typecheck only": checked - set(paths)}


def test_make_quality_runs_vulture_with_that_table() -> None:
    """Both quality targets reach ``make deadcode``, which runs vulture on the table alone."""
    assert make_recipe(_E2E_MAKEFILE.read_text(), "deadcode") == ["uv run vulture"]
    root = _ROOT_MAKEFILE.read_text()
    assert make_recipe(root, "harness-deadcode-check") == ["@$(MAKE) -C tests/e2e deadcode"]
    for target in ("quality", "quality-ci"):
        line = next(line for line in root.splitlines() if line.startswith(f"{target}:"))
        assert "harness-deadcode-check" in line.split(":", 1)[1].split(), (
            f"`make {target}` does not run harness-deadcode-check: {line}"
        )

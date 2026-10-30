"""The isolation contract: collection refuses a harness test that declares no valid mode.

ADR-028 D7 extends ADR-008 D5 to every e2e test: each one declares a
``stack_isolation`` mode. No linter can require a marker, so the harness conftest
checks it as it collects and refuses the collection otherwise (ADR-028 D8: a custom
check, where neither structure nor a standard linter can state the rule).
``test_guards_harness_collection`` collects the real harness, which holds the real
tree to the contract in ``make quality``. This suite shows the check refusing each
kind of violation, so a check that stopped refusing one is seen.

The items are stand-ins carrying the three attributes the check reads. The hook
itself is called, through the conftest module, so a hook that stopped calling the
check fails here as well.

Pure: no stack, no Docker, no file read.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import cast

import pytest

from . import conftest

pytestmark = pytest.mark.stack_isolation("isolated")

_HARNESS = Path(conftest.__file__).resolve().parent


@dataclass(frozen=True)
class _Item:
    """A collected test as the check sees it: its node id, its path and its mark."""

    nodeid: str
    path: Path
    mark: pytest.Mark | None = None

    def get_closest_marker(self, name: str) -> pytest.Mark | None:
        return self.mark if name == "stack_isolation" else None


def _declared(mode: str, nodeid: str = "harness/test_x.py::test_declared") -> _Item:
    return _Item(nodeid, _HARNESS / "test_x.py", pytest.mark.stack_isolation(mode).mark)


def _collect(*items: _Item) -> None:
    """Run the conftest's collection hook over ``items``."""
    conftest.pytest_collection_modifyitems(
        cast(pytest.Config, None), cast(list[pytest.Item], list(items))
    )


@pytest.mark.parametrize("mode", ["isolated", "shared-clean-fixtures", "shared-without-cleanup"])
def test_each_declared_mode_is_accepted(mode: str) -> None:
    _collect(_declared(mode))


@pytest.mark.parametrize(
    ("item", "says"),
    [
        pytest.param(
            _Item("harness/test_x.py::test_silent", _HARNESS / "test_x.py"),
            "harness/test_x.py::test_silent: missing @pytest.mark.stack_isolation(...)",
            id="undeclared",
        ),
        pytest.param(
            _Item(
                "harness/obligations/test_y.py::test_silent",
                _HARNESS / "obligations" / "test_y.py",
            ),
            "harness/obligations/test_y.py::test_silent: missing @pytest.mark.stack_isolation",
            id="undeclared-in-a-subpackage",
        ),
        pytest.param(
            _declared("isolate", "harness/test_x.py::test_misspelt"),
            "harness/test_x.py::test_misspelt: stack_isolation mode 'isolate' not in",
            id="unknown-mode",
        ),
        pytest.param(
            _Item(
                "harness/test_x.py::test_bare",
                _HARNESS / "test_x.py",
                pytest.mark.stack_isolation.mark,
            ),
            "harness/test_x.py::test_bare: stack_isolation mode '<missing>' not in",
            id="bare-marker",
        ),
    ],
)
def test_each_violation_is_refused_by_name(item: _Item, says: str) -> None:
    """The refusal names the offending test, and only it."""
    with pytest.raises(pytest.UsageError) as refused:
        _collect(_declared("isolated"), item)
    assert says in str(refused.value)
    assert "test_declared" not in str(refused.value)


def test_a_test_outside_the_harness_is_not_judged() -> None:
    """The hook sees every item of the session; a tree collected beside this one is not its own."""
    _collect(_Item("elsewhere/test_z.py::test_silent", _HARNESS.parent / "elsewhere" / "test_z.py"))

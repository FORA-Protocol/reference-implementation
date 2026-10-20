"""Builders for the obligation-registry tests.

The registry module loads ``<its own directory>/../obligations`` when it is
imported and builds the ``Obligation`` enum from what it finds there. A test of
the enum or of the end-of-session report therefore cannot patch the loaded
module: that would test the patch, not the plugin. Instead these builders plant
a temporary tree for a subprocess to import:

* ``<root>/harness/``: a copy of every non-test module of the real harness
  package, so the registry module's own relative imports resolve;
* ``<root>/obligations/``: the area files the test writes;
* ``<root>/suite/``: the claiming test modules of a pytester session.

A subprocess started in ``<root>`` imports ``harness`` from that tree, because
the working directory precedes the editable install on ``sys.path``.
"""

from __future__ import annotations

import importlib
import json
import shutil
import textwrap
from collections.abc import Mapping
from pathlib import Path
from types import ModuleType

import pytest
import yaml

from .ast_scan import E2E_ROOT

HARNESS_DIR = E2E_ROOT / "harness"

PROTOCOL_MODULE = "github.com/FORA-Protocol/protocol"
# Written out in full, not built from PROTOCOL_MODULE: the published-references
# gate accepts the module-qualified path only as one literal string on the line.
PROTOCOL_FILE_CITE = "github.com/FORA-Protocol/protocol/proto/fora/v1/fora.proto"
# In the pinned fora.proto this sentence wraps after "domain —", so it spans two
# comment lines and matches only once the "// " leaders are dropped.
PROTOCOL_QUOTE = (
    "A recipient MUST reject a request whose `exchange` is not its own domain — "
    "an Exchange has exactly one identity"
)
ADR_CITE = "docs/architecture/adr-028-e2e-obligation-harness.md"
ADR_QUOTE = "Each fact about an obligation is written in exactly one place."

REPORT_FILE = "obligation-report.json"

# The pytester session's own configuration. xfail_strict makes a bare
# @pytest.mark.xfail strict, the same default the e2e pyproject sets, and
# --strict-markers proves the plugin registers the obligation marker itself.
_SESSION_INI = """\
[pytest]
testpaths = suite
xfail_strict = true
addopts = --strict-markers -p no:cacheprovider
"""


def registry_module() -> ModuleType:
    """Import the real registry module, inside the calling test.

    Imported per test rather than at module top, so that a missing or broken
    module fails each test that needs it instead of erroring collection.
    """
    return importlib.import_module("harness.obligation_registry")


def entry_fields(oid: str = "AREA-001", **overrides: object) -> dict[str, object]:
    """One registry entry, valid unless an override makes it otherwise."""
    fields: dict[str, object] = {
        "id": oid,
        "title": "An Exchange refuses an offer that names another Exchange",
        "level": "MUST",
        "actor": "exchange",
        "surface": "sdk",
        "source": {"cite": PROTOCOL_FILE_CITE, "quote": PROTOCOL_QUOTE},
        "positive": "An offer naming the Exchange's own domain is accepted.",
        "negative": [
            {
                "code": "invalid_argument",
                "reason": "EXCHANGE_MISMATCH",
                "side_effect": "no transaction is recorded",
            }
        ],
    }
    fields.update(overrides)
    return fields


def write_area(directory: Path, area: str, *entries: Mapping[str, object]) -> Path:
    """Write ``<directory>/<area>.yaml`` as a top-level list of entries."""
    directory.mkdir(parents=True, exist_ok=True)
    path = directory / f"{area}.yaml"
    path.write_text(yaml.safe_dump([dict(e) for e in entries], sort_keys=False, allow_unicode=True))
    return path


def plant_harness(root: Path) -> None:
    """Copy the real harness package's non-test modules into ``<root>/harness``."""
    target = root / "harness"
    target.mkdir(parents=True, exist_ok=True)
    for module in sorted(HARNESS_DIR.glob("*.py")):
        if module.name.startswith("test_") or module.name == "conftest.py":
            continue
        shutil.copy2(module, target / module.name)


def plant_registry(root: Path, *entries: Mapping[str, object]) -> Path:
    """Create ``<root>/obligations`` with a non-YAML placeholder, plus one area file."""
    registry = root / "obligations"
    registry.mkdir(parents=True, exist_ok=True)
    (registry / ".gitkeep").touch()
    if entries:
        write_area(registry, "area", *entries)
    return registry


def plant_session(
    pytester: pytest.Pytester,
    entries: list[dict[str, object]],
    modules: Mapping[str, str],
) -> None:
    """Plant a full pytester session: harness copy, registry, ini and suite modules."""
    plant_harness(pytester.path)
    plant_registry(pytester.path, *entries)
    (pytester.path / "pytest.ini").write_text(_SESSION_INI)
    suite = pytester.path / "suite"
    suite.mkdir()
    for name, source in modules.items():
        (suite / name).write_text(textwrap.dedent(source))


def run_session(pytester: pytest.Pytester, *args: str) -> pytest.RunResult:
    """Run the planted session in a subprocess with the shipped plugin loaded."""
    return pytester.runpytest_subprocess("-p", "harness.obligation_registry", *args)


def read_report(pytester: pytest.Pytester) -> dict[str, dict[str, object]]:
    """The JSON report the session wrote at ``<rootpath>/obligation-report.json``."""
    report: dict[str, dict[str, object]] = json.loads((pytester.path / REPORT_FILE).read_text())
    return report


def read_states(pytester: pytest.Pytester) -> dict[str, object]:
    """Each obligation's state from the JSON report, without its skips and note."""
    return {oid: status["state"] for oid, status in read_report(pytester).items()}

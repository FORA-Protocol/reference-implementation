"""Companion tests for the harness bans in ``tests/e2e/ruff-harness.toml`` (ADR-028 D8).

A lint ban that is misconfigured passes silently: a mistyped module path, a ban
listed under a name the SDK does not export it as, or a scope that no longer
reaches the test modules all leave every lint run clean. So each ban has a
sample violation here, and ruff is run on it with the real configuration file,
the way ``make lint`` runs it, and must report it.

Each case builds a small project in a temporary directory: a verbatim copy of
``ruff-harness.toml`` at its root and a ``harness`` package beside it. The
``include`` patterns in the configuration are relative to the file's own
directory, so the copy scopes the temporary tree exactly as the original scopes
``tests/e2e``. Every sample line carries a ``# noqa: TID251`` comment, so a
sample fires only because the run ignores it.

No stack, no Docker: ruff is run in a subprocess. The runner image carries
neither ``ruff-harness.toml`` nor the Makefile, so the module skips there and runs
in the host's ``test-fast`` tier, which is also where ``make lint`` runs the bans.
"""

from __future__ import annotations

import json
import subprocess
import sys
import tomllib
from pathlib import Path

import pytest

from .guard_harness import guard_marks, make_recipe

_E2E_DIR = Path(__file__).resolve().parents[1]
_CONFIG = "ruff-harness.toml"

pytestmark = guard_marks(
    skip_when=not ((_E2E_DIR / _CONFIG).is_file() and (_E2E_DIR / "Makefile").is_file()),
    reason="ruff-harness.toml and the Makefile are not in the runner image; host test-fast runs this",
)

#: How ``make lint`` runs the bans, minus the ``uv run`` prefix: the arguments
#: every case below runs ruff with, and the command the Makefile must carry.
_BAN_ARGS = ("check", "--config", _CONFIG, "--ignore-noqa", ".")

_NOQA = "  # noqa: TID251"

#: One sample violation per ban, keyed by the name the configuration bans.
_SAMPLES = {
    "httpx": "import httpx",
    "fora_sdk.sign_request": "from fora_sdk import sign_request",
    "fora_sdk.httpsig.sign_request": "from fora_sdk.httpsig import sign_request",
    "fora_sdk.append_signature": "from fora_sdk import append_signature",
    "fora_sdk.httpsig.append_signature": "from fora_sdk.httpsig import append_signature",
    "fora_sdk.sign_offer_jcs": "from fora_sdk import sign_offer_jcs",
    "fora_sdk.core.sign_offer_jcs": "from fora_sdk.core import sign_offer_jcs",
    "fora_sdk.sign_offer_acceptance": "from fora_sdk import sign_offer_acceptance",
    "fora_sdk.acceptance.sign_offer_acceptance": (
        "from fora_sdk.acceptance import sign_offer_acceptance"
    ),
    "fora_sdk.sign_offer_acceptance_jcs": "from fora_sdk import sign_offer_acceptance_jcs",
    "fora_sdk.core.sign_offer_acceptance_jcs": (
        "from fora_sdk.core import sign_offer_acceptance_jcs"
    ),
    "fora_sdk.sign_request_acceptance_jcs": "from fora_sdk import sign_request_acceptance_jcs",
    "fora_sdk.core.sign_request_acceptance_jcs": (
        "from fora_sdk.core import sign_request_acceptance_jcs"
    ),
    "fora_sdk.sign_agent_binding": "from fora_sdk import sign_agent_binding",
    "fora_sdk.pop.sign_agent_binding": "from fora_sdk.pop import sign_agent_binding",
    "fora_sdk.sign_ed25519_signed_url": "from fora_sdk import sign_ed25519_signed_url",
    "fora_sdk.signedurl.sign_ed25519_signed_url": (
        "from fora_sdk.signedurl import sign_ed25519_signed_url"
    ),
    "fora_sdk.client.proof_headers": "from fora_sdk.client import proof_headers",
    "fora_sdk.client.content.proof_headers": "from fora_sdk.client.content import proof_headers",
    "cryptography.hazmat.primitives.asymmetric.ed25519.Ed25519PrivateKey.generate": (
        "from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey\n"
        "Ed25519PrivateKey.generate()"
    ),
    "cryptography.hazmat.primitives.asymmetric.ed25519.Ed25519PrivateKey.from_private_bytes": (
        "from cryptography.hazmat.primitives.asymmetric import ed25519\n"
        "ed25519.Ed25519PrivateKey.from_private_bytes(bytes(32))"
    ),
    "cryptography.hazmat.primitives.asymmetric.ed25519.Ed25519PrivateKey.sign": (
        "from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PrivateKey\n"
        "Ed25519PrivateKey.sign(KEY, b'message')"
    ),
    "fastmcp": "from fastmcp import Client",
    "mcp": "from mcp import ClientSession",
    "time.sleep": "import time\ntime.sleep(1)",
    "asyncio.sleep": "from asyncio import sleep\nsleep(1)",
    "psycopg": "import psycopg",
    "psycopg2": "from psycopg2 import connect",
    "asyncpg": "import asyncpg",
    "pg8000": "import pg8000.native",
    "sqlalchemy": "from sqlalchemy import create_engine",
    "sqlite3": "import sqlite3",
    "redis": "import redis",
    # Relative, the way a test module in the harness package imports it.
    "harness.operator_writes": "from .operator_writes import provision_seat",
}


def _with_noqa(source: str) -> str:
    return "".join(f"{line}{_NOQA}\n" for line in source.splitlines())


def _project(root: Path, modules: dict[str, str]) -> Path:
    """A temporary project: the real configuration file and ``modules`` under it."""
    (root / _CONFIG).write_text((_E2E_DIR / _CONFIG).read_text())
    for name, source in modules.items():
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(source)
    return root


def _fired(project: Path) -> list[tuple[str, str]]:
    """``(file, banned name)`` of every ban ruff reports in ``project``."""
    pytest.importorskip("ruff", reason="ruff is a dev dependency; the runner image has none")
    result = subprocess.run(  # noqa: S603 -- fixed argv, this interpreter's own ruff
        [sys.executable, "-m", "ruff", *_BAN_ARGS[:-1], "--no-cache", "--output-format", "json"],
        cwd=project,
        capture_output=True,
        text=True,
        check=False,
    )
    assert result.returncode in (0, 1), result.stderr
    fired = []
    for finding in json.loads(result.stdout):
        assert finding["code"] == "TID251", finding
        name = finding["message"].split("`")[1]
        fired.append((Path(finding["filename"]).relative_to(project).as_posix(), name))
    return fired


_PACKAGE = {"harness/__init__.py": "", "harness/obligations/__init__.py": ""}


@pytest.mark.parametrize("banned", sorted(_SAMPLES))
def test_each_ban_fires_on_its_sample_in_a_test_module(banned: str, tmp_path: Path) -> None:
    """ruff reports the sample, in a test module, despite its noqa comment."""
    sample = _with_noqa(_SAMPLES[banned])
    project = _project(tmp_path, {**_PACKAGE, "harness/test_sample.py": sample})
    assert ("harness/test_sample.py", banned) in _fired(project), (
        f"{banned} is not refused in a test module; is it still in {_CONFIG}?"
    )


@pytest.mark.parametrize(
    "form",
    [
        "import httpx as client",
        "from httpx import get",
        "from httpx._models import Response",
        "import httpx.codes",
    ],
)
def test_the_httpx_ban_refuses_every_import_form(form: str, tmp_path: Path) -> None:
    """H3 bans the module, so an alias, a member import or a submodule is refused too."""
    project = _project(tmp_path, {**_PACKAGE, "harness/test_sample.py": _with_noqa(form)})
    assert ("harness/test_sample.py", "httpx") in _fired(project), (
        f"{form!r} is not refused in a test module"
    )


@pytest.mark.parametrize(
    ("form", "banned"),
    [
        ("import fastmcp", "fastmcp"),
        ("from fastmcp.client import Client", "fastmcp"),
        ("from fastmcp.client.client import CallToolResult", "fastmcp"),
        ("from fastmcp.exceptions import ToolError", "fastmcp"),
        ("from mcp.client.session import ClientSession", "mcp"),
        ("from mcp import types", "mcp"),
    ],
)
def test_the_mcp_client_bans_refuse_every_import_form(
    form: str, banned: str, tmp_path: Path
) -> None:
    """D3 bans both MCP client libraries as modules, so no import form reaches a raw client."""
    project = _project(tmp_path, {**_PACKAGE, "harness/test_sample.py": _with_noqa(form)})
    assert ("harness/test_sample.py", banned) in _fired(project), (
        f"{form!r} is not refused in a test module"
    )


def test_the_mcp_ban_spares_the_harness_mcp_modules(tmp_path: Path) -> None:
    """A test imports the seam by its relative name, which the bare ``mcp`` ban must not catch."""
    source = (
        "from .mcp_client import mcp_session\n"
        "from .mcp_transport import send\n"
        "from . import mcp_session as seam\n"
    )
    project = _project(tmp_path, {**_PACKAGE, "harness/test_sample.py": source})
    assert _fired(project) == []


def test_the_bans_reach_test_modules_in_subpackages(tmp_path: Path) -> None:
    """The obligation tests live one package down; the scope reaches them."""
    project = _project(
        tmp_path,
        {
            **_PACKAGE,
            "harness/obligations/test_sample.py": _with_noqa("import time\ntime.sleep(1)"),
        },
    )
    assert _fired(project) == [("harness/obligations/test_sample.py", "time.sleep")]


def test_the_bans_do_not_reach_harness_infrastructure(tmp_path: Path) -> None:
    """The seam, the poller and the transport alteration module may call what tests may not."""
    every_sample = "\n".join(_SAMPLES.values())
    project = _project(
        tmp_path,
        {
            **_PACKAGE,
            "harness/poll.py": every_sample,
            "harness/obligations/flow.py": every_sample,
        },
    )
    assert _fired(project) == []


def test_every_ban_in_the_configuration_has_a_sample() -> None:
    """A ban added without a companion sample, or a sample left for a removed ban, fails here."""
    config = tomllib.loads((_E2E_DIR / _CONFIG).read_text())
    banned = set(config["lint"]["flake8-tidy-imports"]["banned-api"])
    assert banned == set(_SAMPLES), {
        "without a sample": sorted(banned - set(_SAMPLES)),
        "sample for no ban": sorted(set(_SAMPLES) - banned),
    }


def test_make_lint_runs_the_bans_with_ignore_noqa() -> None:
    """The gate is ``make lint``'s command, so the arguments above must be its arguments.

    The root Makefile's ``lint`` (and through it ``quality`` and ``quality-ci``)
    descends into this directory's ``lint``, so this is the one recipe to check.
    """
    command = f"uv run ruff {' '.join(_BAN_ARGS)}"
    recipe = make_recipe((_E2E_DIR / "Makefile").read_text(), "lint")
    assert command in recipe, f"tests/e2e `make lint` runs {recipe}, not {command!r}"

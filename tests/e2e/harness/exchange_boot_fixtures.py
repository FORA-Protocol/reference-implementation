"""The rows each Exchange in the e2e stack starts with: its boot fixture (ADR-029).

Each Exchange service reads one file from ``fixtures/exchange-boot/``, named after
the service and mounted read-only at :data:`FIXTURE_MOUNT`, with the shared
listing terms mounted beside it at :data:`TERMS_MOUNT`. The Exchange creates what
its file declares when it is absent, so these files, not the seed, are where the
stack's seats, the demo payee, the sites and their ACTIVE listings come from.

This module reads the files for the guards that hold them to the compose file and
to the harness's constants. It reads plain YAML: what the Exchange makes of a
file is the Exchange's own parser's business, which its Go tests cover.
"""

from __future__ import annotations

from pathlib import Path
from typing import Any

import yaml

#: Where the files live, in the checkout and in the runner image alike.
FIXTURE_DIR = Path(__file__).resolve().parent / "fixtures" / "exchange-boot"

#: Where each Exchange reads its file: its EXCHANGE_BOOT_FIXTURE_FILE.
FIXTURE_MOUNT = "/boot/fixture.yml"

#: The terms file every listing names, mounted beside the fixture from testdata/,
#: where the Exchange's Go integration fixtures read the same document.
TERMS_FILE = "free-listing-terms.json"
TERMS_MOUNT = f"/boot/{TERMS_FILE}"
TERMS_SOURCE = f"./testdata/{TERMS_FILE}"


def fixture_source(service: str) -> str:
    """The compose mount source of ``service``'s boot fixture."""
    return f"./tests/e2e/harness/fixtures/exchange-boot/{service}.yml"


def read_boot_fixture(service: str) -> dict[str, Any]:
    """``service``'s boot fixture, as YAML reads it."""
    fixture: dict[str, Any] = yaml.safe_load((FIXTURE_DIR / f"{service}.yml").read_text())
    return fixture


def seat_domains(fixture: dict[str, Any]) -> set[str]:
    """The domains of the seats ``fixture`` declares."""
    return {str(seat["domain"]) for seat in fixture.get("seats") or []}

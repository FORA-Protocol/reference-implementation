"""Meta-guard — each Exchange's boot fixture agrees with the compose file and the harness.

Each Exchange in the stack creates its seats, the demo payee, the sites it sells
and their ACTIVE listings at start-up, from its boot fixture (ADR-029,
``harness/exchange_boot_fixtures.py``). The harness names the same things in its
constants and the compose file mounts the files, and when the three disagree the
failure reads as the platform being broken rather than as the configuration
mistake it is:

* MOUNTED. Each Exchange reads its own file, mounted read-only where its
  EXCHANGE_BOOT_FIXTURE_FILE points, with the shared terms file beside it. Without
  them the Exchange holds no seat, and every Register there is refused.
* THE SEAT. Each file declares its Exchange's seat under the id the harness names
  and the domain the Exchange answers on, which is also its
  EXCHANGE_DEFAULT_TENANT. A seat under any other domain is one Register never
  finds. This is the default-tenant check restated for the seat's new source.
* THE POLICIES. A seat refuses the Broker relay exactly where
  RELAY_REFUSING_EXCHANGES says, and activates new agents exactly as the seed's
  ``SEATS_ACTIVATE_NEW_AGENTS`` says, which every test's own agent relies on to be
  funded at Register. The check is agreement with that constant, so a change to
  the constant and every fixture together passes it; the e2e funding checks are
  what catch that.
* THE SECOND SEAT. exchange-a declares the second seat the tenant-isolation suite
  names from its surfaces, and no other Exchange declares any second seat.
* THE SITES. Each Exchange sells exactly the sites ``seed.E2E_PUBLISHERS`` places
  on it, each listed on the Exchange's seat with the shared terms. A site missing
  here is one the seed's ingest is refused for. A site's signing scheme is written
  in its fixture alone: the Exchange refuses to boot on a CloudFront RSA site with
  no key-pair id, and the edge in front of a site refuses a URL signed under the
  wrong scheme, which the suites that fetch the site's content would report.
* THE PAYEE. Every file declares the demo payee under the id the sites' manifests
  attest, which the catalog's resource-owner foreign key also needs.

Pure file reading: no stack, no Docker, no database.
"""

from __future__ import annotations

import copy
from collections.abc import Callable
from typing import Any

import pytest
import yaml

from .conftest import REPO_ROOT
from .e2e_catalog import DEMO_PUBLISHER_ID, DEMO_PUBLISHER_NAME
from .exchange_boot_fixtures import (
    FIXTURE_MOUNT,
    TERMS_FILE,
    TERMS_MOUNT,
    TERMS_SOURCE,
    fixture_source,
    read_boot_fixture,
)
from .exchanges import (
    EXCHANGE_A_DOMAIN,
    RELAY_REFUSING_EXCHANGES,
    SECOND_SEAT_OF_EXCHANGE_A,
    SECOND_SEAT_OF_EXCHANGE_A_DOMAIN,
    SERVICE_IDENTITIES,
    seat_tenant_id,
)
from .guard_harness import guard_marks
from .seed import E2E_PUBLISHERS
from .seed_accounts import SEATS_ACTIVATE_NEW_AGENTS

_COMPOSE = REPO_ROOT / "docker-compose.e2e.yml"

pytestmark = guard_marks(
    skip_when=not _COMPOSE.is_file(),
    reason="docker-compose.e2e.yml is absent; this guard reads it",
)


def _mount_problems(service: str, svc: dict[str, Any]) -> list[str]:
    env = svc.get("environment") or {}
    volumes = {str(v) for v in svc.get("volumes") or []}
    problems = []
    if env.get("EXCHANGE_BOOT_FIXTURE_FILE") != FIXTURE_MOUNT:
        problems.append(
            f"{service} reads its boot fixture from "
            f"{env.get('EXCHANGE_BOOT_FIXTURE_FILE')!r}, not {FIXTURE_MOUNT}"
        )
    for mount in (
        f"{fixture_source(service)}:{FIXTURE_MOUNT}:ro",
        f"{TERMS_SOURCE}:{TERMS_MOUNT}:ro",
    ):
        if mount not in volumes:
            problems.append(f"{service} does not mount {mount}")
    return problems


def _expected_seats(identity: str) -> dict[str, tuple[str, bool, bool]]:
    """The seats the Exchange answering as ``identity`` must declare: domain, relay, activation."""
    seats = {
        seat_tenant_id(identity): (
            identity,
            identity not in RELAY_REFUSING_EXCHANGES,
            SEATS_ACTIVATE_NEW_AGENTS,
        ),
    }
    if identity == EXCHANGE_A_DOMAIN:
        seats[SECOND_SEAT_OF_EXCHANGE_A] = (SECOND_SEAT_OF_EXCHANGE_A_DOMAIN, False, False)
    return seats


def _seat_problems(service: str, identity: str, svc: dict[str, Any], fixture: dict) -> list[str]:
    declared = {
        str(s.get("tenant_id")): (
            str(s.get("domain")),
            s.get("allow_broker_relay"),
            s.get("activate_new_agents_by_default"),
        )
        for s in fixture.get("seats") or []
    }
    expected = _expected_seats(identity)
    problems = [
        f"{service}'s boot fixture declares seat {tenant} as (domain, relay, activation) "
        f"{declared.get(tenant)}, not {want}"
        for tenant, want in sorted(expected.items())
        if declared.get(tenant) != want
    ]
    problems += [
        f"{service}'s boot fixture declares seat {tenant}, which the harness does not name"
        for tenant in sorted(set(declared) - set(expected))
    ]
    default_tenant = (svc.get("environment") or {}).get("EXCHANGE_DEFAULT_TENANT")
    if default_tenant != identity:
        problems.append(
            f"{service} names {default_tenant!r} as its default tenant, not its own seat's "
            f"domain {identity!r}, so Register finds no seat"
        )
    return problems


def _expected_listings(identity: str) -> dict[str, list[dict[str, str]]]:
    """The listings of each site the Exchange answering as ``identity`` sells, as its file writes them."""
    return {
        domain: [{"tenant_id": seat_tenant_id(identity), "terms_file": TERMS_FILE}]
        for domain, exchange, _feed, _key in E2E_PUBLISHERS
        if exchange == identity
    }


def _site_problems(service: str, identity: str, fixture: dict) -> list[str]:
    publishers = fixture.get("publishers") or []
    payees = [(p.get("publisher_id"), p.get("display_name")) for p in publishers]
    if payees != [(DEMO_PUBLISHER_ID, DEMO_PUBLISHER_NAME)]:
        return [
            f"{service}'s boot fixture declares the payees {payees}, not only "
            f"{(DEMO_PUBLISHER_ID, DEMO_PUBLISHER_NAME)}"
        ]
    declared = {str(s.get("domain")): s.get("listings") for s in publishers[0].get("sites") or []}
    expected = _expected_listings(identity)
    return [
        f"{service}'s boot fixture lists site {domain} as {declared.get(domain)}, not {want}"
        for domain, want in sorted(expected.items())
        if declared.get(domain) != want
    ] + [
        f"{service}'s boot fixture declares site {domain}, which seed.E2E_PUBLISHERS does not place on it"
        for domain in sorted(set(declared) - set(expected))
    ]


def _fixture_problems(services: dict[str, Any], fixtures: dict[str, dict]) -> list[str]:
    """Every way an Exchange's boot fixture disagrees with the compose file or the harness."""
    problems = []
    for service, identity in sorted(SERVICE_IDENTITIES.items()):
        svc = services.get(service) or {}
        fixture = fixtures.get(service) or {}
        problems += _mount_problems(service, svc)
        problems += _seat_problems(service, identity, svc, fixture)
        problems += _site_problems(service, identity, fixture)
    return problems


def _services() -> dict[str, Any]:
    services: dict[str, Any] = yaml.safe_load(_COMPOSE.read_text())["services"]
    return services


def _fixtures() -> dict[str, dict]:
    return {service: read_boot_fixture(service) for service in SERVICE_IDENTITIES}


def test_each_exchange_boots_the_seat_sites_and_payee_the_harness_names() -> None:
    """The compose file, the boot fixtures and the harness's constants agree."""
    assert _fixture_problems(_services(), _fixtures()) == []


# The edits below each break one thing the check exists to catch, on a copy of
# the real compose file and fixtures, and require the check to report exactly it.


def _seat(fixtures: dict, service: str, index: int = 0) -> dict:
    seat: dict = fixtures[service]["seats"][index]
    return seat


def _site(fixtures: dict, service: str) -> dict:
    site: dict = fixtures[service]["publishers"][0]["sites"][0]
    return site


def _wrong_seat_domain(services: dict, fixtures: dict) -> None:
    _seat(fixtures, "exchange-b")["domain"] = "exchange-b:9999"


def _relaying_exchange_d(services: dict, fixtures: dict) -> None:
    _seat(fixtures, "exchange-d")["allow_broker_relay"] = True


def _held_seat(services: dict, fixtures: dict) -> None:
    _seat(fixtures, "exchange-c")["activate_new_agents_by_default"] = False


def _writable_fixture_mount(services: dict, fixtures: dict) -> None:
    volumes = services["exchange"]["volumes"]
    i = volumes.index(f"{fixture_source('exchange')}:{FIXTURE_MOUNT}:ro")
    volumes[i] = f"{fixture_source('exchange')}:{FIXTURE_MOUNT}"


def _dropped_terms_mount(services: dict, fixtures: dict) -> None:
    services["exchange-d"]["volumes"].remove(f"{TERMS_SOURCE}:{TERMS_MOUNT}:ro")


def _dropped_listing(services: dict, fixtures: dict) -> None:
    _site(fixtures, "exchange-b")["listings"] = []


def _dropped_second_seat(services: dict, fixtures: dict) -> None:
    del fixtures["exchange"]["seats"][1]


def _other_payee(services: dict, fixtures: dict) -> None:
    fixtures["exchange-d"]["publishers"][0]["publisher_id"] = "someone-else"


def _default_tenant_elsewhere(services: dict, fixtures: dict) -> None:
    services["exchange-c"]["environment"]["EXCHANGE_DEFAULT_TENANT"] = "sfx.demo.fora-protocol.org"


@pytest.mark.parametrize(
    ("edit", "fragment"),
    [
        pytest.param(
            _wrong_seat_domain,
            "exchange-b's boot fixture declares seat tenant-seat-b",
            id="seat-domain",
        ),
        pytest.param(
            _relaying_exchange_d,
            "exchange-d's boot fixture declares seat tenant-seat-d",
            id="relay",
        ),
        pytest.param(
            _held_seat, "exchange-c's boot fixture declares seat tenant-seat-c", id="activation"
        ),
        pytest.param(
            _writable_fixture_mount,
            "exchange does not mount ./tests/e2e/harness",
            id="writable-mount",
        ),
        pytest.param(
            _dropped_terms_mount, "exchange-d does not mount ./testdata", id="terms-mount"
        ),
        pytest.param(_dropped_listing, "exchange-b's boot fixture lists site music", id="listing"),
        pytest.param(
            _dropped_second_seat,
            "exchange's boot fixture declares seat tenant-seat-a2",
            id="second-seat",
        ),
        pytest.param(_other_payee, "exchange-d's boot fixture declares the payees", id="payee"),
        pytest.param(
            _default_tenant_elsewhere,
            "exchange-c names 'sfx.demo.fora-protocol.org'",
            id="default-tenant",
        ),
    ],
)
def test_the_boot_fixture_check_reports_each_mistake(
    edit: Callable[[dict, dict], None], fragment: str
) -> None:
    """Each mistake the check exists to catch makes it report that mistake and nothing else."""
    services, fixtures = copy.deepcopy(_services()), copy.deepcopy(_fixtures())
    edit(services, fixtures)
    problems = _fixture_problems(services, fixtures)
    assert len(problems) == 1 and problems[0].startswith(fragment), problems

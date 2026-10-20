"""Meta-guard — the publisher suites' stack wiring must agree with itself and with production.

The publisher suites (``obligations/test_publisher_*.py``) rest on wiring no
running service checks, and each piece fails as something else entirely:

* every per-test site is a host under the sites zone, which only the wildcard-DNS
  sidecar resolves. If the sidecar points the zone anywhere but the ``sites``
  service, or a service that reads publisher sites (the console, the Exchange, the
  importer, the runner, and the onboarding preview where an overlay adds it) does
  not resolve through the sidecar, the suite fails as "no CDN detected" or a
  manifest check that cannot reach the site;
* the stack's ``importer`` job is meant to be the production importer. If its
  entrypoint, image, profile or settings drift from the production template, the
  suite tests a job production does not run;
* the importer and the console's seat price in one denomination. If they
  disagree, imported pages carry a currency the publisher was never shown;
* the contributor's private key is the importer's alone, apart from the host that
  publishes its public half.

This suite reads the compose files with a YAML parser and the production compose
template as text, and asserts each of those. It needs nothing brought up. The
production template is not copied into the e2e runner image, so the checks that
read it skip there and run in the per-commit tier.
"""

from __future__ import annotations

import re

import pytest
import yaml

from .conftest import REPO_ROOT
from .guard_harness import guard_marks
from .publisher_sites import SITE_ZONE
from .test_guards_identity_lists import proxy_zone_routes

_COMPOSE = REPO_ROOT / "docker-compose.e2e.yml"
#: The script every e2e compose call goes through, and the line in it that names
#: the optional overlay it adds when the tree carries one.
_E2E_STACK = REPO_ROOT / "scripts" / "e2e-stack.sh"
_OVERLAY_LINE = re.compile(r'^OPTIONAL_OVERLAY="\$ROOT/([^"]+)"$', re.MULTILINE)
_TEMPLATE = REPO_ROOT / "deploy/terraform/modules/compose-stack/templates/docker-compose.yml.tftpl"

pytestmark = guard_marks(skip_when=not _COMPOSE.is_file())

_SITES = "sites"
_IMPORTER = "importer"
_DNS = "dns"
_SEAT = "exchange"
#: The services that read a publisher's own site, and so must resolve the sites zone.
_SITE_READERS = ("console", _SEAT, _IMPORTER, "runner")
#: Site readers an optional overlay adds. Each is checked wherever the stack's
#: files define it: the runner image mounts only the base file, and a tree that
#: carries no overlay runs no such service.
_OPTIONAL_SITE_READERS = ("onboarding",)
#: The fixture holding the operator's contributor key, and the one host besides the
#: importer allowed to mount it: the contributor's key directory, which publishes
#: the public half.
_CONTRIBUTOR_KEY = "catalog_contributor_key.json"
_CONTRIBUTOR_DIRECTORY = "catalog-contributor-e2e-jwks"
_DENOMINATION = ("EXCHANGE_TERMS_CURRENCY", "EXCHANGE_TERMS_UNIT")
#: Settings the stack's importer adds for this network only: the address guard and
#: the plaintext refusal relaxed, and the log level the job's lines are read at.
_E2E_ONLY = frozenset({"SKIP_SSRF", "ALLOW_INSECURE", "EXCHANGE_LOG_LEVEL"})


def _services() -> dict:
    return yaml.safe_load(_COMPOSE.read_text())["services"]


def _stack_files() -> list:
    """The compose files the stack is composed from by default, in compose order.

    The base file, and the optional overlay ``scripts/e2e-stack.sh`` adds when
    the tree carries it; the overlay is read from that script, so the two cannot
    disagree about which file it is. Overlays a caller adds itself, such as CI's
    build cache, define no service of their own and are not read. Without the
    script (the e2e runner image) the base file is all there is.
    """
    files = [_COMPOSE]
    if _E2E_STACK.is_file():
        match = _OVERLAY_LINE.search(_E2E_STACK.read_text())
        assert match, f"{_E2E_STACK.name} names no OPTIONAL_OVERLAY to read the stack by"
        overlay = REPO_ROOT / match.group(1)
        if overlay.is_file():
            files.append(overlay)
    return files


def _stack_services() -> dict:
    """Every service the stack's compose files define, read in compose order.

    For a service that more than one file defines, a later file's value for a
    service key replaces the earlier file's whole. Compose merges deeper: it merges
    a mapping such as ``depends_on`` key by key, where this keeps only the later
    file's entries. So the result agrees with compose only for a key that at most
    one file sets on that service, and a caller must read no other key.
    """
    merged: dict = {}
    for path in _stack_files():
        for name, service in (yaml.safe_load(path.read_text()).get("services") or {}).items():
            merged[name] = {**merged.get(name, {}), **(service or {})}
    return merged


def _resolution_problems(services: dict, sidecar: str) -> dict:
    """The site readers that do not resolve through ``sidecar``, with what they use."""
    readers = [*_SITE_READERS, *(name for name in _OPTIONAL_SITE_READERS if name in services)]
    return {
        name: services.get(name, {}).get("dns")
        for name in readers
        if services.get(name, {}).get("dns") != [sidecar]
    }


def _wildcard_dns(services: dict) -> str:
    address = (services[_DNS].get("networks") or {}).get("default", {}).get("ipv4_address")
    assert address, f"the {_DNS!r} service holds no static address"
    return address


def test_the_sites_zone_reaches_the_sites_service_through_the_proxy() -> None:
    services = _services()
    sites = services.get(_SITES)
    assert sites is not None, f"no {_SITES!r} service: no per-test site would be served"
    address, routes = proxy_zone_routes(services)
    command = " ".join(services[_DNS].get("command") or [])
    zones = dict(re.findall(r"--address=/([^/\s]+)/(\S+)", command))
    assert zones.get(SITE_ZONE) == address, (
        f"the DNS sidecar answers {SITE_ZONE} with {zones.get(SITE_ZONE)!r}, but the "
        f"capture proxy, which serves the zone's https, holds {address!r}"
    )
    assert routes.get(SITE_ZONE) == _SITES, (
        f"the capture proxy routes {SITE_ZONE} to {routes.get(SITE_ZONE)!r}, not {_SITES!r}"
    )
    assert (sites.get("environment") or {}).get("SITE_ZONE") == SITE_ZONE, sites


def test_every_site_reader_resolves_through_the_sidecar() -> None:
    services = _stack_services()
    sidecar = _wildcard_dns(services)
    wrong = _resolution_problems(services, sidecar)
    assert not wrong, f"these services do not resolve {SITE_ZONE} through {sidecar}: {wrong}"


def test_an_overlay_site_reader_off_the_sidecar_is_reported() -> None:
    """An optional reader is held to the sidecar too, and only where it is defined.

    An overlay cannot read the base file's anchor, so it writes the sidecar's
    address out again; a copy that drifted would leave that service unable to
    reach any per-test site.
    """
    sidecar = "${FORA_E2E_NET}.153"
    base = {name: {"dns": [sidecar]} for name in _SITE_READERS}

    assert _resolution_problems(base, sidecar) == {}
    drifted = {**base, "onboarding": {"dns": ["${FORA_E2E_NET}.154"]}}
    assert _resolution_problems(drifted, sidecar) == {"onboarding": ["${FORA_E2E_NET}.154"]}


def test_the_runner_waits_for_the_sites_and_holds_the_provider_tokens_read_only() -> None:
    runner = _services()["runner"]
    deps = runner.get("depends_on") or {}
    assert deps.get(_SITES, {}).get("condition") == "service_healthy", deps
    # One admin token per login-provider instance: the developers' one mints the
    # MCP agents, the publishers' one the console publishers (publisher_accounts.py).
    volumes = runner.get("volumes") or []
    for mount in (
        "zitadel-bootstrap:/zitadel-bootstrap:ro",
        "zitadel-publishers:/zitadel-publishers:ro",
    ):
        assert mount in volumes, runner


def test_the_importer_is_a_profiled_job_on_the_seat_image() -> None:
    services = _services()
    importer = services.get(_IMPORTER)
    assert importer is not None, f"no {_IMPORTER!r} service in the stack"
    assert importer.get("entrypoint") == ["/fora-import"], importer
    assert importer.get("profiles") == [_IMPORTER], importer
    assert importer.get("build") == services[_SEAT].get("build"), (
        "the importer must run the image the exchange is built from"
    )
    environment = importer.get("environment") or {}
    assert environment.get("EXCHANGE_DOMAIN") == services[_SEAT]["environment"]["EXCHANGE_DOMAIN"]
    for name in _DENOMINATION:
        assert environment.get(name) == services[_SEAT]["environment"].get(name), name


def test_only_the_importer_and_its_directory_mount_the_contributor_key() -> None:
    mounting = {
        name
        for name, svc in _services().items()
        for volume in svc.get("volumes") or []
        if isinstance(volume, str) and volume.split(":", 1)[0].endswith(_CONTRIBUTOR_KEY)
    }
    assert mounting == {_IMPORTER, _CONTRIBUTOR_DIRECTORY}, mounting


def _template_importer_settings() -> set[str]:
    """The environment names the production template gives its importer."""
    text = _TEMPLATE.read_text()
    block = re.search(r"^  importer:\n(.*?)^  [a-z][a-z-]*:\n", text, re.DOTALL | re.MULTILINE)
    assert block, "could not find the importer service in the production template"
    names = set(re.findall(r"^      ([A-Z][A-Z0-9_]+):", block.group(1), re.MULTILINE))
    assert names, "the production importer block matched but no settings parsed"
    return names


@pytest.mark.skipif(not _TEMPLATE.is_file(), reason="the production template is not in this tree")
def test_the_importer_carries_the_production_settings_and_no_others() -> None:
    environment = set(_services()[_IMPORTER].get("environment") or {})
    production = _template_importer_settings()
    assert environment - _E2E_ONLY == production, (
        f"missing from the stack's importer: {sorted(production - environment)}; "
        f"not in production's: {sorted(environment - production - _E2E_ONLY)}"
    )

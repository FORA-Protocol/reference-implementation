"""Meta-guard — the three e2e identity lists must agree.

An e2e identity exists in three places no tool connects: the ``IDENTITIES``
list in ``scripts/gen-e2e-keys.sh`` (mints the private fixture), a per-kid
jwks service in ``docker-compose.e2e.yml`` (mounts the fixture, aliases the
kid), and that service's entry in a ``depends_on`` block so the stack waits
for it. One direction of a mismatch fails quietly: a fixture minted with no
jwks host resolves nowhere, which is exactly the shape of the deliberate
ghost-agent negative control — so an accidental omission reads as the
intended case and no test goes red.

This suite is the tool that connects the lists. It parses ``IDENTITIES`` out
of the real script (failing loudly if the extraction breaks) and the real
compose file with a YAML parser, then asserts:

* every minted identity is served by a service that mounts ITS fixture and
  aliases ITS kid — except the two documented exceptions (the ghost agent,
  served nowhere by design; the broker relay key, published by the Broker's
  own directory);
* the ghost agent stays hostless — no alias, no mount, anywhere;
* every jwks service is named in some service's ``depends_on``, so the stack
  actually waits for the directory hosts it relies on.

The per-test agents (``fresh_agent.py``, ADR-028 D7) are in none of these lists,
by design: each is minted at run time under a random host, so there is nothing
per agent to list, and this guard needs no edit when a test mints one. What they
all depend on is one mechanism, and the guard checks that instead:

* the wildcard-DNS sidecar resolves the agent zone to the capture proxy, the
  stack's TLS endpoint, and the proxy routes the zone to the ``agent-directory``
  service, so every agent host reaches the service over http and https;
* the sidecar keeps every zone it answers local, so no query for an agent host
  is forwarded out of the stack;
* the service serves the zone the harness mints agents in;
* the runner waits for the service before any test mints an agent.

Pure file reading: no stack, no Docker, no database.
"""

from __future__ import annotations

import re

import yaml

from .conftest import REPO_ROOT
from .fresh_agent import AGENT_ZONE
from .guard_harness import guard_marks

_KEYGEN = REPO_ROOT / "scripts" / "gen-e2e-keys.sh"
_COMPOSE = REPO_ROOT / "docker-compose.e2e.yml"

pytestmark = guard_marks(skip_when=not (_KEYGEN.is_file() and _COMPOSE.is_file()))

# Served nowhere by design — the negative control every resolution route must
# come up empty for.
_GHOST_KID = "ghost-agent-e2e"
# Published by the Broker's OWN WBA directory (the broker service mounts the
# fixture); it has no standalone jwks host on purpose.
_BROKER_RELAY_KID = "broker.broker-local.v1"


def _identities() -> list[tuple[str, str]]:
    """(kid, fixture filename) pairs lifted from the real IDENTITIES list.

    Extraction failing is itself a finding: the list moved or changed shape,
    and this guard is no longer reading the code that mints the fixtures.
    """
    text = _KEYGEN.read_text()
    block = re.search(r"^IDENTITIES = \[\n(.*?)^\]\n", text, re.DOTALL | re.MULTILINE)
    assert block, (
        "could not find the IDENTITIES list in scripts/gen-e2e-keys.sh — "
        "the list moved; fix this guard's extraction so it keeps reading the "
        "real one"
    )
    pairs = re.findall(r'\("([^"]+)", "([^"]+)"\)', block.group(1))
    assert pairs, "IDENTITIES block matched but no (kid, filename) tuples parsed"
    return pairs


def _compose_services() -> dict:
    return yaml.safe_load(_COMPOSE.read_text())["services"]


def _service_aliases(svc: dict) -> set[str]:
    networks = svc.get("networks") or {}
    if not isinstance(networks, dict):
        return set()
    out: set[str] = set()
    for net in networks.values():
        if isinstance(net, dict):
            out.update(net.get("aliases") or [])
    return out


def _mounted_fixtures(svc: dict) -> set[str]:
    """Basenames of host paths this service bind-mounts."""
    out: set[str] = set()
    for vol in svc.get("volumes") or []:
        if isinstance(vol, str):
            host = vol.split(":", 1)[0]
            out.add(host.rsplit("/", 1)[-1])
    return out


def test_every_minted_identity_is_served_and_depended_on() -> None:
    services = _compose_services()
    for kid, filename in _identities():
        if kid in (_GHOST_KID, _BROKER_RELAY_KID):
            continue
        serving = [
            name
            for name, svc in services.items()
            if kid in _service_aliases(svc) and filename in _mounted_fixtures(svc)
        ]
        assert serving, (
            f"identity {kid!r} is minted by gen-e2e-keys.sh but no compose "
            f"service both aliases it and mounts {filename!r} — its key would "
            "resolve nowhere, which is indistinguishable from the ghost-agent "
            "negative control"
        )


def test_broker_relay_fixture_is_mounted_into_the_broker() -> None:
    services = _compose_services()
    relay_file = dict(_identities())[_BROKER_RELAY_KID]
    broker = services.get("broker")
    assert broker is not None, "no broker service in docker-compose.e2e.yml"
    assert relay_file in _mounted_fixtures(broker), (
        f"the broker service no longer mounts {relay_file!r} — the relay key "
        "would be published nowhere (the Broker's own directory is its only "
        "publication path)"
    )


def test_ghost_agent_stays_hostless() -> None:
    services = _compose_services()
    ghost_file = dict(_identities())[_GHOST_KID]
    offenders = [
        name
        for name, svc in services.items()
        if _GHOST_KID in _service_aliases(svc) or ghost_file in _mounted_fixtures(svc)
    ]
    assert not offenders, (
        f"services {offenders} serve the ghost agent's identity — the negative "
        "control depends on that key being served NOWHERE"
    )


def test_every_jwks_service_is_depended_on() -> None:
    services = _compose_services()
    jwks = {name for name in services if name.endswith("-jwks") or name == "publisher-jwks"}
    assert jwks, "no jwks services found — the compose file changed shape"
    depended: set[str] = set()
    for svc in services.values():
        deps = svc.get("depends_on") or {}
        depended.update(deps.keys() if isinstance(deps, dict) else deps)
    orphans = sorted(jwks - depended)
    assert not orphans, (
        f"jwks services {orphans} appear in nobody's depends_on — the stack "
        "does not wait for them, so their identities resolve only by luck of "
        "startup order"
    )


# The service that serves every per-test agent's key directory, the DNS sidecar
# that points the agent zone at the capture proxy, and the proxy that routes it on.
_AGENT_DIRECTORY = "agent-directory"
_DNS = "dns"
_PROXY = "capture-proxy"


def proxy_zone_routes(services: dict) -> tuple[str, dict[str, str]]:
    """The capture proxy's static address and its zone-to-service table (CAPTURE_ZONES)."""
    proxy = services.get(_PROXY)
    assert proxy is not None, f"no {_PROXY!r} service in docker-compose.e2e.yml"
    address = (proxy.get("networks") or {}).get("default", {}).get("ipv4_address")
    assert address, f"the {_PROXY!r} service holds no static address to resolve to"
    table = str((proxy.get("environment") or {}).get("CAPTURE_ZONES", ""))
    routes = dict(item.strip().split("=", 1) for item in table.split(",") if "=" in item)
    return address, routes


def _dnsmasq_addresses(services: dict) -> dict[str, str]:
    """The ``--address=/zone/ip`` pairs the DNS sidecar's dnsmasq command answers with."""
    dns = services.get(_DNS)
    assert dns is not None, f"no {_DNS!r} service in docker-compose.e2e.yml"
    command = " ".join(dns.get("command") or [])
    pairs = dict(re.findall(r"--address=/([^/\s]+)/(\S+)", command))
    assert pairs, f"the {_DNS!r} service's command names no --address zone: {command!r}"
    return pairs


def test_every_answered_zone_is_kept_local() -> None:
    # dnsmasq answers only the record type an --address gives and forwards every
    # other query for the name. Without --local, an AAAA lookup of a fresh agent
    # host leaves the stack for the zone's public servers and can stall for a
    # whole resolver timeout, which an Exchange's agent-directory fetch does not
    # survive.
    services = _compose_services()
    command = " ".join(services[_DNS].get("command") or [])
    local = set(re.findall(r"--local=/([^/\s]+)/(?=\s|$)", command))
    forwarded = sorted(set(_dnsmasq_addresses(services)) - local)
    assert not forwarded, (
        f"the DNS sidecar answers {forwarded} with an --address but has no --local for "
        "them, so their AAAA and other queries are forwarded out of the stack"
    )


def test_agent_zone_reaches_the_agent_directory_through_the_proxy() -> None:
    services = _compose_services()
    assert services.get(_AGENT_DIRECTORY) is not None, (
        f"no {_AGENT_DIRECTORY!r} service in docker-compose.e2e.yml — no per-test "
        "agent's key directory would be served anywhere"
    )
    address, routes = proxy_zone_routes(services)
    zones = _dnsmasq_addresses(services)
    assert zones.get(AGENT_ZONE) == address, (
        f"the DNS sidecar answers {AGENT_ZONE} with {zones.get(AGENT_ZONE)!r}, but the "
        f"capture proxy, which serves the zone's https, holds {address!r}"
    )
    assert routes.get(AGENT_ZONE) == _AGENT_DIRECTORY, (
        f"the capture proxy routes {AGENT_ZONE} to {routes.get(AGENT_ZONE)!r}, not "
        f"{_AGENT_DIRECTORY!r} — every per-test agent's host would reach a service that "
        "does not serve its directory"
    )


def test_agent_directory_serves_the_zone_the_harness_mints_in() -> None:
    environment = _compose_services()[_AGENT_DIRECTORY].get("environment") or {}
    assert environment.get("AGENT_ZONE") == AGENT_ZONE, (
        f"{_AGENT_DIRECTORY!r} serves AGENT_ZONE={environment.get('AGENT_ZONE')!r}; the "
        f"harness mints agents under {AGENT_ZONE!r}, so it would refuse to register them"
    )


def test_the_runner_waits_for_the_agent_directory() -> None:
    runner = _compose_services().get("runner") or {}
    deps = runner.get("depends_on") or {}
    condition = deps.get(_AGENT_DIRECTORY, {}).get("condition") if isinstance(deps, dict) else None
    assert condition == "service_healthy", (
        f"the runner does not wait for {_AGENT_DIRECTORY!r} to be healthy, so the first "
        "test to mint an agent could register it before the service listens"
    )

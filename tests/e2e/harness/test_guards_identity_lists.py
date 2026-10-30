"""Meta-guard — the e2e identity lists and the key-directories service must agree.

An e2e identity exists in two places no tool connects: the ``IDENTITIES`` list
in ``scripts/gen-e2e-keys.sh`` (mints the private fixture), and the
``key-directories`` service in ``docker-compose.e2e.yml``, which mounts the
fixture at ``/keys/<kid>.json`` and answers to the kid as a network alias. One
direction of a mismatch fails quietly: a fixture minted with no host resolves
nowhere, so every call signed as it is refused unauthenticated, in whichever test
first signs as it, far from the missing line.

This suite is the tool that connects them. It parses ``IDENTITIES`` out of the
real script (failing loudly if the extraction breaks) and the real compose file
with a YAML parser, then asserts:

* the service mounts each key as one read-only file at ``/keys/<host>.json``
  and answers to exactly the hosts it holds keys for. A key with no alias, an
  alias with no key, and any other mount are each reported: a directory mount
  would carry every private key in it into the container;
* every minted identity is mounted from ITS fixture under ITS kid — except the
  broker relay key, which the Broker's own directory publishes;
* every publisher host ``PUBLISHER_HOSTS_JSON`` configures has a key;
* the hosts the in-network directory test checks are exactly the hosts the
  service answers to, so a host added to the service is checked there too;
* the four Exchanges, which fetch these directories, and the runner, whose
  tests sign as these identities, wait for the service to be healthy.

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

import copy
import json
import re
from collections.abc import Callable

import pytest
import yaml

from .conftest import REPO_ROOT
from .constants import KEY_DIRECTORIES_SERVICE, STATIC_KEY_HOSTS
from .fresh_agent import AGENT_ZONE
from .guard_harness import guard_marks

_KEYGEN = REPO_ROOT / "scripts" / "gen-e2e-keys.sh"
_COMPOSE = REPO_ROOT / "docker-compose.e2e.yml"

pytestmark = guard_marks(skip_when=not (_KEYGEN.is_file() and _COMPOSE.is_file()))

# Published by the Broker's OWN WBA directory (the broker service mounts the
# fixture); it has no standalone jwks host on purpose.
_BROKER_RELAY_KID = "broker.broker-local.v1"

# The one service that serves every fixed identity's key directory, and the
# services that must not start before it answers: the four Exchanges fetch those
# directories to verify signers, and the runner's tests sign as those identities.
_KEY_DIRECTORIES = KEY_DIRECTORIES_SERVICE
_DIRECTORY_WAITERS = ("exchange", "exchange-b", "exchange-c", "exchange-d", "runner")
# Where a key is mounted: one file per host, named after the host.
_KEY_TARGET = re.compile(r"/keys/(?P<host>[^/]+)\.json")


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


def _key_mounts(svc: dict) -> tuple[dict[str, str], list[str]]:
    """The fixture each host's key is mounted from, and every other mount as written.

    A key mount is one read-only file mounted at ``/keys/<host>.json``; the first
    mapping takes each such host to its fixture's basename. Anything else, a
    directory or a writable mount included, lands in the second list.
    """
    keys: dict[str, str] = {}
    others: list[str] = []
    for vol in svc.get("volumes") or []:
        parts = vol.split(":") if isinstance(vol, str) else []
        target = _KEY_TARGET.fullmatch(parts[1]) if len(parts) == 3 else None
        if target and parts[2] == "ro" and parts[0].endswith(".json"):
            keys[target.group("host")] = parts[0].rsplit("/", 1)[-1]
        else:
            others.append(str(vol))
    return keys, others


def _key_directory_problems(services: dict, identities: list[tuple[str, str]]) -> list[str]:
    """Every way the key-directories service fails to serve the minted identities."""
    svc = services.get(_KEY_DIRECTORIES)
    if svc is None:
        return [f"no {_KEY_DIRECTORIES} service serves the fixed identities' key directories"]
    keys, others = _key_mounts(svc)
    aliases = _service_aliases(svc)
    problems = [
        f"{_KEY_DIRECTORIES} mounts {vol!r}, not one key file, read-only, at /keys/<host>.json"
        for vol in others
    ]
    problems += [
        f"{_KEY_DIRECTORIES} holds a key for {host} but does not answer to {host}"
        for host in sorted(set(keys) - aliases)
    ]
    problems += [
        f"{_KEY_DIRECTORIES} answers to {host} but holds no key for it"
        for host in sorted(aliases - set(keys))
    ]
    for kid, filename in identities:
        if kid != _BROKER_RELAY_KID and keys.get(kid) != filename:
            problems.append(
                f"{kid} is served from {keys.get(kid)!r}, not its own fixture {filename!r}"
            )
    publishers = json.loads(str((svc.get("environment") or {}).get("PUBLISHER_HOSTS_JSON") or "{}"))
    problems += [
        f"PUBLISHER_HOSTS_JSON configures {host}, which has no key"
        for host in sorted(set(publishers) - set(keys))
    ]
    for waiter in _DIRECTORY_WAITERS:
        deps = (services.get(waiter) or {}).get("depends_on") or {}
        wait = (
            (deps.get(_KEY_DIRECTORIES) or {}).get("condition") if isinstance(deps, dict) else None
        )
        if wait != "service_healthy":
            problems.append(
                f"{waiter} waits for {_KEY_DIRECTORIES} with {wait!r}, not service_healthy"
            )
    return problems


def test_the_key_directories_serve_every_minted_identity() -> None:
    assert _key_directory_problems(_compose_services(), _identities()) == []


def test_the_directory_test_checks_every_host_the_service_answers_to() -> None:
    """STATIC_KEY_HOSTS, which the in-network directory test checks, names every alias."""
    aliases = _service_aliases(_compose_services().get(_KEY_DIRECTORIES) or {})
    assert set(STATIC_KEY_HOSTS) == aliases, (
        f"the directory test checks {sorted(STATIC_KEY_HOSTS)}, but {_KEY_DIRECTORIES} "
        f"answers to {sorted(aliases)}; a host only one of them names is either never "
        "checked or checked without being served"
    )


def _volume_edit(old: str, new: str | None) -> Callable[[dict], None]:
    """An edit that replaces the key-directories mount ``old`` with ``new``, or drops it."""

    def edit(services: dict) -> None:
        volumes = services[_KEY_DIRECTORIES]["volumes"]
        index = volumes.index(old)
        if new is None:
            del volumes[index]
        else:
            volumes[index] = new

    return edit


def _drop_alias(services: dict) -> None:
    services[_KEY_DIRECTORIES]["networks"]["default"]["aliases"].remove("agent-nobilling-e2e")


_CONTRIBUTOR_KEY = "./tests/e2e/harness/fixtures/catalog_contributor_key.json:/keys/catalog-contributor-e2e.json:ro"
_NOBILLING_KEY = (
    "./tests/e2e/harness/fixtures/agent_nobilling_e2e_key.json:/keys/agent-nobilling-e2e.json:ro"
)


def _swap_two_keys(services: dict) -> None:
    volumes = services[_KEY_DIRECTORIES]["volumes"]
    first = volumes.index(_CONTRIBUTOR_KEY)
    second = volumes.index(_NOBILLING_KEY)
    volumes[first] = (
        "./tests/e2e/harness/fixtures/agent_nobilling_e2e_key.json:/keys/catalog-contributor-e2e.json:ro"
    )
    volumes[second] = (
        "./tests/e2e/harness/fixtures/catalog_contributor_key.json:/keys/agent-nobilling-e2e.json:ro"
    )


def _mount_the_fixtures_directory(services: dict) -> None:
    services[_KEY_DIRECTORIES]["volumes"].append("./tests/e2e/harness/fixtures:/keys:ro")


def _publisher_without_a_key(services: dict) -> None:
    env = services[_KEY_DIRECTORIES]["environment"]
    publishers = json.loads(env["PUBLISHER_HOSTS_JSON"])
    publishers["stoa.example"] = {"exchanges": []}
    env["PUBLISHER_HOSTS_JSON"] = json.dumps(publishers)


def _runner_does_not_wait(services: dict) -> None:
    services["runner"]["depends_on"][_KEY_DIRECTORIES]["condition"] = "service_started"


@pytest.mark.parametrize(
    ("edit", "problems"),
    [
        pytest.param(
            _swap_two_keys,
            [
                "catalog-contributor-e2e is served from 'agent_nobilling_e2e_key.json', not its "
                "own fixture 'catalog_contributor_key.json'",
                "agent-nobilling-e2e is served from 'catalog_contributor_key.json', not its own "
                "fixture 'agent_nobilling_e2e_key.json'",
            ],
            id="two-keys-swapped",
        ),
        pytest.param(
            _drop_alias,
            [
                "key-directories holds a key for agent-nobilling-e2e but does not answer to agent-nobilling-e2e"
            ],
            id="alias-dropped",
        ),
        pytest.param(
            _volume_edit(_NOBILLING_KEY, None),
            [
                "key-directories answers to agent-nobilling-e2e but holds no key for it",
                "agent-nobilling-e2e is served from None, not its own fixture "
                "'agent_nobilling_e2e_key.json'",
            ],
            id="key-dropped",
        ),
        pytest.param(
            _volume_edit(_NOBILLING_KEY, _NOBILLING_KEY.removesuffix(":ro")),
            [
                "key-directories mounts './tests/e2e/harness/fixtures/agent_nobilling_e2e_key.json:"
                "/keys/agent-nobilling-e2e.json', not one key file, read-only, at /keys/<host>.json",
                "key-directories answers to agent-nobilling-e2e but holds no key for it",
                "agent-nobilling-e2e is served from None, not its own fixture "
                "'agent_nobilling_e2e_key.json'",
            ],
            id="key-writable",
        ),
        pytest.param(
            _mount_the_fixtures_directory,
            [
                "key-directories mounts './tests/e2e/harness/fixtures:/keys:ro', not one key "
                "file, read-only, at /keys/<host>.json"
            ],
            id="directory-mounted",
        ),
        pytest.param(
            _publisher_without_a_key,
            ["PUBLISHER_HOSTS_JSON configures stoa.example, which has no key"],
            id="publisher-without-a-key",
        ),
        pytest.param(
            _runner_does_not_wait,
            ["runner waits for key-directories with 'service_started', not service_healthy"],
            id="runner-does-not-wait",
        ),
    ],
)
def test_the_key_directory_check_refuses_each_mistake(
    edit: Callable[[dict], None], problems: list[str]
) -> None:
    """Each mistake the check exists to catch makes it report exactly that mistake."""
    services = copy.deepcopy(_compose_services())
    edit(services)
    assert _key_directory_problems(services, _identities()) == problems


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

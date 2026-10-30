"""Meta-guard — the harness and the compose stack agree about the exchanges.

Several things have to line up between ``harness/exchanges.py``, the seed, and
``docker-compose.e2e.yml``, and each of them fails in a way that reads as the
platform being broken rather than as the configuration mistake it is.

IDENTITY. One constant per exchange says who it is, and every addressed
request this suite sends carries one of them as its recipient. An exchange
refuses a request naming anything else, so a renamed service or a retyped
``EXCHANGE_DOMAIN`` has every request in the suite refused at once.

THE DEFAULT TENANT. A registration reads its "activate new agents
automatically" policy from the tenant named by ``EXCHANGE_DEFAULT_TENANT``. A
tenant row is the Exchange SEAT, so the only name that matches one is the
exchange's own hostname — its ``EXCHANGE_DOMAIN``. Point the setting anywhere
else, at a publisher's domain the way it read while a tenant meant a publisher,
and it matches no row: every ``fora_register`` there is refused with a
failed-precondition, and the exchange says so only in a boot warning nobody
reads. The stack states the value rather than leaving the binary to fall back to
``EXCHANGE_DOMAIN`` on its own, so the seat each exchange reads is written where
this guard and the exchange's boot fixture, which creates the seat, can be
compared against it.

THE EXCHANGE POLICY. The identity service refuses to sign and send to a domain
outside ``IDENTITY_MCP_EXCHANGE_ALLOWLIST``. It names every exchange here so
the lever runs in its production posture rather than sitting empty; an exchange
added to the stack and not to the list is refused by policy, which reads as a
routing failure.

THE RELAY OPT-OUT. Each exchange's boot fixture declares ``allow_broker_relay``
false on the seats ``RELAY_REFUSING_EXCHANGES`` names and true on every other
(``test_guards_exchange_boot_fixtures.py``). A name there that the stack does not
run would leave the money-path suite asserting a refusal from an exchange that
does not exist, and every other exchange accepting the relay is what the rest of
the suite buys through.

THE LEDGER. Every exchange bills on the stack's one TigerBeetle cluster, which
is what keeps four ledgers' worth of memory out of the stack. A TigerBeetle id
is unique per cluster, and the platform fee and liquidity accounts and a
resource owner's revenue account are derived from fixed names, so each exchange
salts every id it derives with its own namespace,
``EXCHANGE_BILLING_TB_ID_NAMESPACE``. Two exchanges with the same namespace
would post into the same accounts, and every balance test would still pass,
because the balance suites read only the buyer's own account. The namespace is
the exchange's service name, so the namespaces differ by construction. The
address is an IP written in two places — the ledger's static address and the
exchanges' shared setting — because the client takes no hostname, so it is
checked against the ledger the exchanges wait for.

THE BROKER REGISTRY. The Broker reads the exchanges it routes to from its
registry file (``fixtures/broker_registry.yml``) at boot. An exchange missing
from it is never routed to, and an endpoint that is not the exchange's
``EXCHANGE_PUBLIC_ORIGIN`` fails the relay's endpoint check, which reads as the
relay being broken.

THE EXCHANGE IMPOSTOR. The same file names two identities of the
``misbehaving-exchange`` service, each at the trust level the fault-path suites
rely on: one the Broker pays, one it only knows. Its third identity must stay out
of the file, because the suite uses it as the Exchange the Broker has never heard
of. The service answers as exactly the identities ``harness/misbehaving_exchange.py``
names, and the compose network resolves each of their hosts to it. A drift in any
of those reads as a Broker that refuses, or buys, for the wrong reason.

THE SHIPPED BUNDLE. The Terraform module renders one more compose file, for a
deployment, and its identity service is bound by the same policy. That file is a
template rather than YAML, so it is read line by line, and the same line reader
is turned on the e2e compose file too: one exchange there is read the same way as
the four here, and the reader is proven below to see every ``EXCHANGE_DOMAIN`` a
file declares rather than the first one only.

Pure file reading: no stack, no Docker, no database.
"""

from __future__ import annotations

import copy
import re
from collections.abc import Callable
from pathlib import Path
from urllib.parse import urlsplit

import pytest
import yaml

from .conftest import REPO_ROOT
from .exchange_boot_fixtures import read_boot_fixture, seat_domains
from .exchanges import (
    SERVICE_IDENTITIES,
    EXCHANGE_A_DOMAIN,
    EXCHANGE_A_INTERNAL_URL,
    EXCHANGE_B_DOMAIN,
    EXCHANGE_B_INTERNAL_URL,
    EXCHANGE_C_DOMAIN,
    EXCHANGE_C_INTERNAL_URL,
    EXCHANGE_D_DOMAIN,
    EXCHANGE_D_INTERNAL_URL,
    RELAY_REFUSING_EXCHANGES,
)
from .guard_harness import guard_marks
from .misbehaving_exchange import IDENTITIES as IMPOSTOR_IDENTITIES
from .misbehaving_exchange import REGISTERED_TRUST as IMPOSTOR_TRUST
from .misbehaving_exchange import SERVICE as IMPOSTOR_SERVICE

_COMPOSE = REPO_ROOT / "docker-compose.e2e.yml"
_TEMPLATE = REPO_ROOT / "deploy/terraform/modules/compose-stack/templates/docker-compose.yml.tftpl"

# The scalar assignments the line reader collects. EXCHANGE_DOMAIN occurs once per
# exchange service; the identity service's allowlist occurs once.
_IDENTITY_LINE = re.compile(r"\s*(EXCHANGE_DOMAIN|IDENTITY_MCP_EXCHANGE_ALLOWLIST)\s*:\s*(.+)")

pytestmark = guard_marks(
    skip_when=not _COMPOSE.is_file(),
    reason="docker-compose.e2e.yml is absent from the e2e runner image; "
    "this guard runs on the host",
)


def _compose_environment(service: str) -> dict[str, str]:
    """The environment block ``service`` declares, as a mapping."""
    services = yaml.safe_load(_COMPOSE.read_text())["services"]
    svc = services.get(service)
    assert svc is not None, (
        f"no {service!r} service in docker-compose.e2e.yml — the harness addresses "
        f"requests to an exchange this stack no longer runs"
    )
    env = svc.get("environment") or {}
    assert isinstance(env, dict), (
        f"{service!r} declares its environment as a list; this guard reads the "
        f"mapping form, so it can no longer see EXCHANGE_DOMAIN"
    )
    return {str(k): str(v) for k, v in env.items()}


def test_each_exchange_answers_to_the_identity_the_harness_names() -> None:
    for service, expected in SERVICE_IDENTITIES.items():
        published = _compose_environment(service).get("EXCHANGE_DOMAIN")
        assert published == expected, (
            f"{service!r} runs with EXCHANGE_DOMAIN={published!r} while the harness "
            f"addresses its requests to {expected!r}. The exchange refuses every "
            f"request naming anything else, so this disagreement fails the whole "
            f"suite as a protocol rejection. Fix the constant in harness/exchanges.py "
            f"or the compose value, whichever moved"
        )


def test_the_in_network_url_of_each_exchange_names_that_exchange() -> None:
    """The URL and the identity are derived from one constant; this pins that.

    An exchange is reached at its own identity inside the compose network, so
    the two must stay the same string. Splitting them is how a request ends up
    dialled at one exchange and addressed to another.
    """
    for url, domain in (
        (EXCHANGE_A_INTERNAL_URL, EXCHANGE_A_DOMAIN),
        (EXCHANGE_B_INTERNAL_URL, EXCHANGE_B_DOMAIN),
        (EXCHANGE_C_INTERNAL_URL, EXCHANGE_C_DOMAIN),
        (EXCHANGE_D_INTERNAL_URL, EXCHANGE_D_DOMAIN),
    ):
        assert url == f"http://{domain}", (
            f"the in-network URL {url!r} does not name the exchange {domain!r} it "
            f"reaches; a request dialled there would be addressed to somebody else"
        )


def test_each_exchange_names_its_own_seat_as_the_default_tenant() -> None:
    """A registration needs a tenant row, and only the exchange's own seat is one."""
    for service, identity in SERVICE_IDENTITIES.items():
        configured = _compose_environment(service).get("EXCHANGE_DEFAULT_TENANT")
        assert configured, (
            f"{service!r} sets no EXCHANGE_DEFAULT_TENANT. The binary then falls back to "
            f"its own EXCHANGE_DOMAIN, so nothing in the compose file states which seat "
            f"this exchange reads and neither this guard nor the seed can be compared "
            f"against it. State it"
        )
        assert configured == identity, (
            f"{service!r} names tenant {configured!r} while its own seat is {identity!r}. "
            f"A tenant row is the Exchange seat, keyed on the exchange's own hostname, so "
            f"{configured!r} matches no row: every fora_register there is refused with a "
            f"failed-precondition, and the exchange says so only in a boot warning"
        )
        declared = seat_domains(read_boot_fixture(service))
        assert configured in declared, (
            f"{service!r} names seat {configured!r}, which its boot fixture never creates; "
            f"it declares {sorted(declared)}"
        )


def test_the_identity_policy_names_every_exchange_in_the_stack() -> None:
    """The allowlist confines the identity service; it must not confine it out of this stack."""
    raw = _compose_environment("identity").get("IDENTITY_MCP_EXCHANGE_ALLOWLIST")
    assert raw is not None, (
        "the identity service sets no IDENTITY_MCP_EXCHANGE_ALLOWLIST. An empty policy "
        "permits every exchange and the suite still passes, which is the point of "
        "setting one here: the lever runs in the posture a deployment runs it in"
    )
    listed = {entry.strip() for entry in raw.split(",") if entry.strip()}
    assert listed == set(SERVICE_IDENTITIES.values()), (
        f"the identity policy lists {sorted(listed)} while the stack runs "
        f"{sorted(SERVICE_IDENTITIES.values())}. An exchange missing from the list is refused "
        f"before anything is dialled, which reads as a routing failure"
    )


def test_every_relay_refusing_exchange_runs_in_the_stack() -> None:
    """The seats whose boot fixtures opt out of the Broker relay belong to exchanges the stack runs."""
    unknown = sorted(RELAY_REFUSING_EXCHANGES - set(SERVICE_IDENTITIES.values()))
    assert not unknown, (
        f"RELAY_REFUSING_EXCHANGES names {unknown}, which the stack does not run; no boot "
        f"fixture opts that seat out, so the refusal under test cannot happen"
    )
    accepting = set(SERVICE_IDENTITIES.values()) - RELAY_REFUSING_EXCHANGES
    assert accepting, "every exchange refuses the Broker relay, so nothing can be bought through it"


def _compose_services() -> dict:
    services: dict = yaml.safe_load(_COMPOSE.read_text())["services"]
    return services


def _ledger_ip(service: dict) -> str | None:
    """The static address a ledger service holds on the default network."""
    networks = service.get("networks") or {}
    default = networks.get("default") if isinstance(networks, dict) else None
    return default.get("ipv4_address") if isinstance(default, dict) else None


def _ledger_problems(services: dict) -> list[str]:
    """Every way the exchanges fail to share the stack's one ledger safely.

    Each problem is a sentence naming the service and what it does wrong. The
    stack must run exactly one TigerBeetle service. Every exchange bills on it
    with the TigerBeetle adapter, waits for it to be healthy (an exchange refuses
    to boot when its ledger does not answer), dials its address, and salts its
    ids with its own service name.
    """
    ledgers = sorted(
        name
        for name, svc in services.items()
        if str((svc or {}).get("image", "")).startswith("ghcr.io/tigerbeetle/")
    )
    if len(ledgers) != 1:
        return [f"the stack runs {len(ledgers)} TigerBeetle services {ledgers}, not one"]
    [ledger] = ledgers
    address = f"{_ledger_ip(services[ledger])}:3000"
    problems = []
    for service in sorted(SERVICE_IDENTITIES):
        svc = services.get(service) or {}
        env = svc.get("environment") or {}
        deps = svc.get("depends_on") or {}
        wait = (deps.get(ledger) or {}).get("condition") if isinstance(deps, dict) else None
        if env.get("FORA_BILLING_ADAPTER") != "tigerbeetle":
            problems.append(
                f"{service} bills with {env.get('FORA_BILLING_ADAPTER')!r}, not tigerbeetle"
            )
        if wait != "service_healthy":
            problems.append(f"{service} waits for {ledger} with {wait!r}, not service_healthy")
        if env.get("EXCHANGE_BILLING_TB_ADDRESS") != address:
            problems.append(
                f"{service} dials {env.get('EXCHANGE_BILLING_TB_ADDRESS')!r}, not {address}"
            )
        if env.get("EXCHANGE_BILLING_TB_ID_NAMESPACE") != service:
            problems.append(
                f"{service} salts its ids with {env.get('EXCHANGE_BILLING_TB_ID_NAMESPACE')!r}, "
                f"not its own service name"
            )
    return problems


def test_the_exchanges_share_one_ledger_under_their_own_namespaces() -> None:
    """One TigerBeetle for every exchange, each salting its ids with its service name."""
    assert _ledger_problems(_compose_services()) == []


def _env_edit(service: str, key: str, value: str | None) -> Callable[[dict], None]:
    """An edit that sets ``key`` in ``service``'s environment, or removes it."""

    def edit(services: dict) -> None:
        env = services[service]["environment"]
        if value is None:
            del env[key]
        else:
            env[key] = value

    return edit


def _second_ledger(services: dict) -> None:
    services["tigerbeetle-2"] = copy.deepcopy(services["tigerbeetle"])


def _started_not_healthy(services: dict) -> None:
    services["exchange"]["depends_on"]["tigerbeetle"]["condition"] = "service_started"


@pytest.mark.parametrize(
    ("edit", "problem"),
    [
        pytest.param(
            _second_ledger,
            "the stack runs 2 TigerBeetle services ['tigerbeetle', 'tigerbeetle-2'], not one",
            id="second-ledger",
        ),
        pytest.param(
            _env_edit("exchange-c", "EXCHANGE_BILLING_TB_ID_NAMESPACE", "exchange-b"),
            "exchange-c salts its ids with 'exchange-b', not its own service name",
            id="repeated-namespace",
        ),
        pytest.param(
            _env_edit("exchange-d", "EXCHANGE_BILLING_TB_ID_NAMESPACE", None),
            "exchange-d salts its ids with None, not its own service name",
            id="missing-namespace",
        ),
        pytest.param(
            _env_edit("exchange", "EXCHANGE_BILLING_TB_ID_NAMESPACE", "exchange-a"),
            "exchange salts its ids with 'exchange-a', not its own service name",
            id="namespace-not-the-service-name",
        ),
        pytest.param(
            _env_edit("exchange-b", "EXCHANGE_BILLING_TB_ADDRESS", "${FORA_E2E_NET}.212:3000"),
            "exchange-b dials '${FORA_E2E_NET}.212:3000', not ${FORA_E2E_NET}.211:3000",
            id="address-mismatch",
        ),
        pytest.param(
            _started_not_healthy,
            "exchange waits for tigerbeetle with 'service_started', not service_healthy",
            id="started-not-healthy",
        ),
        pytest.param(
            _env_edit("exchange-d", "FORA_BILLING_ADAPTER", "inmemory"),
            "exchange-d bills with 'inmemory', not tigerbeetle",
            id="not-tigerbeetle",
        ),
    ],
)
def test_the_ledger_check_refuses_each_unsafe_way_to_share(
    edit: Callable[[dict], None], problem: str
) -> None:
    """Each mistake the ledger check exists to catch makes it report exactly that mistake."""
    services = copy.deepcopy(_compose_services())
    edit(services)
    assert _ledger_problems(services) == [problem]


_BROKER_REGISTRY = Path(__file__).resolve().parent / "fixtures" / "broker_registry.yml"


def test_the_broker_registry_routes_to_each_exchange_at_its_public_origin() -> None:
    """The Broker's registry file names every exchange, at the origin that exchange publishes.

    The Broker reads its registry from that file at boot, and its relay compares
    the endpoint an exchange's well-known advertises against the registered one.
    An exchange missing from the file is one the Broker never routes to; an
    endpoint that is not the exchange's EXCHANGE_PUBLIC_ORIGIN fails that check
    on every relayed purchase.
    """
    broker = yaml.safe_load(_COMPOSE.read_text())["services"]["broker"]
    path = _compose_environment("broker").get("BROKER_REGISTRY_FILE")
    mounts = [str(v) for v in broker.get("volumes") or []]
    assert path and any(
        m.startswith("./tests/e2e/harness/fixtures/broker_registry.yml:" + path) for m in mounts
    ), f"the broker reads its registry from {path!r}, which is not the registry file: {mounts}"
    entries = yaml.safe_load(_BROKER_REGISTRY.read_text())["exchanges"]
    assert len({entry["domain"] for entry in entries}) == len(entries), (
        f"an exchange is registered twice: {entries}"
    )
    registered = {
        entry["domain"]: entry["endpoint"]
        for entry in entries
        if entry["domain"] not in IMPOSTOR_IDENTITIES
    }
    assert set(registered) == set(SERVICE_IDENTITIES.values()), (
        f"the broker registry names {sorted(registered)}, but the stack runs "
        f"{sorted(SERVICE_IDENTITIES.values())}"
    )
    for service, domain in SERVICE_IDENTITIES.items():
        origin = _compose_environment(service).get("EXCHANGE_PUBLIC_ORIGIN")
        assert registered[domain] == origin, (
            f"the broker registry routes {domain} to {registered[domain]!r}, but {service!r} "
            f"publishes EXCHANGE_PUBLIC_ORIGIN={origin!r}"
        )


def test_the_broker_registry_names_the_impostor_identities_at_their_trust_levels() -> None:
    """The registry names the impostor's paid and known identities, and not its unknown one.

    Each named identity is registered at the impostor's own origin for it, at the
    trust level the fault-path suites assume. The identity the registry leaves
    out is the one the suite buys from as an Exchange the Broker never heard of.
    """
    entries = yaml.safe_load(_BROKER_REGISTRY.read_text())["exchanges"]
    named = {
        entry["domain"]: (entry["endpoint"], entry["trust_level"])
        for entry in entries
        if entry["domain"] in IMPOSTOR_IDENTITIES
    }
    want = {domain: (f"http://{domain}", trust) for domain, trust in IMPOSTOR_TRUST.items()}
    assert named == want, (
        f"the broker registry names the impostor as {named}, but the fault-path suites "
        f"rely on {want} and on every other impostor identity being absent"
    )


def test_the_impostor_answers_as_every_identity_the_harness_names() -> None:
    """The service answers as the harness's identities, and the network resolves each to it."""
    identities = _compose_environment(IMPOSTOR_SERVICE).get("IMPOSTOR_IDENTITIES", "")
    assert tuple(identities.split(",")) == IMPOSTOR_IDENTITIES, (
        f"{IMPOSTOR_SERVICE!r} answers as {identities!r}, but the harness names "
        f"{IMPOSTOR_IDENTITIES}"
    )
    service = yaml.safe_load(_COMPOSE.read_text())["services"][IMPOSTOR_SERVICE]
    aliases = set(service["networks"]["default"]["aliases"]) | {IMPOSTOR_SERVICE}
    hosts = {str(urlsplit(f"http://{identity}").hostname) for identity in IMPOSTOR_IDENTITIES}
    assert hosts <= aliases, (
        f"the compose network resolves {sorted(aliases)} to {IMPOSTOR_SERVICE!r}, "
        f"but the harness reaches it as {sorted(hosts)}"
    )


def _published_identities(source: str) -> tuple[set[str], str | None]:
    """Every EXCHANGE_DOMAIN in ``source``, plus the identity service's allowlist.

    A line reader rather than a YAML parse because the Terraform template carries
    directives YAML will not take. Every occurrence is kept: an exchange whose
    identity the reader dropped would be one the allowlist could omit unnoticed.
    """
    domains: set[str] = set()
    allowlist = None
    for line in source.splitlines():
        match = _IDENTITY_LINE.match(line)
        if not match:
            continue
        value = str(yaml.safe_load(match[2]))
        if match[1] == "EXCHANGE_DOMAIN":
            domains.add(value)
        else:
            allowlist = value
    return domains, allowlist


def _assert_identity_names_every_exchange(source: str) -> None:
    domains, allowlist = _published_identities(source)
    assert domains, "no exchange publishes an EXCHANGE_DOMAIN"
    listed = {entry.strip() for entry in (allowlist or "").split(",") if entry.strip()}
    missing = sorted(domains - listed)
    assert not missing, (
        f"the identity allowlist {sorted(listed)} does not name {missing}; the "
        f"identity service refuses to sign for an exchange outside it"
    )


@pytest.mark.parametrize(
    "path",
    [
        _COMPOSE,
        pytest.param(
            _TEMPLATE,
            marks=pytest.mark.skipif(
                not _TEMPLATE.is_file(),
                reason="deploy/ is absent from the e2e runner image; this guard runs on the host",
            ),
        ),
    ],
    ids=["e2e-compose", "terraform-template"],
)
def test_identity_names_every_exchange_it_ships_with(path: Path) -> None:
    _assert_identity_names_every_exchange(path.read_text())


_TWO_EXCHANGES = (
    "  EXCHANGE_DOMAIN: \"exchange.example\"\n  EXCHANGE_DOMAIN: 'exchange-b.example'\n"
)


@pytest.mark.parametrize(
    "allowlist",
    ['"exchange.example,exchange-b.example"', "exchange-b.example, exchange.example # both"],
)
def test_reader_accepts_an_allowlist_naming_every_exchange(allowlist: str) -> None:
    _assert_identity_names_every_exchange(
        f"{_TWO_EXCHANGES}  IDENTITY_MCP_EXCHANGE_ALLOWLIST: {allowlist}\n"
    )


@pytest.mark.parametrize(
    ("setting", "complaint"),
    [
        ("", "does not name"),
        # Only the FIRST exchange is listed: the shape a first-match reader passes.
        ("  IDENTITY_MCP_EXCHANGE_ALLOWLIST: exchange.example", "exchange-b.example"),
        ("  IDENTITY_MCP_EXCHANGE_ALLOWLIST: exchange-internal:8081", "does not name"),
    ],
)
def test_reader_refuses_an_allowlist_missing_an_exchange(setting: str, complaint: str) -> None:
    with pytest.raises(AssertionError, match=complaint):
        _assert_identity_names_every_exchange(f"{_TWO_EXCHANGES}{setting}\n")

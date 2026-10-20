"""The identity each exchange in the stack publishes, and the URL it answers on.

The two are different things and the stack is where that is easiest to see. An
exchange's IDENTITY is the value it stamps into the offers it issues and the
name it answers to as the recipient of a request — ``EXCHANGE_DOMAIN`` in
``docker-compose.e2e.yml``. Its URL is wherever the suite can reach it, which
inside the compose network is the same string and from the host is an ephemeral
``127.0.0.1`` port that names no exchange at all.

Every addressed request carries the identity, so a suite that has a URL and needs
to say who it is talking to comes here. This module holds the four identities,
the in-network URL built from each, and the mapping between a URL and an identity
in both directions. Other views of the same exchanges exist elsewhere — the
seed maps an identity to the database that exchange owns — and each of them
derives its keys from the constants here rather than respelling them.

The SEAT each exchange runs is here too. A ``fora.tenants`` row is the Exchange
seat rather than a publisher, so the seat's domain IS the exchange identity
below, and the seat's tenant id is a property of the exchange rather than of any
publisher.
"""

from __future__ import annotations

from functools import lru_cache
from urllib.parse import SplitResult, urlsplit

from .stack_urls import StackURLs

# Per-exchange identity domains. These MUST equal the EXCHANGE_DOMAIN values in
# docker-compose.e2e.yml: an exchange refuses a request that names anything else.
EXCHANGE_A_DOMAIN = "exchange:8081"
EXCHANGE_B_DOMAIN = "exchange-b:8081"
# exchange-c is the account suite's RESERVED never-registered target: no test
# may open an account there. Three of that suite's assertions are claims about a
# total — this agent has no account here — and a refusal cannot be observed
# anywhere the agent already holds one, because Register is idempotent. See the
# docstring of test_multi_exchange_mcp and exchange-c's own compose block.
EXCHANGE_C_DOMAIN = "exchange-c:8081"
# exchange-d is the seat that REFUSES purchases the Broker relays: the seed
# writes it with allow_broker_relay = FALSE. It sells the archive site only.
EXCHANGE_D_DOMAIN = "exchange-d:8081"

# The URL each exchange answers on INSIDE the compose network, where the host and
# the identity are the same string. Derived rather than written out, so the two
# cannot come apart: a service renamed in compose changes one constant above and
# both the identity and the URL follow. A host run reaches the same exchanges
# through ephemeral 127.0.0.1 ports instead — ``StackURLs`` carries those.
EXCHANGE_A_INTERNAL_URL = f"http://{EXCHANGE_A_DOMAIN}"
EXCHANGE_B_INTERNAL_URL = f"http://{EXCHANGE_B_DOMAIN}"
EXCHANGE_C_INTERNAL_URL = f"http://{EXCHANGE_C_DOMAIN}"
EXCHANGE_D_INTERNAL_URL = f"http://{EXCHANGE_D_DOMAIN}"


def exchange_url(stack: StackURLs, domain: str) -> str:
    """The URL this stack reaches ``domain`` at.

    The harness's in-network equivalent of the well-known endpoint resolution a
    real client does: an offer names an exchange by domain, and the caller has to
    turn that into somewhere to dial.
    """
    return {
        EXCHANGE_A_DOMAIN: stack.exchange,
        EXCHANGE_B_DOMAIN: stack.exchange_b,
        EXCHANGE_C_DOMAIN: stack.exchange_c,
        EXCHANGE_D_DOMAIN: stack.exchange_d,
    }[domain]


# The compose service that runs each exchange, and the identity that exchange
# publishes. A host run reaches them through ephemeral 127.0.0.1 ports; in-network
# the host IS the identity, so both directions of the mapping resolve through one
# table.
#
# Exported because it is the answer to "which exchanges does this stack run, and
# what is each one called" and more than this module asks it. The compose guard
# had grown its own copy of the same pairs, plus a second table keyed on the
# same service names, so an exchange added here would have left that guard
# green while it silently stopped checking the new one.
SERVICE_IDENTITIES = {
    "exchange": EXCHANGE_A_DOMAIN,
    "exchange-b": EXCHANGE_B_DOMAIN,
    "exchange-c": EXCHANGE_C_DOMAIN,
    "exchange-d": EXCHANGE_D_DOMAIN,
}

# The exchanges whose seat refuses purchases the Broker relays. The seed writes
# allow_broker_relay = FALSE on exactly these seats and TRUE on every other one.
RELAY_REFUSING_EXCHANGES = frozenset({EXCHANGE_D_DOMAIN})

# The seat each exchange runs, as a tenant id.
#
# A tenant row is one Exchange identity that sells on a deployment. Its
# ``fora.tenants.domain`` is the exchange's own hostname — the identity above —
# and the row holds the seller settings and the URL-signing key refs. It is not
# a publisher: a publisher is a ``fora.publishers`` row, its site a
# ``fora.publisher_domains`` row, and the site offered on a seat a
# ``fora.publisher_domain_listings`` row.
#
# Every catalog row an exchange stores is scoped to its own seat, so this is the
# tenant id a push to that exchange carries and the one its catalog rows read
# back under. What tells the demo publishers apart is
# ``catalog.resource_owner_id``, not the tenant id.
SEAT_TENANT_IDS = {
    EXCHANGE_A_DOMAIN: "tenant-seat-a",
    EXCHANGE_B_DOMAIN: "tenant-seat-b",
    EXCHANGE_C_DOMAIN: "tenant-seat-c",
    EXCHANGE_D_DOMAIN: "tenant-seat-d",
}

# A second seat on exchange-a, which the tenant-isolation suite names from
# exchange-a's own surfaces. The reference Exchange sells as exactly one seat,
# the one whose domain is its EXCHANGE_DOMAIN (ADR-026 D1). This row is a seat
# the Exchange holds and does not sell as, so a call that names it is a call for
# another tenant on the same instance and the same database. Its domain is a
# reserved name that nothing answers on, because nothing has to.
SECOND_SEAT_OF_EXCHANGE_A = "tenant-seat-a2"
SECOND_SEAT_OF_EXCHANGE_A_DOMAIN = "second-seat.exchange.example"


def seat_tenant_id(exchange_domain: str) -> str:
    """The tenant id of the seat ``exchange_domain`` runs."""
    return SEAT_TENANT_IDS[exchange_domain]


@lru_cache(maxsize=None)
def recipient_of(url: str) -> str:
    """Who a request posted to ``url`` is addressed to.

    In-network the answer is the URL's host, because that is the identity each
    exchange publishes there. From the host it is NOT: ``conftest`` maps the
    exchanges onto ephemeral ``127.0.0.1`` ports, and a port number names no
    exchange — a request addressed to one passes the wire's domain pattern and
    is then refused by the recipient check, which reads as a protocol failure
    rather than as the routing detail it is.

    Cached per URL: a host run answers this by asking compose which port belongs
    to which service, and the question is asked while building the body of every
    request and inside polling loops. The stack's port map does not change while
    a session runs, so one lookup per URL is enough.

    So a mapped port is resolved back to the identity behind it. A loopback port
    this stack publishes no exchange on raises, rather than naming a port number
    as the recipient; any other host is returned as it stands, which is right for
    a service-DNS URL and is the caller's problem for anything else. Returning
    the host regardless is what made a host run fail everywhere at once.
    """
    parts = urlsplit(url)
    host = parts.netloc or url
    service = parts.hostname or host.split(":", 1)[0]
    if service in SERVICE_IDENTITIES:
        return SERVICE_IDENTITIES[service]
    if not _is_mapped_port(parts, host):
        return host
    return _identity_behind(url, host)


def _is_mapped_port(parts: SplitResult, host: str) -> bool:
    """Whether host is a loopback address with a port — the host-run shape.

    Read through ``urlsplit`` rather than by splitting on the first colon: an
    IPv6 host arrives bracketed, so splitting yields "[" and the IPv6 loopback
    could never match. That branch was dead, and a bracketed loopback URL fell
    through to being returned as the recipient — a value the exchange refuses.
    """
    try:
        name, port = parts.hostname, parts.port
    except ValueError:
        # A malformed port. Not a mapped port, and not this function's to
        # report: the caller returns the host and the far end refuses it.
        return False
    if name is None or port is None:
        return ":" in host and host.split(":", 1)[0] in _LOOPBACK_NAMES
    return name in _LOOPBACK_NAMES


# Every spelling of loopback a URL in this harness can carry. urlsplit strips the
# brackets from an IPv6 literal, so "::1" is the value seen here.
_LOOPBACK_NAMES = frozenset({"127.0.0.1", "localhost", "::1"})


def _identity_behind(url: str, host: str) -> str:
    """Resolve a mapped loopback port back to the exchange identity behind it.

    The stack's own port map is the only thing that knows which exchange a port
    belongs to, so this asks compose rather than guessing.
    """
    from ._compose import COMPOSE_FILE, host_project, resolve_host_port

    project = host_project()
    for service, domain in SERVICE_IDENTITIES.items():
        try:
            mapped = resolve_host_port(COMPOSE_FILE, service, 8081, project=project)
        except RuntimeError:
            # The stack is not up, so compose can answer for no service. Keep
            # going: the caller's own error about an unreachable stack is more
            # use than one about a port table, and the raise below still names
            # what went wrong if no service matches.
            continue
        if host.endswith(f":{mapped}"):
            return domain
    msg = (
        f"recipient_of({url!r}): {host} is a loopback port, and this stack "
        f"publishes no exchange on it — either the stack is not running or the "
        f"port belongs to something else. A request addressed to a port number "
        f"is refused by the recipient check, so name the exchange explicitly."
    )
    raise LookupError(msg)

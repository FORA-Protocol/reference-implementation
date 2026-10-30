"""Seeding helpers for the E2E stack — Phase-2b demo-catalog edition.

The e2e catalog is this harness's own publisher feeds
(``fixtures/catalog/{philosophy,music,sfx}.jsonl``), ingested through the
REAL production binary ``cmd/fora-ingest`` (ParseJSONL → mapRecord, which
canonicalizes terms through the SDK helper → signed
``CatalogService/PushResources`` RPC; the Exchange runs the SDK's ingest-tier
term checks).
There is NO Python re-implementation of mapper.go and NO ``push_catalog`` of
demo content — the binary's signed RPCs are the only catalog write. It sends one
per submission of at most the wire bound on ``entries``; every feed here fits in
one.

The binary ships in the runner image at ``/usr/local/bin/fora-ingest``
(built by the Go builder stage of ``tests/e2e/Dockerfile``); the seed shells
out to it once per feed, under the seat of the exchange that sells that site.

Everything else arrives through the stack's own surfaces. Each Exchange creates
its seats, the demo payee, the sites it sells and their listings at start-up,
from its boot fixture (``exchange_boot_fixtures``, ADR-029), and answers its
health check only after that. The Broker reads its exchange registry from its
configuration file (``BROKER_REGISTRY_FILE``). Every Exchange learns each buyer's
identity from the buyer's own Web Bot Auth directory at the buyer's signed
Register, its first call that resolves the buyer, and the same Register opens the
buyer's account (see ``seed_accounts``). The Broker's relay identity needs no row:
an Exchange recognizes the Broker it is configured with from the directory that
publishes the Broker's key. The seed writes no row and opens no database
connection.

A ``fora.tenants`` row is the EXCHANGE SEAT, not a publisher: its domain is the
exchange's own hostname, and it carries the seller settings and the URL-signing
key refs. A publisher is a ``fora.publishers`` row (the payee), its site a
``fora.publisher_domains`` row (which carries the CDN signing scheme and the
CloudFront key-pair id), and the site offered on a seat a
``fora.publisher_domain_listings`` row. Catalog push resolves the seat from
``EXCHANGE_DOMAIN`` and the entry's domain from ``fora.publisher_domains``, then
requires the listing for that pair to be ``ACTIVE`` — so every boot fixture
declares its listings, and the Exchange creates them, already ACTIVE.

NO catalog-writer signing key is pre-seeded into ``fora.agents`` (Core
Invariant). Each demo feed is ingested under its publisher's OWN self-publish
key (caller_id == domain); the Exchange learns that key ONLY by fetching the
publisher's edge Web Bot Auth directory
(``/.well-known/http-message-signatures-directory``) keys[] (Gate-1 self-signup).
The 3rd-party ``catalog-contributor-e2e`` identity is likewise learned only via
its own well-known host, which the ``key-directories`` compose service serves.

The three demo domains resolve in-network to their edges via docker-compose
network aliases (demo.→Cloudflare, music.→Fastly, sfx.→AWS). Each edge passes
its publisher's ``/.well-known/fora.json`` (listing ``catalog-contributor-e2e``
for Gate 2 and the publisher's Exchange for the Broker probe) through to the
publisher's own origin, AND verifies + serves the signed content on port 80.
So BOTH the Exchange Gate-2 manifest fetch and the agent's signed-URL content
fetch resolve natively to the right edge — tests fetch demo-domain URIs
directly, no Host override.

The fourth domain, ``archive.demo.fora-protocol.org``, has no edge. The
``key-directories`` service answers for it with the publisher's key directory
and its ``fora.json`` (naming exchange-d), which is everything the ingest and the
Broker read; nothing fetches its content.
"""

from __future__ import annotations

import json
from pathlib import Path

import httpx
from fora_sdk.sync import BrokerClient

from .admin_plane import admin_plane_reachable
from .broker_purchase import resolve_request
from .poll import poll_until
from . import seed_agents
from .seed_accounts import open_credit_less_accounts
from .seed_ingest import ingest_all_feeds
from .exchanges import (
    EXCHANGE_A_DOMAIN,
    EXCHANGE_B_DOMAIN,
    EXCHANGE_B_INTERNAL_URL,
    EXCHANGE_C_DOMAIN,
    EXCHANGE_C_INTERNAL_URL,
    EXCHANGE_D_DOMAIN,
    EXCHANGE_D_INTERNAL_URL,
)
from .e2e_catalog import (
    _FIXTURES_DIR,
    CANARY_MARKER,  # noqa: F401  (re-exported for the ~40 `from .seed import` call sites)
    CATALOG_CONTRIBUTOR_ID,  # noqa: F401  (re-exported)
    CONTRIBUTOR_KEY_PATH,  # noqa: F401  (re-exported)
    DEMO_ARCHIVE_DOMAIN,
    DEMO_MUSIC_DOMAIN,
    DEMO_PHILOSOPHY_DOMAIN,
    DEMO_SFX_DOMAIN,
    DemoResource,  # noqa: F401  (re-exported)
    SEAT_OF_MUSIC,  # noqa: F401  (re-exported)
    SEAT_OF_PHILOSOPHY,  # noqa: F401  (re-exported)
    SEAT_OF_SFX,  # noqa: F401  (re-exported)
    SeededFixture,
    _build_fixture,
)

# ───── Multi-exchange topology: 4 REAL exchanges, each with its own
# catalog DB + identity. exchange-a keeps the cluster default DB `fora` (shared
# with the broker schema); exchange-b/c/d get separate databases
# `fora_b`/`fora_c`/`fora_d`.
# Each exchange's EXCHANGE_DOMAIN is stamped onto Offer.exchange, named as the
# recipient of every addressed request, and its EXCHANGE_PUBLIC_ORIGIN MUST
# equal the endpoint the Broker's registry file (fixtures/broker_registry.yml)
# names for it (the post-resolve SSRF equality gate). The music publisher (fastly-edge) is fronted by exchange-b;
# the sfx publisher (aws-edge) by exchange-c; the archive publisher (no edge) by
# exchange-d.
#
# The identities and the in-network URL built from each are defined in
# exchanges.py, which also carries the mapping between them in both directions.

# Per-domain SELF-PUBLISH signing keys: caller_id == publisher domain. The
# demo ingest signs each feed with its publisher's own key (caller_id == domain;
# keyid == thumbprint), so Gate-1 self-signup fetches the publisher's edge WBA
# directory (keys[]) and Gate-2 passes on the caller==domain branch — no DB
# pre-seed. Generated idempotently
# by scripts/gen-e2e-keys.sh; the SAME private fixture's pubkey is served by the edge.
SELFPUB_PHILOSOPHY_KEY_PATH = _FIXTURES_DIR / "selfpub_philosophy_key.json"
SELFPUB_MUSIC_KEY_PATH = _FIXTURES_DIR / "selfpub_music_key.json"
SELFPUB_SFX_KEY_PATH = _FIXTURES_DIR / "selfpub_sfx_key.json"
# The archive publisher has no edge: its directory is served by the
# key-directories service, which mounts this same fixture.
SELFPUB_ARCHIVE_KEY_PATH = _FIXTURES_DIR / "selfpub_archive_key.json"

# The e2e catalog feeds, owned by this harness — see fixtures/catalog/README.md.
# They are resolved from this package, not from the repository root, because the
# runner image copies the harness tree to /runner/harness: a repo-root-relative
# path would not exist in the container.
CATALOG_DIR = _FIXTURES_DIR / "catalog"

# The demo SITES: which exchange sells each one, its feed, and its self-publish
# key. Each site's row, with the signing scheme its CDN needs, is declared in the
# boot fixture of the exchange that sells it, and
# test_guards_exchange_boot_fixtures.py holds the fixtures to this table. No tenant
# id appears here: the tenant is the seat, one per exchange, and it is looked up
# from the exchange that fronts the site.
#
# (domain, exchange identity, feed path, self-publish key)
E2E_PUBLISHERS: tuple[tuple[str, str, Path, Path], ...] = (
    (
        DEMO_PHILOSOPHY_DOMAIN,
        EXCHANGE_A_DOMAIN,
        CATALOG_DIR / "philosophy.jsonl",
        SELFPUB_PHILOSOPHY_KEY_PATH,
    ),
    (
        DEMO_MUSIC_DOMAIN,
        EXCHANGE_B_DOMAIN,
        CATALOG_DIR / "music.jsonl",
        SELFPUB_MUSIC_KEY_PATH,
    ),
    (
        DEMO_SFX_DOMAIN,
        EXCHANGE_C_DOMAIN,
        CATALOG_DIR / "sfx.jsonl",
        SELFPUB_SFX_KEY_PATH,
    ),
    (
        DEMO_ARCHIVE_DOMAIN,
        EXCHANGE_D_DOMAIN,
        CATALOG_DIR / "archive.jsonl",
        SELFPUB_ARCHIVE_KEY_PATH,
    ),
)


def _wait_exchange_ready(exchange_host_url: str, timeout_seconds: float = 20.0) -> None:
    """Wait for Exchange healthz."""

    def healthy() -> bool:
        try:
            r = httpx.get(f"{exchange_host_url}/healthz", timeout=2.0)
        except httpx.HTTPError:
            return False
        return r.status_code == httpx.codes.OK

    poll_until(
        healthy,
        timeout_seconds,
        interval_s=0.5,
        failure=lambda: f"exchange not healthy at {exchange_host_url}",
    )


# The absence reason the Broker answers a resolve with while the Exchange that
# sells the resource is not routable: its registry row is not LIVE.
_NOT_ROUTABLE = "OFFER_ABSENCE_REASON_TEMPORARILY_UNAVAILABLE"
# Long enough for one full pass of the Broker's health refresher, which runs
# every 30 seconds, after an Exchange starts answering, with margin.
_ROUTES_DEADLINE_SEC = 90.0


def _first_uri(feed: Path) -> str:
    """The catalog URI of the first entry in ``feed``."""
    with feed.open() as lines:
        entry = json.loads(next(lines))
    return f"http://{entry['domain']}{entry['path']}"


def wait_until_broker_routes(broker: BrokerClient) -> None:
    """Wait until the Broker routes a resolve to every Exchange the seed put a catalog on.

    The Broker learns its Exchanges from its registry file at boot and probes each
    one at once. An Exchange that is still starting at that moment is held not
    routable until the refresher's next pass, and a resolve of anything it sells
    is answered with no offer and ``TEMPORARILY_UNAVAILABLE``. So the stack is
    ready for the suites only once a resolve of one resource per Exchange stops
    getting that answer, observed through the Broker's own resolve.

    It is also the wait ``edge_runtimes.key_directory_unreachable`` and the
    transient-retry test use after they stop and restart an Exchange: a probe pass
    that ran while the container was down holds it not routable the same way.

    ``broker`` is the client the resolves go through, and it is left open. A test
    passes its own agent's (``fresh_agent.broker``); the seed passes the client of
    an agent it mints for the wait.
    """
    pending = {exchange: _first_uri(feed) for _d, exchange, feed, _k in E2E_PUBLISHERS}
    last: dict[str, str | None] = {}

    def routed() -> bool:
        for exchange, uri in list(pending.items()):
            result = broker.resolve(resolve_request([uri]))
            last[exchange] = result.absence_reason
            if result.groups or result.absence_reason != _NOT_ROUTABLE:
                del pending[exchange]
        return not pending

    poll_until(
        routed,
        _ROUTES_DEADLINE_SEC,
        interval_s=1.0,
        failure=lambda: f"the Broker still does not route to {sorted(pending)}: {last}",
    )


# ───── Public seed entrypoint ───────────────────────────────────────────────


def seed_stack(
    exchange_host_url: str,
    *,
    exchange_b_host_url: str | None = None,
    exchange_c_host_url: str | None = None,
    exchange_d_host_url: str | None = None,
) -> SeededFixture:
    """Seed the 4-exchange E2E stack with the demo catalogs.

    Each exchange already holds its seat and the sites it sells, from its boot
    fixture. Inside the compose network the credit-less buyer then opens its
    accounts at every exchange, held for review and approved (see
    ``seed_accounts``), which is also where each exchange learns its identity. A
    run from the host cannot reach the controls that hold and approve, so it skips
    that step, and the ``credit_less_buyer`` fixture skips with it. Each demo feed
    is then ingested at the exchange that sells it. The Broker's registry is not
    written here: the Broker reads it from its configuration file at boot.

    ``exchange_b_host_url`` / ``exchange_c_host_url`` / ``exchange_d_host_url``
    default to the docker-DNS
    in-network URLs (the runner path); a host run passes the host-mapped URLs.

    Idempotent: a buyer's repeat Register returns its existing account and grants
    nothing, approving an account already approved changes nothing, and
    re-ingesting a feed re-pushes the same entries (the catalog is keyed by
    tenant+URI, so a repeat push is a no-op overwrite).
    """
    url_by_exchange = {
        EXCHANGE_A_DOMAIN: exchange_host_url,
        EXCHANGE_B_DOMAIN: exchange_b_host_url or EXCHANGE_B_INTERNAL_URL,
        EXCHANGE_C_DOMAIN: exchange_c_host_url or EXCHANGE_C_INTERNAL_URL,
        EXCHANGE_D_DOMAIN: exchange_d_host_url or EXCHANGE_D_INTERNAL_URL,
    }
    for url in url_by_exchange.values():
        _wait_exchange_ready(url)

    if admin_plane_reachable():
        open_credit_less_accounts(url_by_exchange, seed_agents.CREDIT_LESS_BUYER)

    ingest_all_feeds(url_by_exchange, E2E_PUBLISHERS)
    return _build_fixture()

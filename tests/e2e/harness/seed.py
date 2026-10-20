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

Most of the stack arrives through its own surfaces. The Broker reads its
exchange registry from its configuration file (``BROKER_REGISTRY_FILE``). Each
buyer's identity is learned by every Exchange from the buyer's own Web Bot Auth
directory the first time it signs a call, and each buyer opens its own account
through a signed Register (see ``seed_accounts``). The Broker's relay identity
needs no row: an Exchange recognizes the Broker it is configured with from the
directory that publishes the Broker's key.

What no product surface can create yet is written by ``operator_writes``, the
one harness module that opens a database connection: the seat row
(``fora.tenants``) with its relay opt-in and activation policy, the publisher
registration rows (``fora.publishers``, ``fora.publisher_domains``,
``fora.publisher_domain_listings``), the approval of the credit-less buyer's
held account, and the second seat exchange-a holds for the tenant-isolation
suite. Each of those functions names the surface that is missing.

A ``fora.tenants`` row is the EXCHANGE SEAT, not a publisher: its domain is the
exchange's own hostname, and it carries the seller settings and the URL-signing
key refs. A publisher is a ``fora.publishers`` row (the payee), its site a
``fora.publisher_domains`` row (which carries the CDN signing scheme and the
CloudFront key-pair id), and the site offered on a seat a
``fora.publisher_domain_listings`` row. Catalog push resolves the seat from
``EXCHANGE_DOMAIN`` and the entry's domain from ``fora.publisher_domains``, then
requires the listing for that pair to be ``ACTIVE`` — so every listing is seeded
already ACTIVE.

NO catalog-writer signing key is pre-seeded into ``fora.agents`` (Core
Invariant). Each demo feed is ingested under its publisher's OWN self-publish
key (caller_id == domain); the Exchange learns that key ONLY by fetching the
publisher's edge Web Bot Auth directory
(``/.well-known/http-message-signatures-directory``) keys[] (Gate-1 self-signup).
The 3rd-party ``catalog-contributor-e2e`` identity is likewise learned only via
its own well-known host (the ``catalog-contributor-e2e-jwks`` compose service).

The three demo domains resolve in-network to their edges via docker-compose
network aliases (demo.→Cloudflare, music.→Fastly, sfx.→AWS), each edge serving
its publisher's ``/.well-known/fora.json`` (listing ``catalog-contributor-e2e``
for Gate 2 and ``exchange.e2e.local`` for the Broker probe) AND verifying +
serving the signed content on port 80. So BOTH the Exchange Gate-2 manifest
fetch and the agent's signed-URL content fetch resolve natively to the right
edge — tests fetch demo-domain URIs directly, no Host override.

The fourth domain, ``archive.demo.fora-protocol.org``, has no edge. The
``archive-jwks`` host answers for it with the publisher's key directory and its
``fora.json`` (naming exchange-d), which is everything the ingest and the Broker
read; nothing fetches its content.
"""

from __future__ import annotations

import json
from contextlib import closing, nullcontext
from pathlib import Path

import httpx
from fora_sdk.sync import BrokerClient

from .broker_purchase import resolve_request
from .operator_writes import (
    activate_new_agents,
    approve_held_account,
    boot_fixture_second_seat,
    list_site_on_seat,
    pg_dsn,
    provision_seat,
    register_publisher,
    register_site,
)
from .poll import poll_until
from .sdk_client import sdk_broker_client
from .seed_accounts import FUNDED_BUYERS, register_buyers, register_credit_less_buyer
from .seed_ingest import ingest_all_feeds
from .stack_urls import StackURLs
from .exchanges import (
    EXCHANGE_A_DOMAIN,
    EXCHANGE_B_DOMAIN,
    EXCHANGE_B_INTERNAL_URL,
    EXCHANGE_C_DOMAIN,
    EXCHANGE_C_INTERNAL_URL,
    EXCHANGE_D_DOMAIN,
    EXCHANGE_D_INTERNAL_URL,
    RELAY_REFUSING_EXCHANGES,
    SECOND_SEAT_OF_EXCHANGE_A,
    SECOND_SEAT_OF_EXCHANGE_A_DOMAIN,
    seat_tenant_id,
)
from .e2e_catalog import (
    _FIXTURES_DIR,
    CANARY_MARKER,  # noqa: F401  (re-exported for the ~40 `from .seed import` call sites)
    CATALOG_CONTRIBUTOR_ID,  # noqa: F401  (re-exported)
    CONTRIBUTOR_KEY_PATH,  # noqa: F401  (re-exported)
    DEMO_ARCHIVE_DOMAIN,
    DEMO_MUSIC_DOMAIN,
    DEMO_PHILOSOPHY_DOMAIN,
    DEMO_PUBLISHER_ID,
    DEMO_PUBLISHER_NAME,
    DEMO_SFX_DOMAIN,
    DemoResource,  # noqa: F401  (re-exported)
    EUR_AGENT_ID,  # noqa: F401  (re-exported)
    EUR_AGENT_KEY_PATH,  # noqa: F401  (re-exported)
    NOBILLING_AGENT_ID,  # noqa: F401  (re-exported)
    NOBILLING_AGENT_KEY_PATH,  # noqa: F401  (re-exported)
    SEAT_OF_MUSIC,  # noqa: F401  (re-exported)
    SEAT_OF_PHILOSOPHY,  # noqa: F401  (re-exported)
    SEAT_OF_SFX,  # noqa: F401  (re-exported)
    SeededFixture,
    USD_AGENT_ID,  # noqa: F401  (re-exported)
    USD_AGENT_KEY_PATH,  # noqa: F401  (re-exported)
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
# The archive publisher has no edge: its directory is served by the archive-jwks
# host, which mounts this same fixture.
SELFPUB_ARCHIVE_KEY_PATH = _FIXTURES_DIR / "selfpub_archive_key.json"

# The e2e catalog feeds, owned by this harness — see fixtures/catalog/README.md.
# They are resolved from this package, not from the repository root, because the
# runner image copies the harness tree to /runner/harness: a repo-root-relative
# path would not exist in the container.
CATALOG_DIR = _FIXTURES_DIR / "catalog"

# The demo SITES: which exchange sells each one, the signing scheme its
# CDN needs, its feed, and its self-publish key.
#
# The signing scheme is a fact about the site's CDN, so it is written on the
# site's fora.publisher_domains row. No tenant id appears here any more: the
# tenant is the seat, one per exchange, and it is looked up from the exchange
# that fronts the site.
#
# (domain, exchange identity, signing_scheme, feed path, self-publish key)
E2E_PUBLISHERS: tuple[tuple[str, str, str, Path, Path], ...] = (
    (
        DEMO_PHILOSOPHY_DOMAIN,
        EXCHANGE_A_DOMAIN,
        "ED25519",
        CATALOG_DIR / "philosophy.jsonl",
        SELFPUB_PHILOSOPHY_KEY_PATH,
    ),
    (
        DEMO_MUSIC_DOMAIN,
        EXCHANGE_B_DOMAIN,
        "ED25519",
        CATALOG_DIR / "music.jsonl",
        SELFPUB_MUSIC_KEY_PATH,
    ),
    (
        DEMO_SFX_DOMAIN,
        EXCHANGE_C_DOMAIN,
        "AWS_CLOUDFRONT_RSA",
        CATALOG_DIR / "sfx.jsonl",
        SELFPUB_SFX_KEY_PATH,
    ),
    (
        DEMO_ARCHIVE_DOMAIN,
        EXCHANGE_D_DOMAIN,
        "ED25519",
        CATALOG_DIR / "archive.jsonl",
        SELFPUB_ARCHIVE_KEY_PATH,
    ),
)

# The catalog database each exchange owns. The broker schema lives in `fora`
# alongside exchange-a's catalog; the other exchanges get their own.
CATALOG_DB_BY_EXCHANGE = {
    EXCHANGE_A_DOMAIN: "fora",
    EXCHANGE_B_DOMAIN: "fora_b",
    EXCHANGE_C_DOMAIN: "fora_c",
    EXCHANGE_D_DOMAIN: "fora_d",
}

# The account registry (System of Record) each exchange owns, by its
# EXCHANGE_SOR_DSN. The seed approves the credit-less buyer's held account
# here, the way an operator does; see seed_accounts and operator_writes.
# test_guards_exchange_identity.py fails when this and the compose file disagree.
SOR_DB_BY_EXCHANGE = {
    EXCHANGE_A_DOMAIN: "fora_sor",
    EXCHANGE_B_DOMAIN: "fora_sor_b",
    EXCHANGE_C_DOMAIN: "fora_sor_c",
    EXCHANGE_D_DOMAIN: "fora_sor_d",
}

# The domains the seed creates seat rows under — one per exchange, each equal to
# that exchange's own EXCHANGE_DOMAIN. The compose guard compares every
# service's EXCHANGE_DEFAULT_TENANT against this set.
SEEDED_SEAT_DOMAINS = frozenset(CATALOG_DB_BY_EXCHANGE)


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


# ───── Seat and publisher registration (operator writes) ────────────────────
#
# A publisher registers themselves through the console. No e2e suite completes
# that registration: the console suite walks the wizard up to the document
# check, which this stack's origin fails, and a completed registration is
# covered by the console's integration tests, which run the Exchange's real
# registration surface in-process. Every suite here needs a registered
# publisher as a precondition rather than as its subject. The console cannot
# give it one either: only exchange-a runs a console, and a console
# registration mints its own publisher id, while the demo sites' manifests
# attest a fixed one. So the rows are written by operator_writes, which says
# what surface is missing for each.


def _provision_seat_and_sites(dsn: str, *, exchange: str) -> None:
    """Provision ONE exchange's seat and the sites it sells.

    The seat is NOT replicated: each exchange owns exactly one, keyed on its own
    hostname, and its catalog rows are scoped to it. Only the sites this exchange
    fronts are registered and listed here — a push of another exchange's site
    would find no listing on this seat, which is the truth the topology already
    had and now states in the data.

    No identity is written. The buyers' identities are learned from their own
    directories when they first sign a call, and the catalog-writer keys (the
    publishers' self-publish keys, the third-party contributor key) are learned
    from the well-known fetch at catalog-push time (Gate-1 self-signup).
    """
    tenant_id = seat_tenant_id(exchange)
    # An agent buys through the Broker, which re-packages the purchase and signs
    # it with its own key; authorizeForAgent honours that only on a seat that
    # accepts the relay. Every seat does except the ones RELAY_REFUSING_EXCHANGES
    # names, which exist to be refused.
    provision_seat(
        dsn,
        tenant_id=tenant_id,
        domain=exchange,
        allow_broker_relay=exchange not in RELAY_REFUSING_EXCHANGES,
    )
    # The payee first: the site row references it, and so does every catalog row
    # the ingest writes.
    register_publisher(dsn, publisher_id=DEMO_PUBLISHER_ID, display_name=DEMO_PUBLISHER_NAME)
    for domain, site_exchange, scheme, _feed, _key in E2E_PUBLISHERS:
        if site_exchange != exchange:
            continue
        register_site(dsn, domain=domain, publisher_id=DEMO_PUBLISHER_ID, scheme=scheme)
        list_site_on_seat(dsn, tenant_id=tenant_id, domain=domain)


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


def wait_until_broker_routes(stack: StackURLs, broker: BrokerClient | None = None) -> None:
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

    ``broker`` is the client the resolves go through. A test passes its own agent's
    (``fresh_agent.broker``), which is left open; with none, the seed's buyer
    resolves, through a client opened and closed here.
    """
    pending = {exchange: _first_uri(feed) for _d, exchange, _s, feed, _k in E2E_PUBLISHERS}
    last: dict[str, str | None] = {}
    client = closing(sdk_broker_client(stack)) if broker is None else nullcontext(broker)
    with client as resolver:

        def routed() -> bool:
            for exchange, uri in list(pending.items()):
                result = resolver.resolve(resolve_request([uri]))
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
    compose_file: str,
    exchange_host_url: str,
    *,
    exchange_b_host_url: str | None = None,
    exchange_c_host_url: str | None = None,
    exchange_d_host_url: str | None = None,
) -> SeededFixture:
    """Seed the 4-exchange E2E stack with the demo catalogs.

    Multi-exchange topology: each exchange's OWN catalog DB (fora / fora_b /
    fora_c / fora_d) gets that exchange's seat and the publisher and site rows for
    the catalogs it sells. The buyers then register at every exchange, which is
    also where each exchange learns their identities, and each demo feed is
    ingested at the exchange that sells it. The Broker's registry is not written
    here: the Broker reads it from its configuration file at boot.

    ``exchange_b_host_url`` / ``exchange_c_host_url`` / ``exchange_d_host_url``
    default to the docker-DNS
    in-network URLs (the runner path); a host run passes the host-mapped URLs.

    Idempotent: seat, publisher, site and listing writes are upserts, a buyer's
    repeat Register returns its existing account and grants nothing, approving
    an account already approved changes nothing, and re-ingesting a feed
    re-pushes the same entries (the catalog is keyed by tenant+URI, so a repeat
    push is a no-op overwrite).
    """
    url_by_exchange = {
        EXCHANGE_A_DOMAIN: exchange_host_url,
        EXCHANGE_B_DOMAIN: exchange_b_host_url or EXCHANGE_B_INTERNAL_URL,
        EXCHANGE_C_DOMAIN: exchange_c_host_url or EXCHANGE_C_INTERNAL_URL,
        EXCHANGE_D_DOMAIN: exchange_d_host_url or EXCHANGE_D_INTERNAL_URL,
    }
    dsn_by_exchange = {
        exchange: pg_dsn(compose_file, db) for exchange, db in CATALOG_DB_BY_EXCHANGE.items()
    }
    # A healthy Exchange has reached its database, so the writes below have a
    # database to land in.
    for url in url_by_exchange.values():
        _wait_exchange_ready(url)

    for exchange, dsn in dsn_by_exchange.items():
        _provision_seat_and_sites(dsn, exchange=exchange)
    # exchange-a also holds a seat it does not sell as, for the tenant-isolation
    # suite to name from exchange-a's own surfaces.
    boot_fixture_second_seat(
        dsn_by_exchange[EXCHANGE_A_DOMAIN],
        tenant_id=SECOND_SEAT_OF_EXCHANGE_A,
        domain=SECOND_SEAT_OF_EXCHANGE_A_DOMAIN,
    )
    # The buyers' accounts, opened by their own signed Register at every
    # exchange. The credit-less buyer goes first, while every seat holds new
    # agents for review, so its account opens inactive and is granted nothing;
    # the seed then approves it in each exchange's account registry. The seats
    # then activate new agents, and the funded buyers register and receive the
    # welcome credit each exchange is configured with. See seed_accounts for why
    # the order carries the meaning.
    held = register_credit_less_buyer(url_by_exchange)
    for exchange, billing_ref in held.items():
        approve_held_account(
            pg_dsn(compose_file, SOR_DB_BY_EXCHANGE[exchange]), billing_ref=billing_ref
        )
    for exchange, dsn in dsn_by_exchange.items():
        activate_new_agents(dsn, tenant_id=seat_tenant_id(exchange))
    register_buyers(url_by_exchange, FUNDED_BUYERS)

    ingest_all_feeds(url_by_exchange, E2E_PUBLISHERS)
    return _build_fixture()

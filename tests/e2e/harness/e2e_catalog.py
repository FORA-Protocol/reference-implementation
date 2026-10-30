"""Pure-data demo-catalog constants + fixture builders (no DB, no I/O).

This leaf module holds the demo-domain constants and the ``DemoResource`` /
``SeededFixture`` dataclasses together with their pure builders. It performs NO database access and NO network/subprocess I/O, so it
imports nothing from the seeding layer — the dependency is one-directional
(``seed`` → ``e2e_catalog`` → ``exchanges``) and cycle-free. ``exchanges`` is
pure data too, and it is where the seat each exchange runs is named.

``seed.py`` re-exports the names the test modules import from it, so the ~40
test modules that ``from .seed import X`` continue to resolve unchanged. The
agents the seed registers are not here: they are in ``seed_agents``, which no
test module may import.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from .exchanges import (
    EXCHANGE_A_DOMAIN,
    EXCHANGE_B_DOMAIN,
    EXCHANGE_C_DOMAIN,
    seat_tenant_id,
)

# ───── Demo publisher domains (port-less; alias to their edges on port 80) ──
DEMO_PHILOSOPHY_DOMAIN = "demo.fora-protocol.org"
DEMO_MUSIC_DOMAIN = "music.demo.fora-protocol.org"
DEMO_SFX_DOMAIN = "sfx.demo.fora-protocol.org"
# The archive publisher: no edge, sold only by exchange-d, the seat that refuses
# purchases the Broker relays.
DEMO_ARCHIVE_DOMAIN = "archive.demo.fora-protocol.org"

# The payee every demo site attests. One publisher owning every site: the same
# value each demo publisher's origin serves in its manifest's
# exchanges[].ext.resource_owner_id
# and the value fora.catalog.resource_owner_id carries, which is a foreign key to
# fora.publishers. It is what tells the demo publishers' rows apart from
# each other inside one seat's catalog.
DEMO_PUBLISHER_ID = "demo-resource-owner"
DEMO_PUBLISHER_NAME = "FORA demo publisher"

# Which exchange fronts each demo site, and therefore which SEAT that site's
# catalog rows are stored under. The seat is the tenant now; the site is a
# fora.publisher_domains row listed on that seat.
SEAT_OF_PHILOSOPHY = seat_tenant_id(EXCHANGE_A_DOMAIN)
SEAT_OF_MUSIC = seat_tenant_id(EXCHANGE_B_DOMAIN)
SEAT_OF_SFX = seat_tenant_id(EXCHANGE_C_DOMAIN)

_FIXTURES_DIR = Path(__file__).resolve().parent / "fixtures"

# 3rd-party contributor identity (kid == catalog-contributor-e2e). Served under its
# own well-known host by the key-directories service (NOT pre-seeded in
# fora.agents); the Exchange learns its key by fetching that host. Used by the catalog tests that push
# as the operator's contributor (catalog_calls.contributor) and by the ingest probes.
# The keyfile format is the one httpsig_signer.load_keypair reads.
CATALOG_CONTRIBUTOR_ID = "catalog-contributor-e2e"
CONTRIBUTOR_KEY_PATH = _FIXTURES_DIR / "catalog_contributor_key.json"

# Canary marker embedded in the thales-of-miletus.txt content body, which is
# still served from the shared content tree under deploy/content/demo.
CANARY_MARKER = "FORA-DEMO-CANARY-8FK3J2-0418"


# ───── E2E resource catalog (from fixtures/catalog/, described in that
# directory's README). Each is a port-less demo-domain URI the tests fetch
# directly (the domain aliases to its edge). The seat the row is stored under
# and the term's currency are carried alongside so test scenarios can pick a
# resource by term-shape. A test buys a resource as its own agent.


@dataclass(frozen=True)
class DemoResource:
    """One demo-catalog resource the rewritten e2e suites assert against.

    ``uri`` is the port-less demo-domain URI (resolves to ``edge_service`` via
    the network alias). ``tenant_id`` is the SEAT the row is stored under, which
    is the exchange fronting ``domain`` — not the publisher.
    """

    uri: str
    tenant_id: str
    domain: str
    edge_service: str  # "edge" | "fastly-edge" | "aws-edge"
    currency: str


def _demo_uri(domain: str, path: str) -> str:
    return f"http://{domain}{path}"


def _resource(uri_domain: str, path: str, tenant: str, edge: str, currency: str) -> DemoResource:
    return DemoResource(
        uri=_demo_uri(uri_domain, path),
        tenant_id=tenant,
        domain=uri_domain,
        edge_service=edge,
        currency=currency,
    )


@dataclass(frozen=True)
class SeededFixture:
    """Demo-catalog resources the rewritten e2e suites consume.

    Every resource is produced by ingesting the demo feeds through the real
    ``fora-ingest`` binary — none is pushed by a test.
    """

    # FREE term (socrates: FREE EUR, academic/EU) on the Cloudflare edge.
    free: DemoResource
    # Priced PER_UNIT term (wooden-door-creak: 0.25 USD/accesses, commercial,
    # EU/US/GB) on the AWS edge.
    priced_per_unit: DemoResource
    priced_per_unit_rate: float
    # Geo/user-type restricted multi-term resource (plato: academic-FREE +
    # commercial-PER_UNIT, EUR, geo[EU]) on the Cloudflare edge.
    restricted: DemoResource
    # Retrieval canary (thales-of-miletus: FLAT 9.99 EUR, geo[EU]) on the
    # Cloudflare edge. Body carries CANARY_MARKER.
    canary: DemoResource
    # One resource per edge runtime (the FREE/affordable resource the
    # signed-URL fetch tests follow to each edge).
    cloudflare: DemoResource
    fastly: DemoResource
    aws: DemoResource


def _build_fixture() -> SeededFixture:
    """Assemble the demo-resource fixture (pure data, no I/O)."""
    socrates = _resource(
        DEMO_PHILOSOPHY_DOMAIN,
        "/articles/philosophers/socrates.txt",
        SEAT_OF_PHILOSOPHY,
        "edge",
        "EUR",
    )
    wooden_door = _resource(
        DEMO_SFX_DOMAIN, "/sfx/wooden-door-creak.json", SEAT_OF_SFX, "aws-edge", "USD"
    )
    plato = _resource(
        DEMO_PHILOSOPHY_DOMAIN,
        "/articles/philosophers/plato.txt",
        SEAT_OF_PHILOSOPHY,
        "edge",
        "EUR",
    )
    thales = _resource(
        DEMO_PHILOSOPHY_DOMAIN,
        "/articles/philosophers/thales-of-miletus.txt",
        SEAT_OF_PHILOSOPHY,
        "edge",
        "EUR",
    )
    # paper-satellites: FLAT 3.50 USD, geo[US], no user-type restriction — a
    # clean non-subscription Fastly-edge resource a USD buyer fetches. (The
    # demo's truly-unrestricted music resources are all subscription/
    # reference_only, which yield no offer on a plain resolve.)
    paper_satellites = _resource(
        DEMO_MUSIC_DOMAIN, "/lyrics/paper-satellites.txt", SEAT_OF_MUSIC, "fastly-edge", "USD"
    )
    # rain-on-tin-roof: FREE USD, individual, EU/US/GB — the AWS-edge FREE
    # resource a USD individual buyer can fetch.
    rain = _resource(DEMO_SFX_DOMAIN, "/sfx/rain-on-tin-roof.json", SEAT_OF_SFX, "aws-edge", "USD")
    return SeededFixture(
        free=socrates,
        priced_per_unit=wooden_door,
        priced_per_unit_rate=0.25,
        restricted=plato,
        canary=thales,
        cloudflare=socrates,
        fastly=paper_satellites,
        aws=rain,
    )

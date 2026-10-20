"""The publisher journey, end to end: register, import, sell, and read verified figures.

One fresh publisher (a login of its own at the stack's provider) takes a fresh
domain under sites.example through the whole of what ADR-026 describes. The
journey is one stack journey, because every later step needs the earlier ones:
nothing can be imported before the domain is registered, and nothing sold before
it is imported. Each step is a module-scoped fixture that runs once, in order,
and each obligation is its own test, attached to the step that proves it:

  1. Registration (PUB-001 to PUB-004). The publisher signs in to the console,
     registers its business twice, as a double submit would, and adds the domain:
     the wizard detects the CDN from the site's own answer, builds the discovery
     document, the publisher publishes it on the domain byte for byte, the
     console's check passes and the last step registers the domain.
  2. Import (PUB-009 to PUB-011). The operator's importer runs (the stack's
     ``importer`` job). It imports the two sitemap pages inside the licensed path
     and leaves the one outside it, and the console's catalog page lists exactly
     the two. A second run imports the same two again and the catalog still lists
     each once.
  3. Sale (PUB-010, PUB-013). A fresh agent, funded at the Exchange, discovers one
     of the pages there and buys it through the SDK while the seat's fee rate is
     1000 basis points, and the dashboard's income file settles at that rate.
  4. Reads of the sale (PUB-014 to PUB-016): the transaction list has the sale's
     row, and the sale's evidence verifies through an oracle independent of the
     console and the Exchange, on a page that never shows the delivery address.
  5. Refusals that need a sale to be meaningful (PUB-008, PUB-006): a second
     publisher opening this transaction is answered 404, and a read of this
     publisher's transactions signed by a key directory other than the console's
     is answered 401.

A test whose step failed reports that step's failure as its own error, so every
obligation the failure leaves unproven is named in the run, not only the first.

Every leg goes in through a surface a publisher or an agent uses (the console's
pages and wizard calls, the importer job, the SDK) and is read back through the
console, with one exception: what an importer run did for the listing (its
outcome and its entry counts) is read from the job's own log line, because no
public or admin surface reports a listing's import outcome yet. Nothing reads or
writes a database.

Each run claims its own login, domain and agent, so the journey passes again on a
stack it has already run on (ADR-028 D7).
"""

from __future__ import annotations

import html
import os
from collections.abc import Iterator
from dataclasses import dataclass
from decimal import ROUND_FLOOR, Decimal

import pytest
from wire.models import Cost, Role

from ..admin_plane import require_admin_plane, tenant_fee_rate
from ..broker_purchase import discover_group, only_verified_offer
from ..conftest import StackURLs
from ..console_client import Actor, Wizard, foreign_signed_list_status
from ..console_dashboard import (
    CatalogRow,
    IncomeRow,
    Period,
    TransactionDetail,
    catalog,
    income_total,
    page_answer,
    registered_sites,
    transaction,
    transaction_path,
    transactions,
)
from ..evidence_oracle import verify_evidence
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import AgentFactory, FreshAgent
from ..importer import COMPLETE, ListingOutcome, run_import
from ..obligation_registry import Obligation, obligation
from ..poll import poll_until
from ..publisher_accounts import PublisherFactory, PublisherSession
from ..publisher_journey import Onboarding, onboard
from ..publisher_sites import Site, mint_site
from ..sdk_client import agent_signer
from ..seed import SeededFixture
from .flow import buy_verified_and_observe_charge

# ADR-008 D5 — declare stack-isolation contract. The fee rate is the one shared
# setting the journey changes, and tenant_fee_rate puts it back however it ends.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: The seat's fee rate while the page is sold, in basis points.
FEE_RATE_BPS = 1000

#: The ledger keeps money in units of 10^-8 of the currency
#: (``src/exchange/internal/money``), and the fee is floored in those units.
LEDGER_SCALE = Decimal("0.00000001")

#: The catalog contributor the stack's operator runs, which every generated file
#: must authorize (EXCHANGE_CATALOG_CONTRIBUTOR_HOST in docker-compose.e2e.yml).
CONTRIBUTOR = "catalog-contributor-e2e"

#: How long a sale takes to show in the publisher's totals: the ledger posts the
#: settlement a few seconds after the sale executes.
_SETTLE_DEADLINE_S = 60.0


def _fee_of(gross: Decimal, bps: int) -> Decimal:
    """floor(gross x bps / 10000) at the ledger's scale: the fee the protocol defines."""
    return (gross * bps / Decimal(10_000)).quantize(LEDGER_SCALE, rounding=ROUND_FLOOR)


# ── The journey's steps, each run once for the module, in order ────────────


@pytest.fixture(scope="module")
def publishers(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: registration needs the seed's seat
) -> Iterator[PublisherFactory]:
    """The journey's publishers: the one that sells, and a second one for PUB-008."""
    if os.environ.get("FORA_E2E_IN_NETWORK") != "1" or not compose_stack.sites:
        pytest.skip("a fresh publisher signs in through the provider, in-network only")
    factory = PublisherFactory(compose_stack)
    yield factory
    factory.close()


@pytest.fixture(scope="module")
def buyer(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed opens the seat the agent funds
    tmp_path_factory: pytest.TempPathFactory,
) -> Iterator[FreshAgent]:
    """The journey's own agent, new to the stack, which buys the publisher's page."""
    factory = AgentFactory(compose_stack, tmp_path_factory.mktemp("journey-agent"))
    yield factory()
    factory.close()


@dataclass(frozen=True)
class Registered:
    """Step 1: the publisher, its site, and what each onboarding step answered."""

    publisher: PublisherSession
    site: Site
    onboarding: Onboarding


@pytest.fixture(scope="module")
def registered(compose_stack: StackURLs, publishers: PublisherFactory) -> Registered:
    publisher = publishers()
    site = mint_site()
    return Registered(publisher, site, onboard(compose_stack, publisher, site))


@dataclass(frozen=True)
class Imported:
    """Step 2: what one importer run did for the listing, and the catalog read after it."""

    run: ListingOutcome
    listed: list[CatalogRow]


def _import(stack: StackURLs, registered: Registered) -> Imported:
    assert registered.onboarding.registered.verified, (
        f"step 1 did not register {registered.site.domain} (PUB-004), so the importer has "
        f"no listing to import: {registered.onboarding.registered}"
    )
    run = run_import(registered.site.domain)
    return Imported(run, catalog(registered.publisher.browser, stack.console))


@pytest.fixture(scope="module")
def first_import(compose_stack: StackURLs, registered: Registered) -> Imported:
    return _import(compose_stack, registered)


@pytest.fixture(scope="module")
def second_import(
    compose_stack: StackURLs, registered: Registered, first_import: Imported
) -> Imported:
    assert first_import.run.outcome == COMPLETE, (
        f"step 2's first run did not complete, so a second run proves nothing: {first_import.run}"
    )
    return _import(compose_stack, registered)


@dataclass(frozen=True)
class Sale:
    """Step 3: the sale of one licensed page, and the income file it settled into."""

    page: str
    cost: Cost
    gross: Decimal
    transaction_id: str
    endpoint: str
    period: Period
    total: IncomeRow


@pytest.fixture(scope="module")
def sale(
    compose_stack: StackURLs, registered: Registered, first_import: Imported, buyer: FreshAgent
) -> Sale:
    require_admin_plane()
    listed = {row.uri for row in first_import.listed}
    page = registered.site.licensed[0]
    assert page in listed, f"step 2 did not import {page} (PUB-010), so it cannot be sold"
    period = Period.around_today()
    publisher = registered.publisher
    buyer.fund(EXCHANGE_A_DOMAIN)
    with tenant_fee_rate(EXCHANGE_A_DOMAIN, FEE_RATE_BPS):
        group = discover_group(
            buyer.exchange(EXCHANGE_A_DOMAIN), EXCHANGE_A_DOMAIN, page, "discover"
        )
        offer = only_verified_offer(group, f"discovery of {page}")
        item = buy_verified_and_observe_charge(buyer, EXCHANGE_A_DOMAIN, offer, "the sale")
        cost, sold, endpoint = item.cost, item.transaction_id, item.retrieval_endpoint
        assert cost is not None and cost.amount and sold and endpoint, item
        gross = Decimal(cost.amount)

        def settled() -> IncomeRow | None:
            total = income_total(publisher.browser, compose_stack.console, period)
            return total if total.gross == gross else None

        total = poll_until(
            settled,
            _SETTLE_DEADLINE_S,
            failure=lambda: (
                f"the income file never settled at {gross}: "
                f"{income_total(publisher.browser, compose_stack.console, period)}"
            ),
        )
    return Sale(page, cost, gross, str(sold), endpoint, period, total)


@pytest.fixture(scope="module")
def detail(compose_stack: StackURLs, registered: Registered, sale: Sale) -> TransactionDetail:
    """Step 4: the sale's transaction page, as the publisher that made the sale reads it."""
    return transaction(registered.publisher.browser, compose_stack.console, sale.transaction_id)


# ── Step 1: registration ───────────────────────────────────────────────────


@obligation(Obligation.PUB_001)
def test_registering_a_business_mints_a_publisher_id(registered: Registered) -> None:
    onboarding = registered.onboarding
    assert onboarding.publisher_id.startswith("pub_"), onboarding
    payees = [(e.ext or {}).get("resource_owner_id") for e in onboarding.manifest.exchanges or []]
    assert payees == [onboarding.publisher_id], onboarding.manifest


@obligation(Obligation.PUB_002)
def test_registering_the_business_again_returns_the_same_publisher(
    compose_stack: StackURLs, registered: Registered
) -> None:
    onboarding = registered.onboarding
    assert onboarding.publisher_id_again == onboarding.publisher_id, onboarding
    assert Wizard(registered.publisher.browser, compose_stack.console).registered, (
        "the console forgot the business"
    )


@obligation(Obligation.PUB_003)
def test_the_generated_file_names_this_exchange_the_publisher_and_the_contributor(
    registered: Registered,
) -> None:
    onboarding = registered.onboarding
    assert onboarding.lookup.supported and onboarding.lookup.provider == "cloudflare", onboarding
    assert onboarding.lookup.taken is False, onboarding
    manifest = onboarding.manifest
    assert manifest.role is Role.ROLE_PUBLISHER and manifest.domain == registered.site.domain, (
        manifest
    )
    exchanges = manifest.exchanges or []
    assert [e.domain for e in exchanges] == [EXCHANGE_A_DOMAIN], manifest
    assert (exchanges[0].ext or {}).get("resource_owner_id") == onboarding.publisher_id, manifest
    assert [c.domain for c in manifest.catalog_contributors or []] == [CONTRIBUTOR], manifest


@obligation(Obligation.PUB_004)
def test_publishing_the_file_activates_the_domains_listing(
    compose_stack: StackURLs, registered: Registered
) -> None:
    onboarding = registered.onboarding
    assert onboarding.check.passed and onboarding.check.reasons == [], onboarding.check
    assert onboarding.registered.verified, onboarding.registered
    domain = registered.site.domain
    sites = {
        s.domain: s for s in registered_sites(registered.publisher.browser, compose_stack.console)
    }
    assert set(sites) == {domain} and sites[domain].verified, sites


# ── Step 2: import ─────────────────────────────────────────────────────────


@obligation(Obligation.PUB_009)
def test_the_importer_imports_only_the_licensed_pages(
    registered: Registered, first_import: Imported
) -> None:
    run = first_import.run
    assert (run.outcome, run.entries, run.accepted) == (COMPLETE, 2, 2), run
    assert run.publisher == registered.onboarding.publisher_id, run
    listed = sorted(row.uri for row in first_import.listed)
    assert listed == sorted(registered.site.licensed), first_import.listed
    assert not set(listed) & set(registered.site.unlicensed), first_import.listed


@obligation(Obligation.PUB_010)
def test_the_imported_pages_are_in_the_catalog_and_one_sells(
    registered: Registered, first_import: Imported, sale: Sale
) -> None:
    assert sorted(row.uri for row in first_import.listed) == sorted(registered.site.licensed), (
        first_import.listed
    )
    assert sale.page in registered.site.licensed and sale.transaction_id, sale


@obligation(Obligation.PUB_011)
def test_a_repeat_import_creates_no_duplicate_catalog_entries(
    registered: Registered, second_import: Imported
) -> None:
    run = second_import.run
    assert (run.outcome, run.entries, run.accepted) == (COMPLETE, 2, 2), run
    relisted = sorted(row.uri for row in second_import.listed)
    assert relisted == sorted(registered.site.licensed), second_import.listed


# ── Step 3: the sale settles at the fee rate in force ──────────────────────


@obligation(Obligation.PUB_013)
def test_the_income_shows_the_sale_at_the_fee_rate_in_force(sale: Sale) -> None:
    fee = _fee_of(sale.gross, FEE_RATE_BPS)
    total = sale.total
    assert total.transactions == 1, total
    assert (total.gross, total.fee, total.net) == (sale.gross, fee, sale.gross - fee), total
    assert total.currency == sale.cost.currency, total


# ── Step 4: the sale's reads ───────────────────────────────────────────────


@obligation(Obligation.PUB_014)
def test_the_transaction_list_shows_the_sale(
    compose_stack: StackURLs, registered: Registered, sale: Sale, buyer: FreshAgent
) -> None:
    rows = transactions(registered.publisher.browser, compose_stack.console, sale.period)
    assert len(rows) == 1, rows
    row = rows[0]
    assert (row.transaction_id, row.resource_uri, row.agent) == (
        sale.transaction_id,
        sale.page,
        buyer.host,
    ), row
    assert row.unit_price == f"{sale.cost.amount} {sale.cost.currency}", row


@obligation(Obligation.PUB_015)
def test_the_transactions_evidence_verifies_against_the_published_keys(
    compose_stack: StackURLs, sale: Sale, detail: TransactionDetail, buyer: FreshAgent
) -> None:
    assert (detail.transaction_id, detail.agent) == (sale.transaction_id, buyer.host), detail
    verdict = verify_evidence(
        detail.evidence,
        exchange=EXCHANGE_A_DOMAIN,
        exchange_origin=compose_stack.exchange,
        agent_origin=f"http://{buyer.host}",
    )
    assert verdict.holds, verdict


@obligation(Obligation.PUB_016)
def test_the_transaction_page_never_shows_the_delivery_address(
    sale: Sale, detail: TransactionDetail
) -> None:
    assert detail.transaction_id == sale.transaction_id, detail
    for spelling in {sale.endpoint, html.escape(sale.endpoint)}:
        assert spelling not in detail.html, "the transaction page shows the delivery address"


# ── Step 5: refusals that need the sale ────────────────────────────────────


@obligation(Obligation.PUB_008, negative=True)
def test_another_publisher_is_answered_not_found_for_the_transaction(
    compose_stack: StackURLs, publishers: PublisherFactory, sale: Sale, detail: TransactionDetail
) -> None:
    """The other publisher holds a domain of its own.

    The console sends a person with no domain to its home page before any read,
    so only a publisher with one reaches the Exchange's grant check.
    """
    other = publishers()
    assert onboard(compose_stack, other, mint_site()).registered.verified
    foreign = page_answer(
        other.browser, compose_stack.console, transaction_path(sale.transaction_id)
    )
    assert foreign.status == 404, foreign.status
    assert detail.evidence.offer_sig not in foreign.html, "the 404 page carries the evidence"


@obligation(Obligation.PUB_006, negative=True)
def test_a_signer_from_another_directory_is_refused_the_consoles_read(
    compose_stack: StackURLs, registered: Registered, sale: Sale, buyer: FreshAgent
) -> None:
    """The console's own read lists the sale; the same read signed elsewhere is answered 401."""
    listed = transactions(registered.publisher.browser, compose_stack.console, sale.period)
    assert [row.transaction_id for row in listed] == [sale.transaction_id], listed
    login = registered.publisher.login
    refused = foreign_signed_list_status(
        compose_stack.exchange,
        agent_signer(buyer.key_path),
        actor=Actor(oidc_issuer=login.issuer, oidc_subject=login.subject, email=login.name),
        publisher_id=registered.onboarding.publisher_id,
        period=sale.period,
    )
    assert refused == 401, refused

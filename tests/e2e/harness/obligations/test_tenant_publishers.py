"""Obligations TENANT-006 to TENANT-009: two publishers on one Exchange seat.

The multi-tenant page of the pinned protocol module
``github.com/FORA-Protocol/protocol`` calls each provider a tenant, resolves the
tenant from the domain of the URI a call names, and allows no tenant to access
another tenant's catalog or transaction records. In the reference Exchange a
provider is a publisher listed on a seat, and everything tenant-specific about it
lives on its (seat, publisher) relationship. So these tests take two publishers on
exchange-a's one seat and drive each one's data from the other's side.

The module is one stack journey, because every later step needs the earlier ones:

  1. Two fresh publishers sign in to the console, and each registers a fresh
     domain under sites.example through the wizard. Each site then also serves a
     key directory of its own, so the publisher can sign its own catalog calls as
     its domain.
  2. Each publisher pushes its catalog with its own key. Both push the same path
     and the same content id on their own domains, at different prices, and the
     first also pushes a path only it has.
  3. One fresh agent, funded at exchange-a, buys the shared page from each
     publisher, and both sales settle into their publishers' income.
  4. The agent reports its purchase from the first publisher only.

Every leg goes in through a surface a publisher or an agent uses (the console's
wizard and pages, the SDK's CatalogService and Exchange clients) and is read back
through the same kind of surface: what an agent is offered through
DiscoverResources, what a publisher sees through its console. Nothing reads or
writes a database. Each run claims its own logins, domains, keys and agent, so the
module passes again on a stack it has already run on (ADR-028 D7).
"""

from __future__ import annotations

import os
from collections.abc import Iterator
from dataclasses import dataclass
from decimal import Decimal

import pytest
from fora_sdk.client import CallError
from fora_sdk.core import VerifiedOffer
from wire.models import TransactionResultItem

from ..broker_purchase import discover_group
from ..catalog_calls import (
    HOME_SEAT,
    NOT_CATALOG_CONTRIBUTOR,
    Catalog,
    accepted,
    answer,
    fresh_entry,
    offers,
    refusal,
    rejection,
    sole,
    stored,
)
from ..catalog_push import PRICING_MODEL_PER_UNIT, CatalogEntry, license_term
from ..conftest import StackURLs
from ..console_dashboard import (
    IncomeRow,
    Period,
    TransactionRow,
    catalog,
    income_total,
    page_answer,
    transaction,
    transaction_path,
    transactions,
)
from ..defects import (
    CATALOG_CONTRIBUTOR_REFUSAL_NOT_PERMISSION_DENIED,
    REMOVE_RESOURCES_UNIMPLEMENTED,
)
from ..evidence_oracle import verify_evidence
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import AgentFactory, FreshAgent
from ..obligation_registry import Obligation, obligation
from ..poll import poll_until
from ..publisher_accounts import PublisherFactory, PublisherSession
from ..publisher_journey import MANIFEST_FILE, Onboarding, onboard
from ..publisher_sites import Site, mint_site, mint_site_key, publish
from ..reporting import report_body
from ..seed import SeededFixture
from .flow import buy_verified_and_observe_charge

# ADR-008 D5 — every publisher, site, key and agent here is minted for this run,
# and nothing the module does changes a setting another test reads.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

#: What each publisher charges per access for its shared page, in USD. Different
#: on purpose: an offer at the other publisher's price would show.
FIRST_RATE = Decimal("0.11")
SECOND_RATE = Decimal("0.07")

_NOT_IN_CATALOG = "OFFER_ABSENCE_REASON_NOT_IN_CATALOG"
_PENDING, _RECEIVED = "pending", "received"

#: How long a sale takes to show in its publisher's income: the ledger posts the
#: settlement a few seconds after the sale executes.
_SETTLE_DEADLINE_S = 60.0
#: How long a filed report takes to show on its sale's row.
_REPORT_DEADLINE_S = 15.0


@dataclass(frozen=True)
class Tenant:
    """One publisher on the seat: its session, its site, and its own catalog client."""

    name: str
    publisher: PublisherSession
    site: Site
    onboarding: Onboarding
    catalog: Catalog
    rate: Decimal

    def entry(self, path: str, content_id: str) -> CatalogEntry:
        """An entry for ``path`` on this publisher's own domain, at its own rate."""
        term = license_term(
            model=PRICING_MODEL_PER_UNIT,
            rate=float(self.rate),
            unit="accesses",
            estimated_quantity=1,
        )
        return CatalogEntry(
            domain=self.site.domain, path=path, content_id=content_id, terms=(term,)
        )

    def uri(self, path: str) -> str:
        return f"{self.site.origin}{path}"


def _register(stack: StackURLs, factory: PublisherFactory, name: str, rate: Decimal) -> Tenant:
    """A fresh publisher with a fresh, registered site that also serves its own key directory."""
    publisher = factory()
    site = mint_site()
    onboarding = onboard(stack, publisher, site)
    assert onboarding.registered.verified, f"{name}: {site.domain} was not registered: {onboarding}"
    key = mint_site_key(site)
    publish(stack, site, manifest=onboarding.files[MANIFEST_FILE].body, key=key)
    return Tenant(name, publisher, site, onboarding, Catalog(stack.exchange, key.signer), rate)


@dataclass(frozen=True)
class Seat:
    """Step 1: the two publishers on exchange-a's seat."""

    first: Tenant
    second: Tenant


@pytest.fixture(scope="module")
def seat(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: registration needs the seed's seat
) -> Iterator[Seat]:
    if os.environ.get("FORA_E2E_IN_NETWORK") != "1" or not compose_stack.sites:
        pytest.skip("a fresh publisher signs in through the provider, in-network only")
    factory = PublisherFactory(compose_stack)
    yield Seat(
        first=_register(compose_stack, factory, "the first publisher", FIRST_RATE),
        second=_register(compose_stack, factory, "the second publisher", SECOND_RATE),
    )
    factory.close()


@pytest.fixture(scope="module")
def buyer(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed opens the seat the agent funds
    tmp_path_factory: pytest.TempPathFactory,
) -> Iterator[FreshAgent]:
    """The module's own agent, funded at exchange-a, which buys from both publishers."""
    factory = AgentFactory(compose_stack, tmp_path_factory.mktemp("tenant-buyer"))
    agent = factory()
    agent.fund(EXCHANGE_A_DOMAIN)
    yield agent
    factory.close()


@dataclass(frozen=True)
class Pushed:
    """Step 2: the path both publishers pushed, and the path only the first did."""

    shared: str
    first_only: str


@pytest.fixture(scope="module")
def pushed(seat: Seat) -> Pushed:
    """Each publisher pushes its catalog with its own key, the first publisher first.

    Both push the same path under the same content id; the second push therefore
    names the first publisher's content id on the same seat.
    """
    shared_entry, first_only = fresh_entry(), fresh_entry()
    shared = Pushed(shared=shared_entry.path, first_only=first_only.path)
    first, second = seat.first, seat.second
    accepted(
        first.catalog.push(
            HOME_SEAT,
            [
                first.entry(shared.shared, shared_entry.content_id),
                first.entry(shared.first_only, first_only.content_id),
            ],
        )
    )
    accepted(second.catalog.push(HOME_SEAT, [second.entry(shared.shared, shared_entry.content_id)]))
    return shared


def _offer(agent: FreshAgent, uri: str) -> VerifiedOffer:
    return sole(offers(agent, EXCHANGE_A_DOMAIN, uri), f"offer for {uri}")


def _price(offer: VerifiedOffer) -> Decimal:
    return Decimal(offer.offer["pricing"]["rate"])


@dataclass(frozen=True)
class Sale:
    """One purchase of a publisher's shared page, and the income it settled into."""

    item: TransactionResultItem
    gross: Decimal
    income: IncomeRow

    @property
    def transaction_id(self) -> str:
        return str(self.item.transaction_id)


@dataclass(frozen=True)
class Sales:
    """Step 3: the agent's purchase from each publisher."""

    period: Period
    first: Sale
    second: Sale


def _settled(stack: StackURLs, tenant: Tenant, period: Period, gross: Decimal) -> IncomeRow:
    """The publisher's income total once it holds ``gross``, the one sale it made."""

    def total() -> IncomeRow | None:
        row = income_total(tenant.publisher.browser, stack.console, period)
        return row if row.gross == gross else None

    return poll_until(
        total,
        _SETTLE_DEADLINE_S,
        failure=lambda: (
            f"{tenant.name}'s income never settled at {gross}: "
            f"{income_total(tenant.publisher.browser, stack.console, period)}"
        ),
    )


@pytest.fixture(scope="module")
def sales(compose_stack: StackURLs, seat: Seat, pushed: Pushed, buyer: FreshAgent) -> Sales:
    period = Period.around_today()
    bought: dict[str, tuple[TransactionResultItem, Decimal]] = {}
    for tenant in (seat.first, seat.second):
        offer = _offer(buyer, tenant.uri(pushed.shared))
        item = buy_verified_and_observe_charge(buyer, EXCHANGE_A_DOMAIN, offer, tenant.name)
        assert item.cost is not None and item.cost.amount, f"{tenant.name}: no cost: {item}"
        bought[tenant.name] = (item, Decimal(item.cost.amount))
    first_item, first_gross = bought[seat.first.name]
    second_item, second_gross = bought[seat.second.name]
    return Sales(
        period=period,
        first=Sale(
            first_item, first_gross, _settled(compose_stack, seat.first, period, first_gross)
        ),
        second=Sale(
            second_item, second_gross, _settled(compose_stack, seat.second, period, second_gross)
        ),
    )


def _rows(stack: StackURLs, tenant: Tenant, period: Period) -> list[TransactionRow]:
    return transactions(tenant.publisher.browser, stack.console, period)


# ── Catalog calls (TENANT-006) ─────────────────────────────────────────────


@obligation(Obligation.TENANT_006)
def test_each_publisher_pushes_its_own_domain_with_its_own_key(
    seat: Seat, pushed: Pushed, buyer: FreshAgent
) -> None:
    for tenant in (seat.first, seat.second):
        uri = tenant.uri(pushed.shared)
        assert stored(buyer, EXCHANGE_A_DOMAIN, uri), f"{tenant.name}'s own push is not offered"


@obligation(Obligation.TENANT_006, negative=True)
@CATALOG_CONTRIBUTOR_REFUSAL_NOT_PERMISSION_DENIED.mark
def test_a_publishers_push_on_the_other_publishers_domain_is_refused(
    seat: Seat, pushed: Pushed, buyer: FreshAgent
) -> None:
    """The first publisher signs a push of an entry on the second's domain: refused, not stored.

    Control: the same publisher's push of an entry on its own domain is stored.
    """
    first, second = seat.first, seat.second
    own = fresh_entry()
    accepted(first.catalog.push(HOME_SEAT, [first.entry(own.path, own.content_id)]))
    assert stored(buyer, EXCHANGE_A_DOMAIN, first.uri(own.path)), "the control push is not offered"

    foreign = second.entry(pushed.first_only, fresh_entry().content_id)
    error = refusal(first.catalog.push(HOME_SEAT, [foreign]), "an entry on the other's domain")
    assert error.code != "unauthenticated", f"the signature gate refused it: {error}"
    assert not stored(buyer, EXCHANGE_A_DOMAIN, second.uri(foreign.path)), "the push was stored"
    reason, paths = rejection(error)
    CATALOG_CONTRIBUTOR_REFUSAL_NOT_PERMISSION_DENIED.check(
        error.code == "permission_denied" and reason is not None,
        f"refused {error.code} with catalog_rejection {reason}",
    )
    assert (reason, paths) == (NOT_CATALOG_CONTRIBUTOR, [foreign.path]), (reason, paths)


@obligation(Obligation.TENANT_006, negative=True)
@REMOVE_RESOURCES_UNIMPLEMENTED.mark
def test_a_publishers_removal_of_the_other_publishers_page_is_refused(
    seat: Seat, pushed: Pushed, buyer: FreshAgent
) -> None:
    """The first publisher removes the second's shared page: refused, still offered at its price.

    Control: the same publisher's removal of its own page removes it.
    """
    first, second = seat.first, seat.second
    theirs = second.entry(pushed.shared, "")
    outcome = answer(first.catalog.remove(HOME_SEAT, [theirs]))
    REMOVE_RESOURCES_UNIMPLEMENTED.check(
        not (isinstance(outcome, CallError) and outcome.code == "unimplemented"),
        f"answered {outcome}",
    )
    assert isinstance(outcome, CallError), f"removing the other's page was answered: {outcome}"
    assert _price(_offer(buyer, second.uri(pushed.shared))) == second.rate, "the page moved"
    reason, _paths = rejection(outcome)
    assert (outcome.code, reason) == ("permission_denied", NOT_CATALOG_CONTRIBUTOR), outcome

    own = fresh_entry()
    accepted(first.catalog.push(HOME_SEAT, [first.entry(own.path, own.content_id)]))
    control = answer(first.catalog.remove(HOME_SEAT, [first.entry(own.path, own.content_id)]))
    assert not isinstance(control, CallError), f"the control removal was refused: {control}"
    assert not stored(buyer, EXCHANGE_A_DOMAIN, first.uri(own.path)), "the control did not remove"


# ── Discovery (TENANT-007) ─────────────────────────────────────────────────


@obligation(Obligation.TENANT_007)
def test_one_path_on_two_domains_is_offered_at_each_publishers_own_price(
    seat: Seat, pushed: Pushed, buyer: FreshAgent
) -> None:
    """The second publisher pushed the first's path and content id last; each URI keeps its price."""
    for tenant in (seat.first, seat.second):
        offer = _offer(buyer, tenant.uri(pushed.shared))
        assert _price(offer) == tenant.rate, f"{tenant.name}'s page is offered at {offer.offer}"


@obligation(Obligation.TENANT_007, negative=True)
def test_a_path_only_one_publisher_pushed_is_not_offered_under_the_other_domain(
    seat: Seat, pushed: Pushed, buyer: FreshAgent
) -> None:
    """The first publisher's own path, asked for under the second's domain: offered by no one.

    Control: the same path under the first publisher's domain is offered.
    """
    assert stored(buyer, EXCHANGE_A_DOMAIN, seat.first.uri(pushed.first_only)), "no control offer"
    uri = seat.second.uri(pushed.first_only)
    group = discover_group(buyer.exchange(EXCHANGE_A_DOMAIN), EXCHANGE_A_DOMAIN, uri, "discover")
    assert not group.result.verified, f"the other domain offers the first's path: {group}"
    assert group.absence_reason == _NOT_IN_CATALOG, f"answered {group}"


# ── Catalog and transaction records (TENANT-008) ───────────────────────────


@obligation(Obligation.TENANT_008)
def test_each_publishers_catalog_lists_its_own_pages_alone(
    compose_stack: StackURLs, seat: Seat, pushed: Pushed
) -> None:
    """Each catalog lists the pages its publisher pushed, and every row is on its own domain.

    The first publisher's catalog can hold more of its own pages, which the
    catalog-call tests above push as their controls; the second publisher pushes
    nothing else, so its catalog is exactly its one page.
    """
    first, second = seat.first, seat.second
    listed = {
        tenant.name: {row.uri for row in catalog(tenant.publisher.browser, compose_stack.console)}
        for tenant in (first, second)
    }
    for tenant in (first, second):
        foreign = [
            uri for uri in listed[tenant.name] if not uri.startswith(f"{tenant.site.origin}/")
        ]
        assert not foreign, f"{tenant.name}'s catalog lists pages it does not own: {foreign}"
    assert {first.uri(pushed.shared), first.uri(pushed.first_only)} <= listed[first.name], listed
    assert listed[second.name] == {second.uri(pushed.shared)}, listed


@obligation(Obligation.TENANT_008)
def test_each_publishers_transactions_and_income_hold_its_own_sale_alone(
    compose_stack: StackURLs, seat: Seat, sales: Sales, buyer: FreshAgent
) -> None:
    for tenant, sale in ((seat.first, sales.first), (seat.second, sales.second)):
        rows = _rows(compose_stack, tenant, sales.period)
        assert [(r.transaction_id, r.agent) for r in rows] == [(sale.transaction_id, buyer.host)], (
            f"{tenant.name}'s transactions: {rows}"
        )
        assert (sale.income.transactions, sale.income.gross) == (1, tenant.rate), (
            f"{tenant.name}'s income: {sale.income}"
        )


@obligation(Obligation.TENANT_008, negative=True)
def test_each_publisher_opens_its_own_sale_and_not_the_others(
    compose_stack: StackURLs, seat: Seat, sales: Sales, buyer: FreshAgent
) -> None:
    """Each publisher's own sale page verifies; the other's sale page is answered 404."""
    pairs = ((seat.first, sales.first, sales.second), (seat.second, sales.second, sales.first))
    for tenant, own, other in pairs:
        browser = tenant.publisher.browser
        detail = transaction(browser, compose_stack.console, own.transaction_id)
        verdict = verify_evidence(
            detail.evidence,
            exchange=EXCHANGE_A_DOMAIN,
            exchange_origin=compose_stack.exchange,
            agent_origin=f"http://{buyer.host}",
        )
        assert verdict.holds, f"{tenant.name}'s own sale: {verdict}"
        foreign = page_answer(
            browser, compose_stack.console, transaction_path(other.transaction_id)
        )
        assert foreign.status == 404, f"{tenant.name} opened the other's sale: {foreign.status}"
        assert other.transaction_id not in foreign.html, "the 404 page names the other's sale"


# ── Usage reports (TENANT-009) ─────────────────────────────────────────────


def _status(stack: StackURLs, tenant: Tenant, period: Period) -> str:
    return sole(_rows(stack, tenant, period), f"{tenant.name}'s sale").status


@obligation(Obligation.TENANT_009)
def test_a_report_of_one_publishers_sale_leaves_the_others_pending(
    compose_stack: StackURLs, seat: Seat, sales: Sales, buyer: FreshAgent
) -> None:
    """The agent reports its purchase from the first publisher; the second's sale stays pending."""
    for tenant in (seat.first, seat.second):
        assert _status(compose_stack, tenant, sales.period) == _PENDING, tenant.name
    item = sales.first.item
    report = report_body(
        exchange=EXCHANGE_A_DOMAIN,
        transaction_id=sales.first.transaction_id,
        consumed_quantity=1,
        billing_id=item.billing_id,
    )
    assert buyer.exchange(EXCHANGE_A_DOMAIN).report_usage(report).report_id, "no report id"
    poll_until(
        lambda: _status(compose_stack, seat.first, sales.period) == _RECEIVED,
        _REPORT_DEADLINE_S,
        failure=lambda: f"the first sale shows {_status(compose_stack, seat.first, sales.period)}",
    )
    assert _status(compose_stack, seat.second, sales.period) == _PENDING, "the other sale moved"

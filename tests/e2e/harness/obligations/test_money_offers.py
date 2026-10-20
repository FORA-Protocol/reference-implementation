"""Obligations MONEY-001 to MONEY-003, MONEY-032, MONEY-033 and MONEY-035: offers and their price.

The pinned protocol module ``github.com/FORA-Protocol/protocol`` states an
offer's price once, in ``Offer.pricing``; the term the offer sells carries none.
A resource with several terms yields one offer per term. A metered offer (its
pricing PER_UNIT) may carry an estimate or none. An estimate the publisher
states, on the term or once for the resource, is carried onto the offer, and an
item whose offer states one costs the estimate times the rate at purchase. An
offer without one is still offered and sold, and its purchase charges one unit.

Every offer is read from ``DiscoverResources`` through the SDK's own client as
the test's own agent, and every purchase is the SDK's ``ExecuteTransaction``,
with the charge read through ``GetAccountStatus``. A resource with the shape a
case needs is pushed by the test to exchange-a through ``CatalogService``.
"""

from __future__ import annotations

from decimal import Decimal
from typing import Any

import pytest

from ..catalog_calls import HOME_SEAT, accepted, contributor, fresh_entry
from ..catalog_push import (
    PRICING_MODEL_FLAT,
    PRICING_MODEL_FREE,
    PRICING_MODEL_PER_UNIT,
    license_term,
)
from ..defects import (
    METERED_COST_FROM_UNIT_COST,
    RECORD_ESTIMATE_DROPPED,
)
from ..exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN
from ..fresh_agent import FreshAgent
from ..money_path import (
    ARISTOTLE,
    RAIN,
    assert_bought,
    assert_moved,
    charged,
    discover_offer,
    discover_offers,
    only_item,
)
from ..obligation_registry import Obligation, obligation
from ..seed import SeededFixture
from ..stack_urls import StackURLs

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


def _push(stack: StackURLs, *terms: dict[str, Any], estimated_quantity: int | None = None) -> str:
    """Push one new resource to exchange-a carrying ``terms``; return its URI."""
    assert terms, "a probe resource names the terms it sells"
    entry = fresh_entry(terms=terms, estimated_quantity=estimated_quantity)
    accepted(contributor(stack).push(HOME_SEAT, [entry]))
    return entry.uri


def _pricing(offer: Any) -> dict[str, Any]:
    pricing = offer.offer.get("pricing")
    assert isinstance(pricing, dict), f"the offer carries no pricing: {offer.offer}"
    return pricing


def _assert_one_unpriced_term(offer: Any) -> None:
    terms = offer.offer.get("terms") or []
    assert len(terms) == 1, f"an offer carries exactly one term, this one {len(terms)}: {terms}"
    assert "pricing" not in terms[0], f"the offer's term repeats a price: {terms[0]}"


@obligation(Obligation.MONEY_001)
def test_a_resource_with_two_terms_is_offered_once_per_term(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed registers the catalog contributor
    fresh_agent: FreshAgent,
) -> None:
    """Two terms, two offers: distinct ids, one unpriced term each, each priced as its term."""
    uri = _push(
        compose_stack,
        license_term(model=PRICING_MODEL_FLAT, rate=1.25, currency="USD"),
        license_term(model=PRICING_MODEL_FREE, currency="USD"),
    )
    offers = discover_offers(fresh_agent, uri, EXCHANGE_A_DOMAIN)
    assert len(offers) == 2, f"two terms, {len(offers)} offers: {[o.offer for o in offers]}"
    assert len({o.offer.get("offer_id") for o in offers}) == 2, "two offers share an offer_id"
    for offer in offers:
        _assert_one_unpriced_term(offer)
    prices = sorted(
        (str(_pricing(o).get("model")), Decimal(str(_pricing(o).get("rate") or 0))) for o in offers
    )
    assert prices == [(PRICING_MODEL_FLAT, Decimal("1.25")), (PRICING_MODEL_FREE, Decimal(0))], (
        f"each offer must carry the price of the term it sells: {prices}"
    )


@obligation(Obligation.MONEY_002)
def test_an_offer_states_its_price_once_and_is_charged_that_price(fresh_agent: FreshAgent) -> None:
    """Aristotle's offer: priced in Offer.pricing, an unpriced term, and charged its rate."""
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    offer = discover_offer(fresh_agent, ARISTOTLE)
    _assert_one_unpriced_term(offer)
    pricing = _pricing(offer)
    assert pricing.get("model") == PRICING_MODEL_FLAT, pricing

    before = fresh_agent.balances(EXCHANGE_A_DOMAIN)
    item = only_item(fresh_agent.exchange(EXCHANGE_A_DOMAIN).execute(offer), "the purchase")
    assert_bought(item, "the purchase")
    currency, cost = charged(item)
    assert (currency, cost) == (str(pricing.get("currency")), Decimal(str(pricing.get("rate")))), (
        f"charged {cost} {currency}, the offer's price is {pricing}"
    )
    assert_moved(
        before, fresh_agent.balances(EXCHANGE_A_DOMAIN), {EXCHANGE_A_DOMAIN: {currency: cost}}
    )


@obligation(Obligation.MONEY_003)
def test_a_free_offer_charges_nothing(fresh_agent: FreshAgent) -> None:
    """Rain on a Tin Roof is FREE: the item costs zero and no balance moves."""
    exchange = EXCHANGE_C_DOMAIN
    fresh_agent.fund(exchange)
    offer = discover_offer(fresh_agent, RAIN)
    assert _pricing(offer).get("model") == PRICING_MODEL_FREE, _pricing(offer)

    before = fresh_agent.balances(exchange)
    item = only_item(fresh_agent.exchange(exchange).execute(offer), "the free purchase")
    assert_bought(item, "the free purchase")
    assert charged(item)[1] == 0, f"a free item was charged: {item.cost}"
    assert fresh_agent.balances(exchange) == before, "a free purchase moved money"


@obligation(Obligation.MONEY_035)
def test_a_metered_offer_without_an_estimate_is_offered_and_sold(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed registers the catalog contributor
    fresh_agent: FreshAgent,
) -> None:
    """A PER_UNIT term with no estimate anywhere is offered, verified, and bought.

    A metered offer may state a rate with or without an estimate. The offer is
    a verified PER_UNIT offer at the term's rate, and a purchase of it delivers.
    What the purchase charges is proved by the next test.
    """
    uri = _push(
        compose_stack,
        license_term(model=PRICING_MODEL_PER_UNIT, rate=0.25, unit="accesses", currency="USD"),
    )
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    offer = discover_offer(fresh_agent, uri, EXCHANGE_A_DOMAIN)
    pricing = _pricing(offer)
    assert pricing.get("model") == PRICING_MODEL_PER_UNIT, pricing
    assert Decimal(str(pricing.get("rate"))) == Decimal("0.25"), pricing
    estimate = pricing.get("estimated_quantity")
    assert estimate is None or (isinstance(estimate, int) and estimate > 0), (
        f"an estimate the offer states is positive: {pricing}"
    )
    item = only_item(fresh_agent.exchange(EXCHANGE_A_DOMAIN).execute(offer), "the purchase")
    assert_bought(item, "the purchase of a metered offer without an estimate")


@obligation(Obligation.MONEY_035)
def test_buying_a_metered_offer_without_an_estimate_charges_one_unit(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed registers the catalog contributor
    fresh_agent: FreshAgent,
) -> None:
    """The purchase of an unestimated metered offer at 0.25 USD costs one unit: 0.25 USD.

    Without an estimate the purchase charges one unit at the offer's rate, and that
    charge is final. The bought item's cost is read from ExecuteTransaction, and
    the buyer's balance drop from GetAccountStatus, both through the SDK.
    """
    uri = _push(
        compose_stack,
        license_term(model=PRICING_MODEL_PER_UNIT, rate=0.25, unit="accesses", currency="USD"),
    )
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    offer = discover_offer(fresh_agent, uri, EXCHANGE_A_DOMAIN)
    assert "estimated_quantity" not in _pricing(offer), _pricing(offer)
    before = fresh_agent.balances(EXCHANGE_A_DOMAIN)
    item = only_item(fresh_agent.exchange(EXCHANGE_A_DOMAIN).execute(offer), "the purchase")
    assert_bought(item, "the purchase of a metered offer without an estimate")
    currency, cost = charged(item)
    assert (currency, cost) == ("USD", Decimal("0.25")), (
        f"the item cost {cost} {currency}; one unit at the rate is 0.25 USD"
    )
    assert_moved(
        before, fresh_agent.balances(EXCHANGE_A_DOMAIN), {EXCHANGE_A_DOMAIN: {currency: cost}}
    )


@obligation(Obligation.MONEY_032)
@RECORD_ESTIMATE_DROPPED.mark
def test_a_metered_offer_carries_the_resources_estimate(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed registers the catalog contributor
    fresh_agent: FreshAgent,
) -> None:
    """A PER_UNIT term with no estimate, on a resource that states one: the offer carries it.

    The protocol lets a publisher state the estimate once for the resource, on
    ResourceEntry.estimated_quantity, instead of on each term. The offer for the
    metered term then carries that estimate on its pricing.
    """
    uri = _push(
        compose_stack,
        license_term(model=PRICING_MODEL_PER_UNIT, rate=0.25, unit="accesses", currency="USD"),
        estimated_quantity=7,
    )
    pricing = _pricing(discover_offer(fresh_agent, uri, EXCHANGE_A_DOMAIN))
    assert pricing.get("model") == PRICING_MODEL_PER_UNIT, pricing
    RECORD_ESTIMATE_DROPPED.check(
        "estimated_quantity" in pricing,
        f"the resource states an estimate of 7 and the offer carries none: {pricing}",
    )
    assert pricing.get("estimated_quantity") == 7, (
        f"the offer does not carry the resource's estimate of 7: {pricing}"
    )


def _metered_purchase(
    stack: StackURLs, agent: FreshAgent, *, rate: float, unit_cost: float | None, estimate: int
) -> tuple[Decimal, Decimal]:
    """Push and buy one metered offer; return (what the item cost, the balance drop)."""
    uri = _push(
        stack,
        license_term(
            model=PRICING_MODEL_PER_UNIT,
            rate=rate,
            unit_cost=unit_cost,
            estimated_quantity=estimate,
            unit="accesses",
            currency="USD",
        ),
    )
    agent.fund(EXCHANGE_A_DOMAIN)
    offer = discover_offer(agent, uri, EXCHANGE_A_DOMAIN)
    assert _pricing(offer).get("estimated_quantity") == estimate, _pricing(offer)
    before = agent.balance(EXCHANGE_A_DOMAIN, "USD")
    item = only_item(agent.exchange(EXCHANGE_A_DOMAIN).execute(offer), "the metered purchase")
    assert_bought(item, "the metered purchase")
    currency, cost = charged(item)
    assert currency == "USD", item.cost
    return cost, before - agent.balance(EXCHANGE_A_DOMAIN, "USD")


@obligation(Obligation.MONEY_033)
def test_a_metered_item_costs_the_estimate_times_the_rate(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed registers the catalog contributor
    fresh_agent: FreshAgent,
) -> None:
    """Four accesses at 0.25 USD: the item costs 1.00 USD and the balance drops by it."""
    cost, drop = _metered_purchase(
        compose_stack, fresh_agent, rate=0.25, unit_cost=None, estimate=4
    )
    assert cost == Decimal("1.00"), f"the item cost {cost}, the estimate times the rate is 1.00"
    assert drop == cost, f"the balance dropped by {drop}, the item cost {cost}"


@obligation(Obligation.MONEY_033)
@METERED_COST_FROM_UNIT_COST.mark
def test_a_metered_item_costs_the_rate_whatever_its_unit_cost(
    compose_stack: StackURLs,
    seeded: SeededFixture,  # noqa: ARG001 — ordering: the seed registers the catalog contributor
    fresh_agent: FreshAgent,
) -> None:
    """One access at a rate of 0.20 USD with a unit_cost of 0.05: the item costs 0.20 USD.

    unit_cost is a normalized comparison metric, in the Exchange's base currency.
    What the agent accepts at purchase is the estimate times the rate.
    """
    cost, drop = _metered_purchase(
        compose_stack, fresh_agent, rate=0.20, unit_cost=0.05, estimate=1
    )
    METERED_COST_FROM_UNIT_COST.check(
        cost == Decimal("0.20"), f"the item cost {cost}, the estimate times the rate is 0.20"
    )
    assert drop == cost, f"the balance dropped by {drop}, the item cost {cost}"

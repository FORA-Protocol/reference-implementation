"""E2E: an agent's spend budget at Broker Resolve, and delivery around it.

An agent declares a spend budget (``constraints.period_budget``) on a Resolve.
``BrokerService.Resolve`` in ``fora.proto`` of the pinned module
``github.com/FORA-Protocol/protocol`` may apply it as a filter: an offer the
budget leaves out is not returned, a URI left with no offer is answered OK with
``OFFER_ABSENCE_REASON_BUDGET_EXCEEDED``, and Resolve never charges.
The outcome is observed through the typed ``DiscoveryResponse`` an agent
consumes, and the purchase and delivery that follow an admitted Resolve.

Three legs, pinning one axis, budget presence:

1. Within budget: the offer is returned, and the purchase delivers real bytes.
2. Over budget: no offer, and the absence says BUDGET_EXCEEDED.
3. No budget set: the Broker applies no filter, and delivery succeeds.

A fourth test reads the agent's balances around two Resolves: Resolve never
charges.

Legs 1 and 3 buy the metered ``wooden-door-creak`` sound effect; leg 2 uses a
flat-priced resource. How the Broker computes a budget, which price it compares
and how it counts spend, is the Broker's own behaviour and not part of the
protocol, so no leg tests it: each budget here is far above or far below the
price.
"""

from __future__ import annotations

from contextlib import closing
from decimal import Decimal

from fora_sdk.core import DiscoveryResult

from .broker_purchase import (
    AI_INPUT,
    one_group_per_uri,
    only_item,
    only_verified_offer,
    resolve_request,
)
from .conftest import StackURLs
from .e2e_catalog import DemoResource
from .edge_fetch import fetch_signed
from .sdk_client import sdk_broker_client
from .defects import RESOLVE_BUDGET_REPORTED_AS_NOT_AUTHORIZED
from .fresh_agent import FreshAgent
from .exchanges import EXCHANGE_C_DOMAIN
from .money_path import SCI_FI_DOOR
from .obligation_registry import Obligation, obligation
from .seed import SeededFixture

# 0.01 USD: below any positive per-unit cost → over budget.
_BUDGET_OVER = "0.01"
# 10,000.00 USD: above any plausible demo cost → within budget.
_BUDGET_AMPLE = "10000.00"
_BUDGET_EXCEEDED = "OFFER_ABSENCE_REASON_BUDGET_EXCEEDED"


def _resolve_priced(
    compose_stack: StackURLs, res: DemoResource, *, budget: str | None
) -> DiscoveryResult:
    """Discover the priced demo resource as its buyer, with an optional USD budget.

    Broker Resolve is discovery-only AND it is where the budget PRE-FLIGHT gate
    lives — the Broker checks "can the agent afford the cheapest offer?" and
    refuses DISCOVERY (no offers + NOT_AUTHORIZED) when not, before any offer is
    surfaced. So the budget GATE is observed here, on the discovery answer;
    delivery (the signed URL) is the separate purchase below. The affordability
    pre-flight keys on the authenticated agent (see the module docstring);
    ``budget`` is the only axis that varies across these legs (absent ⇒ the gate
    is skipped).
    """
    buyer = sdk_broker_client(
        compose_stack, agent_id=res.buyer_agent_id, key_path=res.buyer_key_path
    )
    with closing(buyer) as client:
        return client.resolve(
            resolve_request(
                [res.uri],
                intended_use=AI_INPUT,
                period_budget=(budget, "USD") if budget is not None else None,
            )
        )


def _buy_priced(compose_stack: StackURLs, res: DemoResource, result: DiscoveryResult):
    """Buy the discovered priced offer through the Broker as its currency-matched buyer.

    The signed URL is minted on the purchase. The Broker routes from the signed
    offer.exchange, so no Exchange URL is supplied.
    """
    offer = only_verified_offer(
        one_group_per_uri(result, [res.uri], "broker resolve")[res.uri], "broker resolve"
    )
    buyer = sdk_broker_client(
        compose_stack,
        agent_id=res.buyer_agent_id,
        key_path=res.buyer_key_path,
    )
    with closing(buyer) as client:
        return only_item(client.execute([offer]), f"broker purchase of {res.uri}")


def test_within_budget_resolves_and_delivers_content(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """An ample budget → discovery passes the gate, and the purchase delivers."""
    res = seeded.priced_per_unit  # wooden-door-creak, PER_UNIT 0.25 USD
    # Phase 1 — discovery passes the budget pre-flight gate (ample budget).
    discovery = _resolve_priced(compose_stack, res, budget=_BUDGET_AMPLE)

    # Phase 2 — buy the discovered offer; the signed URL and the delivered price
    # ride on the purchase's result item.
    item = _buy_priced(compose_stack, res, discovery)

    # ADR-019: the PAID-term price is the canonical typed per-item cost.
    # unit_cost is the per-unit rate (amount = unit_cost × est_quantity), so it
    # equals the fixture term rate. Money is a decimal string on the wire.
    assert item.cost is not None, item
    assert Decimal(str(item.cost.unit_cost)) == Decimal(str(seeded.priced_per_unit_rate)), item.cost
    assert item.cost.currency == "USD", item.cost

    signed_url = item.retrieval_endpoint
    assert signed_url, item
    content = fetch_signed(signed_url, compose_stack, key_path=res.buyer_key_path)
    assert content.body.strip(), content.body[:256]


@obligation(Obligation.RELAY_024, negative=True)
@RESOLVE_BUDGET_REPORTED_AS_NOT_AUTHORIZED.mark
def test_a_budget_every_offer_exceeds_is_answered_budget_exceeded(fresh_agent: FreshAgent) -> None:
    """A budget below the price: no offer, and the absence says BUDGET_EXCEEDED.

    Sci-Fi Door is 12 USD flat. Under a 0.01 USD budget, Resolve filters its only
    offer out and answers OK with OFFER_ABSENCE_REASON_BUDGET_EXCEEDED, the only
    way the protocol reports a budget. The control after it, under a budget far
    above the price, returns the offer, so the budget is the only difference.
    """
    refused = fresh_agent.broker.resolve(
        resolve_request([SCI_FI_DOOR], period_budget=(_BUDGET_OVER, "USD"))
    )
    offered = [g.uri for g in refused.groups if g.result.verified or g.result.rejected]
    assert not offered, f"a budget below the price let offers through for {offered}"
    reasons = {refused.absence_reason} | {g.absence_reason for g in refused.groups}
    RESOLVE_BUDGET_REPORTED_AS_NOT_AUTHORIZED.check(
        _BUDGET_EXCEEDED in reasons, f"the absence was reported as {sorted(map(str, reasons))}"
    )
    control = fresh_agent.broker.resolve(
        resolve_request([SCI_FI_DOOR], period_budget=(_BUDGET_AMPLE, "USD"))
    )
    only_verified_offer(
        one_group_per_uri(control, [SCI_FI_DOOR], "control")[SCI_FI_DOOR], "control"
    )


@obligation(Obligation.RELAY_022)
def test_a_resolve_charges_nothing(fresh_agent: FreshAgent) -> None:
    """A Resolve that returns Sci-Fi Door moves no money at the Exchange that sells it.

    Returning an offer reserves nothing: the agent's balances at exchange-c are
    the same after two Resolves of the offer, one under a budget and one without,
    as before them.
    """
    fresh_agent.fund(EXCHANGE_C_DOMAIN)
    before = fresh_agent.balances(EXCHANGE_C_DOMAIN)
    budgets: tuple[tuple[str, str] | None, ...] = ((_BUDGET_AMPLE, "USD"), None)
    for budget in budgets:
        result = fresh_agent.broker.resolve(resolve_request([SCI_FI_DOOR], period_budget=budget))
        only_verified_offer(
            one_group_per_uri(result, [SCI_FI_DOOR], "the Resolve")[SCI_FI_DOOR], "the Resolve"
        )
    assert fresh_agent.balances(EXCHANGE_C_DOMAIN) == before, "a Resolve moved the agent's money"


def test_no_budget_skips_the_gate(
    compose_stack: StackURLs,
    seeded: SeededFixture,
) -> None:
    """SAME priced resolve but NO budget declared → licensed (gate skipped).

    The budget pre-flight engages ONLY when the agent declares a positive
    period_budget (checkBudget short-circuits on a non-positive limit). With no
    budget to meter against, the priced resolve surfaces an offer and delivery
    succeeds. The only difference from
    :func:`test_over_budget_is_refused_before_licensing` is the absence of a
    declared budget; the outcome flipping from refused to licensed pins the
    declared budget as the sole cause of enforcement.
    """
    res = seeded.priced_per_unit
    # Phase 1 — no budget → the Broker skips the gate, so the resolve surfaces an
    # offer even for the priced resource.
    discovery = _resolve_priced(compose_stack, res, budget=None)
    # Phase 2 — and delivery succeeds (nothing metered the priced request).
    item = _buy_priced(compose_stack, res, discovery)
    assert item.retrieval_endpoint, item

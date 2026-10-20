"""E2E: a purchase moves the buyer's money on the Exchange's ledger, and a refusal does not.

Every Exchange in this stack bills on its own TigerBeetle ledger, one ledger per
currency. This suite observes that ledger only through public surfaces, all of
them driven by the SDK's own client and signed with the buyer's own key:

* ``GetAccountStatus`` reports the buyer's available balance in every currency
  the Exchange bills in, read live from the ledger;
* ``DiscoverResources`` and ``ExecuteTransaction`` buy the item.

So each test is a full round-trip: the purchase goes in through the Exchange's
RPC surface and the ledger's answer comes back out through the same surface.
Nothing here reads the ledger, the database or the Exchange's process.

The paying buyer is the test's own agent (``fresh_agent``), whose account its own
signed Register opened with the welcome credit in both currencies. The refused
buyer is the seed's credit-less buyer, a fixed identity because its account has
to be opened while the seats hold new agents for review; see ``seed_accounts``.
Every claim here is about the CHANGE around one purchase, never about an
absolute balance.

Money is compared as ``Decimal``: the Exchange sends exact decimal strings, and
float arithmetic would turn an exact 0.25 into an approximate one.
"""

from __future__ import annotations

from contextlib import closing
from decimal import Decimal

import pytest
from fora_sdk.core import VerifiedOffer, Verifier
from fora_sdk.sync import Client

from .broker_purchase import resource_query
from .conftest import StackURLs
from .e2e_catalog import DemoResource
from .exchanges import EXCHANGE_C_DOMAIN
from .fresh_agent import FreshAgent
from .ledger import BILLED_CURRENCIES, balances
from .sdk_client import offer_verifier, sdk_exchange_client
from .seed import SeededFixture

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_INSUFFICIENT_BALANCE = "DENIAL_REASON_INSUFFICIENT_BALANCE"


@pytest.fixture(scope="module")
def verifier(compose_stack: StackURLs) -> Verifier:  # noqa: ARG001 — the stack must be up
    """Holds exchange-c's offer-signing key: the priced resource below is sold there."""
    return offer_verifier([EXCHANGE_C_DOMAIN])


def _the_offer(client: Client, res: DemoResource) -> VerifiedOffer:
    """The one verified offer exchange-c issues for ``res``."""
    result = client.discover(resource_query(EXCHANGE_C_DOMAIN, [res.uri]))
    groups = [group for group in result.groups if group.uri == res.uri]
    assert len(groups) == 1, f"want one offer group for {res.uri}, got {result.groups}"
    verified = groups[0].result.verified
    rejected = [(r.offer.get("offer_id"), r.reason) for r in groups[0].result.rejected]
    assert len(verified) == 1 and not rejected, (
        f"want one verified offer for {res.uri}; verified={len(verified)} rejected={rejected}"
    )
    offer = verified[0]
    assert offer.offer.get("exchange") == EXCHANGE_C_DOMAIN, (
        f"the offer for {res.uri} is sold by {offer.offer.get('exchange')!r}, not exchange-c"
    )
    return offer


def test_a_paid_purchase_lowers_the_balance_in_its_currency_by_its_cost(
    fresh_agent: FreshAgent, seeded: SeededFixture
) -> None:
    """Status, buy, status: the purchase currency drops by the cost, the other stays put.

    The buyer is this test's own agent, funded at exchange-c by its Register
    there, so the account holds the welcome credit and nothing another test spent.
    """
    res = seeded.priced_per_unit
    fresh_agent.fund(EXCHANGE_C_DOMAIN)
    client = fresh_agent.exchange_client(EXCHANGE_C_DOMAIN)
    before = balances(client, EXCHANGE_C_DOMAIN)
    purchased = client.execute(_the_offer(client, res))
    after = balances(client, EXCHANGE_C_DOMAIN)

    assert purchased.items, f"execute answered no items: {purchased}"
    item = purchased.items[0]
    assert item.denial_reason is None, f"the purchase was denied: {item}"
    assert item.cost is not None and item.cost.currency == res.currency, (
        f"the item is priced in {res.currency}, but execute charged {item.cost}"
    )
    cost = Decimal(str(item.cost.amount))
    assert cost > 0, f"the resource was expected to be paid: {item}"

    assert after[res.currency] == before[res.currency] - cost, (
        f"{res.currency} went from {before[res.currency]} to {after[res.currency]}; "
        f"the purchase cost {cost}"
    )
    for currency in BILLED_CURRENCIES - {res.currency}:
        assert after[currency] == before[currency], (
            f"a {res.currency} purchase moved the {currency} balance from "
            f"{before[currency]} to {after[currency]}"
        )


def test_a_refused_purchase_moves_no_money(
    compose_stack: StackURLs, verifier: Verifier, seeded: SeededFixture
) -> None:
    """The credit-less buyer is refused for its balance, and its balances stay where they were.

    Its account is active and empty, so the paid purchase reaches the ledger's
    balance check and stops there. The refusal must hold nothing back either: a
    hold placed and left behind would show as a lower available balance.
    """
    res = seeded.priced_per_unit
    with closing(
        sdk_exchange_client(
            compose_stack,
            EXCHANGE_C_DOMAIN,
            verifier,
            agent_id=seeded.nobilling_agent_id,
            key_path=seeded.nobilling_agent_key_path,
        )
    ) as client:
        before = balances(client, EXCHANGE_C_DOMAIN)
        refused = client.execute(_the_offer(client, res))
        after = balances(client, EXCHANGE_C_DOMAIN)

    # The seed registers this buyer while the seat holds new agents for review,
    # so no welcome credit is granted, and then switches its account on: it is
    # open in every currency and holds nothing in any of them. A
    # non-zero figure here means the seed funded the buyer meant to be unfunded.
    assert all(amount == 0 for amount in before.values()), (
        f"the credit-less buyer holds money at exchange-c: {before}"
    )
    assert refused.items, f"execute answered no items: {refused}"
    item = refused.items[0]
    assert item.denial_reason is not None and item.denial_reason.value == _INSUFFICIENT_BALANCE, (
        f"the credit-less buyer must be refused {_INSUFFICIENT_BALANCE}: {item}"
    )
    assert not item.retrieval_endpoint, f"a refused purchase issued a delivery URL: {item}"
    assert after == before, f"a refused purchase moved money: {before} -> {after}"

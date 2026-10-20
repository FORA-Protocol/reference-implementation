"""Obligation PAID-006: a buyer with nothing to pay with cannot accept an offer.

Driven against the demo ``epicurus`` resource (PER_UNIT USD) under the
credit-less ``agent-nobilling-e2e`` identity: keyed, and registered by the
seed while the seat holds new agents for review, so it was granted no welcome
credit; the seed then switched its account on, so it is active and holds
nothing on the Exchange's ledger.

The refusal surfaces as a per-item denial inside a successful answer: on the
items[] batch path the Exchange classifies a no-billing buyer as a PER-ITEM
denial rather than aborting the envelope (``KindBillingDenied`` →
``denialReasonByKind``, denial.go). The observables:

1. the purchase is answered, not refused: the SDK client returns its typed
   ``TransactionResponse`` rather than raising.
2. ``items[0].denial_reason`` is the billing-family reason
   ``DENIAL_REASON_INSUFFICIENT_BALANCE`` — the reason the platform states.
3. no ``items[0].retrieval_endpoint`` is issued, and the denied item names no
   transaction and no billing record.
4. no sale is recorded under the credit-less agent. The agent cannot read its
   usage record through any RPC, but it can read the money a recorded sale
   moves: its balances, as ``GetAccountStatus`` reports them, are the same after
   the refusal as before it.

Control: the test's own agent, funded at exchange-a with its welcome credit,
accepts the same priced offer, and its balance, read the same way, drops by
exactly the item's cost. So the offer is priced, and a sale of it is visible to
the reading (4) relies on.

Every leg goes in through the Exchange's public RPCs, sent and read through the
SDK's own client; no leg reads the deployment's datastore.
"""

from __future__ import annotations

from contextlib import closing

import pytest

from ..broker_purchase import discover_group
from ..conftest import StackURLs
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import FreshAgent
from ..ledger import balances
from ..sdk_client import sdk_exchange_client, stack_offer_verifier
from ..seed import DEMO_PHILOSOPHY_DOMAIN, SeededFixture
from .flow import buy_verified_and_observe_charge
from ..obligation_registry import Obligation, obligation

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


_RESOURCE_URI = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/epicurus.txt"

# The billing-family per-item denial reason the items[] batch path emits for a
# credit-less buyer.
_INSUFFICIENT_BALANCE = "DENIAL_REASON_INSUFFICIENT_BALANCE"


@obligation(Obligation.PAID_006, negative=True)
def test_credit_less_agent_is_refused_with_billing_signal_and_no_url(
    compose_stack: StackURLs, seeded: SeededFixture, fresh_agent: FreshAgent
) -> None:
    """No billing arrangement → refusal, billing-family reason, no URL, no money moved."""
    # The credit-less buyer is a fixed identity: its account has to be opened while
    # the seats hold new agents for review, which only the seed can do.
    with closing(
        sdk_exchange_client(
            compose_stack,
            EXCHANGE_A_DOMAIN,
            stack_offer_verifier(),
            agent_id=seeded.nobilling_agent_id,
            key_path=seeded.nobilling_agent_key_path,
        )
    ) as credit_less:
        group = discover_group(credit_less, EXCHANGE_A_DOMAIN, _RESOURCE_URI, "discovery")
        assert group.result.verified, f"DiscoverResources returned no offers: {group}"
        offer = group.result.verified[0]
        before = balances(credit_less, EXCHANGE_A_DOMAIN)
        # The SDK signs the agent acceptance with the credit-less agent's own key,
        # so the request PASSES the acceptance gate and REACHES the billing gate,
        # which is the refusal this scenario asserts.
        response = credit_less.execute(offer)
        after = balances(credit_less, EXCHANGE_A_DOMAIN)

    # (1) The purchase was answered: execute returned rather than raised.
    items = response.items or []
    assert len(items) == 1, f"one offer bought, {len(items)} items answered: {response}"
    item = items[0]
    # (2) Billing-family per-item denial reason, the reason the platform states.
    assert item.denial_reason is not None and item.denial_reason.value == _INSUFFICIENT_BALANCE, (
        f"credit-less buyer must surface items[0].denial_reason == {_INSUFFICIENT_BALANCE!r}; "
        f"got {item.denial_reason!r} (item={item})"
    )
    # (3) No signed URL on the denied item, and nothing a sale would carry.
    assert item.retrieval_endpoint is None, (
        f"denial must NOT issue items[0].retrieval_endpoint; got {item.retrieval_endpoint!r}"
    )
    assert not item.transaction_id and not item.billing_id, (
        f"a denied item must name no transaction and no billing record: {item}"
    )

    # (4) No sale recorded under the credit-less agent: its balances did not move.
    assert after == before, (
        f"the refused purchase moved the credit-less agent's money: {before} -> {after}"
    )

    # Control: a funded buyer accepting the same priced offer IS charged, and the
    # same reading shows it.
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    funded = discover_group(
        fresh_agent.exchange(EXCHANGE_A_DOMAIN), EXCHANGE_A_DOMAIN, _RESOURCE_URI, "control"
    )
    assert funded.result.verified, f"the control discovery returned no offers: {funded}"
    buy_verified_and_observe_charge(
        fresh_agent, EXCHANGE_A_DOMAIN, funded.result.verified[0], "control purchase"
    )

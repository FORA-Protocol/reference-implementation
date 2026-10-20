"""Obligation PAID-004: the agent reports usage, and the paid access is recorded.

Driven against the demo ``epicurus`` resource (PER_UNIT 0.0001/characters USD)
as the test's own agent (``fresh_agent``), funded at exchange-a. The recorded
access and the report filed against it belong to that agent alone, so no other
test's purchases or reports are in the record this test reads:

  1. Discover the offer and accept it. The buyer's balance, read through
     ``GetAccountStatus`` before and after, drops by exactly the item's cost in
     USD and does not move in any other currency: the paid access, its price and
     its currency, as the buyer's own account shows them.
  2. Fetch the signed URL through the Cloudflare edge (the "after delivery" half).
  3. Read the recorded access back from exchange-a's admin plane, the operator
     evidence read: the offer the sale was made on names the resource URI, and
     the record carries the time the sale was made.
  4. Report the usage (ReportUsage, through the SDK's own client) for the
     returned transaction_id. The agent read the whole article, so it reports
     the 2331 characters the publisher states for it. The report is accepted.
  5. Replay the same report under the same idempotency key. The Exchange answers
     a replay from the accepted report it stored, so the same report_id coming
     back shows the report was recorded against that transaction; every other
     exchange refuses the same report not_found, so it was recorded nowhere else.
     The admin plane then shows the obligation fulfilled with the reported
     quantity.

Legs 1, 2, 4 and the replay go in through the Exchange's public RPCs and the
edge, and are read back through them. Legs 3 and 5 also read the Exchange's
admin plane, which no buyer can reach, because no public RPC returns a buyer's
recorded transactions. So the URI, the timestamp and the recorded quantity are
an operator's view of the record, not a protocol round-trip.

The report is refused today. epicurus states its estimate once, on the resource
(ResourceEntry.estimated_quantity). The Exchange's offer drops it, and the
Exchange then refuses a positive quantity against the zero it recorded. The test
is a strict xfail on that defect, which holds only for a quantity refusal of a
report against an offer that carries no estimate.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
from decimal import Decimal
from typing import Any

import pytest
from fora_sdk.client import CallError
from wire.models import UsageReport

from ..admin_plane import require_admin_plane, transaction_evidence
from ..defects import RECORD_ESTIMATE_DROPPED
from ..exchanges import recipient_of
from ..reporting import assert_report_recorded_only_at, file_report, report_body
from ..conftest import StackURLs
from ..edge_fetch import fetch_signed
from ..fresh_agent import FreshAgent
from ..money_path import discover_offer
from ..seed import DEMO_PHILOSOPHY_DOMAIN
from .flow import buy_verified_and_observe_charge
from ..obligation_registry import Obligation, obligation

# ADR-008 D5 — declare stack-isolation contract.
pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


# epicurus: PER_UNIT 0.0001/characters USD, individual, US/GB.
_RESOURCE_URI = f"http://{DEMO_PHILOSOPHY_DOMAIN}/articles/philosophers/epicurus.txt"
_EXPECTED_UNIT_COST = Decimal("0.0001")
_CURRENCY = "USD"
# The characters the agent consumed: the whole article, whose size the publisher
# states on the resource's catalog entry (estimated_quantity).
_CONSUMED_CHARACTERS = 2331


def _file_consumed_usage(
    stack: StackURLs, report: UsageReport, offer: dict[str, Any], agent: FreshAgent
) -> str:
    """File the report of the consumed characters; return its report_id.

    A refusal for the quantity, of a report against an offer that carries no
    estimate, is the record-estimate defect: the publisher stated the estimate on
    the resource, and the Exchange's offer dropped it. Any other refusal fails the
    test.
    """
    try:
        return file_report(stack, report, agent_id=agent.agent_id, key_path=agent.key_path)
    except CallError as refused:
        pricing = offer.get("pricing") or {}
        RECORD_ESTIMATE_DROPPED.check(
            "estimated_quantity" in pricing or refused.code != "invalid_argument",
            f"the offer carries no estimate ({pricing}) and the report of "
            f"{_CONSUMED_CHARACTERS} characters was refused: {refused}",
        )
        raise


@obligation(Obligation.PAID_004)
@RECORD_ESTIMATE_DROPPED.mark
def test_paid_usage_record_shows_uri_price_currency_and_timestamp(
    compose_stack: StackURLs,
    fresh_agent: FreshAgent,
) -> None:
    """Usage record for a paid demo access carries URI, non-zero price, currency, timestamp."""
    require_admin_plane()
    seller = recipient_of(compose_stack.exchange)
    fresh_agent.fund(seller)

    offer = discover_offer(fresh_agent, _RESOURCE_URI, seller)
    # (1) Accept, and read the charge off the buyer's own balance: it drops by
    # exactly the item's cost in that cost's currency, and by nothing elsewhere.
    item = buy_verified_and_observe_charge(fresh_agent, seller, offer, "the paid access")
    cost = item.cost
    assert cost is not None, f"the paid item carries no cost: {item}"
    # "price" — exact decimal equality against the term rate.
    assert Decimal(str(cost.unit_cost)) == _EXPECTED_UNIT_COST, (
        f"the charged unit cost must equal the term rate {_EXPECTED_UNIT_COST}; got {cost}"
    )
    # "currency"
    assert cost.currency == _CURRENCY, f"currency must be {_CURRENCY!r}; got {cost}"

    signed_url = item.retrieval_endpoint
    transaction_id = str(item.transaction_id)
    billing_id = str(item.billing_id or "")
    assert signed_url, f"signed URL missing from the paid item: {item}"
    assert billing_id, f"billing_id missing from the paid item: {item}"

    # (2) "After delivery" — the accept-minted URL is bound to the executing
    # agent's key, so the fetch presents that same key.
    fetch_signed(
        signed_url,
        compose_stack,
        expect_marker="Epicurus",
        key_path=fresh_agent.key_path,
    )

    # (3) "with the resource URI" and "timestamp", read from the admin plane: the
    # offer the sale was made on names the resource, and the record its time.
    recorded = transaction_evidence(seller, transaction_id)
    assert recorded.requester_id == fresh_agent.agent_id, (
        f"the access is recorded for {recorded.requester_id!r}, not the buyer: {recorded}"
    )
    recorded_uri = (recorded.offer.get("identity") or {}).get("canonical_url")
    assert recorded_uri == _RESOURCE_URI, (
        f"the recorded access must name {_RESOURCE_URI!r}; got {recorded_uri!r}"
    )
    now = datetime.now(tz=UTC)
    assert now - recorded.created_at < timedelta(minutes=5), (
        f"the recorded access time {recorded.created_at!r} is older than the test run "
        f"(now = {now!r})"
    )

    # (4) "the agent reports the usage back" — and the report MUST be accepted.
    report = report_body(
        exchange=seller,
        transaction_id=transaction_id,
        consumed_quantity=_CONSUMED_CHARACTERS,
        billing_id=billing_id,
    )
    # The SDK's client signs as the test's agent, the agent the transaction is
    # recorded under, and gives every send a fresh signature, so the replay
    # below is a protocol replay (same idempotency key) rather than a signature
    # the Exchange's request-replay guard refuses.
    report_id = _file_consumed_usage(compose_stack, report, offer.offer, fresh_agent)

    # (5) The report was recorded against the transaction: a replay under the same
    # idempotency key is answered from the stored report with the same report_id,
    # and the obligation the sale minted is fulfilled with the reported quantity.
    assert_report_recorded_only_at(
        compose_stack,
        report,
        report_id,
        agent_id=fresh_agent.agent_id,
        key_path=fresh_agent.key_path,
    )
    owed = transaction_evidence(seller, transaction_id).obligation
    assert owed is not None and owed.fulfilled_at is not None, (
        f"the report was not recorded against {transaction_id}: {owed}"
    )
    assert Decimal(str(owed.consumed_quantity)) == _CONSUMED_CHARACTERS, (
        f"the obligation records {owed.consumed_quantity!r} consumed, "
        f"want the {_CONSUMED_CHARACTERS} reported"
    )

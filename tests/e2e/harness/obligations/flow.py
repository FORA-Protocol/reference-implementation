"""The control purchase the obligation tests share.

A test that claims "no sale was recorded" reads the buyer's balance before and
after the action it drives. That claim needs a control: the same reading does
move when a paid sale IS recorded. :func:`buy_verified_and_observe_charge` is
that control, on the SDK's own client: the offer comes from the SDK's discovery,
the purchase is the SDK's ExecuteTransaction signed and accepted as the agent,
and both balance readings are its GetAccountStatus.
"""

from __future__ import annotations

from fora_sdk import to_wire
from fora_sdk.core import VerifiedOffer
from wire.models import TransactionResultItem

from ..fresh_agent import FreshAgent
from ..ledger import assert_charged


def buy_verified_and_observe_charge(
    agent: FreshAgent, exchange: str, offer: VerifiedOffer, what: str
) -> TransactionResultItem:
    """Buy ``offer`` at ``exchange`` as ``agent``; assert its balance dropped by the item's cost.

    The purchase is the SDK's ExecuteTransaction, signed and accepted as the agent,
    and both balance readings are its GetAccountStatus: the balance drops by
    exactly the item's cost in that cost's currency, and by nothing elsewhere.
    Returns the bought item.
    """
    before = agent.balances(exchange)[exchange]
    response = agent.exchange(exchange).execute(offer)
    after = agent.balances(exchange)[exchange]
    items = response.items or []
    assert len(items) == 1, f"{what}: one offer bought, {len(items)} items answered: {response}"
    item = items[0]
    assert item.denial_reason is None, f"{what}: the purchase was denied: {item}"
    assert item.transaction_id, f"{what}: the purchase names no transaction: {item}"
    assert_charged(before, after, to_wire(item.cost) if item.cost else None, what)
    return item

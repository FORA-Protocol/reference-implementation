"""Obligation RELAY-015: a relayed item whose AgentAcceptance does not verify is denied, in the body.

``DENIAL_REASON_SIGNATURE_INVALID`` and ``Requester.domain`` in ``fora.proto`` of the
pinned module ``github.com/FORA-Protocol/protocol``: on a purchase a Broker relayed,
the Exchange verifies each item's AgentAcceptance against the keys the directory
``requester.domain`` names publishes, never against the Broker's key. An item whose
acceptance does not verify under them is denied with SIGNATURE_INVALID, in the body,
exactly as for the AgentRequestAcceptance: that item is denied, and the whole request
is not refused.

The purchase is the agent's, through the Broker, of two items exchange-a sells, so
the Broker relays both to exchange-a in one sub-request. The control buys both. The
altered purchase is the same purchase with the first item's AgentAcceptance signed,
over exactly the bytes the agent signed, by a second agent's key: a key that
second agent's own directory publishes, and the buyer's directory does not. Nothing
else changes, and the SDK signs the request afterwards, so the Broker's checks pass
and the acceptance is the one thing an Exchange can refuse. The first item is
denied SIGNATURE_INVALID with nothing sold on it, the second is bought, and the
buyer's balance at exchange-a drops by the second item's cost alone.

The request is the one the SDK built, altered in its pre-signing hook as a declared
mutation and sent after its unaltered control (``harness.malformed``). Balances are
read through ``GetAccountStatus``.
"""

from __future__ import annotations

from typing import Any

import pytest
from fora_sdk.wire import to_wire
from wire.models import BrokerTransactionResponse

from ..defects import RELAYED_ACCEPTANCE_REFUSES_WHOLE_SUBREQUEST
from ..exchanges import EXCHANGE_A_DOMAIN
from ..fresh_agent import AgentFactory
from ..ledger import Balances, assert_charged
from ..malformed import Alteration, Mutation, malformed_answer
from ..money_path import (
    ARISTOTLE,
    SIGNATURE_INVALID,
    THALES,
    assert_bought,
    assert_denied,
    broker_purchase,
    resolve_offers,
)
from ..obligation_registry import Obligation, obligation
from ..requester_alteration import acceptance_signed_by

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


def _by_offer(response: BrokerTransactionResponse) -> dict[str, Any]:
    """The answer's items keyed by the offer each one answers."""
    return {str(item.offer_id): item for item in response.items or []}


@obligation(Obligation.RELAY_015, negative=True)
@RELAYED_ACCEPTANCE_REFUSES_WHOLE_SUBREQUEST.mark
def test_an_item_whose_relayed_acceptance_does_not_verify_is_denied_and_the_rest_is_bought(
    fresh_agent_factory: AgentFactory,
) -> None:
    """The first item's acceptance by another agent's key: denied SIGNATURE_INVALID; the second bought."""
    buyer, other = fresh_agent_factory(), fresh_agent_factory()
    buyer.fund(EXCHANGE_A_DOMAIN)
    offers = resolve_offers(buyer, [ARISTOTLE, THALES])
    forged_id, kept_id = (str(offer.offer.get("offer_id")) for offer in offers)
    after_control: dict[str, Balances] = {}

    def bought(response: BrokerTransactionResponse) -> None:
        for item in response.items or []:
            assert_bought(item, "control: both acceptances by the buyer's own key")
        after_control.update(buyer.balances(EXCHANGE_A_DOMAIN))

    answer = malformed_answer(
        Mutation.SIGNATURE,
        "the first item's AgentAcceptance is signed by a key the buyer's directory does not "
        "publish",
        control=broker_purchase(buyer, offers),
        alter=Alteration(before_sign=acceptance_signed_by(0, other.ed25519)),
        accepted=bought,
    )

    items = _by_offer(answer)
    assert set(items) == {forged_id, kept_id}, f"the answer does not cover both items: {answer}"
    RELAYED_ACCEPTANCE_REFUSES_WHOLE_SUBREQUEST.check(
        items[forged_id].refusal is None and items[kept_id].refusal is None,
        f"the sub-request was refused: {items[forged_id].refusal}",
    )
    assert_denied(items[forged_id], SIGNATURE_INVALID, "the item whose acceptance does not verify")
    kept = items[kept_id]
    assert_bought(kept, "the item whose acceptance verifies, in the same sub-request")
    assert_charged(
        after_control[EXCHANGE_A_DOMAIN],
        buyer.balances(EXCHANGE_A_DOMAIN)[EXCHANGE_A_DOMAIN],
        to_wire(kept.cost) if kept.cost else None,
        "the altered purchase charges only the item whose acceptance verifies",
    )

"""Obligations RELAY-006, RELAY-016 and RELAY-017: the set the agent signed, and requests that break it.

``AgentRequestAcceptance`` in ``fora.proto`` of the pinned module
``github.com/FORA-Protocol/protocol`` fixes the complete ordered set of items the
agent authorizes. Each Exchange requires its sub-request to equal the in-order
projection of that set onto itself; a direct request is the degenerate
projection, the whole set:

* a request whose items are not exactly that projection (an item dropped, or the
  items reordered) is refused per item, in the body: every item is denied
  DENIAL_REASON_SIGNATURE_INVALID, nothing is bought;
* the check runs before the idempotency lookup, so a mutated request is never
  answered from the stored result of the honest request under the same key, and
  never changes it.

At the Broker, a purchase missing an item's AgentAcceptance or the
AgentRequestAcceptance, or whose request acceptance does not list the items in
order, is malformed: ``invalid_argument``, and nothing is bought anywhere.

Every request is the one the SDK built, altered in its pre-signing hook as a
declared mutation and sent after its unaltered control (``harness.malformed``).
"""

from __future__ import annotations

import uuid
from collections.abc import Callable
from typing import Any

import pytest
from fora_sdk.client import CallError
from wire.models import BrokerTransactionResponse, TransactionResponse

from ..defects import PROJECTION_MISMATCH_REFUSED_WHOLE
from ..exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN
from ..fresh_agent import FreshAgent
from ..ledger import Balances
from ..malformed import Alteration, Mutation, malformed, malformed_outcome, patch_body
from ..money_path import (
    ARISTOTLE,
    SCI_FI_DOOR,
    SIGNATURE_INVALID,
    THALES,
    assert_bought,
    assert_denied,
    broker_purchase,
    direct_purchase,
    discover_offer,
    resolve_offers,
    typed_reasons,
)
from ..obligation_registry import Obligation, obligation

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")


def _drop_last(body: dict[str, Any]) -> None:
    body["items"].pop()


def _swap(body: dict[str, Any]) -> None:
    body["items"].reverse()


def _answered(outcome: object) -> TransactionResponse:
    """The OK answer the protocol requires for a set mismatch, or the defect it is not."""
    PROJECTION_MISMATCH_REFUSED_WHOLE.check(
        not isinstance(outcome, CallError), f"the request was refused: {outcome}"
    )
    assert isinstance(outcome, TransactionResponse), outcome
    return outcome


def _two_items(agent: FreshAgent) -> list[Any]:
    agent.fund(EXCHANGE_A_DOMAIN)
    return [discover_offer(agent, ARISTOTLE), discover_offer(agent, THALES)]


@obligation(Obligation.RELAY_016, negative=True)
@PROJECTION_MISMATCH_REFUSED_WHOLE.mark
@pytest.mark.parametrize(
    "change", [_drop_last, _swap], ids=["an item dropped", "the items reordered"]
)
def test_a_request_that_is_not_the_signed_set_has_every_item_denied(
    fresh_agent: FreshAgent, change: Callable[[dict[str, Any]], None]
) -> None:
    """The signed set is bought; altered, every item is denied SIGNATURE_INVALID in the body."""
    offers = _two_items(fresh_agent)
    after_control: dict[str, Balances] = {}

    def bought(response: TransactionResponse) -> None:
        for item in response.items or []:
            assert_bought(item, "control: the signed set")
        after_control.update(fresh_agent.balances(EXCHANGE_A_DOMAIN))

    outcome = malformed_outcome(
        Mutation.SIGNATURE,
        "the items are not the set the agent's request acceptance signs",
        control=direct_purchase(fresh_agent, EXCHANGE_A_DOMAIN, offers),
        alter=Alteration(before_sign=patch_body(change)),
        accepted=bought,
    )
    answer = _answered(outcome)
    assert answer.items, f"the altered request was answered with no items: {answer}"
    for item in answer.items:
        assert_denied(item, SIGNATURE_INVALID, "an item of the altered set")
    assert fresh_agent.balances(EXCHANGE_A_DOMAIN) == after_control, "the altered set moved money"


@obligation(Obligation.RELAY_017, negative=True)
@PROJECTION_MISMATCH_REFUSED_WHOLE.mark
def test_a_mutated_request_is_never_served_from_the_honest_requests_stored_result(
    fresh_agent: FreshAgent,
) -> None:
    """Under the honest request's key, a dropped item is denied, and the stored result stands.

    The control buys the signed set under key K. The same set with an item
    dropped, under K, is neither a replay of the control nor a reused key: the
    set claim fails first, so every item is denied SIGNATURE_INVALID. A retry of
    the honest request under K then replays the control's purchase.
    """
    offers = _two_items(fresh_agent)
    key = f"projection-{uuid.uuid4().hex}"
    stored: dict[str, list[str]] = {}

    def bought(response: TransactionResponse) -> None:
        stored["tx"] = [str(item.transaction_id) for item in response.items or []]

    honest = direct_purchase(fresh_agent, EXCHANGE_A_DOMAIN, offers, idempotency_key=key)
    outcome = malformed_outcome(
        Mutation.SIGNATURE,
        "an item dropped from the signed set, under the key the honest request used",
        control=honest,
        alter=Alteration(before_sign=patch_body(_drop_last)),
        accepted=bought,
    )
    answer = _answered(outcome)
    for item in answer.items or []:
        assert_denied(item, SIGNATURE_INVALID, "the mutated request under the honest key")
    retry = fresh_agent.exchange(EXCHANGE_A_DOMAIN).execute(offers, idempotency_key=key)
    assert [str(item.transaction_id) for item in retry.items or []] == stored["tx"], (
        f"the honest retry no longer replays the stored purchase: {retry}"
    )


def _drop_acceptance(body: dict[str, Any]) -> None:
    del body["items"][0]["agent_acceptance"]


def _drop_request_acceptance(body: dict[str, Any]) -> None:
    del body["agent_request_acceptance"]


@obligation(Obligation.RELAY_006, negative=True)
@pytest.mark.parametrize(
    "change",
    [_drop_acceptance, _drop_request_acceptance, _swap],
    ids=["an item without its acceptance", "no request acceptance", "the items reordered"],
)
def test_a_malformed_purchase_is_refused_by_the_broker(
    fresh_agent: FreshAgent, change: Callable[[dict[str, Any]], None]
) -> None:
    """The Broker refuses the malformed purchase invalid_argument, and nothing is bought."""
    fresh_agent.fund(EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN)
    offers = resolve_offers(fresh_agent, [SCI_FI_DOOR, THALES])
    after_control: dict[str, dict[str, Balances]] = {}

    def bought(response: BrokerTransactionResponse) -> None:
        for item in response.items or []:
            assert_bought(item, "control")
        after_control["now"] = fresh_agent.balances(EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN)

    refusal = malformed(
        Mutation.SIGNATURE,
        "the purchase lacks the agent's consent to the set as sent",
        control=broker_purchase(fresh_agent, offers),
        alter=Alteration(before_sign=patch_body(change)),
        accepted=bought,
    )
    assert refusal.code == "invalid_argument", f"refused as {refusal.code!r}: {refusal}"
    assert not typed_reasons(refusal), f"a malformed purchase carries no typed reason: {refusal}"
    assert fresh_agent.balances(EXCHANGE_A_DOMAIN, EXCHANGE_C_DOMAIN) == after_control["now"], (
        "a refused purchase moved money"
    )

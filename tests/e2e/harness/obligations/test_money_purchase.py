"""Obligations MONEY-004, 007, 010, 011 and 016 to 018: a direct purchase at an Exchange.

The contract is ``ExchangeService.ExecuteTransaction`` in ``fora.proto`` of the
pinned module ``github.com/FORA-Protocol/protocol``:

* every per-item decision is answered in the body: the call returns OK with one
  item per item sent, in order, and a denied item carries its ``denial_reason``
  and nothing sold, whatever the item count;
* the Exchange verifies each presented offer's signature, which covers its
  terms, pricing, expiry and Exchange, so an altered offer is denied
  SIGNATURE_INVALID;
* an Exchange refuses a request carrying an item whose offer names another
  Exchange, as a mis-addressed request: ``invalid_argument`` with no typed reason;
* the buyer's account is resolved from what authenticates the agent (on a direct
  purchase, its request signature), never from anything the request names, and a
  delivery URL is bound to the thumbprint of the buyer's request-signing key.

Every purchase is the SDK's ``ExecuteTransaction`` as the test's own agent
(``fresh_agent``); every alteration is a declared mutation of the request the SDK
built (``harness.malformed``), sent after its unaltered control. Charges are read
through ``GetAccountStatus``; delivery through the publisher's edge, fetched by
the SDK with the agent's proof of possession.
"""

from __future__ import annotations

from collections.abc import Callable
from datetime import UTC, datetime
from typing import Any

from http import HTTPStatus

import pytest
from fora_sdk.client import CallError
from wire.models import TransactionResponse

from ..e2e_catalog import CANARY_MARKER
from ..edge_fetch import fetch_signed
from ..exchanges import EXCHANGE_A_DOMAIN, EXCHANGE_B_DOMAIN, EXCHANGE_D_DOMAIN
from ..fresh_agent import AgentFactory, FreshAgent
from ..ledger import Balances
from ..malformed import Alteration, Mutation, malformed_answer, patch_body
from ..money_path import (
    ARISTOTLE,
    LECTURE_NOTES,
    PAPER_SATELLITES,
    SIGNATURE_INVALID,
    THALES,
    assert_bought,
    assert_denied,
    assert_moved,
    assert_not_sold,
    charged,
    direct_purchase,
    discover_offer,
    only_item,
    typed_reasons,
)
from ..obligation_registry import Obligation, obligation
from ..sdk_client import BeforeSign

pytestmark = pytest.mark.stack_isolation("shared-clean-fixtures")

_ACCOUNT_NOT_REGISTERED = "DENIAL_REASON_ACCOUNT_NOT_REGISTERED"


def _alter_item(index: int, change: Callable[[dict[str, Any]], None]) -> BeforeSign:
    """A pre-signing alteration of item ``index``'s presented offer; the SDK then signs it."""

    def alter(body: dict[str, Any]) -> None:
        change(body["items"][index]["offer"])

    return patch_body(alter)


def _lower_rate(offer: dict[str, Any]) -> None:
    offer["pricing"]["rate"] = "0.01"


def _widen_term(offer: dict[str, Any]) -> None:
    """Add a restriction on an axis the term does not restrict yet, keeping one per axis."""
    restrictions = offer["terms"][0].setdefault("restrictions", [])
    kinds = {r.get("kind") for r in restrictions}
    for kind, value in (
        ("RESTRICTION_KIND_GEOGRAPHY", "AQ"),
        ("RESTRICTION_KIND_USER_TYPE", "individual"),
        ("RESTRICTION_KIND_FUNCTION", "ai-train"),
    ):
        if kind not in kinds:
            restrictions.append({"kind": kind, "permitted": [value]})
            return
    restrictions[0].setdefault("permitted", []).append("ai-train")


@obligation(Obligation.MONEY_004, negative=True)
@pytest.mark.parametrize(
    "change",
    [_lower_rate, _widen_term],
    ids=["a lowered rate", "a widened term"],
)
def test_an_offer_altered_after_signing_is_denied_in_the_body(
    fresh_agent: FreshAgent, change: Callable[[dict[str, Any]], None]
) -> None:
    """The offer reflected unchanged is bought; altered, it is denied SIGNATURE_INVALID.

    The answer to the altered one-item purchase is OK, with its only item denied
    and nothing sold on it, and the buyer's balances do not move.
    """
    fresh_agent.fund(EXCHANGE_D_DOMAIN)
    offer = discover_offer(fresh_agent, LECTURE_NOTES)
    after_control: dict[str, Balances] = {}

    def bought(response: TransactionResponse) -> None:
        assert_bought(only_item(response, "control"), "control")
        after_control.update(fresh_agent.balances(EXCHANGE_D_DOMAIN))

    answer = malformed_answer(
        Mutation.SIGNATURE,
        "the presented offer differs from the bytes the Exchange signed",
        control=direct_purchase(fresh_agent, EXCHANGE_D_DOMAIN, offer),
        alter=Alteration(before_sign=_alter_item(0, change)),
        accepted=bought,
    )
    assert_denied(only_item(answer, "the altered offer"), SIGNATURE_INVALID, "the altered offer")
    assert fresh_agent.balances(EXCHANGE_D_DOMAIN) == after_control, "a denied item moved money"


@obligation(Obligation.MONEY_016, negative=True)
def test_items_are_decided_one_by_one_in_request_order(fresh_agent: FreshAgent) -> None:
    """Two items, the second altered: the first is bought, the second denied, in that order."""
    fresh_agent.fund(EXCHANGE_A_DOMAIN)
    offers = [discover_offer(fresh_agent, ARISTOTLE), discover_offer(fresh_agent, THALES)]
    after_control: dict[str, Balances] = {}

    def bought(response: TransactionResponse) -> None:
        for item in response.items or []:
            assert_bought(item, "control")
        after_control.update(fresh_agent.balances(EXCHANGE_A_DOMAIN))

    answer = malformed_answer(
        Mutation.SIGNATURE,
        "the second item's offer differs from the bytes the Exchange signed",
        control=direct_purchase(fresh_agent, EXCHANGE_A_DOMAIN, offers),
        alter=Alteration(before_sign=_alter_item(1, _lower_rate)),
        accepted=bought,
    )
    items = answer.items or []
    assert [i.offer_id for i in items] == [o.offer.get("offer_id") for o in offers], (
        f"items must come back one per request item, in order: {items}"
    )
    first, second = items
    assert_bought(first, "the unaltered first item")
    assert_denied(second, SIGNATURE_INVALID, "the altered second item")
    currency, cost = charged(first)
    assert_moved(
        after_control,
        fresh_agent.balances(EXCHANGE_A_DOMAIN),
        {EXCHANGE_A_DOMAIN: {currency: cost}},
    )


@obligation(Obligation.MONEY_007, negative=True)
def test_an_offer_another_exchange_issued_is_refused_as_mis_addressed(
    fresh_agent: FreshAgent,
) -> None:
    """exchange-b's offer sent to exchange-a: invalid_argument, no typed reason, nothing bought.

    Control first: the offer bought at exchange-b, which issued it. Then the same
    offer bought through a client addressed at exchange-a. The SDK builds the
    request exactly as for exchange-b, so the only difference is the Exchange it
    reaches, whose domain the item's offer does not name.
    """
    fresh_agent.fund(EXCHANGE_A_DOMAIN, EXCHANGE_B_DOMAIN)
    offer = discover_offer(fresh_agent, PAPER_SATELLITES)
    control = only_item(fresh_agent.exchange(EXCHANGE_B_DOMAIN).execute(offer), "control")
    assert_bought(control, "control: bought at exchange-b")
    before = fresh_agent.balances(EXCHANGE_A_DOMAIN, EXCHANGE_B_DOMAIN)

    with pytest.raises(CallError) as refused:
        fresh_agent.exchange(EXCHANGE_A_DOMAIN).execute(offer)
    refusal = refused.value
    assert refusal.code == "invalid_argument", f"refused as {refusal.code!r}: {refusal}"
    assert not typed_reasons(refusal), f"a mis-addressed request carries no typed reason: {refusal}"
    assert fresh_agent.balances(EXCHANGE_A_DOMAIN, EXCHANGE_B_DOMAIN) == before, (
        "a mis-addressed purchase moved money"
    )


@obligation(Obligation.MONEY_010)
def test_the_signer_pays_whatever_requester_id_the_purchase_names(
    fresh_agent_factory: AgentFactory,
) -> None:
    """A direct purchase labelled with another agent's host is charged to its signer."""
    buyer, other = fresh_agent_factory(), fresh_agent_factory()
    buyer.fund(EXCHANGE_D_DOMAIN)
    other.fund(EXCHANGE_D_DOMAIN)
    offer = discover_offer(buyer, LECTURE_NOTES)
    before = {
        "buyer": buyer.balances(EXCHANGE_D_DOMAIN)[EXCHANGE_D_DOMAIN],
        "other": other.balances(EXCHANGE_D_DOMAIN)[EXCHANGE_D_DOMAIN],
    }

    labelled = buyer.exchange_client(EXCHANGE_D_DOMAIN, agent_id=other.host)
    item = only_item(labelled.execute(offer), "the purchase labelled as the other agent")
    assert_bought(item, "the purchase labelled as the other agent")
    currency, cost = charged(item)
    assert_moved(
        before,
        {
            "buyer": buyer.balances(EXCHANGE_D_DOMAIN)[EXCHANGE_D_DOMAIN],
            "other": other.balances(EXCHANGE_D_DOMAIN)[EXCHANGE_D_DOMAIN],
        },
        {"buyer": {currency: cost}},
    )


@obligation(Obligation.MONEY_011, Obligation.MONEY_017, negative=True)
def test_a_caller_with_no_account_is_told_to_register_and_then_buys(
    fresh_agent: FreshAgent,
) -> None:
    """No account: denied ACCOUNT_NOT_REGISTERED, nothing sold. Registered: bought, with an expiry.

    The protocol answers a reason about the caller either on the item, in the
    body, or as a whole-request refusal whose TransactionDenial names the
    Exchange to register at; both shapes are read here. After the agent registers
    at that Exchange, the same offer is bought, and the item carries its delivery
    URL and an expiry in the future.
    """
    offer = discover_offer(fresh_agent, LECTURE_NOTES)
    exchange = fresh_agent.exchange(EXCHANGE_D_DOMAIN)
    try:
        answer = exchange.execute(offer)
    except CallError as refused:
        denial = refused.detail.transaction_denial if refused.detail else None
        assert denial is not None, f"refused without a transaction_denial: {refused}"
        assert denial.reason.value == _ACCOUNT_NOT_REGISTERED, denial
        assert denial.exchange == EXCHANGE_D_DOMAIN, f"the denial names {denial.exchange!r}"
    else:
        denied = only_item(answer, "the purchase with no account")
        assert_denied(denied, _ACCOUNT_NOT_REGISTERED, "the purchase with no account")
        assert_not_sold(denied, "the purchase with no account")

    fresh_agent.fund(EXCHANGE_D_DOMAIN)
    item = only_item(exchange.execute(offer), "the purchase once registered")
    assert_bought(item, "the purchase once registered")
    assert item.expires_at is not None and item.expires_at > datetime.now(UTC), (
        f"a bought item carries its URL's expiry, in the future: {item.expires_at}"
    )


@obligation(Obligation.MONEY_018, negative=True)
@obligation(Obligation.MONEY_017)
def test_the_delivery_url_is_bound_to_the_buyers_key(fresh_agent_factory: AgentFactory) -> None:
    """agent_identity_hash is the buyer's thumbprint; the URL serves its key and no other."""
    buyer, other = fresh_agent_factory(), fresh_agent_factory()
    buyer.fund(EXCHANGE_A_DOMAIN)
    response = buyer.exchange(EXCHANGE_A_DOMAIN).execute(discover_offer(buyer, THALES))
    item = only_item(response, "the purchase")
    assert_bought(item, "the purchase")
    assert response.agent_identity_hash == buyer.thumbprint, (
        f"the URL is bound to {response.agent_identity_hash!r}, want the buyer's key "
        f"thumbprint {buyer.thumbprint!r}"
    )
    url = str(item.retrieval_endpoint)
    fetch_signed(url, buyer.stack, key_path=buyer.key_path, expect_marker=CANARY_MARKER)
    with pytest.raises(CallError) as refused:
        fetch_signed(url, buyer.stack, key_path=other.key_path)
    assert refused.value.status == HTTPStatus.FORBIDDEN, refused.value
    assert refused.value.reason == "keyid_mismatch", refused.value
